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
	"github.com/klahr/dash/internal/textutil"
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
//     already approved it (AutoMerge) or the project approves automatically.
//     In "pr" mode it goes to the merge queue too, which opens its pull
//     request (or pushes to it) and moves it to review, where it waits on
//     GitHub (see MergeQueue.openPR)
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
	// Notes, on a runner, says how many shared notes a task's project has,
	// and false when it has none (not a hub's project linked here). Such a
	// task's runs also get the notes tools (mcptools.NotesTools) on the
	// same token, and a short pointer to them in the system prompt.
	Notes func(model.Todo) (count int, ok bool)
	// Owner is who a todo's runs act as: its user and org. The per-run
	// tokens of the permission and board MCP servers carry it, and a run
	// whose owner can't be found gets neither server.
	Owner        func(model.Todo) (model.Owner, error)
	Usage        *usage.Tracker // optional: pause when the subscription is near its limit
	WorktreeRoot string         // <data>/worktrees
	MaxParallel  int
	// Remote, on the hub (its dispatch.Dispatcher), sends every task to one
	// of its project's runners: the hub runs none itself, and the tasks of
	// a project no runner is linked to wait for one (NoRunnerLinked). Nil
	// on a runner, which runs the tasks it is given here, at most
	// MaxParallel at a time.
	Remote Remote
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
	workers     sync.WaitGroup    // the goroutines working on running todos (see Wait)
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

// ErrSettled is a Remote's error for a task it didn't send because the
// runner had finished it already (merged it while the hub had it queued):
// the Remote recorded where it stands, and it isn't sent again.
var ErrSettled = errors.New("the runner had finished the task already")

// NoRunnerLinked is the Waiting reason of a hub's task whose project no
// runner is linked to.
const NoRunnerLinked = "no runner: the hub doesn't run tasks itself; link the project to a runner in its settings (start one with `dash runner` on a computer with git and Claude Code)"

