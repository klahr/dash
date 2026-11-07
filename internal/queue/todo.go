// Package queue contains the Todo execution engine (parallel, one worktree per
// Todo) and the Merge queue (strictly sequential, one goroutine).
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/usage"
)

type Publisher interface{ Publish(model.Event) }

// TodoEngine pulls Queued todos in priority order and runs up to MaxParallel
// of them at once, each in its own git worktree. Merge conflicts are fixed
// one at a time per project (see conflictBusy).
//
// One run of a todo:
//  1. create the worktree (copy files, run the setup command) or reuse it
//  2. if a merge conflicted, merge the default branch in and leave the
//     conflicts for Claude
//  3. run Claude (resuming its session when there is one), commit
//  4. run the tests; on failure send the output back to Claude, up to
//     the project's MaxAutoFixes
//  5. hand off to review, or straight to the merge queue if a human had
//     already approved it (AutoMerge) or the project approves automatically
type TodoEngine struct {
	Store   *store.Store
	Git     *gitops.Git
	Exec    agent.Executor
	Pub     Publisher
	Log     *TodoLog
	Merges  *MergeQueue
	Perms   *perm.Broker // optional: route Claude's permission prompts to the UI
	BaseURL string       // Dash's own URL, for the permission MCP server
	// Board and BoardURL give every run the issue tools (report_issue,
	// list_issues) of Dash's MCP server, so Claude can log problems it
	// notices outside its task instead of fixing them on the wrong branch.
	Board    *mcptools.Tokens
	BoardURL string
	// Owner is who a todo's runs act as: its user and org. The per-run
	// tokens of the permission and board MCP servers carry it, and a run
	// whose owner can't be found gets neither server.
	Owner        func(model.Todo) (model.Owner, error)
	Usage        *usage.Tracker // optional: pause when the subscription is near its limit
	WorktreeRoot string         // <data>/worktrees
	MaxParallel  int
	// Remote sends the tasks of projects linked to runners to one of them
	// (a hub's dispatch.Dispatcher; nil: such tasks wait). Those tasks
	// don't count against MaxParallel or Usage: each runner has its own.
	Remote Remote
	// LocalTasks reports whether this Dash runs the tasks of projects
	// without runners itself (nil: it does). A hub whose work all goes to
	// runners says no, and those tasks wait, saying why.
	LocalTasks func() bool
	// Hold reports why an org's queued tasks may not start now, or ""
	// (nil: they may). Billing holds the tasks of an unpaid org, wherever
	// they would run: here or on its runners.
	Hold         func(org int64) string
	TestTimeout  time.Duration
	SetupTimeout time.Duration
	// Summarizer condenses Claude's final message into the todo's one-line
	// Summary (optional; without it the message's first sentence is used).
	Summarizer interface {
		Summary(ctx context.Context, report string) (string, error)
	}

	minute      time.Duration // length of a "minute" for run limits; shortened in tests
	wake        chan struct{}
	runCtx      context.Context // the dispatcher's context; done on shutdown
	mu          sync.Mutex
	running     map[string]context.CancelFunc
	interrupted map[string]string // running todo id → why Interrupt stopped it
	// resolving holds the todos (id → project id) dispatched to fix a merge
	// conflict, until they have landed or left the fix-and-merge path.
	resolving map[string]string
}

// Remote hands a queued task to a runner. An error is why it can't now
// (the task stays queued with the error as its Waiting reason).
type Remote interface {
	Dispatch(ctx context.Context, p model.Project, t model.Todo) (runnerID, remoteID string, err error)
}

// NoLocalTasks is the Waiting reason of a task this Dash won't run itself.
const NoLocalTasks = "no runner: this hub doesn't run tasks itself; link the project to a runner in its settings"

