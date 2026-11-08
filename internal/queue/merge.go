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
)

var ErrMergeQueueFull = errors.New("merge queue is full")

// MergeQueue lands approved todos on the default branch one at a time. A
// single goroutine drains the channel, so there are never two merges in
// flight. Merges happen in a checkout only Dash uses (see land), so work in
// the project's own folder can't break them.
//
// Per job:
//  0. when the project pushes to or opens PRs on GitHub, sync the default
//     branch with origin first
//  1. merge the default branch into the todo's worktree. A conflict sends
//     the todo back to Claude to resolve (auto-fix), then it returns here
//     without another review.
//  2. if that brought in new commits, run the tests again; failures go back
//     to Claude the same way
//  3. land it: squash the branch onto the default branch (one commit per
//     todo) and push, or in "pr" mode push the branch and open a pull
//     request, which PollPRs follows until GitHub merges it
//
// When landing would overwrite uncommitted changes in the project's folder,
// the job waits until those files are committed or stashed (see wait).
//
// The same worker also reverts merged todos and runs cleanups, since those
// move the default branch too.
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

	jobs    chan mergeItem
	pending atomic.Int64
	waiting sync.Map // todo ID → a goroutine watching for its files to be committed
}

type mergeItem struct {
	job model.MergeJob
	fn  func(context.Context) // other work that moves the default branch or removes worktrees
}

func NewMergeQueue(s *store.Store, g *gitops.Git, pub Publisher, log *TodoLog, capacity int) *MergeQueue {
	return &MergeQueue{Store: s, Git: g, Pub: pub, Log: log, TestTimeout: 20 * time.Minute, PRPoll: time.Minute,
		Remote: &RemoteSync{Store: s, Git: g, Pub: pub},
		jobs:   make(chan mergeItem, capacity)}
}

// Enqueue adds a job without blocking the caller (an HTTP handler).
func (m *MergeQueue) Enqueue(job model.MergeJob) error {
	return m.push(mergeItem{job: job})
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

func (m *MergeQueue) push(it mergeItem) error {
	select {
	case m.jobs <- it:
		m.pending.Add(1)
		m.publish("")
		return nil
	default:
		return ErrMergeQueueFull
	}
}

// Do runs fn on the merge worker, after the jobs already queued, and waits.
func (m *MergeQueue) Do(ctx context.Context, fn func(context.Context)) error {
	done := make(chan struct{})
	if err := m.push(mergeItem{fn: func(c context.Context) { defer close(done); fn(c) }}); err != nil {
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

// Run is the single merge worker. Start exactly one.
func (m *MergeQueue) Run(ctx context.Context) {
	poll := tickerOrNil(m.PRPoll)
	clean := tickerOrNil(m.CleanupEvery)
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll:
			m.PollPRs(ctx)
		case <-clean:
			for _, p := range m.Store.ListProjects() {
				if r, err := m.cleanup(ctx, p); err != nil {
					slog.Warn("cleanup", "project", p.Name, "err", err)
				} else if !r.Empty() {
					slog.Info("cleanup", "project", p.Name, "report", r.String())
				}
			}
		case it := <-m.jobs:
			if it.fn != nil {
				it.fn(ctx)
				m.pending.Add(-1)
				m.publish("")
				continue
			}
			job := it.job
			m.publish(job.TodoID)
			if job.Revert {
				m.revert(ctx, job)
			} else if err := m.process(ctx, job); errors.As(err, new(*gitops.ErrDirtyCheckout)) {
				m.wait(ctx, job, err)
			} else if err != nil {
				slog.Warn("merge failed", "todo", job.TodoID, "err", err)
				if t, gerr := m.Store.GetTodo(job.TodoID); gerr == nil {
					m.Log.Line(t, "✖ merge: "+err.Error())
				}
				var se *syncError
				if errors.As(err, &se) {
					// The work is fine; GitHub isn't reachable or main diverged. Approve again once fixed.
					_, _ = m.Store.SetStatus(job.TodoID, model.StatusReview, "Not merged, can't sync with GitHub: "+brief(err))
				} else {
					noteFailure(m.Store, job.TodoID, "merge: "+err.Error(), "")
					_, _ = m.Store.SetStatus(job.TodoID, model.StatusFailed, "merge: "+err.Error())
				}
			}
			m.pending.Add(-1)
			m.publish("")
		}
	}
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

	// 0. Start from what's on GitHub, so the push (or the PR) can't be rejected.
	p = m.refreshRemote(ctx, p).For(t)
	if p.SyncsRemote() {
		if err := m.syncRemote(ctx, p); err != nil {
			return err
		}
	}

	var conflict *gitops.ErrConflict
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

		// 2. Re-test if the default branch moved since the todo's tests ran.
		if changed && p.TestCommand != "" {
			checks := runChecks(ctx, m.Log, t, p, wt.Path, "merge", m.TestTimeout)
			if t, err = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error { x.Checks = &checks; return nil }); err != nil {
				return err
			}
			if !checks.Passed {
				note := fmt.Sprintf("Your change was merged with the latest `%s`, and now the tests fail. %s", p.Base(), testNote(checks))
				return m.sendBack(t, p, note, false, "tests fail after merging "+p.Base())
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
	if p.UsesPR() {
		return m.openPR(ctx, t, p)
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
	return head, m.Git.Advance(ctx, p.ID, p.Path, p.Base(), old, head)
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
		tick := time.NewTicker(5 * time.Second)
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

// PushDefault pushes the default branch on the merge worker (after the
// merges already queued), first bringing in origin's new commits if origin
// moved on, so a push rejected as non-fast-forward is retried once synced.
func (m *MergeQueue) PushDefault(ctx context.Context, p model.Project) error {
	var err error
	if derr := m.Do(ctx, func(c context.Context) { err = m.pushSynced(c, p) }); derr != nil {
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
	m.setSyncError(p.ID, err)
	m.Remote.recount(ctx, p)
	return err
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
	body := strings.TrimSpace(t.Prompt)
	if len(body) > 2000 {
		body = body[:2000] + "…"
	}
	return fmt.Sprintf("%s\n\n%s\n\nDash-Todo: %s", subject, body, t.ID)
}

func tickerOrNil(d time.Duration) <-chan time.Time {
	if d <= 0 {
		return nil
	}
	return time.NewTicker(d).C
}

// publish tells every dashboard how deep the merge queue is. The queue is
// shared by all orgs; which task is being merged is not said, as it may be
// another org's (its todo.updated events tell its own org).
func (m *MergeQueue) publish(current string) {
	_ = current
	m.Pub.Publish(model.Event{Type: model.EvMergeQueue, Time: time.Now(), Data: map[string]any{
		"pending": m.pending.Load(),
	}})
}
