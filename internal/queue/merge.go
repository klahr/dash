package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/textutil"
)

var ErrMergeQueueFull = errors.New("merge queue is full")

// MergeQueue lands approved todos on their projects' default branches. Each
// project has its own merge worker (a lane), so a project's merges happen
// one at a time while a long test run in one project never holds up merges
// in the others. Merges happen in a checkout only Dash uses (see land), so
// work in the project's own folder can't break them.
//
// Per job:
//  0. when the project pushes to or opens PRs on GitHub, sync the default
//     branch with origin first
//  1. merge the default branch into the todo's worktree. A conflict sends
//     the todo back to Claude to resolve (auto-fix), then it returns here
//     without another review.
//  2. run the project's test command on the result, unless it already
//     passed on exactly that commit (the review's run, when the default
//     branch brought in nothing); failures go back to Claude the same way,
//     so a red build never lands
//  3. land it: squash the branch onto the default branch (one commit per
//     todo) and push, or in "pr" mode push the branch and open a pull
//     request, which PollPRs follows until GitHub merges it. In a project
//     shared through origin (a runner's clone of its hub's project) the
//     squash is pushed before the default branch moves, and the todo is
//     merged only once origin has it; when origin moved meanwhile, steps
//     0–3 run again on top of it (see landPushed)
//
// When landing would overwrite uncommitted changes in the project's folder,
// the job waits until those files are committed or stashed (see wait). In
// a runner's checkout of its hub's project, any uncommitted change to a
// tracked file there makes it wait (see checkoutClean).
//
// A project's worker also reverts its merged todos, follows its pull
// requests and runs its cleanups, since those move the default branch or
// remove worktrees too.
type MergeQueue struct {
	Store       *store.Store
	Git         *gitops.Git
	Pub         Publisher
	Log         *TodoLog
	Perms       *perm.Broker
	Kick        func() // wake the todo engine after sending a todo back or merging one
	TestTimeout time.Duration
	// PRPoll is how often open pull requests are checked (0 = never).
	PRPoll time.Duration
	// Cleanup settings; see cleanup.go.
	WorktreeRoot string
	UploadRoot   string
	CleanupAfter time.Duration // remove failed todos' worktrees untouched this long (0 = never)
	CleanupEvery time.Duration // run a cleanup of every project this often (0 = only on request)
	// Remote fetches origin in the background and owns the per-project lock
	// every fetch, push and move of the default branch here takes.
	Remote *RemoteSync
	// OnPushed is told when origin's branch now points at sha because Dash
	// moved it here: a push, or a pull request merged (optional). A runner
	// tells its hub, which tells the project's other runners.
	OnPushed func(p model.Project, branch, sha string)

	// beforePush, when set (tests), runs just before a shared project's
	// merge is pushed, e.g. to push to origin first.
	beforePush func()
	// dirtyPoll is how often a job waiting for uncommitted changes checks
	// the files again (default 5 seconds; shortened in tests).
	dirtyPoll time.Duration

	capacity int
	pending  atomic.Int64
	running  sync.WaitGroup // Run and the lanes' workers, while they run (see Wait)
	workers  sync.WaitGroup // the lanes' workers only: Run waits for them before it returns

	lanesMu sync.Mutex
	lanes   map[string]*lane // project ID → its merge worker
	ctx     context.Context  // Run's; nil until Run starts
	stopped bool             // Run returned: no new workers start
	filesMu sync.Mutex       // cleanupFiles, which looks at every project
	waiting sync.Map         // todo ID → a goroutine watching for its files to be committed

	queuedMu sync.Mutex
	queued   map[string]int // todo ID → its merge jobs queued or running
}

type mergeItem struct {
	job model.MergeJob
	fn  func(context.Context) // other work that moves the default branch or removes worktrees
	// quiet work (a timer's) isn't counted in Pending.
	quiet bool
}

// lane is one project's merge worker and its queue.
type lane struct {
	jobs     chan mergeItem
	polling  atomic.Bool // a pull request poll is queued or running
	cleaning atomic.Bool // a timed cleanup is queued or running
}

