package api

import (
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/ci"
	"github.com/klahr/dash/internal/model"
)

// waitSetup is a server whose project p has an environment, staging, that
// waits for the release build, and a second project, q, with one too.
func waitSetup(t *testing.T) (s *Server, evs *events, p, q model.Project, release func(model.Project, string, string), build func(model.Project, string, string, string)) {
	t.Helper()
	s, evs, p = boardServer(t)
	holdDeploys(s)
	s.CI = &ci.Watcher{Store: raw(s)}
	env := model.Environment{Name: "staging", DeployCommand: "true", Trigger: model.EnvTriggerRelease, WaitForBuild: true}
	p = withEnvs(t, s, p, env)
	for _, x := range raw(s).ListProjects() {
		if x.Name == "other" {
			q = withEnvs(t, s, x, env)
		}
	}
	owner := testOwner(t, s)
	release = func(p model.Project, tag, commit string) {
		t.Helper()
		s.deployRelease(p, tag, commit, Caller{Org: owner.Org, User: owner.User})
	}
	build = func(p model.Project, tag, commit, state string) {
		t.Helper()
		if err := raw(s).PutReleaseBuild(model.ReleaseBuild{ProjectID: p.ID, Tag: tag, SHA: commit, State: state, Pushed: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func newestDeploy(t *testing.T, s *Server, p model.Project) model.Deploy {
	t.Helper()
	ds := raw(s).ListDeploys(p.ID, "staging")
	if len(ds) == 0 {
		t.Fatal("no deploy")
	}
	return ds[0]
}

// The CI watcher's word that a tag's build ended moves its waiting deploy
// on at once, without a tick: queued when it passed, failed when it failed
// or none ran.
func TestBuildEndedMovesWaitingDeploy(t *testing.T) {
	s, _, p, _, release, build := waitSetup(t)
	sha := func(c string) string { return strings.Repeat(c, 40) }

	build(p, "1.0.0", sha("a"), ci.ReleaseBuilding)
	release(p, "1.0.0", sha("a"))
	build(p, "1.0.0", sha("a"), ci.ReleasePublished)
	s.releaseBuildEnded(p.ID, "1.0.0")
	if d := newestDeploy(t, s, p); d.Status != model.DeployQueued || d.Tag != "1.0.0" {
		t.Fatalf("passed: %+v", d)
	}
	// Out of the way, so the next ones aren't superseded by it.
	if _, err := raw(s).UpdateDeploy(newestDeploy(t, s, p).ID, func(x *model.Deploy) error {
		x.SetStatus(model.DeployFailed, time.Now())
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	build(p, "1.0.1", sha("b"), ci.ReleaseBuilding)
	release(p, "1.0.1", sha("b"))
	build(p, "1.0.1", sha("b"), ci.ReleaseFailed)
	s.releaseBuildEnded(p.ID, "1.0.1")
	if d := newestDeploy(t, s, p); d.Status != model.DeployFailed || !strings.Contains(d.Error, "the release build of 1.0.1 failed") {
		t.Fatalf("failed: %+v", d)
	}

	build(p, "1.0.2", sha("c"), ci.ReleaseBuilding)
	release(p, "1.0.2", sha("c"))
	build(p, "1.0.2", sha("c"), ci.ReleaseNoBuild)
	s.releaseBuildEnded(p.ID, "1.0.2")
	if d := newestDeploy(t, s, p); d.Status != model.DeployFailed || !strings.Contains(d.Error, "no GitHub Actions workflow ran for 1.0.2") {
		t.Fatalf("no build: %+v", d)
	}
}

// The watcher's word and a tick for the same build, even at once, queue
// the deploy once.
func TestBuildEndedAndTickQueueOnce(t *testing.T) {
	s, evs, p, _, release, build := waitSetup(t)
	sha := strings.Repeat("a", 40)
	build(p, "1.0.0", sha, ci.ReleaseBuilding)
	release(p, "1.0.0", sha)
	d := newestDeploy(t, s, p)
	build(p, "1.0.0", sha, ci.ReleasePublished)
	done := make(chan struct{})
	go func() { s.releaseBuildEnded(p.ID, "1.0.0"); close(done) }()
	s.pollBuildWaits(time.Now())
	<-done
	s.releaseBuildEnded(p.ID, "1.0.0")
	s.checkBuildWaits(time.Now())

	queued := 0
	evs.mu.Lock()
	for _, ev := range evs.evs {
		if x, ok := ev.Data.(model.Deploy); ok && ev.Type == model.EvDeployUpdated && x.ID == d.ID && x.Status == model.DeployQueued {
			queued++
		}
	}
	evs.mu.Unlock()
	if queued != 1 {
		t.Fatalf("queued %d times, want once", queued)
	}
	if d := newestDeploy(t, s, p); d.Status != model.DeployQueued {
		t.Fatalf("%+v", d)
	}
}

// The watcher's word of one tag's build leaves the deploys waiting for
// another tag, or in another project, waiting.
func TestBuildEndedOnlyItsTag(t *testing.T) {
	s, _, p, q, release, build := waitSetup(t)
	sha := strings.Repeat("a", 40)
	other := strings.Repeat("b", 40)
	// q waits for 1.0.0, p for 2.0.0; both builds passed, as saved. The
	// word that p's 1.0.0 ended concerns neither.
	build(q, "1.0.0", sha, ci.ReleaseBuilding)
	release(q, "1.0.0", sha)
	build(p, "2.0.0", other, ci.ReleaseBuilding)
	release(p, "2.0.0", other)
	build(q, "1.0.0", sha, ci.ReleasePublished)
	build(p, "2.0.0", other, ci.ReleasePublished)
	build(p, "1.0.0", sha, ci.ReleasePublished)
	s.releaseBuildEnded(p.ID, "1.0.0")
	if d := newestDeploy(t, s, q); d.Status != model.DeployWaitingBuild {
		t.Fatalf("other project: %+v", d)
	}
	if d := newestDeploy(t, s, p); d.Status != model.DeployWaitingBuild || d.Tag != "2.0.0" {
		t.Fatalf("other tag: %+v", d)
	}
	// Its own word moves it.
	s.releaseBuildEnded(q.ID, "1.0.0")
	if d := newestDeploy(t, s, q); d.Status != model.DeployQueued {
		t.Fatalf("own project: %+v", d)
	}
	if d := newestDeploy(t, s, p); d.Status != model.DeployWaitingBuild {
		t.Fatalf("other tag after q's word: %+v", d)
	}
}

// While a project's webhook works, the ticker looks at its waiting deploys
// only every buildWaitSlow; otherwise every buildWaitPoll.
func TestBuildWaitPollFollowsHook(t *testing.T) {
	s, _, p, q, release, build := waitSetup(t)
	p, err := raw(s).UpdateProject(p.ID, func(x *model.Project) error { x.RemoteURL = "https://github.com/o/r.git"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	s.CI.Hooks = &ci.Hooks{Store: raw(s), PublicURL: "https://dash.example.com"}
	if got := s.buildWaitEvery(p.ID); got != 15*time.Second {
		t.Fatalf("no hook: %v", got)
	}
	now := time.Now()
	if err := raw(s).PutGitHubHook(model.GitHubHook{Repo: "o/r", HookID: 1, URL: s.CI.Hooks.URL(), State: ci.HookActive,
		Registered: now.Add(-time.Minute), LastDelivery: now}); err != nil {
		t.Fatal(err)
	}
	if got := s.buildWaitEvery(p.ID); got != buildWaitSlow {
		t.Fatalf("hook active: %v", got)
	}
	if got := s.buildWaitEvery(q.ID); got != 15*time.Second {
		t.Fatalf("project without a hook: %v", got)
	}

	// The ticker: p (hook) is looked at once, then not again until
	// buildWaitSlow has passed; q (no hook) at every tick.
	sha := strings.Repeat("a", 40)
	build(p, "1.0.0", sha, ci.ReleaseBuilding)
	release(p, "1.0.0", sha)
	build(q, "1.0.0", sha, ci.ReleaseBuilding)
	release(q, "1.0.0", sha)
	s.pollBuildWaits(now)
	build(p, "1.0.0", sha, ci.ReleasePublished)
	build(q, "1.0.0", sha, ci.ReleasePublished)
	s.pollBuildWaits(now.Add(15 * time.Second))
	if d := newestDeploy(t, s, q); d.Status != model.DeployQueued {
		t.Fatalf("no hook, next tick: %+v", d)
	}
	if d := newestDeploy(t, s, p); d.Status != model.DeployWaitingBuild {
		t.Fatalf("hook, next tick: %+v", d)
	}
	s.pollBuildWaits(now.Add(buildWaitSlow))
	if d := newestDeploy(t, s, p); d.Status != model.DeployQueued {
		t.Fatalf("hook, after the slow fallback: %+v", d)
	}
}
