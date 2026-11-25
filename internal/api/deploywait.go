package api

// Waiting for the release build: an environment that deploys each release
// tag may wait for the GitHub Actions runs the tag started to pass first
// ("Wait for the release build before deploying",
// model.Environment.WaitForBuild), so it doesn't deploy before the release
// workflow has published what it deploys. The tag's deploy is recorded at
// once, waiting (model.DeployWaitingBuild), which the queue doesn't run.
// The waiting deploys look at their tag's build, as the CI watcher keeps it
// (ci.Watcher.ReleaseBuild, saved in the store), as soon as the watcher
// saves it ended (ci.Watcher.OnReleaseEnded: a webhook delivery or a poll
// learned it), and every so often all the same (buildWaitEvery): once it
// passed, the deploy goes on as any deploy does (the requirements, the
// freezes, an approval, the queue); if it failed, none ran, or it took too
// long, the deploy fails with why, and so is notified like a failed deploy.
// A newer deploy to the environment (waiting, queued, on its way or live)
// supersedes it: it is cancelled, so an older release never goes out after
// a newer one. The record is the wait: a restart picks it up again.
//
// The Deploy… picker shows each release tag's build too (tagBuild), and a
// person deploying a tag whose build failed or still runs is asked first,
// not refused.

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/klahr/dash/internal/ci"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/textutil"
)

// How long a deploy waits for its release build: waitNoBuild for Dash to
// know of one at all (it follows the tags it pushes at once), waitBuildFor
// for it to end.
var (
	waitNoBuild  = 15 * time.Minute
	waitBuildFor = 6 * time.Hour
)

// buildWaitSlow is how often a project's waiting deploys look at their
// build while its webhook works: the watcher tells them when it ends, so
// this is only a safety net (and catches the time limits).
var buildWaitSlow = 3 * time.Minute

// buildWaitPoll is how often the ticker looks for waiting deploys due.
func (s *Server) buildWaitPoll() time.Duration {
	if s.BuildWaitPoll > 0 {
		return s.BuildWaitPoll
	}
	return 15 * time.Second
}

// buildWaitEvery is how often project projectID's waiting deploys look at
// their build: buildWaitPoll, or buildWaitSlow while its webhook works.
func (s *Server) buildWaitEvery(projectID string) time.Duration {
	if s.CI.HookActive(projectID) {
		return max(buildWaitSlow, s.buildWaitPoll())
	}
	return s.buildWaitPoll()
}

// waitForBuild records the deploy of release tag (on commit) to e of p,
// triggered for c, waiting for the tag's release build.
func (s *Server) waitForBuild(p model.Project, e model.Environment, commit, tag string, c Caller) (model.Deploy, bool) {
	d, err := s.Store.CreateDeploy(model.Deploy{ProjectID: p.ID, Environment: e.Name, Ref: commit, Tag: tag,
		Trigger: model.DeployByRelease, TriggerRef: tag, By: c.User, ByName: s.username(c.User), Status: model.DeployWaitingBuild})
	if err != nil {
		slog.Warn("deploy: trigger", "project", p.Name, "environment", e.Name, "trigger", model.DeployByRelease, "ref", tag, "err", err)
		return model.Deploy{}, false
	}
	slog.Info("deploy: triggered, waiting for the release build", "project", p.Name, "environment", e.Name, "tag", tag, "id", d.ID)
	return d, true
}