func NewMergeQueue(s *store.Store, g *gitops.Git, pub Publisher, log *TodoLog, capacity int) *MergeQueue {
	return &MergeQueue{Store: s, Git: g, Pub: pub, Log: log, TestTimeout: 20 * time.Minute, PRPoll: time.Minute,
		Remote: &RemoteSync{Store: s, Git: g, Pub: pub}, capacity: capacity}
}

// Enqueue adds a job to its project's queue without blocking the caller
// (an HTTP handler).
func (m *MergeQueue) Enqueue(job model.MergeJob) error {
	return m.push(job.ProjectID, mergeItem{job: job})
}

// Wait waits, up to timeout, for Run and every project's worker to return
// once their context ended (the merges in hand stopped and left to resume),
// and reports whether they did.
func (m *MergeQueue) Wait(timeout time.Duration) bool {
	return waitGroup(&m.running, timeout)
}

// waitGroup waits for wg up to timeout and reports whether it finished.
func waitGroup(wg *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Resume queues again the merges a previous process was running or had
// queued when it stopped, so an approval survives a restart. A todo whose
// pull request is open is followed by the PR poller instead.
func (m *MergeQueue) Resume() {
	for _, t := range m.Store.ListTodos("", model.StatusMerging) {
		if (t.PR != nil && t.PR.State == "OPEN") || t.OnRunner() {
			continue
		}
		if err := m.Enqueue(model.MergeJob{TodoID: t.ID, ProjectID: t.ProjectID}); err != nil {
			_, _ = m.Store.SetStatus(t.ID, model.StatusReview, "merge queue full")
		}
	}
}

// push queues it on project projectID's worker. Each project's queue holds
// up to capacity jobs.
func (m *MergeQueue) push(projectID string, it mergeItem) error {
	l := m.lane(projectID)
	m.track(it.job.TodoID, 1)
	if !it.quiet {
		m.pending.Add(1) // before the worker can take it and count it done
	}
	select {
	case l.jobs <- it:
		if !it.quiet {
			m.publish("")
		}
		return nil
	default:
		m.track(it.job.TodoID, -1)
		if !it.quiet {
			m.pending.Add(-1)
		}
		return ErrMergeQueueFull
	}
}

// lane is project id's worker, made (and started, once Run runs) on first use.
func (m *MergeQueue) lane(id string) *lane {
	m.lanesMu.Lock()
	defer m.lanesMu.Unlock()
	if l := m.lanes[id]; l != nil {
		return l
	}
	if m.lanes == nil {
		m.lanes = map[string]*lane{}
	}
	l := &lane{jobs: make(chan mergeItem, max(m.capacity, 1))}
	m.lanes[id] = l
	if m.ctx != nil && !m.stopped {
		m.start(l)
	}
	return l
}

// start runs l's worker; lanesMu is held.
func (m *MergeQueue) start(l *lane) {
	m.running.Add(1)
	m.workers.Add(1)
	go func() {
		defer m.running.Done()
		defer m.workers.Done()
		for {
			select {
			case <-m.ctx.Done():
				return
			case it := <-l.jobs:
				m.handle(m.ctx, it)
			}
		}
	}()
}

// later queues fn as quiet work on project id's worker, unless the work
// flag says (and then holds) is already queued there.
func (m *MergeQueue) later(id string, flag func(*lane) *atomic.Bool, fn func(context.Context)) {
	busy := flag(m.lane(id))
	if !busy.CompareAndSwap(false, true) {
		return
	}
	if m.push(id, mergeItem{quiet: true, fn: func(c context.Context) { defer busy.Store(false); fn(c) }}) != nil {
		busy.Store(false)
	}
}

// track counts a todo's merge jobs queued or running (id "" is other work).
func (m *MergeQueue) track(id string, d int) {
	if id == "" {
		return
	}
	m.queuedMu.Lock()
	defer m.queuedMu.Unlock()
	if m.queued == nil {
		m.queued = map[string]int{}
	}
	if m.queued[id] += d; m.queued[id] <= 0 {
		delete(m.queued, id)
	}
}

// inQueue reports a merge job for todo id queued or running: its branch may
// not be pushed yet, so GitHub's view of its pull request is out of date.
func (m *MergeQueue) inQueue(id string) bool {
	m.queuedMu.Lock()
	defer m.queuedMu.Unlock()
	return m.queued[id] > 0
}

// Do runs fn on project projectID's merge worker, after the jobs already
// queued there, and waits.
func (m *MergeQueue) Do(ctx context.Context, projectID string, fn func(context.Context)) error {
	done := make(chan struct{})
	if err := m.push(projectID, mergeItem{fn: func(c context.Context) { defer close(done); fn(c) }}); err != nil {
		return err
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *MergeQueue) Pending() int { return int(m.pending.Load()) }

// Run starts the projects' merge workers and follows pull requests and
// timed cleanups, handing each project's to its worker. Start exactly one;
// it returns once ctx ends and every worker has stopped.
func (m *MergeQueue) Run(ctx context.Context) {
	m.running.Add(1)
	defer m.running.Done()
	m.lanesMu.Lock()
	m.ctx = ctx
	for _, l := range m.lanes {
		m.start(l) // the jobs queued before Run (Resume)
	}
	m.lanesMu.Unlock()
	poll := tickerOrNil(m.PRPoll)
	clean := tickerOrNil(m.CleanupEvery)
	for {
		select {
		case <-ctx.Done():
			m.lanesMu.Lock()
			m.stopped = true
			m.lanesMu.Unlock()
			// The merges in hand stop and are left to resume; wait for
			// them, so that once Run returns no worker is still running
			// git in a checkout (callers may remove it next).
			m.workers.Wait()
			return
		case <-poll:
			m.schedulePRPolls()
		case <-clean:
			for _, p := range m.Store.ListProjects() {
				m.later(p.ID, func(l *lane) *atomic.Bool { return &l.cleaning }, func(c context.Context) {
					p, err := m.Store.GetProject(p.ID)
					if err != nil {
						return // deleted meanwhile
					}
					if r, err := m.cleanup(c, p); err != nil {
						slog.Warn("cleanup", "project", p.Name, "err", err)
					} else if !r.Empty() {
						slog.Info("cleanup", "project", p.Name, "report", r.String())
					}
				})
			}
		}
	}
}

// handle runs one item on its project's worker.
func (m *MergeQueue) handle(ctx context.Context, it mergeItem) {
	if it.fn != nil {
		it.fn(ctx)
		if !it.quiet {
			m.pending.Add(-1)
			m.publish("")
		}
		return
	}
	job := it.job
	m.publish(job.TodoID)
	if job.Revert {
		m.revert(ctx, job)
	} else if err := m.process(ctx, job); err != nil && ctx.Err() != nil {
		// Dash is stopping, not the merge failing: the todo stays
		// merging and Resume queues it again at the next start.
		// Nothing counts until origin has it (or the default branch
		// moved to it), so the merge starts over cleanly.
		if t, gerr := m.Store.GetTodo(job.TodoID); gerr == nil && t.Status == model.StatusMerging {
			m.Log.Line(t, "■ merge interrupted by shutdown; will resume on restart")
		}
	} else if errors.As(err, new(*gitops.ErrDirtyCheckout)) {
		m.wait(ctx, job, err)
	} else if err != nil {
		slog.Warn("merge failed", "todo", job.TodoID, "err", err)
		if t, gerr := m.Store.GetTodo(job.TodoID); gerr == nil {
			m.Log.Line(t, "✖ merge: "+err.Error())
		}
		var se *syncError
		var pe *prError
		if errors.As(err, &se) {
			// The work is fine; GitHub isn't reachable or main diverged. Approve again once fixed.
			_, _ = m.Store.SetStatus(job.TodoID, model.StatusReview, "Not merged, can't sync with GitHub: "+brief(err))
		} else if errors.As(err, &pe) {
			// The work is fine; GitHub (or gh) isn't. Approving tries again.
			_, _ = m.Store.SetStatus(job.TodoID, model.StatusReview, "Couldn't open the pull request: "+brief(err)+". Approve to try again")
		} else {
			noteFailure(m.Store, job.TodoID, "merge: "+err.Error(), "")
			m.reviewFailed(ctx, job.TodoID, "merge: "+err.Error())
			_, _ = m.Store.SetStatus(job.TodoID, model.StatusFailed, "merge: "+err.Error())
		}
	}
	m.track(job.TodoID, -1)
	m.pending.Add(-1)
	m.publish("")
}

func (m *MergeQueue) process(ctx context.Context, job model.MergeJob) error {
	t, err := m.Store.GetTodo(job.TodoID)
	if err != nil {
		return err
	}
	if t.Status != model.StatusMerging || t.Worktree == nil || t.OnRunner() {
		return fmt.Errorf("todo %s not mergeable here (status %s)", t.ID, t.Status)
	}
	p, err := m.Store.GetProject(t.ProjectID)
	if err != nil {
		return err
	}
	p = p.For(t)
	wt := *t.Worktree
	log := func(s string) { m.Log.Line(t, s) }
	log("▶ merging into " + p.Base())
	// A merge cut off by a shutdown or crash starts over from the todo's
	// own commits.
	if m.Git.AbortStaleMerge(ctx, p.ID, wt.Path) {
		log("↺ undid a merge of " + p.Base() + " left unfinished; starting it over")
	}

	var conflict *gitops.ErrConflict
	// A shared project's merge counts once origin has it; when origin moves
	// between the sync and the push (another runner, someone's push), the
	// whole merge runs again on top of origin's new tip.
	for attempt := 1; ; attempt++ {
		// 0. Start from what's on GitHub, so the push (or the PR) can't be rejected.
		p = m.refreshRemote(ctx, p).For(t)
		if p.SyncsRemote() {
			if err := m.syncRemote(ctx, p); err != nil {
				return err
			}
		}

		// 1–2. Repeated (a few times) when the default branch moves meanwhile,
		// e.g. a commit in the project's folder while the tests ran.
		for range 3 {
			base, err := m.Git.Head(ctx, p.ID, p.Path, p.Base())
			if err != nil {
				return err
			}

			// 1. Bring the default branch into the todo's branch.
			changed, err := m.Git.MergeIn(ctx, p.ID, wt.Path, p.Base())
			if errors.As(err, &conflict) {
				log("⚠ " + conflict.Error())
				return m.sendBack(t, p, "", true, "conflicts with "+p.Base()+" in "+strings.Join(conflict.Files, ", "))
			}
			if err != nil {
				return err
			}

			// 2. Test what lands, unless the tests already passed on exactly
			// this commit: a red build goes back to the todo, not onto the
			// default branch.
			if p.TestCommand != "" {
				head, err := m.Git.Head(ctx, p.ID, wt.Path, "HEAD")
				if err != nil {
					return err
				}
				if !checksPassedOn(t.Checks, p.TestCommand, head) {
					checks := runChecks(ctx, m.Git, m.Log, t, p, wt.Path, "merge", m.TestTimeout)
					if ctx.Err() != nil {
						return ctx.Err() // tests cut off by a shutdown haven't failed
					}
					if t, err = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error { x.Checks = &checks; return nil }); err != nil {
						return err
					}
					if !checks.Passed {
						if changed {
							note := fmt.Sprintf("Your change was merged with the latest `%s`, and now the tests fail. %s", p.Base(), testNote(checks))
							return m.sendBack(t, p, note, false, "tests fail after merging "+p.Base())
						}
						note := "Your change was approved, but the tests fail, so it wasn't merged. " + testNote(checks)
						return m.sendBack(t, p, note, false, "tests fail; not merged")
					}
				}
			}

			if head, err := m.Git.Head(ctx, p.ID, p.Path, p.Base()); err != nil || head == base {
				break
			}
			log("↻ " + p.Base() + " moved during the merge; merging it in again")
		}

		// 3. Land it; a task that changed nothing is simply closed (there is no
		// commit to squash, and GitHub refuses a pull request without one).
		if files, _, _, err := m.Git.DiffStat(ctx, p.ID, wt.Path, p.Base()); err == nil && files == 0 {
			m.landed(ctx, t, p, "", "✔ closed; nothing to merge")
			return nil
		}
		// A pull request opened before the project switched to merging
		// locally is followed until it closes, rather than landed twice.
		if p.UsesPR() || (t.PR != nil && t.PR.State == "OPEN") {
			return m.openPR(ctx, t, p)
		}
		if !p.LandsOnOrigin() {
			break
		}
		sha, err := m.landPushed(ctx, p, func(dir string) (string, error) {
			return m.Git.SquashInto(ctx, p.ID, dir, wt.Branch, commitMessage(t))
		})
		if errors.As(err, &conflict) {
			log("⚠ " + conflict.Error())
			return m.sendBack(t, p, "", true, "conflicts with "+p.Base()+" in "+strings.Join(conflict.Files, ", "))
		}
		var moved *originMoved
		if errors.As(err, &moved) {
			if attempt < maxLandAttempts {
				log("↻ " + moved.Error() + "; merging again on top of it")
				continue
			}
			return &syncError{fmt.Errorf("%w %d times in a row; approve again to retry", moved, attempt)}
		}
		if err != nil {
			return err
		}
		m.landed(ctx, t, p, sha, "✔ merged into "+p.Base()+" and pushed")
		return nil
	}

	var sha string
	_, err = m.land(ctx, p, func(dir string) (err error) {
		sha, err = m.Git.SquashInto(ctx, p.ID, dir, wt.Branch, commitMessage(t))
		return err
	})
	if errors.As(err, &conflict) {
		log("⚠ " + conflict.Error())
		return m.sendBack(t, p, "", true, "conflicts with "+p.Base()+" in "+strings.Join(conflict.Files, ", "))
	}
	if errors.As(err, new(*gitops.ErrDirtyCheckout)) {
		return err
	}
	if err != nil {
		return fmt.Errorf("squash onto %s: %w", p.Base(), err)
	}
	// Push before marking it merged; a sync during the push may rebase the commit.
	if p.AutoPush && p.RemoteURL != "" {
		m.pushDefault(ctx, p, t)
		if head, err := m.Git.Head(ctx, p.ID, p.Path, p.Base()); err == nil && sha != "" {
			sha = head
		}
	}
	m.landed(ctx, t, p, sha, "✔ merged into "+p.Base())
	return nil
}

// maxLandAttempts is how many times a shared project's merge (or revert) is
// redone on top of origin when origin keeps moving before the push.
const maxLandAttempts = 5

// originMoved is a push rejected because origin's branch moved on since
// the merge started.
type originMoved struct{ branch string }

func (e *originMoved) Error() string { return "origin's " + e.branch + " moved on before the push" }

// landPushed is land for a shared project (model.Project.LandsOnOrigin):
// fn commits in Dash's own checkout of the default branch and returns the
// commit, which is pushed to origin before the default branch moves to it,
// so a merge counts only once origin has it. A push rejected because origin
// moved returns *originMoved (nothing changed; merge again on top of it),
// any other failure a *syncError.
func (m *MergeQueue) landPushed(ctx context.Context, p model.Project, fn func(dir string) (string, error)) (string, error) {
	unlock, err := m.Remote.Lock(ctx, p.ID)
	if err != nil {
		return "", err
	}
	defer unlock()
	if err := m.checkoutClean(ctx, p); err != nil {
		return "", err // before the push: nothing changes until they're committed
	}
	old, err := m.Git.Head(ctx, p.ID, p.Path, p.Base())
	if err != nil {
		return "", err
	}
	dir := m.landingDir(p)
	if err := m.Git.LandingCheckout(ctx, p.ID, p.Path, dir, old); err != nil {
		return "", err
	}
	sha, err := fn(dir)
	if err != nil || sha == "" {
		return sha, err
	}
	if m.beforePush != nil {
		m.beforePush()
	}
	if err := m.Git.PushHead(ctx, p.ID, dir, p.Base()); err != nil {
		if m.originMovedFrom(ctx, p, old) {
			return "", &originMoved{branch: p.Base()}
		}
		m.setSyncError(p.ID, err)
		return "", &syncError{fmt.Errorf("can't push %s to GitHub: %w", p.Base(), err)}
	}
	m.setSyncError(p.ID, nil)
	m.pushed(p, p.Base(), sha)
	// Pushed: the default branch here follows. Should that fail (files in
	// the way in the project's folder), origin has the commit anyway and
	// the next sync brings it in.
	if err := m.Git.Advance(ctx, p.ID, p.Path, p.Base(), old, sha); err != nil {
		slog.Warn("move the default branch after pushing", "project", p.Name, "err", err)
	}
	m.Remote.recount(ctx, p)
	return sha, nil
}

// originMovedFrom fetches origin's branch and reports whether it has
// commits old doesn't: then a push from old was rejected for that.
func (m *MergeQueue) originMovedFrom(ctx context.Context, p model.Project, old string) bool {
	fctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := m.Git.FetchBranch(fctx, p.ID, p.Path, p.Base(), false); err != nil {
		return false
	}
	tip, err := m.Git.Head(ctx, p.ID, p.Path, "refs/remotes/origin/"+p.Base())
	if err != nil || tip == old {
		return false
	}
	mb, err := m.Git.MergeBase(ctx, p.ID, p.Path, tip, old)
	return err == nil && mb != tip
}

// landed marks a todo merged and removes its worktree.
func (m *MergeQueue) landed(ctx context.Context, t model.Todo, p model.Project, sha, msg string) {
	if _, err := m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		now := time.Now()
		x.Status, x.Error, x.MergedCommit, x.FinishedAt = model.StatusMerged, "", sha, &now
		if x.PR != nil {
			x.PR.State = "MERGED"
		}
		return nil
	}); err != nil {
		slog.Error("mark merged", "todo", t.ID, "err", err)
		return
	}
	m.Log.Line(t, msg)
	if m.Kick != nil {
		m.Kick() // todos waiting on this one may start now
	}
	if t.Worktree != nil {
		if err := m.Git.RemoveWorktree(ctx, p.ID, p.Path, *t.Worktree); err != nil {
			slog.Warn("remove worktree", "todo", t.ID, "err", err)
		}
	}
	if m.Perms != nil {
		m.Perms.Forget(t.ID)
	}
}