// NewTodoEngine finishes initialising e; call Run in a goroutine afterwards.
func NewTodoEngine(e *TodoEngine) *TodoEngine {
	e.wake = make(chan struct{}, 1)
	e.running = map[string]context.CancelFunc{}
	e.resolving = map[string]string{}
	if e.MaxParallel <= 0 {
		e.MaxParallel = 1
	}
	if e.Usage != nil {
		e.Usage.SetParallel(e.MaxParallel)
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

// EffectiveParallel is how many tasks may run at once now: MaxParallel, or
// more while the queue follows the weekly pace and is behind it (see
// usage.Pace). MaxParallel itself never changes.
func (e *TodoEngine) EffectiveParallel() int {
	if e.Usage != nil {
		if n := e.Usage.Snapshot().Pace.Slots; n > 0 {
			return n
		}
	}
	return e.MaxParallel
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
		local := true   // a task may start here now
		forced := false // a task with StartNow may: the queue only waits for the pace
		limit := e.MaxParallel
		if e.Usage != nil {
			s := e.Usage.Snapshot()
			if s.Paused {
				local = false
				forced = s.PaceOnly()
			}
			if s.Pace.Slots > 0 {
				limit = s.Pace.Slots
			}
		}
		e.mu.Lock()
		free := limit - len(e.running)
		e.mu.Unlock()
		if free <= 0 {
			local, forced = false, false
		}
		var next *model.Todo
		var all []model.Todo // loaded only when a queued todo has dependencies or builds on a task
		for _, q := range e.Store.ListTodos("", model.StatusQueued) {
			if q.Paused || sent[q.ID] {
				continue
			}
			if q.MergeMain && e.conflictBusy(q.ProjectID) {
				continue // waits until the conflict being fixed has landed
			}
			if len(q.DependsOn) > 0 || q.BuildsOn != "" {
				if all == nil {
					all = e.Store.ListTodos("")
				}
				if len(q.Blockers(all)) > 0 {
					continue // waits until its dependencies are merged and the task it builds on is finished
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
			if e.Remote != nil {
				// The hub: tasks go to runners, never run here.
				if p.OnRunners() {
					sent[q.ID] = true
					e.sendToRunner(ctx, p, q)
				} else {
					e.wait(q, NoRunnerLinked)
				}
				continue
			}
			if !local && !(forced && q.StartNow) {
				continue
			}
			next = &q
			break
		}
		if next == nil {
			return
		}
		t, err := e.Store.SetStatus(next.ID, model.StatusInProgress, "") // uses StartNow up
		if err != nil {
			slog.Error("dispatch", "todo", next.ID, "err", err)
			return
		}
		if !local {
			e.Log.Line(t, "▶ "+StartedNowNote)
		}
		wctx, cancel := context.WithCancel(ctx)
		e.mu.Lock()
		e.running[t.ID] = cancel
		if t.MergeMain {
			e.resolving[t.ID] = t.ProjectID
		}
		e.mu.Unlock()
		e.workers.Add(1)
		go func() {
			defer e.workers.Done()
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

// StartedNowNote is the task log's line for a run started with Start now.
const StartedNowNote = "started now, despite the weekly pace: this run counts toward usage, so the queue may wait a little longer for the pace afterwards"

// ErrNotQueued is StartNow's error for a todo that isn't queued.
var ErrNotQueued = errors.New("only a queued task can start now; queue it first")

// MarkStartNow lets queued todo x start while the queue waits for the
// weekly pace (see model.Todo.StartNow); a paused todo is resumed too.
// It is an update for store.Store.UpdateTodo; kick the engine afterwards.
func MarkStartNow(x *model.Todo) error {
	if x.Status != model.StatusQueued {
		return fmt.Errorf("task %q is %s: %w", x.ID, x.Status, ErrNotQueued)
	}
	x.StartNow, x.Paused = true, false
	return nil
}

// StartNow marks queued todo id to start now (MarkStartNow) and kicks the
// dispatcher.
func (e *TodoEngine) StartNow(id string) (model.Todo, error) {
	t, err := e.Store.UpdateTodo(id, MarkStartNow)
	if err == nil {
		e.Kick()
	}
	return t, err
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
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	runnerID, remoteID, err := e.Remote.Dispatch(cctx, p, t)
	cancel()
	if errors.Is(err, ErrSettled) {
		return
	}
	if err != nil {
		e.wait(t, err.Error())
		return
	}
	_, err = e.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		now := time.Now()
		x.Status, x.Error, x.Waiting, x.Paused, x.Resumable = model.StatusInProgress, "", "", false, false
		x.StartedNow, x.StartNow = x.StartNow, false // Start now is used up by this run
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

// Wait waits, up to timeout, for the todos running when Run's context
// ended to stop (their Claude and test processes signalled, each left in
// progress to resume at the next start), and reports whether they all did.
func (e *TodoEngine) Wait(timeout time.Duration) bool {
	return waitGroup(&e.workers, timeout)
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
	r := &todoRun{e: e, ctx: ctx, t: t}
	p, err := e.Store.GetProject(t.ProjectID)
	if err != nil {
		r.fail(err)
		return
	}
	r.p = p

	// 1. Worktree.
	if err := r.ensureWorktree(); err != nil {
		r.fail(err)
		return
	}
	// 2. Bring in the default branch after a merge conflict.
	if err := r.mergeBaseIn(); err != nil {
		r.fail(err)
		return
	}
	// Edits saved in the web editor take the place of Claude's first run.
	editing := len(r.t.Edits) > 0 && !r.t.EditsApplied
	if editing {
		if err := e.applyEdits(ctx, r.t, r.p, r.wt, r.log); err != nil {
			r.fail(err)
			return
		}
	}

	// 3–4. Claude, commit, tests; loop while tests fail and fixes remain.
	for {
		if editing {
			editing = false
		} else if err := r.runClaude(); err != nil {
			r.fail(err)
			return
		}
		next, err := r.commitAndCheck()
		if err != nil {
			r.fail(err)
			return
		}
		if next == stepDone {
			return
		}
		if next == stepHandOff {
			break
		}
	}

	// 5. Hand off.
	r.handOff()
}

// todoRun is one run of TodoEngine.work on task t: the task as last saved,
// its project and worktree, and Claude's latest report.
type todoRun struct {
	e      *TodoEngine
	ctx    context.Context
	t      model.Todo
	p      model.Project
	wt     model.Worktree
	report string // Claude's final message from its latest run
	// resolving is set while Claude resolves conflicts with the default
	// branch: its work isn't committed while markers remain.
	resolving bool
}

func (r *todoRun) log(s string) { r.e.Log.Line(r.t, s) }

// update changes the task in the store and keeps the result.
func (r *todoRun) update(fn func(*model.Todo)) error {
	nt, err := r.e.Store.UpdateTodo(r.t.ID, func(x *model.Todo) error { fn(x); return nil })
	if err == nil {
		r.t = nt
	}
	return err
}

// addNote adds a note for Claude's next run.
func (r *todoRun) addNote(note string) error {
	return r.update(func(x *model.Todo) { x.Feedback = append(x.Feedback, note) })
}

// fail ends the run with err: the task fails, unless Dash is shutting down
// (it resumes on restart) or the user paused it (it is queued again).
func (r *todoRun) fail(err error) {
	e, t := r.e, r.t
	if e.shuttingDown() {
		// Dash is stopping, not the user: leave the todo in progress so
		// it is requeued (and its worktree reused) on the next start.
		r.log("■ interrupted by shutdown; will resume on restart")
		return
	}
	if r.ctx.Err() != nil {
		if cur, err := e.Store.GetTodo(t.ID); err == nil && cur.Paused {
			r.log("⏸ paused")
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
	if why := e.interruption(t.ID); why != "" && r.ctx.Err() != nil {
		msg = "interrupted: " + why
	}
	r.log("✖ " + msg)
	noteFailure(e.Store, t.ID, msg, r.report)
	if e.Merges != nil {
		// A review's fix that failed: say so on the pull request, where
		// the reviewer waits for it.
		e.Merges.reviewFailed(r.ctx, t.ID, msg)
	}
	if _, err := e.Store.SetStatus(t.ID, model.StatusFailed, msg); err != nil {
		slog.Error("set failed", "todo", t.ID, "err", err)
	}
}

// ensureWorktree gives the task its worktree: the one it has, or a new one
// on a new branch from the project's base, set up with prepareWorktree.
func (r *todoRun) ensureWorktree() error {
	e, ctx := r.e, r.ctx
	if r.t.Worktree == nil {
		p := r.p
		// Start from what's on origin when local is merely behind it (a push
		// from another machine); never blocks the start, never rebases.
		if e.Merges != nil && e.Merges.Remote != nil {
			if note := e.Merges.Remote.FastForward(ctx, p); note != "" {
				r.log(note)
			}
		}
		base, err := e.Git.Head(ctx, p.ID, p.Path, p.Base())
		if err != nil {
			return err
		}
		name := "todo-" + r.t.ID
		wt := model.Worktree{Name: name, Path: filepath.Join(e.WorktreeRoot, p.ID, name), Branch: "dash/" + name, BaseCommit: base, BaseBranch: p.Base()}
		if err := r.update(func(x *model.Todo) { x.Worktree = &wt }); err != nil {
			return err
		}
	}
	r.wt = *r.t.Worktree
	r.p = r.p.For(r.t) // the branch it started from, even if the project has switched since
	p, wt := r.p, r.wt
	if _, err := os.Stat(wt.Path); err == nil {
		r.log(fmt.Sprintf("▶ reusing worktree %s", wt.Path))
		return nil
	}
	if err := e.Git.AddWorktree(ctx, p.ID, p.Path, wt); err != nil {
		return err
	}
	r.log(fmt.Sprintf("▶ new worktree %s (%s)", wt.Path, wt.Branch))
	return e.prepareWorktree(ctx, p, wt, r.log)
}

// mergeBaseIn merges the default branch into the worktree when the task
// asks for it (after a merge conflict). Conflicts are left for Claude, with
// a note, and set resolving.
func (r *todoRun) mergeBaseIn() error {
	if !r.t.MergeMain {
		return nil
	}
	e, ctx, p, wt := r.e, r.ctx, r.p, r.wt
	files, err := e.Git.StartMerge(ctx, p.ID, wt.Path, p.Base())
	if err != nil {
		return fmt.Errorf("merge %s into worktree: %w", p.Base(), err)
	}
	if len(files) > 0 {
		r.resolving = true
		r.log("⚠ conflicts with " + p.Base() + ": " + strings.Join(files, ", "))
		if err := r.addNote(conflictNote(p.Base(), files)); err != nil {
			return err
		}
	} else if _, err := e.Git.CommitAll(ctx, p.ID, wt.Path, "Merge "+p.Base()+" into "+wt.Branch); err != nil {
		return err
	}
	return r.update(func(x *model.Todo) { x.MergeMain = false })
}

// claudeArgs adds to req the arguments of a Claude run on task t: limits,
// model, attachments, Dash's MCP servers and the extra system prompt. It
// sets stat's model. release ends the MCP servers' tokens: call it once the
// run is over.
func (e *TodoEngine) claudeArgs(req *agent.ExecRequest, stat *model.TodoRun, t model.Todo, p model.Project, log func(string)) (release func()) {
	var releases []func()
	req.ExtraArgs = append(req.ExtraArgs, "--max-turns", strconv.Itoa(p.TurnLimit()))
	if m, err := model.CleanModel(t.Model); err != nil {
		log("⚠ " + err.Error() + "; running on the default model")
	} else if m != "" {
		stat.Model = m
		// After -claude-args, so it wins over a --model there.
		req.ExtraArgs = append(req.ExtraArgs, "--model", m)
		log("model: " + m)
	}
	for _, dir := range attachmentDirs(t.Attachments) {
		req.ExtraArgs = append(req.ExtraArgs, "--add-dir", dir)
	}
	servers := map[string]any{}
	var system []string // appended to Claude's system prompt
	owner, ownerErr := model.Owner{}, errors.New("no owner resolver")
	if e.Owner != nil {
		owner, ownerErr = e.Owner(t)
	}
	if ownerErr != nil && (e.Perms != nil || e.Board != nil) {
		slog.Warn("task runs without Dash's MCP servers: no user and org to act as", "todo", t.ID, "err", ownerErr)
	}
	if e.Perms != nil && e.BaseURL != "" && ownerErr == nil {
		token, release := e.Perms.Register(owner, t.ID, t.ProjectID)
		releases = append(releases, release)
		servers[perm.ServerName] = perm.ServerConfig(e.BaseURL, token)
		req.ExtraArgs = append(req.ExtraArgs, "--permission-prompt-tool", perm.PromptTool)
		// Claude waits on the MCP tool while a human decides; don't let it time out first.
		req.Env = []string{"MCP_TOOL_TIMEOUT=3600000"}
	}
	if e.Board != nil && e.BoardURL != "" && ownerErr == nil {
		token, release := e.Board.RegisterTask(owner, t.ProjectID, t.ID)
		releases = append(releases, release)
		servers[mcptools.ServerName] = mcptools.ServerConfig(e.BoardURL, token)
		req.ExtraArgs = append(req.ExtraArgs, mcptools.AllowedToolArgs(mcptools.ServerName, mcptools.IssueToolNames...)...)
		system = append(system, issuesHint)
		if e.Notes != nil {
			if n, ok := e.Notes(t); ok {
				servers[mcptools.NotesServerName] = mcptools.NotesServerConfig(e.BoardURL, token)
				req.ExtraArgs = append(req.ExtraArgs, mcptools.AllowedToolArgs(mcptools.NotesServerName, mcptools.NotesToolNames...)...)
				system = append(system, mcptools.NotesHint(n))
			}
		}
	}
	if s := strings.TrimSpace(p.TaskInstructions); s != "" {
		system = append(system, s)
	}
	if len(system) > 0 {
		req.ExtraArgs = append(req.ExtraArgs, "--append-system-prompt", strings.Join(system, "\n\n"))
	}
	req.ExtraArgs = append(req.ExtraArgs, mcptools.ConfigArgs(servers)...)
	return func() {
		for _, r := range releases {
			r()
		}
	}
}

// runClaude runs Claude once on the task and keeps its report.
func (r *todoRun) runClaude() error {
	report, err := r.e.runClaude(r.ctx, &r.t, r.p, r.wt, r.log)
	if err != nil {
		return err
	}
	r.report = report
	if r.t.PR != nil && r.t.PR.Reply != nil {
		_ = r.update(func(x *model.Todo) { keepReviewReport(x, report) }) // posted on GitHub with the fix
	}
	return nil
}

// step is what follows commitAndCheck.
type step int

const (
	stepAgain   step = iota // Claude runs again, with a note on what to fix
	stepHandOff             // the work is ready: hand it off
	stepDone                // the task is finished here (nothing changed)
)

// commitAndCheck commits Claude's work and runs the project's tests on it.
func (r *todoRun) commitAndCheck() (step, error) {
	e, ctx, p, wt := r.e, r.ctx, r.p, r.wt
	if r.resolving {
		markers, err := e.Git.LeftoverMarkers(ctx, p.ID, wt.Path)
		if err != nil {
			return 0, err
		}
		if markers != "" {
			if r.t.AutoFixes >= p.MaxAutoFixes {
				return 0, errors.New("conflict markers remain after Claude's attempt")
			}
			_ = r.update(func(x *model.Todo) { x.AutoFixes++ })
			_ = r.addNote("Some conflict markers are still in the code:\n```\n" + markers + "\n```\nResolve them all.")
			return stepAgain, nil
		}
		r.resolving = false
	}
	msg := r.t.Title
	if msg == "" {
		msg = firstLine(r.t.Prompt)
	}
	if _, err := e.Git.CommitAll(ctx, p.ID, wt.Path, "dash: "+msg); err != nil {
		return 0, err
	}
	editsPending := len(r.t.Edits) > 0 && !r.t.EditsApplied
	if head, err := e.Git.Head(ctx, p.ID, wt.Path, "HEAD"); err != nil {
		return 0, err
	} else if head == wt.BaseCommit && editsPending {
		return 0, errors.New("the saved edit doesn't change the file")
	}
	if editsPending {
		_ = r.update(func(x *model.Todo) { x.EditsApplied = true })
	}
	files, add, del, derr := e.Git.DiffStat(ctx, p.ID, wt.Path, p.Base())
	if derr == nil {
		_ = r.update(func(x *model.Todo) { x.DiffFiles, x.DiffAdd, x.DiffDel = files, add, del })
	}
	if derr == nil && files == 0 {
		// An answer, an investigation or "it already works": nothing to
		// test or merge, but not a failure. Review it; approving closes it.
		r.log("✔ finished without changing any files; ready for review")
		noteNoChanges(e.Store, r.t.ID, r.report)
		if _, err := e.Store.SetStatus(r.t.ID, model.StatusReview, ""); err != nil {
			slog.Error("set review", "todo", r.t.ID, "err", err)
		}
		return stepDone, nil
	}

	if p.TestCommand == "" {
		return stepHandOff, nil
	}
	checks := e.runChecks(ctx, r.t, p, wt.Path, "review")
	_ = r.update(func(x *model.Todo) { x.Checks = &checks })
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if checks.Passed || r.t.AutoFixes >= p.MaxAutoFixes {
		return stepHandOff, nil
	}
	r.log(fmt.Sprintf("↻ tests failed; sending the output back to Claude (fix %d of %d)", r.t.AutoFixes+1, p.MaxAutoFixes))
	_ = r.update(func(x *model.Todo) { x.AutoFixes++ })
	_ = r.addNote(testNote(checks))
	return stepAgain, nil
}

// handOff sends finished work on: to the merge queue (approved or
// auto-approved), to its pull request, or to Review.
func (r *todoRun) handOff() {
	e, t, p := r.e, r.t, r.p
	passed := t.Checks == nil || t.Checks.Passed
	// In "pr" mode the review happens on GitHub: finished work goes straight
	// to its pull request, opened now (or pushed to, after a fix). Work
	// whose tests fail waits in Review without one, unless its pull request
	// is open already: GitHub's checks then say so there.
	toPR := p.UsesPR() && (passed || t.WaitsOnGitHub())
	if (t.AutoMerge || p.AutoApprove || toPR) && (passed || toPR) && e.Merges != nil {
		switch {
		case toPR && t.WaitsOnGitHub():
			r.log(fmt.Sprintf("✔ done; pushing to pull request #%d", t.PR.Number))
		case t.AutoMerge:
			r.log("✔ fixed; back to the merge queue (already approved)")
		case toPR:
			r.log("✔ finished; opening a pull request for review on GitHub")
		default:
			r.log("✔ approved automatically; to the merge queue")
		}
		if !t.AutoMerge {
			_ = r.update(func(x *model.Todo) { x.AutoFixes = 0 }) // like approving: a fresh budget for merge conflicts
		}
		// Counted as queued before it is merging, so a pull request poll in
		// between doesn't take it for one waiting on GitHub.
		e.Merges.track(t.ID, 1)
		defer e.Merges.track(t.ID, -1)
		if _, err := e.Store.SetStatus(t.ID, model.StatusMerging, ""); err == nil {
			if err := e.Merges.Enqueue(model.MergeJob{TodoID: t.ID, ProjectID: t.ProjectID}); err == nil {
				return
			}
			_, _ = e.Store.SetStatus(t.ID, model.StatusReview, "merge queue full")
			return
		}
	}
	if passed {
		r.log("✔ ready for review")
	} else {
		r.log("✖ ready for review, but tests fail")
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
// and records the session, cost, the run's tokens (RunStats) and which notes
// Claude has now seen. It returns Claude's final message.
func (e *TodoEngine) runClaude(ctx context.Context, t *model.Todo, p model.Project, wt model.Worktree, log func(string)) (string, error) {
	req := agent.ExecRequest{Dir: wt.Path}
	unsent := t.Feedback[min(t.FeedbackSent, len(t.Feedback)):]
	stat := model.TodoRun{Kind: model.RunFirst}
	switch {
	case t.SessionID != "" && len(unsent) > 0:
		req.Resume, req.Prompt = t.SessionID, agent.ContinuePrompt(unsent)
		stat.Kind = model.RunNotes
	case t.SessionID != "":
		req.Resume, req.Prompt = t.SessionID, agent.ContinuePrompt([]string{"You were interrupted. Continue where you left off and finish the task."})
		stat.Kind = model.RunInterrupted
	default:
		req.Prompt = agent.BuildPrompt(*t)
		if parent, ok := e.buildsOn(*t, log); ok {
			// A follow-up: continue the earlier task's session in a fork of
			// it, so that session stays as it is.
			req.Resume, req.ForkSession, req.Prompt = parent.LatestSession(), true, agent.FollowUpPrompt(*t, parent)
			log(fmt.Sprintf("▶ building on %q: continuing its Claude session %s in a new one", taskName(parent), req.Resume))
		}
		if t.Runs > 0 {
			stat.Kind = model.RunRetry
		}
	}
	if req.Resume != "" && !req.ForkSession {
		log("▶ resuming Claude session " + req.Resume)
	}
	emit := func(ev model.RunEvent) { e.Log.Event(*t, ev) }
	switch {
	case req.ForkSession:
		emit(model.RunEvent{Kind: "run_start", Text: "Started from an earlier task's session"})
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
	release := e.claudeArgs(&req, &stat, *t, p, log)
	defer release()

	// Stop runaway runs: a time limit that doesn't count time spent waiting
	// for a human to answer a permission prompt, and a turn limit.
	limit := time.Duration(p.RunMinutes()) * e.minute
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	var overTime atomic.Bool
	go e.watchdog(runCtx, t.ID, limit, func() { overTime.Store(true); stop() })

	stat.At = time.Now()
	res, err := e.Exec.Run(runCtx, req, emit)
	if res.ResumeFailed && req.Resume != "" && runCtx.Err() == nil {
		if req.ForkSession {
			log("↻ the Claude session of the task this builds on isn't on this computer (it ran on another runner, or the session was deleted); starting a fresh one with the full task")
		} else {
			log("↻ the previous Claude session is gone; starting a fresh one with the full task")
		}
		emit(model.RunEvent{Kind: "run_start", Text: "Started over with the full task"})
		req.Resume, req.ForkSession, req.Prompt = "", false, agent.BuildPrompt(*t)
		stat.Kind = model.RunRetry
		res, err = e.Exec.Run(runCtx, req, emit)
	}
	if res.Model != "" {
		stat.Model = res.Model
	}
	stat.ChatUsage, stat.Turns, stat.CostUSD = res.Usage, res.Turns, res.CostUSD
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
			x.SessionID, x.LastSessionID = res.SessionID, res.SessionID
		}
		x.CostUSD += res.CostUSD
		x.Runs++
		x.RunStats = append(x.RunStats, stat)
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

// buildsOn is the task t builds on, when t should start from its Claude
// session: t has no session of its own yet and the earlier task has one.
// Otherwise t starts fresh with the full prompt, and log says why.
func (e *TodoEngine) buildsOn(t model.Todo, log func(string)) (model.Todo, bool) {
	if t.BuildsOn == "" || t.SessionID != "" {
		return model.Todo{}, false
	}
	parent, err := e.Store.GetTodo(t.BuildsOn)
	switch {
	case err != nil:
		log("⚠ the task this builds on is gone; starting a fresh Claude session with the full task")
		return model.Todo{}, false
	case parent.LatestSession() == "":
		log(fmt.Sprintf("⚠ %q, which this task builds on, has no Claude session; starting a fresh one with the full task", taskName(parent)))
		return model.Todo{}, false
	}
	return parent, true
}

// taskName is how a log line names task t.
func taskName(t model.Todo) string {
	if t.Title != "" {
		return t.Title
	}
	return firstLine(t.Prompt)
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

// noteNoChanges says in the todo's chat that Claude changed nothing,
// quoting its final message: usually the answer or the reason.
func noteNoChanges(s *store.Store, todoID, report string) {
	now := time.Now()
	msg := "**Claude finished without changing any files.** Approve to close the task, or rework it to ask for changes."
	if report != "" {
		msg += "\n\nClaude's last message:\n\n" + report
	}
	if err := s.AppendTodoChat(todoID, model.ChatMessage{Role: "assistant", Content: msg, At: &now}); err != nil {
		slog.Error("note no changes", "todo", todoID, "err", err)
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
			return fmt.Errorf("%s changed on %s since it was opened in the editor; open it again and redo the edit", ed.Path, p.Base())
		}
		dst := filepath.Join(wt.Path, filepath.FromSlash(ed.Path))
		if ed.Blob == "" { // a new file
			if err := createFile(wt.Path, dst, ed.Content); err != nil {
				return fmt.Errorf("create %s: %w", ed.Path, err)
			}
			log("✎ created " + ed.Path)
			continue
		}
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

// createFile writes a new file at dst inside root, making its folders,
// unless something is there already or a folder on the way leads out of root.
func createFile(root, dst, content string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(dst))
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(realRoot, dir); err != nil || !filepath.IsLocal(rel) && rel != "." {
		return errors.New("its folder is outside the working tree")
	}
	f, err := os.OpenFile(filepath.Join(dir, filepath.Base(dst)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
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
	return runChecks(ctx, e.Git, e.Log, t, p, dir, stage, e.TestTimeout)
}

func runChecks(ctx context.Context, g *gitops.Git, l *TodoLog, t model.Todo, p model.Project, dir, stage string, timeout time.Duration) model.Checks {
	l.Line(t, "▶ tests: "+p.TestCommand)
	commit := checkedCommit(ctx, g, p.ID, dir)
	start := time.Now()
	code, tail, err := runShell(ctx, dir, p.TestCommand, timeout, nil, func(s string) { l.Line(t, "│ "+s) })
	c := model.Checks{Command: p.TestCommand, ExitCode: code, Output: tail, Stage: stage, At: time.Now(), Duration: time.Since(start).Seconds()}
	if commit != "" {
		if after, herr := g.Head(ctx, p.ID, dir, "HEAD"); herr == nil && after == commit {
			c.Commit = commit // nothing committed while they ran
		}
	}
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

// checkedCommit is the commit checked out in dir when it has no uncommitted
// changes to tracked files, so checks run there test exactly that commit;
// otherwise "".
func checkedCommit(ctx context.Context, g *gitops.Git, projectID, dir string) string {
	if g == nil {
		return ""
	}
	if files, err := g.CheckoutChanges(ctx, projectID, dir); err != nil || len(files) > 0 {
		return ""
	}
	head, err := g.Head(ctx, projectID, dir, "HEAD")
	if err != nil {
		return ""
	}
	return head
}

// checksPassedOn reports whether c passed on commit, so running them again
// before landing it would test the same code.
func checksPassedOn(c *model.Checks, command, commit string) bool {
	return c != nil && c.Passed && c.Command == command && c.Commit != "" && c.Commit == commit
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

// firstLine is s's first line, cut to 72 bytes: a commit subject.
func firstLine(s string) string { return textutil.Clip(textutil.FirstLine(s), 72) }

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
