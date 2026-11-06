// Package queue contains the Todo execution engine (parallel, one worktree per
// Todo) and the Merge queue (strictly sequential, one goroutine).
package queue

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/usage"
)

type Publisher interface{ Publish(model.Event) }

// TodoEngine pulls Queued todos in priority order and runs up to MaxParallel
// of them at once, each in its own git worktree.
//
// One run of a todo:
//  1. create the worktree (copy files, run the setup command) or reuse it
//  2. if a merge conflicted, merge the default branch in and leave the
//     conflicts for Claude
//  3. run Claude (resuming its session when there is one), commit
//  4. run the tests; on failure send the output back to Claude, up to
//     the project's MaxAutoFixes
//  5. hand off to review, or straight to the merge queue if a human had
//     already approved it (AutoMerge)
type TodoEngine struct {
	Store        *store.Store
	Git          *gitops.Git
	Exec         agent.Executor
	Pub          Publisher
	Log          *TodoLog
	Merges       *MergeQueue
	Perms        *perm.Broker   // optional: route Claude's permission prompts to the UI
	BaseURL      string         // Dash's own URL, for the permission MCP server
	Usage        *usage.Tracker // optional: pause when the subscription is near its limit
	WorktreeRoot string         // <data>/worktrees
	MaxParallel  int
	TestTimeout  time.Duration
	SetupTimeout time.Duration

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
	if e.TestTimeout <= 0 {
		e.TestTimeout = 20 * time.Minute
	}
	if e.SetupTimeout <= 0 {
		e.SetupTimeout = 30 * time.Minute
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
		if e.Usage != nil {
			if ok, _ := e.Usage.CanStart(); !ok {
				return
			}
		}
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

func (e *TodoEngine) shuttingDown() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runCtx != nil && e.runCtx.Err() != nil
}

func (e *TodoEngine) work(ctx context.Context, t model.Todo) {
	log := func(s string) { e.Log.Line(t, s) }
	fail := func(err error) {
		if e.shuttingDown() {
			// Dash is stopping, not the user: leave the todo in progress so
			// it is requeued (and its worktree reused) on the next start.
			log("■ interrupted by shutdown; will resume on restart")
			return
		}
		msg := err.Error()
		if errors.Is(err, context.Canceled) {
			msg = "canceled"
		}
		log("✖ " + msg)
		if _, err := e.Store.SetStatus(t.ID, model.StatusFailed, msg); err != nil {
			slog.Error("set failed", "todo", t.ID, "err", err)
		}
	}
	update := func(fn func(*model.Todo)) error {
		nt, err := e.Store.UpdateTodo(t.ID, func(x *model.Todo) error { fn(x); return nil })
		if err == nil {
			t = nt
		}
		return err
	}
	addNote := func(note string) error {
		return update(func(x *model.Todo) { x.Feedback = append(x.Feedback, note) })
	}

	p, err := e.Store.GetProject(t.ProjectID)
	if err != nil {
		fail(err)
		return
	}

	// 1. Worktree.
	if t.Worktree == nil {
		base, err := e.Git.Head(ctx, p.ID, p.Path, p.DefaultBranch)
		if err != nil {
			fail(err)
			return
		}
		name := "todo-" + t.ID
		wt := model.Worktree{Name: name, Path: filepath.Join(e.WorktreeRoot, p.ID, name), Branch: "dash/" + name, BaseCommit: base}
		if err := update(func(x *model.Todo) { x.Worktree = &wt }); err != nil {
			fail(err)
			return
		}
	}
	wt := *t.Worktree
	if _, err := os.Stat(wt.Path); err != nil {
		if err := e.Git.AddWorktree(ctx, p.ID, p.Path, wt); err != nil {
			fail(err)
			return
		}
		log(fmt.Sprintf("▶ new worktree %s (%s)", wt.Path, wt.Branch))
		if err := e.prepareWorktree(ctx, p, wt, log); err != nil {
			fail(err)
			return
		}
	} else {
		log(fmt.Sprintf("▶ reusing worktree %s", wt.Path))
	}

	// 2. Bring in the default branch after a merge conflict.
	resolving := false
	if t.MergeMain {
		files, err := e.Git.StartMerge(ctx, p.ID, wt.Path, p.DefaultBranch)
		if err != nil {
			fail(fmt.Errorf("merge %s into worktree: %w", p.DefaultBranch, err))
			return
		}
		if len(files) > 0 {
			resolving = true
			log("⚠ conflicts with " + p.DefaultBranch + ": " + strings.Join(files, ", "))
			if err := addNote(conflictNote(p.DefaultBranch, files)); err != nil {
				fail(err)
				return
			}
		} else if _, err := e.Git.CommitAll(ctx, p.ID, wt.Path, "Merge "+p.DefaultBranch+" into "+wt.Branch); err != nil {
			fail(err)
			return
		}
		if err := update(func(x *model.Todo) { x.MergeMain = false }); err != nil {
			fail(err)
			return
		}
	}

	// 3–4. Claude, commit, tests; loop while tests fail and fixes remain.
	for {
		if err := e.runClaude(ctx, &t, p, wt, log); err != nil {
			fail(err)
			return
		}
		if resolving {
			markers, err := e.Git.LeftoverMarkers(ctx, p.ID, wt.Path)
			if err != nil {
				fail(err)
				return
			}
			if markers != "" {
				if t.AutoFixes >= p.MaxAutoFixes {
					fail(errors.New("conflict markers remain after Claude's attempt"))
					return
				}
				_ = update(func(x *model.Todo) { x.AutoFixes++ })
				_ = addNote("Some conflict markers are still in the code:\n```\n" + markers + "\n```\nResolve them all.")
				continue
			}
			resolving = false
		}
		msg := t.Title
		if msg == "" {
			msg = firstLine(t.Prompt)
		}
		if _, err := e.Git.CommitAll(ctx, p.ID, wt.Path, "dash: "+msg); err != nil {
			fail(err)
			return
		}
		if head, err := e.Git.Head(ctx, p.ID, wt.Path, "HEAD"); err != nil {
			fail(err)
			return
		} else if head == wt.BaseCommit {
			fail(errors.New("Claude finished without changing any files"))
			return
		}

		if p.TestCommand == "" {
			break
		}
		checks := e.runChecks(ctx, t, p, wt.Path, "review")
		_ = update(func(x *model.Todo) { x.Checks = &checks })
		if ctx.Err() != nil {
			fail(ctx.Err())
			return
		}
		if checks.Passed || t.AutoFixes >= p.MaxAutoFixes {
			break
		}
		log(fmt.Sprintf("↻ tests failed; sending the output back to Claude (fix %d of %d)", t.AutoFixes+1, p.MaxAutoFixes))
		_ = update(func(x *model.Todo) { x.AutoFixes++ })
		_ = addNote(testNote(checks))
	}

	// 5. Hand off.
	passed := t.Checks == nil || t.Checks.Passed
	if t.AutoMerge && passed && e.Merges != nil {
		log("✔ fixed; back to the merge queue (already approved)")
		if _, err := e.Store.SetStatus(t.ID, model.StatusMerging, ""); err == nil {
			if err := e.Merges.Enqueue(model.MergeJob{TodoID: t.ID, ProjectID: t.ProjectID}); err == nil {
				return
			}
			_, _ = e.Store.SetStatus(t.ID, model.StatusReview, "merge queue full")
			return
		}
	}
	if passed {
		log("✔ ready for review")
	} else {
		log("✖ ready for review, but tests fail")
	}
	if _, err := e.Store.SetStatus(t.ID, model.StatusReview, ""); err != nil {
		slog.Error("set review", "todo", t.ID, "err", err)
	}
}

// runClaude runs Claude once, resuming the todo's session when it has one,
// and records the session, cost and which notes Claude has now seen.
func (e *TodoEngine) runClaude(ctx context.Context, t *model.Todo, p model.Project, wt model.Worktree, log func(string)) error {
	req := agent.ExecRequest{Dir: wt.Path}
	unsent := t.Feedback[min(t.FeedbackSent, len(t.Feedback)):]
	switch {
	case t.SessionID != "" && len(unsent) > 0:
		req.Resume, req.Prompt = t.SessionID, agent.ContinuePrompt(unsent)
	case t.SessionID != "":
		req.Resume, req.Prompt = t.SessionID, agent.ContinuePrompt([]string{"You were interrupted. Continue where you left off and finish the task."})
	default:
		req.Prompt = agent.BuildPrompt(*t)
	}
	if req.Resume != "" {
		log("▶ resuming Claude session " + req.Resume)
	}
	if e.Perms != nil && e.BaseURL != "" {
		token, release := e.Perms.Register(t.ID, t.ProjectID)
		defer release()
		req.ExtraArgs = []string{
			"--mcp-config", perm.MCPConfig(e.BaseURL, token),
			"--permission-prompt-tool", perm.PromptTool,
		}
		// Claude waits on the MCP tool while a human decides; don't let it time out first.
		req.Env = []string{"MCP_TOOL_TIMEOUT=3600000"}
	}

	res, err := e.Exec.Run(ctx, req, log)
	if res.ResumeFailed && req.Resume != "" && ctx.Err() == nil {
		log("↻ the previous Claude session is gone; starting a fresh one with the full task")
		req.Resume, req.Prompt = "", agent.BuildPrompt(*t)
		res, err = e.Exec.Run(ctx, req, log)
	}
	nt, uerr := e.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		if res.SessionID != "" {
			x.SessionID = res.SessionID
		}
		x.CostUSD += res.CostUSD
		x.Runs++
		x.FeedbackSent = len(x.Feedback)
		return nil
	})
	if uerr == nil {
		*t = nt
	}
	return err
}