// refreshRemote picks up an origin added (or changed) after the project was created.
func (m *MergeQueue) refreshRemote(ctx context.Context, p model.Project) model.Project {
	if url := m.Git.RemoteURL(ctx, p.ID, p.Path); url != p.RemoteURL {
		if np, err := m.Store.UpdateProject(p.ID, func(x *model.Project) error { x.RemoteURL = url; return nil }); err == nil {
			return np
		}
	}
	return p
}

// land runs fn in Dash's own checkout of the default branch, then moves the
// branch to whatever fn committed there. The project's folder follows along
// when it has the branch checked out, unless that would overwrite
// uncommitted changes there (*gitops.ErrDirtyCheckout; nothing moves).
// It holds the project's remote lock, so a background fetch or a new todo's
// fast-forward never runs at the same time.
func (m *MergeQueue) land(ctx context.Context, p model.Project, fn func(dir string) error) (string, error) {
	unlock, err := m.Remote.Lock(ctx, p.ID)
	if err != nil {
		return "", err
	}
	defer unlock()
	if err := m.checkoutClean(ctx, p); err != nil {
		return "", err
	}
	old, err := m.Git.Head(ctx, p.ID, p.Path, p.Base())
	if err != nil {
		return "", err
	}
	dir := m.landingDir(p)
	if err := m.Git.LandingCheckout(ctx, p.ID, p.Path, dir, old); err != nil {
		return "", err
	}
	if err := fn(dir); err != nil {
		return "", err
	}
	head, err := m.Git.Head(ctx, p.ID, dir, "HEAD")
	if err != nil || head == old {
		return head, err
	}
	if err := m.Git.Advance(ctx, p.ID, p.Path, p.Base(), old, head); err != nil {
		return head, err
	}
	m.Remote.checklistMoved(ctx, p) // a project that doesn't sync never recounts
	return head, nil
}