// NewTodoEngine finishes initialising e; call Run in a goroutine afterwards.
func NewTodoEngine(e *TodoEngine) *TodoEngine {
	e.wake = make(chan struct{}, 1)
	e.running = map[string]context.CancelFunc{}
	e.resolving = map[string]string{}
	if e.MaxParallel <= 0 {
		e.MaxParallel = 1
	}
	if e.TestTimeout <= 0 {
		e.TestTimeout = 20 * time.Minute
	}
	if e.SetupTimeout <= 0 {
		e.SetupTimeout = 30 * time.Minute
	}
	if e.minute <= 0 {
		e.minute = time.Minute
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
	sent := map[string]bool{} // tasks offered to runners this round
	for {
		local := true // a task may start here now
		if e.Usage != nil {
			if ok, _ := e.Usage.CanStart(); !ok {
				local = false
			}
		}
		e.mu.Lock()
		free := e.MaxParallel - len(e.running)
		e.mu.Unlock()
		if free <= 0 {
			local = false
		}
		var next *model.Todo
		var all []model.Todo // loaded only when a queued todo has dependencies
		for _, q := range e.Store.ListTodos("", model.StatusQueued) {
			if q.Paused || sent[q.ID] {
				continue
			}
			if q.MergeMain && e.conflictBusy(q.ProjectID) {
				continue // waits until the conflict being fixed has landed
			}
			if len(q.DependsOn) > 0 {
				if all == nil {
					all = e.Store.ListTodos("")
				}
				if len(q.Blockers(all)) > 0 {
					continue // waits until its dependencies are merged
				}
			}
			p, err := e.Store.GetProject(q.ProjectID)
			if err != nil {
				continue
			}
			if e.Hold != nil {
				if why := e.Hold(p.OrgID); why != "" {
					e.wait(q, why)
					continue
				}
			}
			if p.OnRunners() {
				sent[q.ID] = true
				e.sendToRunner(ctx, p, q)
				continue
			}
			if e.LocalTasks != nil && !e.LocalTasks() {
				e.wait(q, NoLocalTasks)
				continue
			}
			if !local {
				continue
			}
			next = &q
			break
		}
		if next == nil {
			return
		}
		if next.OnRunner() {
			// It ran on a runner of a project since unlinked: it starts over here.
			_, err := e.Store.UpdateTodo(next.ID, func(x *model.Todo) error {
				x.Runner, x.RemoteID, x.Resumable = "", "", false
				x.Worktree, x.Checks, x.PR = nil, nil, nil
				return nil
			})
			if err != nil {
				slog.Error("dispatch", "todo", next.ID, "err", err)
				return
			}
		}
		t, err := e.Store.SetStatus(next.ID, model.StatusInProgress, "")
		if err != nil {
			slog.Error("dispatch", "todo", next.ID, "err", err)
			return
		}
		wctx, cancel := context.WithCancel(ctx)
		e.mu.Lock()
		e.running[t.ID] = cancel
		if t.MergeMain {
			e.resolving[t.ID] = t.ProjectID
		}
		e.mu.Unlock()
		go func() {
			defer func() {
				cancel()
				e.mu.Lock()
				delete(e.running, t.ID)
				delete(e.interrupted, t.ID)
				e.mu.Unlock()
				e.Kick()
			}()
			e.work(wctx, t)
		}()
	}
}

// wait records why queued todo t isn't starting, if that changed.
func (e *TodoEngine) wait(t model.Todo, why string) {
	if t.Waiting == why {
		return
	}
	_, _ = e.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		if x.Status == model.StatusQueued {
			x.Waiting = why
		}
		return nil
	})
}

