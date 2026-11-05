// Package queue contains the Todo execution engine (parallel, one worktree per
// Todo) and the Merge queue (strictly sequential, one goroutine).
package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

type Publisher interface{ Publish(model.Event) }

// TodoEngine pulls Queued todos in priority order and runs up to MaxParallel
// of them at once, each in its own git worktree.
type TodoEngine struct {
	Store        *store.Store
	Git          *gitops.Git
	Exec         agent.Executor
	Pub          Publisher
	WorktreeRoot string // <data>/worktrees
	LogRoot      string // <data>/logs
	MaxParallel  int

	wake    chan struct{}
	runCtx  context.Context // the dispatcher's context; done on shutdown
	mu      sync.Mutex
	running map[string]context.CancelFunc
}

// NewTodoEngine finishes initialising e; call Run in a goroutine afterwards.
func NewTodoEngine(e *TodoEngine) *TodoEngine {
	e.wake = make(chan struct{}, 1)
	e.running = map[string]context.CancelFunc{}
	if e.MaxParallel <= 0 {
		e.MaxParallel = 1
	}
	return e
}

// Kick asks the dispatcher to look for work now (call after enqueueing).
func (e *TodoEngine) Kick() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Run is the dispatcher loop. It is the only goroutine that moves todos from
// Queued to InProgress, so a todo can never be picked twice.
func (e *TodoEngine) Run(ctx context.Context) {
	e.mu.Lock()
	e.runCtx = ctx
	e.mu.Unlock()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		e.dispatch(ctx)
		select {
		case <-ctx.Done():
			return
		case <-e.wake:
		case <-tick.C:
		}
	}
}

func (e *TodoEngine) dispatch(ctx context.Context) {
	for {
		e.mu.Lock()
		free := e.MaxParallel - len(e.running)
		e.mu.Unlock()
		if free <= 0 {
			return
		}
		queued := e.Store.ListTodos("", model.StatusQueued)
		if len(queued) == 0 {
			return
		}
		t, err := e.Store.SetStatus(queued[0].ID, model.StatusInProgress, "")
		if err != nil {
			slog.Error("dispatch", "todo", queued[0].ID, "err", err)
			return
		}
		wctx, cancel := context.WithCancel(ctx)
		e.mu.Lock()
		e.running[t.ID] = cancel
		e.mu.Unlock()
		go func() {
			defer func() {
				cancel()
				e.mu.Lock()
				delete(e.running, t.ID)
				e.mu.Unlock()
				e.Kick()
			}()
			e.work(wctx, t)
		}()
	}
}

// Cancel stops a running todo. Returns false if it was not running.
func (e *TodoEngine) Cancel(todoID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cancel, ok := e.running[todoID]; ok {
		cancel()
		return true
	}
	return false
}

func (e *TodoEngine) work(ctx context.Context, t model.Todo) {
	fail := func(err error) {
		e.mu.Lock()
		shuttingDown := e.runCtx != nil && e.runCtx.Err() != nil
		e.mu.Unlock()
		if shuttingDown {
			// Dash is stopping, not the user: leave the todo in progress so
			// it is requeued (and its worktree reused) on the next start.
			e.logLine(t, "■ interrupted by shutdown; will resume on restart")
			return
		}
		msg := err.Error()
		if errors.Is(err, context.Canceled) {
			msg = "canceled"
		}
		e.logLine(t, "✖ "+msg)
		if _, err := e.Store.SetStatus(t.ID, model.StatusFailed, msg); err != nil {
			slog.Error("set failed", "todo", t.ID, "err", err)
		}
	}

	p, err := e.Store.GetProject(t.ProjectID)
	if err != nil {
		fail(err)
		return
	}

	// 1. Worktree: create on first run, reuse on rework.
	if t.Worktree == nil {
		base, err := e.Git.Head(ctx, p.ID, p.Path, p.DefaultBranch)
		if err != nil {
			fail(err)
			return
		}
		name := "todo-" + t.ID
		wt := model.Worktree{
			Name:       name,
			Path:       filepath.Join(e.WorktreeRoot, p.ID, name),
			Branch:     "dash/" + name,
			BaseCommit: base,
		}
		if t, err = e.Store.UpdateTodo(t.ID, func(x *model.Todo) error { x.Worktree = &wt; return nil }); err != nil {
			fail(err)
			return
		}
	}
	if err := e.Git.AddWorktree(ctx, p.ID, p.Path, *t.Worktree); err != nil {
		fail(err)
		return
	}
	e.logLine(t, fmt.Sprintf("▶ worktree %s (%s)", t.Worktree.Path, t.Worktree.Branch))

	// 2. Let Claude work in the worktree.
	if err := e.Exec.Run(ctx, t, t.Worktree.Path, func(l string) { e.logLine(t, l) }); err != nil {
		fail(err)
		return
	}

	// 3. Commit the result so it can be diffed, reviewed and merged.
	msg := t.Title
	if msg == "" {
		msg = firstLine(t.Prompt)
	}
	if _, err := e.Git.CommitAll(ctx, p.ID, t.Worktree.Path, "dash: "+msg); err != nil {
		fail(err)
		return
	}
	ahead, err := e.Git.Head(ctx, p.ID, t.Worktree.Path, "HEAD")
	if err != nil {
		fail(err)
		return
	}
	if ahead == t.Worktree.BaseCommit {
		fail(errors.New("Claude finished without changing any files"))
		return
	}
	e.logLine(t, "✔ ready for review")
	if _, err := e.Store.SetStatus(t.ID, model.StatusReview, ""); err != nil {
		slog.Error("set review", "todo", t.ID, "err", err)
	}
}

// logLine appends to the todo's log file and streams it to dashboards.
func (e *TodoEngine) logLine(t model.Todo, line string) {
	e.Pub.Publish(model.Event{Type: model.EvTodoOutput, ProjectID: t.ProjectID, TodoID: t.ID, Data: line, Time: time.Now()})
	if err := os.MkdirAll(e.LogRoot, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(e.logPath(t.ID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.TimeOnly), line)
}

func (e *TodoEngine) logPath(id string) string { return filepath.Join(e.LogRoot, id+".log") }

// Log returns the full execution log of a todo.
func (e *TodoEngine) Log(id string) (string, error) {
	b, err := os.ReadFile(e.logPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 72 {
		s = s[:72]
	}
	return s
}