// checkoutClean refuses, with an *gitops.ErrDirtyCheckout, to move the
// branch checked out in the folder of a project that guards its checkout
// (model.Project.GuardsCheckout: a runner's checkout of its hub's project,
// maybe one it adopted that its owner also works in) while that folder has
// uncommitted changes to tracked files, even ones the move wouldn't touch.
// The job then waits for them to be committed or stashed (see wait); they
// are never overwritten. A folder with another branch checked out, or
// detached, isn't moved by landing, so it doesn't matter.
func (m *MergeQueue) checkoutClean(ctx context.Context, p model.Project) error {
	if !p.GuardsCheckout() || m.Git.CheckedOut(ctx, p.ID, p.Path) != p.Base() {
		return nil
	}
	files, err := m.Git.CheckoutChanges(ctx, p.ID, p.Path)
	if err != nil || len(files) == 0 {
		return nil // e.g. no commit yet: Advance still refuses to overwrite anything
	}
	return &gitops.ErrDirtyCheckout{Dir: p.Path, Files: files}
}

func (m *MergeQueue) landingDir(p model.Project) string {
	return filepath.Join(m.WorktreeRoot, p.ID, "_land")
}

// wait parks a job whose landing would overwrite uncommitted changes in the
// project's folder, and queues it again once those files are clean.
func (m *MergeQueue) wait(ctx context.Context, job model.MergeJob, err error) {
	var dirty *gitops.ErrDirtyCheckout
	errors.As(err, &dirty)
	what := "your uncommitted changes"
	if len(dirty.Files) > 0 {
		what = "your changes to " + strings.Join(dirty.Files, ", ")
	}
	t, uerr := m.Store.UpdateTodo(job.TodoID, func(x *model.Todo) error {
		x.Error = "Waiting for you to commit or stash " + what + " in " + dirty.Dir
		return nil
	})
	if uerr != nil {
		return
	}
	m.Log.Line(t, "⏸ "+dirty.Error()+"; continuing once they're committed or stashed")
	if _, busy := m.waiting.LoadOrStore(job.TodoID, true); busy {
		return
	}
	go func() {
		defer m.waiting.Delete(job.TodoID)
		poll := m.dirtyPoll
		if poll <= 0 {
			poll = 5 * time.Second
		}
		tick := time.NewTicker(poll)
		defer tick.Stop()
		since := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			t, err := m.Store.GetTodo(job.TodoID)
			if err != nil || (job.Revert && (t.Status != model.StatusMerged || t.Revert != nil)) ||
				(!job.Revert && t.Status != model.StatusMerging) {
				return // cancelled, or handled some other way
			}
			if len(dirty.Files) > 0 {
				if left, err := m.Git.Dirty(ctx, t.ProjectID, dirty.Dir, dirty.Files); err != nil || len(left) > 0 {
					continue
				}
			} else if time.Since(since) < time.Minute {
				continue
			}
			_, _ = m.Store.UpdateTodo(job.TodoID, func(x *model.Todo) error { x.Error = ""; return nil })
			if m.Enqueue(job) == nil {
				return
			}
		}
	}()
}

