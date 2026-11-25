package ci

import (
	"context"
	"log/slog"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/release"
)

// releaseSlowEvery is how often a release build is polled while the
// project's webhook works: a safety net for lost deliveries, cancelled runs
// and the like. hookRefreshDelay gathers the deliveries of a burst of runs
// on the default branch into one refresh.
var (
	releaseSlowEvery = 3 * time.Minute
	hookRefreshDelay = 5 * time.Second
)

// hookStatus is project projectID's webhook status, nil without Hooks.
func (w *Watcher) hookStatus(projectID string) *HookStatus {
	if w.Hooks == nil || w.Store == nil {
		return nil
	}
	p, err := w.Store.GetProject(projectID)
	if err != nil {
		return nil
	}
	return w.Hooks.Status(p)
}

// hookActive reports whether project p's webhook works.
func (w *Watcher) hookActive(p model.Project) bool {
	return w.Hooks != nil && w.Hooks.Active(p)
}

// publish shows the dashboards project projectID's status again (its
// webhook's state changed, say).
func (w *Watcher) publish(projectID string) {
	st := w.Status(projectID)
	st.ProjectID = projectID
	w.set(st)
}

// releaseWait is how long a followed release build that is still building
// waits for its next poll: every, or slow while the webhook works (hook);
// a tag without runs yet is checked once noBuild has passed all the same,
// so a tag no workflow started for still turns no_build.
func releaseWait(hook bool, rel Release, now time.Time, every, slow, noBuild time.Duration) time.Duration {
	if !hook {
		return every
	}
	if len(rel.Runs) == 0 {
		due := rel.Pushed.Add(noBuild).Add(every).Sub(now)
		if due <= 0 {
			return every
		}
		return min(due, slow)
	}
	return slow
}

// HookRun takes a workflow_run delivery for repo: run runID, of branch (or
// tag) as the payload says. For each project of repo, it reads the run
// again from GitHub, never trusting the payload's state, and when it is
// one of a release tag's builds updates that release as a poll would
// (settleRelease); a run on the default branch refreshes the project's
// builds soon. Projects and tags it doesn't concern are left alone.
func (w *Watcher) HookRun(ctx context.Context, repo string, runID int64, branch string) {
	for _, p := range w.Store.ListProjects() {
		if hookRepo(p) == repo && w.watched(p) && ctx.Err() == nil {
			w.hookRun(ctx, p, runID, branch)
		}
	}
}

func (w *Watcher) hookRun(ctx context.Context, p model.Project, runID int64, branch string) {
	rel, ok, found := w.releaseOf(p, branch)
	if !ok {
		if branch == p.DefaultBranch {
			w.kickRefresh(p.ID)
		}
		return
	}
	if w.limit(p.ID) != nil {
		return // the polls catch up once the rate limit ends
	}
	r, err := w.Git.RunByID(ctx, p.ID, p.Path, runID)
	if err != nil {
		slog.Debug("github webhook: read run", "project", p.Name, "run", runID, "err", err)
		return
	}
	if r.Branch != rel.Tag || !trustedEvents[r.Event] {
		return // not the tag's, or anyone could have started it (see trusted)
	}
	if found {
		rel.SHA, rel.Pushed = r.SHA, r.CreatedAt
	} else if r.SHA != rel.SHA {
		return // a run of another commit the tag was on
	}
	rel = w.settleRelease(ctx, p, rel, []gitops.Run{r}, true)
	cur, following := w.followed(p.ID)
	switch {
	case found && (!following || rel.Pushed.After(cur.Pushed)):
		slog.Info("found a release tag's build", "project", p.Name, "tag", rel.Tag, "state", rel.State)
		w.follow(rel)
	case following && cur.Tag == rel.Tag:
		w.publish(p.ID)
	}
}

// releaseOf is the release build of tag that a run's delivery may update:
// the release followed, or a saved one still building; or, found, a new one
// when tag is in the project's tag format and Dash doesn't know it yet.
func (w *Watcher) releaseOf(p model.Project, tag string) (rel Release, ok, found bool) {
	if tag == "" || tag == p.DefaultBranch {
		return Release{}, false, false
	}
	if cur, ok := w.followed(p.ID); ok && cur.Tag == tag {
		return cur, cur.State == ReleaseBuilding, false
	}
	if b, err := w.Store.ReleaseBuild(p.ID, tag); err == nil {
		return b, b.State == ReleaseBuilding, false
	}
	f, err := release.ParseFormat(p.TagFormat)
	if err != nil {
		return Release{}, false, false
	}
	if _, ok := f.Parse(tag); !ok {
		return Release{}, false, false
	}
	return Release{ProjectID: p.ID, Tag: tag, State: ReleaseBuilding, Runs: []gitops.Run{}, Found: true}, true, true
}

// HookRelease takes a release delivery (published) for repo: a project
// following that tag's build checks it soon.
func (w *Watcher) HookRelease(repo, tag string) {
	for _, p := range w.Store.ListProjects() {
		if hookRepo(p) != repo || !w.watched(p) {
			continue
		}
		if cur, ok := w.followed(p.ID); ok && cur.Tag == tag && cur.State == ReleaseBuilding {
			w.kickRefresh(p.ID)
		}
	}
}

// kickRefresh refreshes project projectID's builds in hookRefreshDelay,
// once for any number of calls meanwhile.
func (w *Watcher) kickRefresh(projectID string) {
	w.mu.Lock()
	if w.kicked == nil {
		w.kicked = map[string]bool{}
	}
	if w.kicked[projectID] {
		w.mu.Unlock()
		return
	}
	w.kicked[projectID] = true
	w.mu.Unlock()
	time.AfterFunc(hookRefreshDelay, func() {
		w.mu.Lock()
		delete(w.kicked, projectID)
		w.mu.Unlock()
		_, _ = w.Refresh(context.Background(), projectID)
	})
}
