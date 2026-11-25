package ci

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/release"
)

// Release states: what became of the workflow runs a pushed release tag
// started.
const (
	ReleaseBuilding  = "building"
	ReleasePublished = "published"
	ReleaseFailed    = "failed"
	// ReleaseNoBuild: no workflow started for the tag (e.g. the release
	// workflow only runs on tags in another format).
	ReleaseNoBuild = "no_build"
)

// Release is a release tag and its build. Each is saved in the store
// (Store.PutReleaseBuild), so following it survives a restart; the
// project's status carries the newest one.
type Release = model.ReleaseBuild

// CommitState is the build state of one commit from runs (any order): failing
// if a workflow's newest run failed, running if one hasn't finished, passing
// when all passed, None without runs.
func CommitState(runs []gitops.Run, sha string) string {
	var mine []gitops.Run
	for _, r := range runs {
		if r.SHA == sha {
			mine = append(mine, r)
		}
	}
	state, _ := summarize(mine)
	return state
}

// CommitRuns lists the workflow runs of a commit: from GitHub when it
// answers, else from the default branch's runs last seen.
func (w *Watcher) CommitRuns(ctx context.Context, p model.Project, sha string) (string, []gitops.Run) {
	var runs []gitops.Run
	var err error
	if rl := w.limit(p.ID); rl != nil {
		err = rl // while rate limited, from the runs last seen
	} else {
		runs, err = w.Git.RunsForCommit(ctx, p.ID, p.Path, sha, 30)
	}
	if err != nil {
		runs = nil
		for _, r := range w.Status(p.ID).Runs {
			if r.SHA == sha {
				runs = append(runs, r)
			}
		}
		if len(runs) == 0 {
			return Unknown, []gitops.Run{}
		}
	}
	if runs == nil {
		runs = []gitops.Run{}
	}
	return CommitState(runs, sha), runs
}

// releaseEvery and releaseFor pace following a release build closely (the
// regular poll follows it after that, until it ends; a webhook slows it:
// see releaseSlowEvery); noBuildAfter is how
// long a pushed tag may go without a workflow run before Dash says none
// started.
var (
	releaseEvery = 15 * time.Second
	releaseFor   = 45 * time.Minute
	noBuildAfter = 3 * time.Minute
)

// WatchRelease follows the workflow runs that pushing tag (on sha) started:
// the project's status carries it as Release, building until they pass
// (published) or fail. Whoever pushed the tag (this Dash, or a runner for
// a hub) calls it; tags pushed elsewhere are found by Refresh.
func (w *Watcher) WatchRelease(projectID, tag, sha string) {
	rel := Release{ProjectID: projectID, Tag: tag, SHA: sha, State: ReleaseBuilding, Runs: []gitops.Run{}, Pushed: time.Now()}
	if w.Store != nil {
		if prev, err := w.Store.ReleaseBuild(projectID, tag); err == nil && prev.SHA == sha && !prev.Pushed.IsZero() {
			rel.Pushed = prev.Pushed // asked twice (the hub and its runner): one release
		}
	}
	w.follow(rel)
}

// follow makes rel the release the project's status shows, saves it, and
// while it builds checks it every releaseEvery for releaseFor (every
// releaseSlowEvery while the project's webhook works: see releaseWait).
func (w *Watcher) follow(rel Release) {
	w.mu.Lock()
	if w.releases == nil {
		w.releases = map[string]Release{}
	}
	w.releases[rel.ProjectID] = rel
	w.mu.Unlock()
	w.save(rel)
	st := w.Status(rel.ProjectID)
	st.ProjectID = rel.ProjectID
	w.set(st) // shows it now
	if rel.State != ReleaseBuilding {
		return
	}
	projectID, tag := rel.ProjectID, rel.Tag
	// Read the pacing here, not in the goroutine, which outlives this call.
	every, slow, noBuild, deadline := releaseEvery, releaseSlowEvery, noBuildAfter, time.Now().Add(releaseFor)
	building := func() (Release, bool) {
		rel, ok := w.followed(projectID)
		return rel, ok && rel.Tag == tag && rel.State == ReleaseBuilding
	}
	go func() {
		for time.Now().Before(deadline) {
			rel, ok := building()
			if !ok {
				return
			}
			// While the webhook works, GitHub says when the build changes:
			// this poll is only a safety net then.
			hook := false
			if w.Hooks != nil && w.Store != nil {
				if p, err := w.Store.GetProject(projectID); err == nil {
					hook = w.hookActive(p)
				}
			}
			time.Sleep(releaseWait(hook, rel, time.Now(), every, slow, noBuild))
			if _, ok := building(); !ok {
				return
			}
			if _, err := w.Refresh(context.Background(), projectID); errors.Is(err, errGone) {
				return
			}
		}
	}()
}

