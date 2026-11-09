package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// RemoteStatus is how a project's default branch compares with origin's, as
// of the last fetch.
type RemoteStatus struct {
	ProjectID string `json:"project_id"`
	Branch    string `json:"branch"`
	// Enabled is false for projects that don't sync with origin (see
	// model.Project.SyncsRemote); they are never fetched.
	Enabled bool `json:"enabled"`
	Behind  int  `json:"behind"` // origin's commits the local branch doesn't have
	Ahead   int  `json:"ahead"`  // local commits origin doesn't have
	// Error is why the last fetch failed (offline, auth); the counts are
	// from the last one that worked. RetryAt is when the next try is due.
	Error   string    `json:"error,omitempty"`
	RetryAt time.Time `json:"retry_at,omitzero"`
	Fetched time.Time `json:"fetched,omitzero"` // last successful fetch
	// Stuck is why the project's checkout was left behind origin instead of
	// fast-forwarded (uncommitted changes, local commits): see SyncCheckout.
	// "" while it isn't behind, or may still follow.
	Stuck string `json:"stuck,omitempty"`
}

// RemoteSync keeps an eye on origin for projects that sync with it: it
// fetches the default branch when a project is selected, every Every for
// the selected projects and those with queued or running todos, and every
// Idle for the others (so pushes made outside Dash show up eventually);
// fast-forwards the default branch before a new todo's worktree is made;
// and serialises all of that with the merge worker's own syncing through one
// lock per project, so two git fetches never race on a repo's lock files.
//
// A runner's clone of a hub's project (model.Project.HubProjectID) also
// follows origin in its checkout: after each fetch, and at once when the hub
// says another runner pushed (OriginMoved), a clean checkout strictly behind
// origin is fast-forwarded; one with uncommitted changes or local commits is
// left alone and reported behind (RemoteStatus.Stuck), for Pull or a person
// to sort out. Task worktrees are never touched.
type RemoteSync struct {
	Store *store.Store
	Git   *gitops.Git
	Pub   Publisher
	Every time.Duration // periodic fetch interval; default 1 minute
	// Timeout bounds one fetch (default 15s); git never prompts (see gitops).
	Timeout time.Duration
	// Backoff is how long to wait after the 1st, 2nd, … consecutive failure
	// (the last entry repeats); default 1, 2, 5 minutes.
	Backoff []time.Duration
	// Selected is how long a project counts as selected after a client last
	// said so (default 10 minutes; the web UI repeats it while open).
	Selected time.Duration
	// Idle is how often a project nobody looks at and without tasks is
	// fetched (default 5 minutes; negative: never).
	Idle time.Duration

	now func() time.Time // for tests

	locks sync.Map // project ID → chan struct{} (capacity 1)

	mu       sync.Mutex
	status   map[string]RemoteStatus
	failures map[string]int
	retryAt  map[string]time.Time
	selected map[string]time.Time
	tried    map[string]time.Time // last fetch attempt, for Idle
}

func (r *RemoteSync) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *RemoteSync) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return 15 * time.Second
}

func (r *RemoteSync) backoff(failures int) time.Duration {
	b := r.Backoff
	if len(b) == 0 {
		b = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute}
	}
	return b[min(failures, len(b))-1]
}

// ---------------------------------------------------------------- lock

func (r *RemoteSync) lockFor(projectID string) chan struct{} {
	ch, _ := r.locks.LoadOrStore(projectID, make(chan struct{}, 1))
	return ch.(chan struct{})
}

// TryLock takes the project's remote lock if it is free.
func (r *RemoteSync) TryLock(projectID string) (unlock func(), ok bool) {
	ch := r.lockFor(projectID)
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, true
	default:
		return nil, false
	}
}