// watchBuildWaits looks at the waiting deploys until ctx ends: those of
// each project every buildWaitEvery, and those of a tag as soon as the CI
// watcher saves the tag's build ended.
func (s *Server) watchBuildWaits(ctx context.Context) {
	if s.CI != nil {
		s.CI.OnReleaseEnded(s.releaseBuildEnded)
	}
	t := time.NewTicker(s.buildWaitPoll())
	defer t.Stop()
	for {
		s.pollBuildWaits(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// releaseBuildOf is the build of a project's release tag Dash knows of.
func (s *Server) releaseBuildOf(projectID, tag string) (model.ReleaseBuild, error) {
	if s.CI == nil {
		return model.ReleaseBuild{}, store.ErrNotFound
	}
	return s.CI.ReleaseBuild(projectID, tag)
}

// checkBuildWaits moves each deploy waiting for its release build on, as of
// now: on to the queue when the build passed, failed when it can't pass.
func (s *Server) checkBuildWaits(now time.Time) {
	s.checkWaits(now, "", nil)
}

// releaseBuildEnded moves the deploys of project projectID waiting for
// tag's release build on at once: the CI watcher saved it ended. Deploys
// of other tags and projects are left to the ticker.
func (s *Server) releaseBuildEnded(projectID, tag string) {
	s.checkWaits(time.Now(), projectID, func(d model.Deploy) bool { return cmp.Or(d.Tag, d.TriggerRef) == tag })
}

// pollBuildWaits is the ticker's look, as of now, at the waiting deploys of
// each project due (see buildWaitEvery).
func (s *Server) pollBuildWaits(now time.Time) {
	due := map[string]bool{} // checkWaits holds s.buildWaits: buildWaitLast is ours
	s.checkWaits(now, "", func(d model.Deploy) bool {
		ok, seen := due[d.ProjectID]
		if !seen {
			last, every := s.buildWaitLast[d.ProjectID], s.buildWaitEvery(d.ProjectID)
			// Half a tick early still counts: the ticker isn't exact.
			ok = last.IsZero() || now.Add(s.buildWaitPoll()/2).Sub(last) >= every
			due[d.ProjectID] = ok
			if ok {
				if s.buildWaitLast == nil {
					s.buildWaitLast = map[string]time.Time{}
				}
				s.buildWaitLast[d.ProjectID] = now
			}
		}
		return ok
	})
}

// checkWaits is the one look at the waiting deploys that the ticker and
// the CI watcher's word share: each of project projectID ("": any) that
// match takes (nil: all) moves on, as of now. One look runs at a time,
// and each move is made only if the deploy still waits as the store has
// it, so two looks at the same build move a deploy once.
func (s *Server) checkWaits(now time.Time, projectID string, match func(model.Deploy) bool) {
	s.buildWaits.Lock()
	defer s.buildWaits.Unlock()
	all := s.Store.ListDeploys(projectID, "")
	for _, d := range all {
		if d.Status != model.DeployWaitingBuild || match != nil && !match(d) {
			continue
		}
		p, err := s.deployProject(d.ProjectID)
		if err != nil {
			continue
		}
		if newer, ok := newerDeploy(all, d); ok {
			s.supersedeWaiting(p, d, newer)
			continue
		}
		goes, why := s.buildVerdict(p, d, now)
		switch {
		case goes:
			s.releaseWaiting(p, d)
		case why != "":
			s.failWaiting(p, d, why)
		}
	}
}

// newerDeploy is a deploy among all to d's environment created after d that
// still goes, is on its way or is live: deploying d once its build passed
// would put the environment back on an older release.
func newerDeploy(all []model.Deploy, d model.Deploy) (model.Deploy, bool) {
	for _, x := range all {
		if x.ID == d.ID || x.ProjectID != d.ProjectID || x.Environment != d.Environment || !x.CreatedAt.After(d.CreatedAt) {
			continue
		}
		if !x.Status.Done() || x.Status == model.DeployLive {
			return x, true
		}
	}
	return model.Deploy{}, false
}

// supersedeWaiting takes d, waiting for its release build, off because
// newer, a later deploy to its environment, replaces it. It is cancelled,
// not failed: nothing went wrong, so it isn't notified as a failure.
func (s *Server) supersedeWaiting(p model.Project, d, newer model.Deploy) {
	what := cmp.Or(newer.Tag, gitops.ShortSHA(newer.Ref), "a newer deploy")
	why := fmt.Sprintf("superseded by %s, a newer deploy to %s, so it didn't deploy", what, d.Environment)
	if _, err := s.Store.UpdateDeploy(d.ID, func(x *model.Deploy) error {
		if x.Status != model.DeployWaitingBuild {
			return errUnchanged
		}
		x.Error, x.Cancelled = why, true
		x.SetStatus(model.DeployFailed, time.Now())
		return nil
	}); err != nil {
		return
	}
	slog.Info("deploy: superseded while waiting for the release build", "project", p.Name, "environment", d.Environment, "tag", d.Tag, "id", d.ID, "by", newer.ID)
}

// buildVerdict says what becomes of d, waiting for its release build, now:
// it goes (true), it fails (why), or it waits on (false, "").
func (s *Server) buildVerdict(p model.Project, d model.Deploy, now time.Time) (bool, string) {
	e, ok := p.Environment(d.Environment)
	if !ok {
		return false, "the environment was removed while it waited"
	}
	if !e.WaitsForBuild() {
		return true, "" // the setting was turned off: it waits no more
	}
	tag := cmp.Or(d.Tag, d.TriggerRef)
	waited := now.Sub(d.CreatedAt)
	b, err := s.releaseBuildOf(p.ID, tag)
	if err != nil {
		if waited > waitNoBuild {
			return false, fmt.Sprintf("Dash found no release build of %s on GitHub Actions in %s, so it didn't deploy it", tag, waitText(waitNoBuild))
		}
		return false, ""
	}
	if b.SHA != "" && d.Ref != "" && !strings.HasPrefix(b.SHA, d.Ref) && !strings.HasPrefix(d.Ref, b.SHA) {
		return false, fmt.Sprintf("%s was moved to %s while its deploy waited for the release build, so it didn't deploy", tag, gitops.ShortSHA(b.SHA))
	}
	switch b.State {
	case ci.ReleasePublished:
		return true, ""
	case ci.ReleaseFailed:
		var failed []string
		for _, r := range b.Runs {
			if r.Failed() {
				failed = append(failed, r.Workflow)
			}
		}
		which := ""
		if len(failed) > 0 {
			which = " (" + strings.Join(failed, ", ") + ")"
		}
		return false, fmt.Sprintf("the release build of %s failed%s, so it didn't deploy", tag, which)
	case ci.ReleaseNoBuild:
		return false, fmt.Sprintf("no GitHub Actions workflow ran for %s, so there was no release build to wait for and it didn't deploy", tag)
	}
	if waited > waitBuildFor {
		return false, fmt.Sprintf("the release build of %s didn't finish within %s, so it didn't deploy", tag, waitText(waitBuildFor))
	}
	return false, ""
}

// waitText says d in words: "15 minutes", "6 hours".
func waitText(d time.Duration) string {
	if d >= time.Hour && d%time.Hour == 0 {
		return textutil.Count(int(d/time.Hour), "hour", "hours")
	}
	return textutil.Count(int(d/time.Minute), "minute", "minutes")
}

// releaseWaiting moves d on once its release build passed: checked as a
// new deploy is (the requirements, the freezes) for whoever tagged, then
// queued, or awaiting an approval. One that is blocked now fails, with why.
func (s *Server) releaseWaiting(p model.Project, d model.Deploy) {
	c := Caller{Org: p.OrgID, User: d.By}
	next, err := s.as(c).prepareDeploy(p, deployAsk{Environment: d.Environment, Commit: d.Ref, Trigger: model.DeployByRelease, TriggerRef: d.TriggerRef})
	if err != nil {
		s.failWaiting(p, d, plainErr(err))
		return
	}
	status := model.DeployQueued
	if next.Status == model.DeployAwaiting {
		status = model.DeployAwaiting
	}
	if _, err := s.Store.UpdateDeploy(d.ID, func(x *model.Deploy) error {
		if x.Status != model.DeployWaitingBuild {
			return errUnchanged // cancelled meanwhile
		}
		x.Ref, x.Tag, x.NeedsApproval = next.Ref, next.Tag, next.NeedsApproval
		x.SetStatus(status, time.Now())
		return nil
	}); err != nil {
		return
	}
	slog.Info("deploy: release build passed", "project", p.Name, "environment", d.Environment, "tag", d.Tag, "id", d.ID, "status", status)
	s.kickDeploys(d.ProjectID, d.Environment)
}

// failWaiting fails d, waiting for its release build, with why: it is
// notified like any failed deploy.
func (s *Server) failWaiting(p model.Project, d model.Deploy, why string) {
	if _, err := s.Store.UpdateDeploy(d.ID, func(x *model.Deploy) error {
		if x.Status != model.DeployWaitingBuild {
			return errUnchanged
		}
		x.Error = why
		x.SetStatus(model.DeployFailed, time.Now())
		return nil
	}); err != nil {
		return
	}
	slog.Info("deploy: not deployed after waiting for the release build", "project", p.Name, "environment", d.Environment, "tag", d.Tag, "id", d.ID, "why", why)
}

// ---- the build state the Deploy… picker shows ----

// Tag build states (tagBuild.State).
const (
	TagBuilding = "building"
	TagPassed   = "passed"
	TagFailed   = "failed"
	// TagUnknown: Dash knows of no runs of the tag (none ran, or it isn't
	// on GitHub, or the tag was moved since).
	TagUnknown = "unknown"
)

// tagBuild is how a release tag's build went, as Deploy's picker shows it.
type tagBuild struct {
	State string `json:"state"`
	// URL is where its runs are (one run's page, or the repository's
	// Actions filtered by the tag), "" when unknown.
	URL  string        `json:"url,omitempty"`
	Runs []model.CIRun `json:"runs,omitempty"`
}

// tagBuildOf is what b, the saved build of a tag on commit, says.
func tagBuildOf(p model.Project, b model.ReleaseBuild, found bool, commit string) *tagBuild {
	out := &tagBuild{State: TagUnknown}
	if !found || commit != "" && b.SHA != "" && b.SHA != commit {
		return out
	}
	switch b.State {
	case ci.ReleaseBuilding:
		out.State = TagBuilding
	case ci.ReleasePublished:
		out.State = TagPassed
	case ci.ReleaseFailed:
		out.State = TagFailed
	default:
		return out
	}
	out.Runs = b.Runs
	switch {
	case len(b.Runs) == 1:
		out.URL = b.Runs[0].URL
	default:
		out.URL = actionsPage(p.RemoteURL, b.Tag)
	}
	return out
}

// actionsPage is the repository's GitHub Actions runs of ref ("" when
// origin isn't on GitHub).
func actionsPage(remote, ref string) string {
	name := gitops.RepoName(remote)
	if !strings.HasPrefix(name, "github.com/") {
		return ""
	}
	return "https://" + name + "/actions?query=" + url.QueryEscape("branch:"+ref)
}

// addTagBuilds gives each of tags (and the commit asked about) its build.
func (s *Server) addTagBuilds(p model.Project, out *deployTags) {
	bs, err := s.releaseBuildsOf(p.ID)
	if err != nil {
		slog.Debug("release builds", "project", p.Name, "err", err)
	}
	byTag := map[string]model.ReleaseBuild{}
	for _, b := range bs {
		byTag[b.Tag] = b
	}
	for i, t := range out.Tags {
		b, ok := byTag[t.Tag]
		out.Tags[i].Build = tagBuildOf(p, b, ok, t.Commit)
	}
	if len(out.CommitTags) > 0 {
		b, ok := byTag[out.CommitTags[0]]
		out.CommitBuild = tagBuildOf(p, b, ok, out.Commit)
	}
}

func (s *Server) releaseBuildsOf(projectID string) ([]model.ReleaseBuild, error) {
	if s.CI == nil {
		return nil, nil
	}
	return s.CI.ReleaseBuilds(projectID, store.KeepReleaseBuilds)
}