// save stores rel's build, stamped now. Once it ended (published, failed,
// no_build), whoever OnReleaseEnded asked is told, whichever path (a poll
// or a webhook delivery) learned it.
func (w *Watcher) save(rel Release) {
	if w.Store == nil {
		return
	}
	rel.Updated = time.Now()
	if err := w.Store.PutReleaseBuild(rel); err != nil {
		slog.Debug("save release build", "project", rel.ProjectID, "tag", rel.Tag, "err", err)
		return
	}
	if rel.State == ReleaseBuilding {
		return
	}
	w.mu.Lock()
	f := w.ended
	w.mu.Unlock()
	if f != nil {
		// Not here: save may run under relMu, and f may take a while.
		go f(rel.ProjectID, rel.Tag)
	}
}

// OnReleaseEnded has f called (in its own goroutine) each time a release
// tag's build is saved as ended: the deploys waiting for it go on at once
// (see api's deploywait.go). f may be called more than once for a build.
func (w *Watcher) OnReleaseEnded(f func(projectID, tag string)) {
	w.mu.Lock()
	w.ended = f
	w.mu.Unlock()
}

// HookActive reports whether project projectID's webhook works, so its
// builds are polled only as a safety net.
func (w *Watcher) HookActive(projectID string) bool {
	if w == nil || w.Hooks == nil || w.Store == nil {
		return false
	}
	p, err := w.Store.GetProject(projectID)
	return err == nil && w.hookActive(p)
}

// Resume shows each project's newest saved release again, and follows
// those still building, as after a restart.
func (w *Watcher) Resume() {
	if w.Store == nil {
		return
	}
	for _, p := range w.Store.ListProjects() {
		bs, err := w.Store.ReleaseBuilds(p.ID, 1)
		if err != nil || len(bs) == 0 {
			continue
		}
		if cur, ok := w.followed(p.ID); ok && !cur.Pushed.Before(bs[0].Pushed) {
			continue
		}
		w.follow(bs[0])
	}
}

var errGone = errors.New("project is gone")

func (w *Watcher) followed(projectID string) (Release, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, ok := w.releases[projectID]
	return r, ok
}

// releaseState is what rel's runs (the tag's newest per workflow) say of it.
func releaseState(rel Release, runs []gitops.Run) string {
	switch state, _ := summarize(runs); {
	case len(runs) == 0 && time.Since(rel.Pushed) > noBuildAfter:
		return ReleaseNoBuild
	case len(runs) == 0, state == Running:
		return ReleaseBuilding
	case state == Failing:
		return ReleaseFailed
	default:
		return ReleasePublished
	}
}

// tagRuns keeps the trusted runs of tag on sha, the newest of each workflow.
func tagRuns(runs []gitops.Run, sha string) []gitops.Run {
	var mine []gitops.Run
	for _, r := range trusted(runs) {
		if r.SHA == sha {
			mine = append(mine, r)
		}
	}
	out := latestPerWorkflow(mine, "")
	if out == nil {
		out = []gitops.Run{}
	}
	return out
}

// refreshRelease checks the build of the release being followed, if any.
func (w *Watcher) refreshRelease(ctx context.Context, p model.Project) *Release {
	rel, ok := w.followed(p.ID)
	if !ok {
		return nil
	}
	if rel.State == ReleaseBuilding {
		runs, err := w.Git.Runs(ctx, p.ID, p.Path, rel.Tag, 10)
		if err != nil {
			slog.Debug("release runs", "project", p.Name, "tag", rel.Tag, "err", err)
			return &rel
		}
		rel = w.settleRelease(ctx, p, rel, runs, false)
	}
	return &rel
}