// Lock waits for the project's remote lock until ctx is done.
func (r *RemoteSync) Lock(ctx context.Context, projectID string) (unlock func(), err error) {
	ch := r.lockFor(projectID)
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// With runs fn holding the project's remote lock (waiting for it).
func (r *RemoteSync) With(ctx context.Context, projectID string, fn func() error) error {
	unlock, err := r.Lock(ctx, projectID)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

// ---------------------------------------------------------------- status

// Status returns the project's last known comparison with origin.
func (r *RemoteSync) Status(p model.Project) RemoteStatus {
	r.mu.Lock()
	st, ok := r.status[p.ID]
	r.mu.Unlock()
	if !ok {
		st = RemoteStatus{ProjectID: p.ID}
	}
	st.Branch, st.Enabled = p.Base(), p.SyncsRemote()
	return st
}

func (r *RemoteSync) set(st RemoteStatus) {
	r.mu.Lock()
	if r.status == nil {
		r.status = map[string]RemoteStatus{}
	}
	prev, had := r.status[st.ProjectID]
	r.status[st.ProjectID] = st
	r.mu.Unlock()
	if had && prev.Behind == st.Behind && prev.Ahead == st.Ahead && prev.Error == st.Error && prev.Branch == st.Branch &&
		prev.Stuck == st.Stuck {
		return // the same news; a fetch every minute shouldn't redraw every dashboard
	}
	if r.Pub != nil {
		r.Pub.Publish(model.Event{Type: model.EvRemote, ProjectID: st.ProjectID, Data: st, Time: r.clock()})
	}
}

// due reports whether a periodic fetch may run (not backing off).
func (r *RemoteSync) due(projectID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.clock().Before(r.retryAt[projectID])
}

// record notes a fetch's outcome: success resets the backoff, a failure
// pushes the next periodic try out (1 → 2 → 5 minutes).
func (r *RemoteSync) record(p model.Project, fetchErr error) {
	now := r.clock()
	r.mu.Lock()
	if r.failures == nil {
		r.failures, r.retryAt = map[string]int{}, map[string]time.Time{}
	}
	if r.tried == nil {
		r.tried = map[string]time.Time{}
	}
	r.tried[p.ID] = now
	st := r.status[p.ID]
	if fetchErr == nil {
		delete(r.failures, p.ID)
		delete(r.retryAt, p.ID)
		st.Error, st.RetryAt, st.Fetched = "", time.Time{}, now
	} else {
		r.failures[p.ID]++
		r.retryAt[p.ID] = now.Add(r.backoff(r.failures[p.ID]))
		st.Error, st.RetryAt = brief(fetchErr), r.retryAt[p.ID]
	}
	r.mu.Unlock()
	st.ProjectID, st.Branch, st.Enabled = p.ID, p.Base(), true
	r.set(st)
}

// recount refreshes the ahead/behind counts from the refs as they are (no
// fetch), e.g. after a merge moved the default branch.
func (r *RemoteSync) recount(ctx context.Context, p model.Project) {
	if !p.SyncsRemote() {
		return
	}
	ahead, behind, err := r.Git.AheadBehind(ctx, p.ID, p.Path, p.Base())
	if err != nil {
		return // e.g. origin/<branch> doesn't exist yet
	}
	st := r.Status(p)
	st.Ahead, st.Behind = ahead, behind
	if behind == 0 {
		st.Stuck = "" // caught up, however that happened
	}
	r.set(st)
}

// Behind lists the projects whose default branch is behind origin's, as of
// the last fetch.
func (r *RemoteSync) Behind() []RemoteStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []RemoteStatus
	for _, st := range r.status {
		if st.Behind > 0 {
			out = append(out, st)
		}
	}
	return out
}

// ---------------------------------------------------------------- fetching

// Select marks a project as the one a client is looking at, so it is
// fetched periodically. With fetchNow it also fetches now in the background
// unless it is backing off after failures (force overrides that) or the
// merge worker is syncing it (which fetches anyway). The status reported is
// the last known one; the new one follows as a remote.updated event.
func (r *RemoteSync) Select(ctx context.Context, p model.Project, fetchNow, force bool) RemoteStatus {
	r.mu.Lock()
	if r.selected == nil {
		r.selected = map[string]time.Time{}
	}
	r.selected[p.ID] = r.clock()
	r.mu.Unlock()
	if fetchNow && p.SyncsRemote() && (force || r.due(p.ID)) {
		go r.fetch(context.WithoutCancel(ctx), p)
	}
	return r.Status(p)
}

// Run fetches every Every until ctx is done.
func (r *RemoteSync) Run(ctx context.Context) {
	every := r.Every
	if every <= 0 {
		every = time.Minute
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		for _, p := range r.Due() {
			go r.fetch(ctx, p)
		}
	}
}

// Due lists the projects a periodic fetch should look at now: those that
// sync with origin, aren't backing off after a failure, and are selected,
// have queued or running todos, or weren't fetched for Idle.
func (r *RemoteSync) Due() []model.Project {
	var out []model.Project
	for _, p := range r.Store.ListProjects() {
		if p.SyncsRemote() && r.due(p.ID) && (r.wanted(p.ID) || r.idle(p.ID)) {
			out = append(out, p)
		}
	}
	return out
}

// idle reports whether a project nobody looks at is due its occasional fetch.
func (r *RemoteSync) idle(projectID string) bool {
	every := r.Idle
	if every < 0 {
		return false
	}
	if every == 0 {
		every = 5 * time.Minute
	}
	r.mu.Lock()
	at, ok := r.tried[projectID]
	r.mu.Unlock()
	return !ok || r.clock().Sub(at) >= every
}

func (r *RemoteSync) wanted(projectID string) bool {
	ttl := r.Selected
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	r.mu.Lock()
	at, ok := r.selected[projectID]
	r.mu.Unlock()
	if ok && r.clock().Sub(at) < ttl {
		return true
	}
	return len(r.Store.ListTodos(projectID, model.StatusQueued, model.StatusInProgress)) > 0
}

// fetch updates origin/<default> and the counts, unless the project's lock
// is taken (a merge is syncing it, or another fetch is running).
func (r *RemoteSync) fetch(ctx context.Context, p model.Project) {
	unlock, ok := r.TryLock(p.ID)
	if !ok {
		return
	}
	defer unlock()
	if r.fetchLocked(ctx, p, false) == nil {
		r.syncCheckout(ctx, p)
	}
}

func (r *RemoteSync) fetchLocked(ctx context.Context, p model.Project, publish bool) error {
	fctx, cancel := context.WithTimeout(ctx, r.timeout())
	err := r.Git.FetchBranch(fctx, p.ID, p.Path, p.Base(), publish)
	cancel()
	if err != nil && fctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("fetching origin timed out after %s", r.timeout())
	}
	if ctx.Err() != nil {
		return ctx.Err() // shutting down; not origin's fault
	}
	r.record(p, err)
	r.recount(ctx, p)
	return err
}