// prepareWorktree copies untracked files and runs the setup command in a new worktree.
func (e *TodoEngine) prepareWorktree(ctx context.Context, p model.Project, wt model.Worktree, log func(string)) error {
	for _, f := range p.CopyFiles {
		f = filepath.Clean(strings.TrimSpace(f))
		if f == "" || f == "." || filepath.IsAbs(f) || strings.HasPrefix(f, "..") {
			continue
		}
		if err := copyPath(filepath.Join(p.Path, f), filepath.Join(wt.Path, f)); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				log("· " + f + " not found in the main checkout; skipped")
				continue
			}
			return fmt.Errorf("copy %s: %w", f, err)
		}
		log("· copied " + f)
	}
	if strings.TrimSpace(p.SetupCommand) == "" {
		return nil
	}
	log("▶ setup: " + p.SetupCommand)
	code, tail, err := runShell(ctx, wt.Path, p.SetupCommand, e.SetupTimeout, nil, func(l string) { log("│ " + l) })
	if err != nil {
		return fmt.Errorf("setup command: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("setup command exited %d: %s", code, lastLine(tail))
	}
	return nil
}

func (e *TodoEngine) runChecks(ctx context.Context, t model.Todo, p model.Project, dir, stage string) model.Checks {
	return runChecks(ctx, e.Log, t, p, dir, stage, e.TestTimeout)
}

