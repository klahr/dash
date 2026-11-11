package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/tunnel"
)

// runnerLinksOf is GET /api/runners/{id}/links as h.
func runnerLinksOf(t *testing.T, h http.Handler, id string) (bool, map[string]runnerLink) {
	t.Helper()
	w := send(h, "GET", "/api/runners/"+id+"/links", "")
	var out struct {
		Runner   runnerView   `json:"runner"`
		Projects []runnerLink `json:"projects"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("links of %s: %d %s", id, w.Code, w.Body)
	}
	m := map[string]runnerLink{}
	for _, l := range out.Projects {
		m[l.Name] = l
	}
	return out.Runner.CanManage, m
}

func linkResultOf(t *testing.T, w *httptest.ResponseRecorder, code int) linkResult {
	t.Helper()
	var res linkResult
	if w.Code != code || json.Unmarshal(w.Body.Bytes(), &res) != nil {
		t.Fatalf("link: %d %s, want %d", w.Code, w.Body, code)
	}
	return res
}

// From a runner's Projects screen its owner, or an admin, links it to one of
// the org's shared projects: the same link as the project's Settings →
// Runners, so the project shows it there. Others may look, not change it;
// another org's runner doesn't exist.
func TestRunnerScreenLinksSharedProjects(t *testing.T) {
	parallel(t)

	f := newFleetServer(t)
	c, u := f.ids["c-box"], f.ids["u-box"]
	body := `{"project_id":"` + f.web.ID + `"}`
	if w := send(f.c, "POST", "/api/runners/"+u+"/links", body); w.Code != http.StatusForbidden {
		t.Errorf("member linking someone else's runner: %d %s", w.Code, w.Body)
	}
	if w := send(f.b, "POST", "/api/runners/"+u+"/links", body); w.Code != http.StatusNotFound {
		t.Errorf("another org linking: %d %s", w.Code, w.Body)
	}
	if w := send(f.b, "GET", "/api/runners/"+u+"/links", ""); w.Code != http.StatusNotFound {
		t.Errorf("another org looking: %d %s", w.Code, w.Body)
	}
	if can, got := runnerLinksOf(t, f.c, u); can || got["shared"].Linked || got["shared"].GitHub != "x/web" {
		t.Errorf("member looking at u-box: can manage %v, %+v", can, got)
	}

	res := linkResultOf(t, send(f.c, "POST", "/api/runners/"+c+"/links", body), http.StatusOK)
	if res.Created || res.Cloned || res.CloneError != "" || !slices.Equal(res.Project.Runners, []string{c}) || res.Project.AutoPush {
		t.Errorf("member linking their runner: %+v", res)
	}
	if can, got := runnerLinksOf(t, f.c, c); !can || !got["shared"].Linked {
		t.Errorf("c-box's links: %v %+v", can, got)
	}
	// The project's settings see the same link.
	var p model.Project
	if w := send(f.u, "GET", "/api/projects/"+f.web.ID, ""); json.Unmarshal(w.Body.Bytes(), &p) != nil || !slices.Equal(p.Runners, []string{c}) {
		t.Errorf("the project's runners: %s", w.Body)
	}
	// Linking again changes nothing; an admin links someone else's runner.
	linkResultOf(t, send(f.c, "POST", "/api/runners/"+c+"/links", body), http.StatusOK)
	res = linkResultOf(t, send(f.d, "POST", "/api/runners/"+u+"/links", body), http.StatusOK)
	if !slices.Equal(res.Project.Runners, []string{c, u}) {
		t.Errorf("admin linking u-box: %v", res.Project.Runners)
	}
	if w := send(f.c, "POST", "/api/runners/"+c+"/links", `{"project_id":"nope"}`); w.Code != http.StatusNotFound {
		t.Errorf("no such project: %d %s", w.Code, w.Body)
	}
	if w := send(f.c, "POST", "/api/runners/"+c+"/links", `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("nothing to link: %d %s", w.Code, w.Body)
	}
}