// LockWait is how long a new todo waits for a sync in progress before it
// starts from the local default branch as it is.
const LockWait = 30 * time.Second

// FastForward brings the local default branch up to origin's before a new
// todo branches off it: fetch, then fast-forward if (and only if) local is
// strictly behind. Local commits origin doesn't have are left for the merge
// step to reconcile. Nothing here stops the todo from starting: the result
// is a note for its log, and on any failure the branch stays as it was.
func (r *RemoteSync) FastForward(ctx context.Context, p model.Project) string {
	if !p.SyncsRemote() {
		return ""
	}
	lctx, cancel := context.WithTimeout(ctx, LockWait)
	unlock, err := r.Lock(lctx, p.ID)
	cancel()
	if err != nil {
		return "⚠ origin is busy syncing; starting from local " + p.Base()
	}
	defer unlock()
	note := ""
	if !r.due(p.ID) {
		// Backing off after failures (offline?): don't make every start wait
		// for a timeout, but still use what the last fetch brought.
		note = "⚠ origin unreachable lately (" + r.Status(p).Error + "); "
	} else if err := r.fetchLocked(ctx, p, true); err != nil {
		note = "⚠ can't fetch origin (" + brief(err) + "); "
	}
	moved, ahead, err := r.Git.FastForwardToOrigin(ctx, p.ID, p.Path, p.Base())
	r.recount(ctx, p)
	switch {
	case errors.As(err, new(*gitops.ErrDirtyCheckout)):
		return note + "⚠ can't fast-forward " + p.Base() + " to origin: " + err.Error() + "; starting from local"
	case err != nil:
		slog.Warn("fast-forward to origin", "project", p.Name, "err", err)
		return note + "starting from local " + p.Base()
	case moved > 0:
		return note + fmt.Sprintf("⇣ fast-forwarded %s to origin (%s new)", p.Base(), commits(moved))
	case ahead > 0:
		return note + fmt.Sprintf("local %s has %s origin doesn't; starting from it as is (the merge reconciles them)", p.Base(), commits(ahead))
	case note != "":
		return note + "starting from local " + p.Base()
	}
	return ""
}

func commits(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", n)
}

// ---------------------------------------------------------------- following origin

// follows reports whether p's checkout follows origin on its own: a
// runner's clone of a hub's project, where every change comes through
// origin. Anyone's own project only moves when they (or a task) say so.
func follows(p model.Project) bool { return p.HubProjectID != "" && p.SyncsRemote() }

// OriginMoved is told that origin's branch now points at sha (another
// runner pushed it; the hub passed the news on). It fetches unless sha is
// already known to be on origin's branch, so repeated or out-of-order news
// costs nothing, then brings the checkout up to date (SyncCheckout). News
// about any branch but the one tasks land on is ignored.
func (r *RemoteSync) OriginMoved(ctx context.Context, p model.Project, branch, sha string) error {
	if !p.SyncsRemote() || branch != p.Base() {
		return nil
	}
	lctx, cancel := context.WithTimeout(ctx, LockWait)
	unlock, err := r.Lock(lctx, p.ID)
	cancel()
	if err != nil {
		return err // a merge is syncing it; that fetches anyway
	}
	defer unlock()
	if r.Git.Contains(ctx, p.ID, p.Path, sha, "refs/heads/"+branch) {
		return nil // the checkout's branch has it already
	}
	if !r.Git.Contains(ctx, p.ID, p.Path, sha, "refs/remotes/origin/"+branch) {
		if err := r.fetchLocked(ctx, p, false); err != nil {
			return err
		}
	}
	r.syncCheckout(ctx, p)
	return nil
}