// syncRemote fast-forwards (or rebases) the default branch onto origin's and
// records failures on the project, where the UI shows them.
func (m *MergeQueue) syncRemote(ctx context.Context, p model.Project) error {
	_, err := m.land(ctx, p, func(dir string) error { return m.Git.SyncWithRemote(ctx, p.ID, dir, p.Base()) })
	if errors.As(err, new(*gitops.ErrDirtyCheckout)) {
		return err
	}
	m.setSyncError(p.ID, err)
	m.Remote.recount(ctx, p)
	if err != nil {
		return &syncError{fmt.Errorf("can't sync %s with GitHub: %w", p.Base(), err)}
	}
	return nil
}

type syncError struct{ error }

// brief is the most telling line of a git error, for the UI.
func brief(err error) string {
	lines := strings.Split(strings.TrimSpace(err.Error()), "\n")
	for _, l := range lines {
		for _, p := range []string{"fatal: ", "error: ", "! ["} {
			if i := strings.Index(l, p); i >= 0 {
				return strings.TrimSpace(l[i:])
			}
		}
	}
	return lines[0]
}

func (e *syncError) Unwrap() error { return e.error }

// pushDefault pushes the default branch after a merge, syncing and retrying
// once if origin moved meanwhile. A failure doesn't undo the local merge.
func (m *MergeQueue) pushDefault(ctx context.Context, p model.Project, t model.Todo) {
	if err := m.pushSynced(ctx, p); err != nil {
		m.Log.Line(t, "⚠ push failed (merged locally; Dash pushes again after the next merge): "+err.Error())
		return
	}
	m.Log.Line(t, "⇡ pushed "+p.Base())
}