func runChecks(ctx context.Context, l *TodoLog, t model.Todo, p model.Project, dir, stage string, timeout time.Duration) model.Checks {
	l.Line(t, "▶ tests: "+p.TestCommand)
	start := time.Now()
	code, tail, err := runShell(ctx, dir, p.TestCommand, timeout, nil, func(s string) { l.Line(t, "│ "+s) })
	c := model.Checks{Command: p.TestCommand, ExitCode: code, Output: tail, Stage: stage, At: time.Now(), Duration: time.Since(start).Seconds()}
	if err != nil {
		c.Output = strings.TrimSpace(tail + "\n" + err.Error())
	}
	c.Passed = err == nil && code == 0
	if c.Passed {
		l.Line(t, fmt.Sprintf("✔ tests passed (%.0fs)", c.Duration))
	} else {
		l.Line(t, fmt.Sprintf("✖ tests failed (exit %d, %.0fs)", code, c.Duration))
	}
	return c
}

func testNote(c model.Checks) string {
	return fmt.Sprintf("The test command `%s` failed (exit %d). Fix the code so it passes; don't weaken or delete tests "+
		"unless they are wrong. Last lines of output:\n```\n%s\n```", c.Command, c.ExitCode, c.Output)
}

func conflictNote(branch string, files []string) string {
	return fmt.Sprintf("`%s` has moved on, and merging it into your work conflicts in:\n- %s\n\n"+
		"The merge is in progress in this working tree and those files contain conflict markers. "+
		"Resolve every conflict so both your change and the incoming changes keep working, remove all "+
		"markers, and make sure the code still builds. Don't run git commands; Dash completes the merge.",
		branch, strings.Join(files, "\n- "))
}

func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if info.IsDir() {
		return os.CopyFS(dst, os.DirFS(src))
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, info.Mode().Perm())
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

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