// Ticking a GitHub repository that is no project yet makes the shared
// project, linked to the runner, in one step; one that is a project already
// is linked instead. Only the runner's owner adds repositories (theirs, from
// its gh); an admin only links existing projects.
func TestRunnerScreenCreatesAndLinks(t *testing.T) {
	parallel(t)

	f := newFleetServer(t)
	c := f.ids["c-box"]
	before := len(raw(f.s).ForOrg(f.web.OrgID).ListProjects())
	res := linkResultOf(t, send(f.c, "POST", "/api/runners/"+c+"/links", `{"github":"x/new"}`), http.StatusCreated)
	if !res.Created || res.Project.Name != "new" || res.Project.RemoteURL != "https://github.com/x/new.git" ||
		!slices.Equal(res.Project.Runners, []string{c}) || res.Project.OrgID != f.web.OrgID {
		t.Fatalf("creating and linking: %+v", res)
	}
	if _, got := runnerLinksOf(t, f.u, c); !got["new"].Linked {
		t.Errorf("the new project isn't linked: %+v", got)
	}
	// The same repository, in another form, is that project.
	res = linkResultOf(t, send(f.c, "POST", "/api/runners/"+c+"/links", `{"github":"x/web.git"}`), http.StatusOK)
	if res.Created || res.Project.ID != f.web.ID || !slices.Equal(res.Project.Runners, []string{c}) {
		t.Errorf("linking an existing project by its repository: %+v", res)
	}
	if n := len(raw(f.s).ForOrg(f.web.OrgID).ListProjects()); n != before+1 {
		t.Errorf("%d projects, want %d", n, before+1)
	}
	// The admin can't add new repositories to c's runner, only link projects.
	if w := send(f.d, "POST", "/api/runners/"+c+"/links", `{"github":"x/other"}`); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "owner") {
		t.Errorf("admin adding a repository to c-box: %d %s", w.Code, w.Body)
	}
	if w := send(f.d, "POST", "/api/runners/"+f.ids["u-box"]+"/links", `{"github":"x/new"}`); w.Code != http.StatusOK {
		t.Errorf("admin linking u-box to an existing one: %d %s", w.Code, w.Body)
	}
	// Nor list them: the hub has no GitHub login, the owner's runner does.
	if w := send(f.d, "GET", "/api/runners/"+c+"/github-repos", ""); w.Code != http.StatusForbidden {
		t.Errorf("admin listing c's GitHub repositories: %d %s", w.Code, w.Body)
	}
}

// Unticking unlinks the runner only: the project, its tasks and the other
// runners' links stay. It waits while a task of the project is running on
// the runner, from either screen; tasks done there don't hold it.
func TestRunnerScreenUnlinks(t *testing.T) {
	parallel(t)

	f := newFleetServer(t)
	c, u := f.ids["c-box"], f.ids["u-box"]
	if _, err := raw(f.s).UpdateProject(f.web.ID, func(p *model.Project) error { p.Runners = []string{c, u}; return nil }); err != nil {
		t.Fatal(err)
	}
	td, _ := raw(f.s).CreateTodo(model.Todo{ProjectID: f.web.ID, Title: "work", Prompt: "x"})
	set := func(sts ...model.TodoStatus) {
		t.Helper()
		for _, st := range sts {
			if _, err := raw(f.s).UpdateTodo(td.ID, func(x *model.Todo) error { x.Status, x.Runner = st, c; return nil }); err != nil {
				t.Fatal(err)
			}
		}
	}
	set(model.StatusQueued, model.StatusInProgress)
	if _, got := runnerLinksOf(t, f.c, c); got["shared"].Running != 1 {
		t.Errorf("running %+v", got["shared"])
	}
	path := "/api/runners/" + c + "/links/" + f.web.ID
	if w := send(f.c, "DELETE", path, ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "1 task of shared is running on c-box") {
		t.Errorf("unlinking with a task running there: %d %s", w.Code, w.Body)
	}
	if w := send(f.d, "PATCH", "/api/projects/"+f.web.ID, `{"runners":["`+u+`"]}`); w.Code != http.StatusConflict {
		t.Errorf("unlinking in the settings with a task running there: %d %s", w.Code, w.Body)
	}
	if w := send(f.c, "DELETE", "/api/runners/"+u+"/links/"+f.web.ID, ""); w.Code != http.StatusForbidden {
		t.Errorf("member unlinking someone else's runner: %d %s", w.Code, w.Body)
	}

	set(model.StatusReview)
	if _, got := runnerLinksOf(t, f.c, c); got["shared"].Running != 0 || got["shared"].Worktrees != 1 {
		t.Errorf("in review %+v", got["shared"])
	}
	w := send(f.c, "DELETE", path, "")
	var p model.Project
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &p) != nil || !slices.Equal(p.Runners, []string{u}) {
		t.Fatalf("unlinking: %d %s", w.Code, w.Body)
	}
	if got, err := raw(f.s).GetTodo(td.ID); err != nil || got.Status != model.StatusReview {
		t.Errorf("the task after unlinking: %+v %v", got, err)
	}
	if w := send(f.c, "DELETE", path, ""); w.Code != http.StatusOK {
		t.Errorf("unlinking again: %d %s", w.Code, w.Body)
	}
}