// PushDefault pushes the default branch on the project's merge worker
// (after the merges already queued there), first bringing in origin's new commits if origin
// moved on, so a push rejected as non-fast-forward is retried once synced.
func (m *MergeQueue) PushDefault(ctx context.Context, p model.Project) error {
	var err error
	if derr := m.Do(ctx, p.ID, func(c context.Context) { err = m.pushSynced(c, p) }); derr != nil {
		return derr
	}
	return err
}

// pushSynced pushes the default branch, syncing with origin and retrying
// once if the push fails, and records the outcome on the project.
func (m *MergeQueue) pushSynced(ctx context.Context, p model.Project) error {
	push := func() error {
		return m.Remote.With(ctx, p.ID, func() error { return m.Git.Push(ctx, p.ID, p.Path, p.Base()) })
	}
	err := push()
	if err != nil {
		if err = m.syncRemote(ctx, p); err == nil {
			err = push()
		}
	}
	if err == nil {
		if head, herr := m.Git.Head(ctx, p.ID, p.Path, p.Base()); herr == nil {
			m.pushed(p, p.Base(), head)
		}
	}
	m.setSyncError(p.ID, err)
	m.Remote.recount(ctx, p)
	return err
}

func (m *MergeQueue) pushed(p model.Project, branch, sha string) {
	if m.OnPushed != nil && sha != "" {
		m.OnPushed(p, branch, sha)
	}
}

