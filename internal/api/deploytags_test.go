package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// tagRepo makes a repository of four commits: the first tagged 1.0.0
// (annotated), the second untagged but for a tag in another format, the
// third tagged 1.1.0 (lightweight), the fourth untagged. It returns the
// commits, oldest first.
func tagRepo(t *testing.T) (string, []string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	var commits []string
	for _, f := range []string{"a", "b", "c", "d"} {
		if err := os.WriteFile(filepath.Join(repo, f), []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "add", f)
		git(t, repo, "commit", "-q", "-m", "add "+f)
		commits = append(commits, git(t, repo, "rev-parse", "HEAD"))
	}
	git(t, repo, "tag", "-a", "1.0.0", "-m", "Release 1.0.0", commits[0])
	git(t, repo, "tag", "nightly", commits[1])
	git(t, repo, "tag", "1.1.0", commits[2])
	return repo, commits
}

// The data Deploy's picker and History's "Deploy to…" use: the release
// tags (in the project's format only), newest first, each compared with
// what the environment runs; and a commit's own release tags.
func TestDeployTagsPicker(t *testing.T) {
	s, _, _ := boardServer(t)
	holdDeploys(s)
	s.Git = gitops.New(s.Hub)
	repo, c := tagRepo(t)
	p, err := raw(s).CreateProject(model.Project{Name: "tags", OrgID: testOwner(t, s).Org, Path: repo, DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	p = withEnvs(t, s, p, model.Environment{Name: "staging", DeployCommand: "true"})
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

	// Nothing live: the tags, newest first, not compared.
	got := get("environment=staging")
	if len(got.Tags) != 2 || got.Tags[0].Tag != "1.1.0" || got.Tags[0].Commit != c[2] || got.Tags[1].Tag != "1.0.0" || got.Tags[1].Commit != c[0] ||
		got.Tags[0].Compared || got.Live != "" || got.Format != "X.Y.Z" {
		t.Fatalf("no live: %+v", got)
	}
	// 1.0.0 live: 1.1.0 is 2 commits ahead, 1.0.0 the same.
	liveDeploy(t, s, p, "staging", c[0], time.Now())
	got = get("environment=staging")
	if got.Live != c[0] || !got.Tags[0].Compared || got.Tags[0].Ahead != 2 || got.Tags[0].Behind != 0 ||
		!got.Tags[1].Compared || got.Tags[1].Ahead != 0 || got.Tags[1].Behind != 0 {
		t.Fatalf("1.0.0 live: %+v", got)
	}
	// The newest commit live: both tags are behind.
	liveDeploy(t, s, p, "staging", c[3], time.Now())
	got = get("environment=staging")
	if got.Tags[0].Ahead != 0 || got.Tags[0].Behind != 1 || got.Tags[1].Behind != 3 {
		t.Fatalf("tip live: %+v", got)
	}
	// A commit's release tags (a tag in another format doesn't count).
	if got := get("commit=" + c[0][:7]); got.Commit != c[0] || strings.Join(got.CommitTags, ",") != "1.0.0" {
		t.Errorf("tagged commit: %+v", got)
	}
	if got := get("commit=" + c[1]); got.Commit != c[1] || len(got.CommitTags) != 0 {
		t.Errorf("untagged commit: %+v", got)
	}
	// Another tag format: other tags.
	if _, err := raw(s).UpdateProject(p.ID, func(x *model.Project) error { x.TagFormat = "vX.Y.Z"; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := get("environment=staging"); len(got.Tags) != 0 || got.Format != "vX.Y.Z" {
		t.Errorf("vX.Y.Z: %+v", got)
	}
}

// "Deploys: release tags only": saved and loaded with the environment, off
// by default. With it on, an untagged commit is refused whatever asks (the
// API, as History and Deploy again do; the deploy tool; promote; a
// rollback to a commit deployed before it was on), a tagged one goes, a
// release tag's own deploy always does, and merges deploy nothing. With it
// off, nothing changes.
func TestReleaseTagsOnly(t *testing.T) {
	s, _, _ := boardServer(t)
	holdDeploys(s)
	s.Git = gitops.New(s.Hub)
	repo, c := tagRepo(t)
	p, err := raw(s).CreateProject(model.Project{Name: "tagsonly", OrgID: testOwner(t, s).Org, Path: repo, DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest(method, path, strings.NewReader(body))))
		return w
	}
	setEnvs := func(envs ...model.Environment) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"environments": envs})
		if w := call("PATCH", "/api/projects/"+p.ID, string(b)); w.Code != http.StatusOK {
			t.Fatalf("set environments: %d %s", w.Code, w.Body)
		}
	}
	envs := func(only bool) []model.Environment {
		return []model.Environment{
			{Name: "dev", DeployCommand: "true"},
			{Name: "staging", DeployCommand: "true", ReleaseTagsOnly: only},
			{Name: "merged", DeployCommand: "true", Trigger: model.EnvTriggerMerge, ReleaseTagsOnly: only},
		}
	}
	deploy := func(env, commit string) *httptest.ResponseRecorder {
		t.Helper()
		return call("POST", "/api/projects/"+p.ID+"/deploys", `{"environment":"`+env+`","commit":"`+commit+`"}`)
	}
	clear := func() {
		t.Helper()
		for _, d := range raw(s).ListDeploys(p.ID, "") {
			_, _ = raw(s).UpdateDeploy(d.ID, func(x *model.Deploy) error { x.SetStatus(model.DeploySuperseded, time.Now()); return nil })
		}
	}

	// Off by default, and as it was: any commit deploys, merges deploy.
	setEnvs(envs(false)...)
	var loaded model.Project
	_ = json.Unmarshal(call("GET", "/api/projects/"+p.ID, "").Body.Bytes(), &loaded)
	if e, _ := loaded.Environment("staging"); e.ReleaseTagsOnly {
		t.Fatalf("on by default: %+v", e)
	}
	if w := deploy("staging", c[1]); w.Code != http.StatusCreated {
		t.Fatalf("off: untagged commit: %d %s", w.Code, w.Body)
	}
	td, _ := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "x"})
	s.deployMerge(merged(t, s, td.ID, c[3]))
	if n := len(raw(s).ListDeploys(p.ID, "merged")); n != 1 {
		t.Errorf("off: merge deploys: %d", n)
	}
	clear()

	// On: saved, loaded, and in the settings history.
	setEnvs(envs(true)...)
	loaded = model.Project{}
	_ = json.Unmarshal(call("GET", "/api/projects/"+p.ID, "").Body.Bytes(), &loaded)
	if e, _ := loaded.Environment("staging"); !e.ReleaseTagsOnly {
		t.Fatalf("not saved: %+v", e)
	}
	if e, _ := loaded.Environment("dev"); e.ReleaseTagsOnly {
		t.Fatalf("dev took it too: %+v", e)
	}
	changes, _ := raw(s).EnvChanges(p.ID, 10)
	found := false
	for _, ch := range changes {
		for _, f := range ch.Diff {
			found = found || ch.Environment == "staging" && f.Field == "release_tags_only" && f.To == "true"
		}
	}
	if !found {
		t.Errorf("not in the settings history: %+v", changes)
	}
	p, _ = raw(s).GetProject(p.ID)

	// The API (History's "Deploy to…", Deploy again): untagged refused, tagged goes.
	w := deploy("staging", c[1])
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "staging only deploys release tags; "+gitops.ShortSHA(c[1])+" isn't tagged") {
		t.Errorf("untagged: %d %s", w.Code, w.Body)
	}
	if w := deploy("staging", ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "pick a release tag") {
		t.Errorf("the branch's tip: %d %s", w.Code, w.Body)
	}
	w = deploy("staging", c[0])
	var d model.Deploy
	_ = json.Unmarshal(w.Body.Bytes(), &d)
	if w.Code != http.StatusCreated || d.Tag != "1.0.0" {
		t.Errorf("tagged: %d %s", w.Code, w.Body)
	}
	if w := deploy("staging", "1.1.0"); w.Code != http.StatusCreated {
		t.Errorf("by tag name: %d %s", w.Code, w.Body)
	}
	if w := deploy("dev", c[1]); w.Code != http.StatusCreated {
		t.Errorf("another environment: %d %s", w.Code, w.Body)
	}
	clear()

	// The deploy tool.
	token, release := s.Board.Register(chatCaller(t, s), p.ID)
	defer release()
	if out, isErr := toolCall(s.Routes(), token, "deploy", `{"environment":"staging","commit":"`+c[3]+`"}`); !isErr || !strings.Contains(out, "isn't tagged") {
		t.Errorf("tool, untagged: %v %s", isErr, out)
	}
	if out, isErr := toolCall(s.Routes(), token, "deploy", `{"environment":"staging","commit":"`+c[2]+`"}`); isErr {
		t.Errorf("tool, tagged: %s", out)
	}
	clear()

	// Promote: what is live on dev must be tagged.
	liveDeploy(t, s, p, "dev", c[1], time.Now())
	w = call("POST", "/api/projects/"+p.ID+"/deploys", `{"environment":"staging","promote_from":"dev"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), gitops.ShortSHA(c[1])+" (live on dev) isn't tagged") {
		t.Errorf("promote untagged: %d %s", w.Code, w.Body)
	}
	liveDeploy(t, s, p, "dev", c[2], time.Now())
	if w := call("POST", "/api/projects/"+p.ID+"/deploys", `{"environment":"staging","promote_from":"dev"}`); w.Code != http.StatusCreated {
		t.Errorf("promote tagged: %d %s", w.Code, w.Body)
	}
	clear()

	// Rollback: to a tagged commit, yes; to one deployed before the setting, no.
	liveDeploy(t, s, p, "staging", c[1], time.Now().Add(-time.Hour))
	bad := liveDeploy(t, s, p, "staging", c[2], time.Now())
	w = call("POST", "/api/deploys/"+bad.ID+"/rollback", "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "isn't tagged") || !strings.Contains(w.Body.String(), "deployed before") {
		t.Errorf("rollback to untagged: %d %s", w.Code, w.Body)
	}
	clear()
	liveDeploy(t, s, p, "staging", c[0], time.Now().Add(-time.Hour))
	bad = liveDeploy(t, s, p, "staging", c[2], time.Now())
	w = call("POST", "/api/deploys/"+bad.ID+"/rollback", "")
	_ = json.Unmarshal(w.Body.Bytes(), &d)
	if w.Code != http.StatusCreated || d.Ref != c[0] || d.Tag != "1.0.0" {
		t.Errorf("rollback to tagged: %d %s", w.Code, w.Body)
	}
	clear()

	// Merges deploy nothing there; a release tag's deploy always does.
	td2, _ := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "y"})
	s.deployMerge(merged(t, s, td2.ID, c[3]))
	if n := len(raw(s).ListDeploys(p.ID, "merged")); n != 1 { // the one from before
		t.Errorf("on: merge deploys: %d", n)
	}
	if e, _ := p.Environment("merged"); e.Trigger != model.EnvTriggerMerge || !e.ReleaseTagsOnly || e.DeploysOnMerge() {
		t.Errorf("merged: trigger %q, release tags only %v, deploys on merge %v", e.Trigger, e.ReleaseTagsOnly, e.DeploysOnMerge())
	}
	staging, _ := p.Environment("staging")
	if d, err := s.as(Caller{Org: p.OrgID}).requestDeploy(p, deployAsk{Environment: "staging", Commit: c[3], Trigger: model.DeployByRelease, TriggerRef: "2.0.0"}); err != nil || d.Tag != "2.0.0" {
		t.Errorf("a release tag's deploy: %+v %v", d, err)
	}

	// Where the deploy runs, it is checked again: one queued before the
	// setting was on doesn't slip through.
	s.DeployRoot, s.EnvDir = t.TempDir(), t.TempDir()
	_, err = s.startDeployHere(t.Context(), p, deployRun{ID: "x1", Environment: staging.Name, Commit: c[3], TagFormat: "X.Y.Z", ReleaseTagsOnly: true}, false)
	if err == nil || !strings.Contains(err.Error(), "isn't tagged") {
		t.Errorf("started untagged: %v", err)
	}
}

// Each deploy records the release tag on its commit when it starts (the
// deploy list and detail show "1.0.0 (a1b2c3d)"), none for an untagged one.
func TestDeployRecordsItsTag(t *testing.T) {
	s, _, _ := boardServer(t)
	s.Git = gitops.New(s.Hub)
	s.DeployPoll = 20 * time.Millisecond
	s.DeployRoot, s.EnvDir = t.TempDir(), t.TempDir()
	repo, c := tagRepo(t)
	p, err := raw(s).CreateProject(model.Project{Name: "tagrec", OrgID: testOwner(t, s).Org, Path: repo, DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	p = withEnvs(t, s, p, model.Environment{Name: "staging", DeployCommand: "true"})
	run := func(commit string) model.Deploy {
		t.Helper()
		d, err := s.as(Caller{Org: p.OrgID}).requestDeploy(p, deployAsk{Environment: "staging", Commit: commit, Trigger: model.DeployByUser})
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "the deploy to finish", func() bool {
			d, _ = raw(s).GetDeploy(d.ID)
			return d.Status.Done()
		})
		return d
	}
	if d := run(c[0]); d.Status != model.DeployLive || d.Tag != "1.0.0" || d.Commit != c[0] {
		t.Errorf("tagged: %+v", d)
	}
	if d := run(c[1]); d.Status != model.DeployLive || d.Tag != "" {
		t.Errorf("untagged: %+v", d)
	}
}

// A tag named like a commit id (git resolves it before the commit) can't
// stand in for that commit: the hub pins a commit id to the full one and
// refuses a tag or branch of that name that is another commit, and the
// runner refuses to start a deploy of a commit id that resolves to another.
func TestDeployRefNamedLikeCommit(t *testing.T) {
	s, _, _ := boardServer(t)
	holdDeploys(s)
	s.Git = gitops.New(s.Hub)
	repo, c := tagRepo(t)
	short := c[0][:7]
	git(t, repo, "tag", short, c[3]) // "staging's commit", pointing at another
	p, err := raw(s).CreateProject(model.Project{Name: "pin", OrgID: testOwner(t, s).Org, Path: repo, DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	p = withEnvs(t, s, p,
		model.Environment{Name: "staging", DeployCommand: "true"},
		model.Environment{Name: "qa", DeployCommand: "true", RequireLiveOn: "staging"},
		model.Environment{Name: "dev", DeployCommand: "true"})
	liveDeploy(t, s, p, "staging", c[0], time.Now())
	sv := s.as(Caller{Org: p.OrgID})

	if _, err := sv.requestDeploy(p, deployAsk{Environment: "qa", Commit: short, Trigger: model.DeployByUser}); err == nil || !strings.Contains(err.Error(), "names a tag or branch") {
		t.Errorf("tag named like staging's commit: %v", err)
	}
	if d, err := sv.requestDeploy(p, deployAsk{Environment: "qa", Commit: c[0], Trigger: model.DeployByUser}); err != nil || d.Ref != c[0] {
		t.Errorf("staging's commit by full id: %+v %v", d, err)
	}
	if d, err := sv.requestDeploy(p, deployAsk{Environment: "dev", Commit: c[1][:7], Trigger: model.DeployByUser}); err != nil || d.Ref != c[1] {
		t.Errorf("short id not pinned: %+v %v", d, err)
	}

	// The runner: a commit id resolves to that commit or is refused; a name
	// resolves as before.
	if _, err := s.deployCommitOf(t.Context(), p, short); err == nil || !strings.Contains(err.Error(), "names a tag or branch") {
		t.Errorf("runner, tag named like a commit: %v", err)
	}
	if got, err := s.deployCommitOf(t.Context(), p, c[0]); err != nil || got.SHA != c[0] {
		t.Errorf("runner, full id: %+v %v", got, err)
	}
	if got, err := s.deployCommitOf(t.Context(), p, "main"); err != nil || got.SHA != c[3] {
		t.Errorf("runner, branch: %+v %v", got, err)
	}
	if got, err := s.deployCommitOf(t.Context(), p, "1.0.0"); err != nil || got.SHA != c[0] {
		t.Errorf("runner, release tag: %+v %v", got, err)
	}
	s.DeployRoot, s.EnvDir = t.TempDir(), t.TempDir()
	if _, err := s.startDeployHere(t.Context(), p, deployRun{ID: "x1", Environment: "qa", Commit: short}, false); err == nil || !strings.Contains(err.Error(), "names a tag or branch") {
		t.Errorf("started a tag named like a commit: %v", err)
	}
}