// sendToRunner hands t, a queued task of p, to one of p's runners, or
// records why none can take it.
func (e *TodoEngine) sendToRunner(ctx context.Context, p model.Project, t model.Todo) {
	if e.Remote == nil {
		e.wait(t, "no runner: this Dash doesn't send tasks to runners")
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	runnerID, remoteID, err := e.Remote.Dispatch(cctx, p, t)
	cancel()
	if err != nil {
		e.wait(t, err.Error())
		return
	}
	_, err = e.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		now := time.Now()
		x.Status, x.Error, x.Waiting, x.Paused, x.Resumable = model.StatusInProgress, "", "", false, false
		x.StartedAt, x.FinishedAt = &now, nil
		if x.Runner != runnerID {
			x.Worktree, x.Checks, x.PR = nil, nil, nil // a fresh start on another runner
		}
		x.Runner, x.RemoteID = runnerID, remoteID
		return nil
	})
	if err != nil {
		slog.Error("dispatch to runner", "todo", t.ID, "err", err)
	}
}

// conflictBusy reports whether another todo in the project is fixing a merge
// conflict, or has fixed one and is on its way through the merge queue.
// Conflicts are fixed one at a time: todos approved around the same time
// often conflict with each other too, and each fix is made against the
// default branch as it is when the run starts, so fixing them side by side
// mostly earns another conflict. Waiting for the first to land lets the
// next one merge it in and resolve everything once.
func (e *TodoEngine) conflictBusy(projectID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	busy := false
	for id, pid := range e.resolving {
		t, err := e.Store.GetTodo(id)
		if err != nil || (t.Status != model.StatusInProgress && t.Status != model.StatusMerging) {
			delete(e.resolving, id) // landed, failed, deleted or back in review or the queue
			continue
		}
		if pid == projectID {
			busy = true
		}
	}
	return busy
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

// Interrupt stops every running todo and marks it failed with
// "interrupted: why" (say, a runner that lost its hub for too long), so none
// waits forever on a person who can't reach it. Their worktrees and Claude
// sessions are kept: queue them again to continue. It returns how many it
// stopped.
func (e *TodoEngine) Interrupt(why string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.interrupted == nil {
		e.interrupted = map[string]string{}
	}
	for id, cancel := range e.running {
		e.interrupted[id] = why
		cancel()
	}
	return len(e.running)
}

func (e *TodoEngine) interruption(todoID string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.interrupted[todoID]
}

// Pause holds a queued todo back, or stops a running one and requeues it
// paused; its worktree and Claude session are kept for Resume.
func (e *TodoEngine) Pause(todoID string) (model.Todo, error) {
	t, err := e.Store.UpdateTodo(todoID, func(x *model.Todo) error {
		if x.Status != model.StatusInProgress && x.Status != model.StatusQueued {
			return model.ErrInvalidTransition{From: x.Status, To: "paused"}
		}
		if x.Status == model.StatusInProgress && x.OnRunner() {
			return ErrOnRunner
		}
		x.Paused = true
		return nil
	})
	if err == nil {
		e.Cancel(todoID) // the worker sees Paused and requeues instead of failing
	}
	return t, err
}

// ErrOnRunner is returned for what can't be done to a task while a runner
// works on it.
var ErrOnRunner = errors.New("this task is running on a runner: cancel it instead, and queue it again to continue")

// Resume lets a paused todo run again.
func (e *TodoEngine) Resume(todoID string) (model.Todo, error) {
	t, err := e.Store.UpdateTodo(todoID, func(x *model.Todo) error { x.Paused = false; return nil })
	if err == nil {
		e.Kick()
	}
	return t, err
}

func (e *TodoEngine) shuttingDown() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runCtx != nil && e.runCtx.Err() != nil
}

