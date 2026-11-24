package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/ci"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// An environment that waits for the release build: a release tag's deploy
// is recorded waiting, goes on (queued, or awaiting an approval) once the
// tag's build passed, and fails with why when it failed, none ran, Dash
// found none, or it took too long. One that doesn't wait is queued at once.
func TestReleaseDeployWaitsForBuild(t *testing.T) {
	s, _, p := boardServer(t)
	holdDeploys(s)
	s.CI = &ci.Watcher{Store: raw(s)}
	p = withEnvs(t, s, p,
		model.Environment{Name: "staging", DeployCommand: "true", Trigger: model.EnvTriggerRelease, WaitForBuild: true},
		model.Environment{Name: "production", DeployCommand: "true", Trigger: model.EnvTriggerRelease, WaitForBuild: true},
		model.Environment{Name: "fast", DeployCommand: "true", Trigger: model.EnvTriggerRelease})
	owner := testOwner(t, s)
	sha := func(c string) string { return strings.Repeat(c, 40) }
	build := func(tag, commit, state string, runs ...model.CIRun) {
		t.Helper()
		if err := raw(s).PutReleaseBuild(model.ReleaseBuild{ProjectID: p.ID, Tag: tag, SHA: commit, State: state, Runs: runs, Pushed: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	newest := func(env string) model.Deploy {
		t.Helper()
		ds := raw(s).ListDeploys(p.ID, env)
		if len(ds) == 0 {
			t.Fatalf("no deploy to %s", env)
		}
		return ds[0]
	}
	release := func(tag, commit string) {
		t.Helper()
		s.deployRelease(p, tag, commit, Caller{Org: owner.Org, User: owner.User})
	}

	// Building: the waiting deploys wait, the other is queued.
	build("1.0.0", sha("a"), ci.ReleaseBuilding)
	release("1.0.0", sha("a"))
	for _, env := range []string{"staging", "production"} {
		if d := newest(env); d.Status != model.DeployWaitingBuild || d.Tag != "1.0.0" || d.Ref != sha("a") || d.Trigger != model.DeployByRelease || d.By != owner.User {
			t.Fatalf("%s: %+v", env, d)
		}
	}
	if d := newest("fast"); d.Status != model.DeployQueued {
		t.Fatalf("fast: %+v", d)
	}
	s.checkBuildWaits(time.Now())
	if d := newest("staging"); d.Status != model.DeployWaitingBuild {
		t.Fatalf("still building, staging: %+v", d)
	}

	// A restart keeps the wait: a new CI watcher reads the build from the
	// store, and the waiting deploys from the store go on once it passed.
	s.CI = &ci.Watcher{Store: raw(s)}
	build("1.0.0", sha("a"), ci.ReleasePublished)
	s.checkBuildWaits(time.Now())
	if d := newest("staging"); d.Status != model.DeployQueued || d.Error != "" {
		t.Fatalf("passed, staging: %+v", d)
	}
	if d := newest("production"); d.Status != model.DeployAwaiting || d.NeedsApproval == "" {
		t.Fatalf("passed, production needs an approval: %+v", d)
	}

	// The build failed: no deploy, and the error says which workflow.
	build("1.0.1", sha("b"), ci.ReleaseFailed,
		model.CIRun{Workflow: "Release", Status: "completed", Conclusion: "failure", SHA: sha("b")},
		model.CIRun{Workflow: "Tests", Status: "completed", Conclusion: "success", SHA: sha("b")})
	release("1.0.1", sha("b"))
	s.checkBuildWaits(time.Now())
	if d := newest("staging"); d.Status != model.DeployFailed || d.Error != "the release build of 1.0.1 failed (Release), so it didn't deploy" {
		t.Fatalf("failed build: %+v", d)
	}

	// No workflow ran.
	build("1.0.2", sha("c"), ci.ReleaseNoBuild)
	release("1.0.2", sha("c"))
	s.checkBuildWaits(time.Now())
	if d := newest("staging"); d.Status != model.DeployFailed || !strings.Contains(d.Error, "no GitHub Actions workflow ran for 1.0.2") {
		t.Fatalf("no build: %+v", d)
	}

	// Dash knows of no build: it waits a while, then gives up.
	release("1.0.3", sha("d"))
	s.checkBuildWaits(time.Now())
	if d := newest("staging"); d.Status != model.DeployWaitingBuild {
		t.Fatalf("no build yet: %+v", d)
	}
	s.checkBuildWaits(time.Now().Add(waitNoBuild + time.Minute))
	if d := newest("staging"); d.Status != model.DeployFailed || !strings.Contains(d.Error, "found no release build of 1.0.3") {
		t.Fatalf("no build found: %+v", d)
	}

	// Building for too long.
	build("1.0.4", sha("e"), ci.ReleaseBuilding)
	release("1.0.4", sha("e"))
	s.checkBuildWaits(time.Now().Add(waitBuildFor + time.Minute))
	if d := newest("staging"); d.Status != model.DeployFailed || !strings.Contains(d.Error, "didn't finish within 6 hours") {
		t.Fatalf("too long: %+v", d)
	}

	// The tag moved to another commit meanwhile.
	build("1.0.5", sha("f"), ci.ReleasePublished)
	release("1.0.5", sha("9"))
	s.checkBuildWaits(time.Now())
	if d := newest("staging"); d.Status != model.DeployFailed || !strings.Contains(d.Error, "was moved") {
		t.Fatalf("moved tag: %+v", d)
	}

	// A waiting deploy can be cancelled; it then never goes.
	build("1.0.6", sha("1"), ci.ReleaseBuilding)
	release("1.0.6", sha("1"))
	waiting := newest("staging")
	w := httptest.NewRecorder()
	s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest("DELETE", "/api/deploys/"+waiting.ID, nil)))
	if w.Code != http.StatusAccepted {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	build("1.0.6", sha("1"), ci.ReleasePublished)
	s.checkBuildWaits(time.Now())
	if d, _ := raw(s).GetDeploy(waiting.ID); d.Status != model.DeployFailed || !d.Cancelled {
		t.Fatalf("cancelled: %+v", d)
	}

	// A freeze started while it waited: once the build passed, the deploy
	// is checked as a new one is, and fails with why.
	envs := p.Environments
	build("1.0.8", sha("3"), ci.ReleaseBuilding)
	release("1.0.8", sha("3"))
	frozen := append([]model.Environment(nil), envs...)
	for i := range frozen {
		if frozen[i].Name == "staging" {
			frozen[i].Freezes = []model.Freeze{{From: "2000-01-01 00:00", To: "2999-01-01 00:00", Note: "code freeze"}}
		}
	}
	p = withEnvs(t, s, p, frozen...)
	build("1.0.8", sha("3"), ci.ReleasePublished)
	s.checkBuildWaits(time.Now())
	if d := newest("staging"); d.Tag != "1.0.8" || d.Status != model.DeployFailed || !strings.Contains(d.Error, "frozen") || !strings.Contains(d.Error, "code freeze") {
		t.Fatalf("frozen meanwhile: %+v", d)
	}

	// The environment was removed while it waited.
	p = withEnvs(t, s, p, envs...)
	build("1.0.9", sha("4"), ci.ReleaseBuilding)
	release("1.0.9", sha("4"))
	var kept []model.Environment
	for _, e := range envs {
		if e.Name != "staging" {
			kept = append(kept, e)
		}
	}
	p = withEnvs(t, s, p, kept...)
	s.checkBuildWaits(time.Now())
	if d := newest("staging"); d.Tag != "1.0.9" || d.Status != model.DeployFailed || d.Error != "the environment was removed while it waited" {
		t.Fatalf("removed meanwhile: %+v", d)
	}
	p = withEnvs(t, s, p, envs...)

	// Turning the setting off lets a waiting deploy go.
	build("1.0.7", sha("2"), ci.ReleaseBuilding)
	release("1.0.7", sha("2"))
	envs = p.Environments
	for i := range envs {
		envs[i].WaitForBuild = false
	}
	p = withEnvs(t, s, p, envs...)
	s.checkBuildWaits(time.Now())
	if d := newest("staging"); d.Status != model.DeployQueued || d.Tag != "1.0.7" {
		t.Fatalf("setting off: %+v", d)
	}
}

// A waiting release deploy that a newer deploy to its environment
// supersedes is cancelled, not deployed after it: an older release whose
// build passes last, or one waiting while someone deploys by hand.
func TestWaitingDeploySuperseded(t *testing.T) {
	s, _, p := boardServer(t)
	holdDeploys(s)
	s.CI = &ci.Watcher{Store: raw(s)}
	p = withEnvs(t, s, p,
		model.Environment{Name: "staging", DeployCommand: "true", Trigger: model.EnvTriggerRelease, WaitForBuild: true})
	owner := testOwner(t, s)
	sha := func(c string) string { return strings.Repeat(c, 40) }
	build := func(tag, commit, state string) {
		t.Helper()
		if err := raw(s).PutReleaseBuild(model.ReleaseBuild{ProjectID: p.ID, Tag: tag, SHA: commit, State: state, Pushed: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	release := func(tag, commit string) model.Deploy {
		t.Helper()
		time.Sleep(time.Millisecond) // a later CreatedAt
		s.deployRelease(p, tag, commit, Caller{Org: owner.Org, User: owner.User})
		return raw(s).ListDeploys(p.ID, "staging")[0]
	}
	get := func(id string) model.Deploy {
		t.Helper()
		d, err := raw(s).GetDeploy(id)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	// 1.0 and 1.1 both wait; 1.1's build passes first and goes. 1.0's
	// passes later: it is superseded, not queued.
	build("1.0", sha("a"), ci.ReleaseBuilding)
	build("1.1", sha("b"), ci.ReleaseBuilding)
	old, cur := release("1.0", sha("a")), release("1.1", sha("b"))
	s.checkBuildWaits(time.Now())
	if d := get(old.ID); d.Status != model.DeployFailed || !d.Cancelled || !strings.Contains(d.Error, "superseded by 1.1") {
		t.Fatalf("1.0 while 1.1 waits: %+v", d)
	}
	build("1.1", sha("b"), ci.ReleasePublished)
	build("1.0", sha("a"), ci.ReleasePublished)
	s.checkBuildWaits(time.Now())
	if d := get(cur.ID); d.Status != model.DeployQueued {
		t.Fatalf("1.1: %+v", d)
	}

	// 1.2 waits while someone deploys 1.3 by hand: 1.2 never goes.
	build("1.2", sha("c"), ci.ReleaseBuilding)
	waiting := release("1.2", sha("c"))
	time.Sleep(time.Millisecond)
	if _, err := raw(s).CreateDeploy(model.Deploy{ProjectID: p.ID, Environment: "staging", Ref: sha("d"), Tag: "1.3",
		Trigger: model.DeployByUser, By: owner.User, Status: model.DeployQueued}); err != nil {
		t.Fatal(err)
	}
	build("1.2", sha("c"), ci.ReleasePublished)
	s.checkBuildWaits(time.Now())
	if d := get(waiting.ID); d.Status != model.DeployFailed || !d.Cancelled || !strings.Contains(d.Error, "superseded by 1.3") {
		t.Fatalf("1.2 after a hand deploy of 1.3: %+v", d)
	}
}

// A deploy waiting for its release build doesn't run, isn't done, and may
// be cancelled.
func TestWaitingBuildStatus(t *testing.T) {
	s := model.DeployWaitingBuild
	if s.Done() || s.Active() || !s.Pending() {
		t.Fatalf("waiting_build: done %v active %v pending %v", s.Done(), s.Active(), s.Pending())
	}
	if (model.Environment{WaitForBuild: true}).WaitsForBuild() {
		t.Error("an environment that doesn't deploy on release waits for a build")
	}
}

// Deploy's picker shows each release tag's build: building, passed, failed,
// or unknown (no build known, or one of another commit), with a link to
// its runs.
func TestDeployTagsShowBuilds(t *testing.T) {
	s, _, _ := boardServer(t)
	holdDeploys(s)
	s.Git = gitops.New(s.Hub)
	s.CI = &ci.Watcher{Store: raw(s)}
	repo, c := tagRepo(t)
	p, err := raw(s).CreateProject(model.Project{Name: "tags", OrgID: testOwner(t, s).Org, Path: repo, DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	p = withEnvs(t, s, p, model.Environment{Name: "staging", DeployCommand: "true"})
	run := model.CIRun{ID: 7, Workflow: "Release", Status: "in_progress", SHA: c[2], Branch: "1.1.0", URL: "https://github.com/o/r/actions/runs/7"}
	for _, b := range []model.ReleaseBuild{
		{ProjectID: p.ID, Tag: "1.1.0", SHA: c[2], State: ci.ReleaseBuilding, Runs: []model.CIRun{run}, Pushed: time.Now()},
		{ProjectID: p.ID, Tag: "1.0.0", SHA: c[1], State: ci.ReleaseFailed, Pushed: time.Now().Add(-time.Hour)}, // another commit
	} {
		if err := raw(s).PutReleaseBuild(b); err != nil {
			t.Fatal(err)
		}
	}
	get := func(q string) deployTags {
		t.Helper()
		w := httptest.NewRecorder()
		s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest("GET", "/api/projects/"+p.ID+"/deploys/tags?"+q, nil)))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", q, w.Code, w.Body)
		}
		var out deployTags
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	got := get("environment=staging")
	if len(got.Tags) != 2 {
		t.Fatalf("tags: %+v", got)
	}
	if b := got.Tags[0].Build; b == nil || b.State != TagBuilding || b.URL != run.URL || len(b.Runs) != 1 {
		t.Errorf("1.1.0: %+v", b)
	}
	if b := got.Tags[1].Build; b == nil || b.State != TagUnknown || b.URL != "" {
		t.Errorf("1.0.0 (a build of another commit): %+v", b)
	}
	// History's "Deploy to…": the commit's tag's build.
	got = get("commit=" + c[2])
	if b := got.CommitBuild; b == nil || b.State != TagBuilding {
		t.Errorf("commit build: %+v", got)
	}

	// Passed, with several runs: the link is the tag's runs on Actions.
	p.RemoteURL = "git@github.com:o/r.git"
	b := tagBuildOf(p, model.ReleaseBuild{Tag: "1.1.0", SHA: c[2], State: ci.ReleasePublished, Runs: []model.CIRun{run, run}}, true, c[2])
	if b.State != TagPassed || b.URL != "https://github.com/o/r/actions?query=branch%3A1.1.0" {
		t.Errorf("passed: %+v", b)
	}
	if b := tagBuildOf(p, model.ReleaseBuild{State: ci.ReleaseNoBuild}, true, ""); b.State != TagUnknown {
		t.Errorf("no build: %+v", b)
	}
}