// settleRelease is where the poll (refreshRelease, with the tag's runs as
// GitHub lists them) and the webhook (HookRun, with the one run a delivery
// named, read again from GitHub: merge) update a release build, so the two
// can't drift apart. It takes the tag's trusted runs on rel's commit, the
// newest of each workflow (a run already seen in a newer state than runs
// show keeps that state), works out the release's state and page, and
// saves it when that changed; the release followed is updated too.
func (w *Watcher) settleRelease(ctx context.Context, p model.Project, rel Release, runs []gitops.Run, merge bool) Release {
	w.relMu.Lock()
	defer w.relMu.Unlock()
	if cur, ok := w.followed(p.ID); ok && cur.Tag == rel.Tag {
		rel = cur // the latest, should another update have come in meanwhile
	} else if saved, err := w.Store.ReleaseBuild(p.ID, rel.Tag); err == nil && saved.SHA == rel.SHA {
		rel = saved
	}
	if rel.State != ReleaseBuilding {
		return rel // it ended: nothing new to learn
	}
	mine := tagRuns(runs, rel.SHA)
	if merge {
		mine = mergeRuns(rel.Runs, mine)
	} else if len(mine) == 0 && len(rel.Runs) > 0 {
		return rel // GitHub listed its runs before: a glitch, not "no build"
	} else {
		mine = keepNewer(rel.Runs, mine)
	}
	prev := rel
	rel.Runs = mine
	rel.State = releaseState(rel, rel.Runs)
	if rel.State == ReleasePublished || rel.State == ReleaseNoBuild {
		if url, err := w.Git.ReleaseURL(ctx, p.ID, p.Path, rel.Tag); err == nil && url != "" {
			rel.URL = url
		} else {
			rel.URL = gitops.ReleasePage(p.RemoteURL, rel.Tag)
		}
	}
	w.mu.Lock()
	cur, ok := w.releases[p.ID]
	if ok && cur.Tag == rel.Tag {
		w.releases[p.ID] = rel
	}
	w.mu.Unlock()
	if !sameRelease(&prev, &rel) {
		w.save(rel)
	}
	return rel
}

// mergeRuns adds the runs in fresh to have (each the newest of its
// workflow): a run replaces its workflow's run of the same or a lower id,
// unless that one is already further along.
func mergeRuns(have, fresh []gitops.Run) []gitops.Run {
	out := slices.Clone(have)
	if out == nil {
		out = []gitops.Run{}
	}
	for _, r := range fresh {
		i := slices.IndexFunc(out, func(x gitops.Run) bool { return x.Workflow == r.Workflow })
		switch {
		case i < 0:
			out = append(out, r)
		case out[i].ID < r.ID, out[i].ID == r.ID && !older(r, out[i]):
			out[i] = r
		}
	}
	slices.SortStableFunc(out, func(a, b gitops.Run) int { return cmpInt(b.ID, a.ID) })
	return out
}

// keepNewer is fresh, a list GitHub answered, with any run that have
// already shows in a newer state (a delivery came in after GitHub made the
// list) kept as it is in have.
func keepNewer(have, fresh []gitops.Run) []gitops.Run {
	for i, r := range fresh {
		for _, h := range have {
			if h.ID == r.ID && older(r, h) {
				fresh[i] = h
			}
		}
	}
	return fresh
}