func (e *TodoEngine) work(ctx context.Context, t model.Todo) {
	log := func(s string) { e.Log.Line(t, s) }
	var report string // Claude's final message from its latest run
	fail := func(err error) {
		if e.shuttingDown() {
			// Dash is stopping, not the user: leave the todo in progress so
			// it is requeued (and its worktree reused) on the next start.
			log("■ interrupted by shutdown; will resume on restart")
			return
		}
		if ctx.Err() != nil {
			if cur, err := e.Store.GetTodo(t.ID); err == nil && cur.Paused {
				log("⏸ paused")
				if _, err := e.Store.SetStatus(t.ID, model.StatusQueued, ""); err != nil {
					slog.Error("set paused", "todo", t.ID, "err", err)
				}
				return
			}
		}
		msg := err.Error()
		if errors.Is(err, context.Canceled) {
			msg = "canceled"
		}
		if why := e.interruption(t.ID); why != "" && ctx.Err() != nil {
			msg = "interrupted: " + why
		}
		log("✖ " + msg)
		noteFailure(e.Store, t.ID, msg, report)
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
		// Start from what's on origin when local is merely behind it (a push
		// from another machine); never blocks the start, never rebases.
		if e.Merges != nil && e.Merges.Remote != nil {
			if note := e.Merges.Remote.FastForward(ctx, p); note != "" {
				log(note)
			}
		}
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

	// Edits saved in the web editor take the place of Claude's first run.
	editing := len(t.Edits) > 0 && !t.EditsApplied
	if editing {
		if err := e.applyEdits(ctx, t, p, wt, log); err != nil {
			fail(err)
			return
		}
	}

	// 3–4. Claude, commit, tests; loop while tests fail and fixes remain.
	for {
		if editing {
			editing = false
		} else if report, err = e.runClaude(ctx, &t, p, wt, log); err != nil {
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
			if len(t.Edits) > 0 && !t.EditsApplied {
				fail(errors.New("the saved edit doesn't change the file"))
			} else {
				fail(errors.New("Claude finished without changing any files"))
			}
			return
		}
		if len(t.Edits) > 0 && !t.EditsApplied {
			_ = update(func(x *model.Todo) { x.EditsApplied = true })
		}
		if files, add, del, err := e.Git.DiffStat(ctx, p.ID, wt.Path, p.DefaultBranch); err == nil {
			_ = update(func(x *model.Todo) { x.DiffFiles, x.DiffAdd, x.DiffDel = files, add, del })
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
	if (t.AutoMerge || p.AutoApprove) && passed && e.Merges != nil {
		if t.AutoMerge {
			log("✔ fixed; back to the merge queue (already approved)")
		} else {
			log("✔ approved automatically; to the merge queue")
			_ = update(func(x *model.Todo) { x.AutoFixes = 0 }) // like approving: a fresh budget for merge conflicts
		}
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

// issuesHint tells a task's Claude about the issue tools.
const issuesHint = "If you notice a bug, vulnerability or other problem in this project that is unrelated to your task, " +
	"don't fix it here and don't ignore it: report it with the " + mcptools.ServerName + " report_issue tool " +
	"(title, type, severity, file and line, details, suggested fix) so the user can triage it, then carry on with your task."

// runClaude runs Claude once, resuming the todo's session when it has one,
// and records the session, cost and which notes Claude has now seen. It
// returns Claude's final message.
func (e *TodoEngine) runClaude(ctx context.Context, t *model.Todo, p model.Project, wt model.Worktree, log func(string)) (string, error) {
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
	emit := func(ev model.RunEvent) { e.Log.Event(*t, ev) }
	switch {
	case len(unsent) > 0 && req.Resume != "":
		emit(model.RunEvent{Kind: "run_start", Text: "Rework"})
	case req.Resume != "":
		emit(model.RunEvent{Kind: "run_start", Text: "Resumed"})
	default:
		emit(model.RunEvent{Kind: "run_start", Text: "Started"})
	}
	for _, n := range unsent {
		emit(model.RunEvent{Kind: "prompt", Text: n})
	}
	req.ExtraArgs = append(req.ExtraArgs, "--max-turns", strconv.Itoa(p.TurnLimit()))
	for _, dir := range attachmentDirs(t.Attachments) {
		req.ExtraArgs = append(req.ExtraArgs, "--add-dir", dir)
	}
	servers := map[string]any{}
	owner, ownerErr := model.Owner{}, errors.New("no owner resolver")
	if e.Owner != nil {
		owner, ownerErr = e.Owner(*t)
	}
	if ownerErr != nil && (e.Perms != nil || e.Board != nil) {
		slog.Warn("task runs without Dash's MCP servers: no user and org to act as", "todo", t.ID, "err", ownerErr)
	}
	if e.Perms != nil && e.BaseURL != "" && ownerErr == nil {
		token, release := e.Perms.Register(owner, t.ID, t.ProjectID)
		defer release()
		servers[perm.ServerName] = perm.ServerConfig(e.BaseURL, token)
		req.ExtraArgs = append(req.ExtraArgs, "--permission-prompt-tool", perm.PromptTool)
		// Claude waits on the MCP tool while a human decides; don't let it time out first.
		req.Env = []string{"MCP_TOOL_TIMEOUT=3600000"}
	}
	if e.Board != nil && e.BoardURL != "" && ownerErr == nil {
		token, release := e.Board.RegisterTask(owner, t.ProjectID, t.ID)
		defer release()
		servers[mcptools.ServerName] = mcptools.ServerConfig(e.BoardURL, token)
		for _, name := range mcptools.IssueToolNames {
			req.ExtraArgs = append(req.ExtraArgs, "--allowedTools", "mcp__"+mcptools.ServerName+"__"+name)
		}
		req.ExtraArgs = append(req.ExtraArgs, "--append-system-prompt", issuesHint)
	}
	if len(servers) > 0 {
		cfg, _ := json.Marshal(map[string]any{"mcpServers": servers})
		req.ExtraArgs = append(req.ExtraArgs, "--mcp-config", string(cfg))
	}

	// Stop runaway runs: a time limit that doesn't count time spent waiting
	// for a human to answer a permission prompt, and a turn limit.
	limit := time.Duration(p.RunMinutes()) * e.minute
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	var overTime atomic.Bool
	go e.watchdog(runCtx, t.ID, limit, func() { overTime.Store(true); stop() })

	res, err := e.Exec.Run(runCtx, req, emit)
	if res.ResumeFailed && req.Resume != "" && runCtx.Err() == nil {
		log("↻ the previous Claude session is gone; starting a fresh one with the full task")
		emit(model.RunEvent{Kind: "run_start", Text: "Started over with the full task"})
		req.Resume, req.Prompt = "", agent.BuildPrompt(*t)
		res, err = e.Exec.Run(runCtx, req, emit)
	}
	switch {
	case overTime.Load() && ctx.Err() == nil:
		err = fmt.Errorf("stopped: Claude worked longer than the %d-minute limit; rework it to let Claude continue, or raise the limit in the project settings", p.RunMinutes())
	case res.Stop == "error_max_turns":
		err = fmt.Errorf("stopped: Claude used its %d-turn limit; rework it to let Claude continue, or raise the limit in the project settings", p.TurnLimit())
	}
	if err != nil {
		emit(model.RunEvent{Kind: "run_end", Text: err.Error(), IsError: true})
	} else {
		emit(model.RunEvent{Kind: "run_end"})
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
	if err == nil {
		e.summarize(t.ID, res.Result)
	}
	return strings.TrimSpace(res.Result), err
}

// noteFailure explains in the todo's chat why it failed, quoting Claude's
// final message when there is one: it usually says why nothing changed.
func noteFailure(s *store.Store, todoID, why, report string) {
	now := time.Now()
	msg := "**This task failed:** " + why
	if report != "" {
		msg += "\n\nClaude's last message:\n\n" + report
	}
	if err := s.AppendTodoChat(todoID, model.ChatMessage{Role: "assistant", Content: msg, At: &now}); err != nil {
		slog.Error("note failure", "todo", todoID, "err", err)
	}
}

// summarize sets the todo's Summary from Claude's final message, in the
// background when a Summarizer has to be asked.
func (e *TodoEngine) summarize(todoID, report string) {
	report = strings.TrimSpace(report)
	if report == "" {
		return
	}
	set := func(s string) {
		_, _ = e.Store.UpdateTodo(todoID, func(x *model.Todo) error { x.Summary = s; return nil })
	}
	if e.Summarizer == nil {
		set(firstSentence(report))
		return
	}
	go func() {
		s, err := e.Summarizer.Summary(context.Background(), report)
		if err != nil {
			slog.Debug("summary", "todo", todoID, "err", err)
			s = firstSentence(report)
		}
		set(s)
	}()
}

func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	for i := 0; i+1 < len(s); i++ {
		if (s[i] == '.' || s[i] == '!' || s[i] == '?') && s[i+1] == ' ' {
			s = s[:i+1]
			break
		}
	}
	if r := []rune(s); len(r) > 160 {
		s = string(r[:159]) + "…"
	}
	return s
}

// watchdog calls over once the run has used limit, not counting time it
// spends waiting on a permission prompt.
func (e *TodoEngine) watchdog(ctx context.Context, todoID string, limit time.Duration, over func()) {
	step := min(5*time.Second, max(limit/20, time.Millisecond))
	tick := time.NewTicker(step)
	defer tick.Stop()
	var used time.Duration
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if e.Perms != nil && e.Perms.Waiting(todoID) {
				continue
			}
			if used += step; used >= limit {
				over()
				return
			}
		}
	}
}

func attachmentDirs(as []model.Attachment) []string {
	var dirs []string
	seen := map[string]bool{}
	for _, a := range as {
		if d := filepath.Dir(a.Path); a.Path != "" && !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// RunTests re-runs the project's tests in a todo's worktree without involving
// Claude (e.g. for a flaky test). It returns at once; results arrive as an
// updated todo.
func (e *TodoEngine) RunTests(todoID string) error {
	t, err := e.Store.GetTodo(todoID)
	if err != nil {
		return err
	}
	p, err := e.Store.GetProject(t.ProjectID)
	if err != nil {
		return err
	}
	switch {
	case p.TestCommand == "":
		return errors.New("this project has no test command; set one in its settings")
	case t.Worktree == nil:
		return errors.New("this task has no worktree yet")
	case t.Status == model.StatusInProgress || t.Status == model.StatusMerging:
		return errors.New("wait until Claude or the merge queue is done with this task")
	}
	e.mu.Lock()
	if _, busy := e.running["test:"+t.ID]; busy {
		e.mu.Unlock()
		return errors.New("tests are already running for this task")
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.running["test:"+t.ID] = cancel // counted like a run, so it shares the parallelism limit
	e.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			e.mu.Lock()
			delete(e.running, "test:"+t.ID)
			e.mu.Unlock()
			e.Kick()
		}()
		checks := e.runChecks(ctx, t, p, t.Worktree.Path, "manual")
		_, _ = e.Store.UpdateTodo(t.ID, func(x *model.Todo) error { x.Checks = &checks; return nil })
	}()
	return nil
}

// applyEdits writes a todo's editor edits into its worktree. A file that
// changed on the default branch since it was opened is not overwritten.
func (e *TodoEngine) applyEdits(ctx context.Context, t model.Todo, p model.Project, wt model.Worktree, log func(string)) error {
	for _, ed := range t.Edits {
		if !filepath.IsLocal(filepath.FromSlash(ed.Path)) {
			return fmt.Errorf("invalid path %q", ed.Path)
		}
		if cur, _ := e.Git.Blob(ctx, p.ID, wt.Path, wt.BaseCommit, ed.Path); cur != ed.Blob {
			return fmt.Errorf("%s changed on %s since it was opened in the editor; open it again and redo the edit", ed.Path, p.DefaultBranch)
		}
		dst := filepath.Join(wt.Path, filepath.FromSlash(ed.Path))
		fi, err := os.Lstat(dst)
		if err != nil || !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", ed.Path)
		}
		if err := os.WriteFile(dst, []byte(ed.Content), fi.Mode().Perm()); err != nil {
			return err
		}
		log("✎ applied the edit to " + ed.Path)
	}
	return nil
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
