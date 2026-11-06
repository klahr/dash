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
}

// RemoteSync keeps an eye on origin for projects that sync with it: it
// fetches (never merges) the default branch when a project is selected and
// every Every for the selected projects and those with queued or running
// todos, fast-forwards the default branch before a new todo's worktree is
// made, and serialises all of that with the merge worker's own syncing
// through one lock per project, so two git fetches never race on a repo's
// lock files.
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

	now func() time.Time // for tests

	locks sync.Map // project ID → chan struct{} (capacity 1)

	mu       sync.Mutex
	status   map[string]RemoteStatus
	failures map[string]int
	retryAt  map[string]time.Time
	selected map[string]time.Time
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
	st.Branch, st.Enabled = p.DefaultBranch, p.SyncsRemote()
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
	if had && prev.Behind == st.Behind && prev.Ahead == st.Ahead && prev.Error == st.Error && prev.Branch == st.Branch {
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
	st.ProjectID, st.Branch, st.Enabled = p.ID, p.DefaultBranch, true
	r.set(st)
}

// recount refreshes the ahead/behind counts from the refs as they are (no
// fetch), e.g. after a merge moved the default branch.
func (r *RemoteSync) recount(ctx context.Context, p model.Project) {
	if !p.SyncsRemote() {
		return
	}
	ahead, behind, err := r.Git.AheadBehind(ctx, p.ID, p.Path, p.DefaultBranch)
	if err != nil {
		return // e.g. origin/<branch> doesn't exist yet
	}
	st := r.Status(p)
	st.Ahead, st.Behind = ahead, behind
	r.set(st)
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
// sync with origin, are selected or have queued or running todos, and
// aren't backing off after a failure.
func (r *RemoteSync) Due() []model.Project {
	var out []model.Project
	for _, p := range r.Store.ListProjects() {
		if p.SyncsRemote() && r.wanted(p.ID) && r.due(p.ID) {
			out = append(out, p)
		}
	}
	return out
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
	r.fetchLocked(ctx, p, false)
}

func (r *RemoteSync) fetchLocked(ctx context.Context, p model.Project, publish bool) error {
	fctx, cancel := context.WithTimeout(ctx, r.timeout())
	err := r.Git.FetchBranch(fctx, p.ID, p.Path, p.DefaultBranch, publish)
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
		return "⚠ origin is busy syncing; starting from local " + p.DefaultBranch
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
	moved, ahead, err := r.Git.FastForwardToOrigin(ctx, p.ID, p.Path, p.DefaultBranch)
	r.recount(ctx, p)
	switch {
	case errors.As(err, new(*gitops.ErrDirtyCheckout)):
		return note + "⚠ can't fast-forward " + p.DefaultBranch + " to origin: " + err.Error() + "; starting from local"
	case err != nil:
		slog.Warn("fast-forward to origin", "project", p.Name, "err", err)
		return note + "starting from local " + p.DefaultBranch
	case moved > 0:
		return note + fmt.Sprintf("⇣ fast-forwarded %s to origin (%s new)", p.DefaultBranch, commits(moved))
	case ahead > 0:
		return note + fmt.Sprintf("local %s has %s origin doesn't; starting from it as is (the merge reconciles them)", p.DefaultBranch, commits(ahead))
	case note != "":
		return note + "starting from local " + p.DefaultBranch
	}
	return ""
}

func commits(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", n)
}