// older reports whether a is an earlier state of the same run than b: b
// is done and a isn't, or b changed later.
func older(a, b gitops.Run) bool {
	if b.Done() != a.Done() {
		return b.Done()
	}
	return !a.UpdatedAt.IsZero() && a.UpdatedAt.Before(b.UpdatedAt)
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// pushRunsLimit is how many of the newest push runs Refresh reads to find
// release tags pushed from anywhere.
const pushRunsLimit = 30

// findReleases looks through the newest runs pushes started for runs of
// release tags (tags in the project's format): those of a tag Dash hasn't
// seen are a release pushed elsewhere (a terminal, GitHub, an agent), and
// are saved; the newest becomes the release the status shows. Tags already
// saved get their state from the runs too, unless they are followed
// (refreshRelease checks that one).
func (w *Watcher) findReleases(ctx context.Context, p model.Project) {
	f, err := release.ParseFormat(p.TagFormat)
	if err != nil {
		return
	}
	runs, err := w.Git.PushRuns(ctx, p.ID, p.Path, pushRunsLimit)
	if err != nil {
		slog.Debug("push runs", "project", p.Name, "err", err)
		return
	}
	type found struct {
		sha   string
		first time.Time
		runs  []gitops.Run
	}
	byTag := map[string]*found{}
	var order []string // newest first, as gh lists them
	for _, r := range trusted(runs) {
		if r.Branch == "" || r.Branch == p.DefaultBranch {
			continue
		}
		if _, ok := f.Parse(r.Branch); !ok {
			continue
		}
		t := byTag[r.Branch]
		if t == nil {
			t = &found{sha: r.SHA}
			byTag[r.Branch] = t
			order = append(order, r.Branch)
		}
		if r.SHA != t.sha {
			continue // the tag moved: its newest commit's runs decide
		}
		t.runs = append(t.runs, r)
		if t.first.IsZero() || r.CreatedAt.Before(t.first) {
			t.first = r.CreatedAt
		}
	}
	cur, following := w.followed(p.ID)
	var newest *Release
	for _, tag := range order {
		t := byTag[tag]
		if following && cur.Tag == tag {
			continue
		}
		rel, err := w.Store.ReleaseBuild(p.ID, tag)
		known := err == nil && rel.SHA == t.sha
		if !known {
			rel = Release{ProjectID: p.ID, Tag: tag, SHA: t.sha, Pushed: t.first, Found: true}
		}
		if known && rel.State != ReleaseBuilding {
			continue // it ended: nothing new to learn
		}
		prev := rel
		rel.Runs = latestPerWorkflow(t.runs, "")
		rel.State = releaseState(rel, rel.Runs)
		if rel.State != ReleaseBuilding {
			rel.URL = gitops.ReleasePage(p.RemoteURL, tag)
		}
		if !known || !sameRelease(&prev, &rel) {
			w.save(rel)
		}
		if !known && (newest == nil || rel.Pushed.After(newest.Pushed)) {
			r := rel
			newest = &r
		}
	}
	if newest != nil && (!following || newest.Pushed.After(cur.Pushed)) {
		slog.Info("found a release tag's build", "project", p.Name, "tag", newest.Tag, "state", newest.State)
		w.follow(*newest)
	}
}

// ReleaseBuilds are the saved builds of a project's latest release tags
// (at most limit), the most recently pushed first; the one followed now is
// as last checked.
func (w *Watcher) ReleaseBuilds(projectID string, limit int) ([]Release, error) {
	bs, err := w.Store.ReleaseBuilds(projectID, limit)
	if err != nil {
		return nil, err
	}
	if cur, ok := w.followed(projectID); ok {
		for i := range bs {
			if bs[i].Tag == cur.Tag {
				bs[i] = cur
			}
		}
	}
	return bs, nil
}

// ReleaseBuild is the saved build of one of a project's release tags
// (store.ErrNotFound if Dash knows nothing of it).
func (w *Watcher) ReleaseBuild(projectID, tag string) (Release, error) {
	if cur, ok := w.followed(projectID); ok && cur.Tag == tag {
		return cur, nil
	}
	return w.Store.ReleaseBuild(projectID, tag)
}

// sameRelease reports whether two statuses show the same release news.
func sameRelease(a, b *Release) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Tag != b.Tag || a.State != b.State || a.URL != b.URL || len(a.Runs) != len(b.Runs) {
		return false
	}
	for i := range a.Runs {
		if a.Runs[i] != b.Runs[i] {
			return false
		}
	}
	return true
}