// FetchAll fetches every project that follows origin now, even those
// backing off after failures, and brings their checkouts up to date: a
// runner calls it when it (re)connects to its hub, to catch up on pushes
// made while it was away. It returns once every project is done.
func (r *RemoteSync) FetchAll(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()
	for _, p := range r.Store.ListProjects() {
		if !follows(p) {
			continue
		}
		wg.Add(1)
		go func(p model.Project) {
			defer wg.Done()
			lctx, cancel := context.WithTimeout(ctx, LockWait)
			unlock, err := r.Lock(lctx, p.ID)
			cancel()
			if err != nil {
				return
			}
			defer unlock()
			if r.fetchLocked(ctx, p, false) == nil {
				r.syncCheckout(ctx, p)
			}
		}(p)
	}
}

// SyncCheckout brings a following project's default branch (and its
// checkout's files, when it is checked out) up to origin's, as of the last
// fetch, holding the project's lock. Only a clean, strictly behind branch
// moves; with uncommitted changes, local commits or a diverged history it
// stays as it is, and Stuck says why.
func (r *RemoteSync) SyncCheckout(ctx context.Context, p model.Project) {
	unlock, err := r.Lock(ctx, p.ID)
	if err != nil {
		return
	}
	defer unlock()
	r.syncCheckout(ctx, p)
}

func (r *RemoteSync) syncCheckout(ctx context.Context, p model.Project) {
	if !follows(p) {
		return
	}
	ahead, behind, err := r.Git.AheadBehind(ctx, p.ID, p.Path, p.Base())
	if err != nil || behind == 0 {
		r.recount(ctx, p)
		return
	}
	stuck := r.whyNotForward(ctx, p, ahead)
	if stuck == "" {
		moved, _, err := r.Git.FastForwardToOrigin(ctx, p.ID, p.Path, p.Base())
		switch {
		case err != nil:
			stuck = brief(err)
		case moved > 0:
			slog.Info("fast-forwarded to origin", "project", p.Name, "branch", p.Base(), "commits", moved)
		}
	}
	r.recount(ctx, p)
	if stuck != "" {
		st := r.Status(p)
		if st.Behind > 0 && st.Stuck != stuck {
			st.Stuck = stuck
			r.set(st)
		}
	}
}

// whyNotForward is why p's branch, behind origin and with ahead commits of
// its own, can't simply fast-forward ("" if it can).
func (r *RemoteSync) whyNotForward(ctx context.Context, p model.Project, ahead int) string {
	branch, dirty, err := r.Git.CheckoutState(ctx, p.ID, p.Path)
	switch {
	case err != nil:
		return "can't read the checkout: " + brief(err)
	case ahead > 0:
		return fmt.Sprintf("local %s has %s origin doesn't (diverged)", p.Base(), commits(ahead))
	case branch == p.Base() && dirty:
		return "uncommitted changes in " + p.Path
	}
	return ""
}

// PullRefused is why Pull didn't fast-forward.
type PullRefused struct{ Reason string }

func (e *PullRefused) Error() string { return e.Reason }

// Pull fetches origin and fast-forwards the default branch (and the
// checkout's files) to it, only if that is a fast-forward of a clean
// checkout; otherwise it refuses with a *PullRefused and changes nothing.
// moved is how many commits the branch gained.
func (r *RemoteSync) Pull(ctx context.Context, p model.Project) (moved int, err error) {
	if p.RemoteURL == "" {
		return 0, &PullRefused{Reason: "this project has no origin to pull from"}
	}
	lctx, cancel := context.WithTimeout(ctx, LockWait)
	unlock, err := r.Lock(lctx, p.ID)
	cancel()
	if err != nil {
		return 0, &PullRefused{Reason: "origin is busy syncing (a merge is running); try again in a moment"}
	}
	defer unlock()
	if err := r.fetchLocked(ctx, p, true); err != nil {
		return 0, fmt.Errorf("can't fetch origin: %s", brief(err))
	}
	ahead, behind, err := r.Git.AheadBehind(ctx, p.ID, p.Path, p.Base())
	if err != nil {
		return 0, err
	}
	if behind == 0 {
		r.recount(ctx, p)
		return 0, nil
	}
	if why := r.whyNotForward(ctx, p, ahead); why != "" {
		return 0, &PullRefused{Reason: "can't fast-forward " + p.Base() + ": " + why + "; sort that out in the folder first"}
	}
	moved, _, err = r.Git.FastForwardToOrigin(ctx, p.ID, p.Path, p.Base())
	r.recount(ctx, p)
	if errors.As(err, new(*gitops.ErrDirtyCheckout)) {
		return 0, &PullRefused{Reason: "can't fast-forward " + p.Base() + ": " + err.Error()}
	}
	return moved, err
}