// "Build all the org's projects" links a runner to every shared project
// it isn't linked to yet, GitHub's or not; a project without a remote
// can't be shared and is left out.
func TestRunnerScreenBuildsAll(t *testing.T) {
	parallel(t)

	f := newFleetServer(t)
	org := f.web.OrgID
	other, _ := raw(f.s).CreateProject(model.Project{Name: "infra", OrgID: org, DefaultBranch: "main", RemoteURL: "https://git.example.com/ops/infra.git"})
	if _, err := raw(f.s).CreateProject(model.Project{Name: "legacy", Path: t.TempDir(), OrgID: org}); err != nil {
		t.Fatal(err)
	}
	c := f.ids["c-box"]
	if w := send(f.c, "POST", "/api/runners/"+f.ids["u-box"]+"/links/all", ""); w.Code != http.StatusForbidden {
		t.Errorf("member on someone else's runner: %d %s", w.Code, w.Body)
	}
	linkResultOf(t, send(f.c, "POST", "/api/runners/"+c+"/links", `{"project_id":"`+other.ID+`"}`), http.StatusOK)
	w := send(f.c, "POST", "/api/runners/"+c+"/links/all", "")
	var out struct {
		Linked []string `json:"linked"`
		Failed []any    `json:"failed"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || !slices.Equal(out.Linked, []string{"shared"}) || len(out.Failed) != 0 {
		t.Fatalf("build all: %d %s", w.Code, w.Body)
	}
	_, got := runnerLinksOf(t, f.c, c)
	if !got["shared"].Linked || !got["infra"].Linked || len(got) != 2 {
		t.Errorf("after building all: %+v", got)
	}
}

// Linked from the runner's screen, an online runner clones the project at
// once, and a remote it can't read says why, in the same words as the
// project's work would ("runner … cannot access … Permission denied").
func TestRunnerScreenReportsCloneErrors(t *testing.T) {
	serial(t) // t.Setenv
	ssh := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\necho 'git@github.com: Permission denied (publickey).' >&2\nexit 255\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", ssh)

	hub, _, _ := boardServer(t)
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	hub.Dispatch = &dispatch.Dispatcher{Store: raw(hub), Fleet: rh}
	srv := httptest.NewServer(hub.Routes())
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner := testOwner(t, hub)
	code, _, err := rh.NewCode(owner)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := tunnel.Register(ctx, srv.URL, code, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	runner, runnerAPI, _ := runnerSide(t, creds.OrgID)
	runner.Peers = &peer.Hub{Self: "laptop"}
	client := &tunnel.Client{Creds: creds, Handler: runnerAPI}
	go func() { _ = client.Run(ctx) }()
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })

	p, err := raw(hub).CreateProject(model.Project{Name: "dash-infra", OrgID: owner.Org, RemoteURL: "https://github.com/klahr/dash-infra.git", DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	hub.mux().ServeHTTP(w, asOwner(t, hub, httptest.NewRequest("POST", "/api/runners/"+creds.ID+"/links", strings.NewReader(`{"project_id":"`+p.ID+`"}`))))
	res := linkResultOf(t, w, http.StatusOK)
	if want := "runner laptop cannot access klahr/dash-infra over ssh: git@github.com: Permission denied (publickey)"; res.Cloned || !strings.Contains(res.CloneError, want) {
		t.Errorf("clone error %q, want %q", res.CloneError, want)
	}
	// The link stands, as from the settings: the project's work says why it waits.
	if got, _ := raw(hub).GetProject(p.ID); !slices.Equal(got.Runners, []string{creds.ID}) {
		t.Errorf("runners %v", got.Runners)
	}
}