func (m *MergeQueue) setSyncError(projectID string, err error) {
	msg := ""
	if err != nil {
		msg = brief(err)
	}
	if p, gerr := m.Store.GetProject(projectID); gerr == nil && p.SyncError == msg {
		return
	}
	_, _ = m.Store.UpdateProject(projectID, func(p *model.Project) error { p.SyncError = msg; return nil })
}

// sendBack returns an approved todo to Claude to fix a conflict or failing
// tests. Once fixed it goes straight back to the merge queue (AutoMerge).
// Without fixes left it fails and waits for a human.
func (m *MergeQueue) sendBack(t model.Todo, p model.Project, note string, mergeMain bool, why string) error {
	if t.AutoFixes >= p.MaxAutoFixes {
		noteFailure(m.Store, t.ID, fmt.Sprintf("%s, and Claude has used its %d automatic fixes — send it back for rework to let Claude fix it", why, p.MaxAutoFixes), "")
		_, err := m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
			if note != "" {
				x.Feedback = append(x.Feedback, note)
			}
			x.MergeMain = x.MergeMain || mergeMain // a rework will resolve it
			x.Status = model.StatusFailed
			x.Error = why + " — send it back for rework to let Claude fix it"
			return nil
		})
		return err
	}
	m.Log.Line(t, fmt.Sprintf("↻ %s; sending back to Claude (fix %d of %d)", why, t.AutoFixes+1, p.MaxAutoFixes))
	_, err := m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		if note != "" {
			x.Feedback = append(x.Feedback, note)
		}
		x.MergeMain = x.MergeMain || mergeMain
		x.AutoMerge = true
		x.SentBack = why
		x.AutoFixes++
		x.Status = model.StatusQueued
		x.Error = ""
		return nil
	})
	if err == nil && m.Kick != nil {
		m.Kick()
	}
	return err
}

func commitMessage(t model.Todo) string {
	subject := t.Title
	if subject == "" {
		subject = firstLine(t.Prompt)
	}
	body := textutil.Clip(strings.TrimSpace(t.Prompt), 2000)
	return fmt.Sprintf("%s\n\n%s\n\nDash-Todo: %s", subject, body, t.ID)
}

func tickerOrNil(d time.Duration) <-chan time.Time {
	if d <= 0 {
		return nil
	}
	return time.NewTicker(d).C
}

// publish tells every dashboard how many merges are queued, in all
// projects together. The count is shared by all orgs; which task is being merged is not said, as it may be
// another org's (its todo.updated events tell its own org).
func (m *MergeQueue) publish(current string) {
	_ = current
	m.Pub.Publish(model.Event{Type: model.EvMergeQueue, Time: time.Now(), Data: map[string]any{
		"pending": m.pending.Load(),
	}})
}
