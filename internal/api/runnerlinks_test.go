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
	"github.com/klahr/dash/internal/testbin"
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

// patchLabels gives runner id labels (a JSON list) as h, and returns the
// answer.
func patchLabels(h http.Handler, id, labels string) *httptest.ResponseRecorder {
	return send(h, "PATCH", "/api/runners/"+id, `{"labels":`+labels+`}`)
}

// A runner's Projects screen shows the org's shared projects it works on:
// those that run on labels it has. Labelling it, its owner or an admin
// changes which; others may look, not label it; another org's runner
// doesn't exist.
func TestRunnerScreenShowsProjectsByLabel(t *testing.T) {
	parallel(t)

	f := newFleetServer(t)
	c, u := f.ids["c-box"], f.ids["u-box"]
	if _, err := raw(f.s).UpdateProject(f.web.ID, func(p *model.Project) error { p.RunsOn = []string{"gpu"}; return nil }); err != nil {
		t.Fatal(err)
	}
	if can, got := runnerLinksOf(t, f.c, u); can || got["shared"].Linked || got["shared"].GitHub != "x/web" ||
		!slices.Equal(got["shared"].RunsOn, []string{"gpu"}) || !slices.Equal(got["shared"].Missing, []string{"gpu"}) {
		t.Errorf("member looking at u-box: can manage %v, %+v", can, got)
	}
	if w := patchLabels(f.c, u, `["gpu"]`); w.Code != http.StatusForbidden {
		t.Errorf("member labelling someone else's runner: %d %s", w.Code, w.Body)
	}
	if w := send(f.b, "GET", "/api/runners/"+u+"/links", ""); w.Code != http.StatusNotFound {
		t.Errorf("another org looking: %d %s", w.Code, w.Body)
	}
	if w := patchLabels(f.b, u, `["gpu"]`); w.Code != http.StatusNotFound {
		t.Errorf("another org labelling: %d %s", w.Code, w.Body)
	}

	if w := patchLabels(f.c, c, `["GPU"]`); w.Code != http.StatusOK {
		t.Fatalf("member labelling their runner: %d %s", w.Code, w.Body)
	}
	if can, got := runnerLinksOf(t, f.c, c); !can || !got["shared"].Linked || len(got["shared"].Missing) != 0 {
		t.Errorf("c-box's projects: %v %+v", can, got)
	}
	// The project's labels didn't change: no project names its runners.
	var p model.Project
	if w := send(f.u, "GET", "/api/projects/"+f.web.ID, ""); json.Unmarshal(w.Body.Bytes(), &p) != nil || !slices.Equal(p.RunsOn, []string{"gpu"}) {
		t.Errorf("the project: %s", w.Body)
	}
	// An admin labels someone else's runner.
	if w := patchLabels(f.d, u, `["gpu"]`); w.Code != http.StatusOK {
		t.Errorf("admin labelling u-box: %d %s", w.Code, w.Body)
	}
	if _, got := runnerLinksOf(t, f.u, u); !got["shared"].Linked {
		t.Errorf("u-box's projects: %+v", got)
	}
}

// Picking a GitHub repository that is no project yet makes the shared
// project in one step, on the runner's own labels (or "any" when it
// has none); one that is a project already is that project. Only the
// runner's owner adds repositories (theirs, from its gh), and a member only
// on labels no one else's runner has.
func TestRunnerScreenCreatesProjects(t *testing.T) {
	parallel(t)

	f := newFleetServer(t)
	c := f.ids["c-box"]
	before := len(raw(f.s).ForOrg(f.web.OrgID).ListProjects())
	// With no labels of its own, c-box's project would run on u's runner too.
	if w := send(f.c, "POST", "/api/runners/"+c+"/links", `{"github":"x/new"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "only they") {
		t.Fatalf("member making a project that runs on every runner: %d %s", w.Code, w.Body)
	}
	if w := patchLabels(f.c, c, `["c-only"]`); w.Code != http.StatusOK {
		t.Fatal(w.Body)
	}
	res := linkResultOf(t, send(f.c, "POST", "/api/runners/"+c+"/links", `{"github":"x/new"}`), http.StatusCreated)
	if !res.Created || res.Project.Name != "new" || res.Project.RemoteURL != "https://github.com/x/new.git" ||
		!slices.Equal(res.Project.RunsOn, []string{"c-only"}) || res.Project.OrgID != f.web.OrgID || res.Project.AutoPush {
		t.Fatalf("creating: %+v", res)
	}
	made := res.Project
	if _, got := runnerLinksOf(t, f.u, c); !got["new"].Linked {
		t.Errorf("c-box doesn't work on the new project: %+v", got)
	}
	// The same repository, in another form, is that project.
	res = linkResultOf(t, send(f.c, "POST", "/api/runners/"+c+"/links", `{"github":"x/web.git"}`), http.StatusOK)
	if res.Created || res.Project.ID != f.web.ID {
		t.Errorf("an existing project by its repository: %+v", res)
	}
	if n := len(raw(f.s).ForOrg(f.web.OrgID).ListProjects()); n != before+1 {
		t.Errorf("%d projects, want %d", n, before+1)
	}
	if w := send(f.c, "POST", "/api/runners/"+c+"/links", `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("nothing to add: %d %s", w.Code, w.Body)
	}
	// An existing project is cloned only on a runner that works on it.
	if _, err := raw(f.s).UpdateProject(f.web.ID, func(p *model.Project) error { p.RunsOn = []string{"gpu"}; return nil }); err != nil {
		t.Fatal(err)
	}
	if w := send(f.c, "POST", "/api/runners/"+c+"/links", `{"project_id":"`+f.web.ID+`"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "lacks the labels gpu") {
		t.Errorf("cloning a project on a runner without its labels: %d %s", w.Code, w.Body)
	}
	linkResultOf(t, send(f.c, "POST", "/api/runners/"+c+"/links", `{"project_id":"`+made.ID+`"}`), http.StatusOK)
	if w := send(f.c, "POST", "/api/runners/"+c+"/links", `{"project_id":"nope"}`); w.Code != http.StatusNotFound {
		t.Errorf("no such project: %d %s", w.Code, w.Body)
	}
	// The admin can't add new repositories to c's runner.
	if w := send(f.d, "POST", "/api/runners/"+c+"/links", `{"github":"x/other"}`); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "owner") {
		t.Errorf("admin adding a repository to c-box: %d %s", w.Code, w.Body)
	}
	// Nor list them: the hub has no GitHub login, the owner's runner does.
	if w := send(f.d, "GET", "/api/runners/"+c+"/github-repos", ""); w.Code != http.StatusForbidden {
		t.Errorf("admin listing c's GitHub repositories: %d %s", w.Code, w.Body)
	}
}

// A runner whose labels, or a project whose labels, change so that the
// runner no longer works on the project leaves it: the project, its tasks
// and its other runners stay. The change waits while a task of the project
// is running on the runner, from either side; tasks done there don't hold
// it.
func TestRelabellingWaitsForRunningTasks(t *testing.T) {
	parallel(t)

	f := newFleetServer(t)
	c, u := f.ids["c-box"], f.ids["u-box"]
	for _, id := range []string{c, u} {
		if w := patchLabels(f.d, id, `["web"]`); w.Code != http.StatusOK {
			t.Fatal(w.Body)
		}
	}
	if _, err := raw(f.s).UpdateProject(f.web.ID, func(p *model.Project) error { p.RunsOn = []string{"web"}; return nil }); err != nil {
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
	if w := patchLabels(f.c, c, `[]`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "1 task of shared is running on c-box") {
		t.Errorf("unlabelling with a task running there: %d %s", w.Code, w.Body)
	}
	if w := send(f.d, "PATCH", "/api/projects/"+f.web.ID, `{"runs_on":["web","gpu"]}`); w.Code != http.StatusConflict {
		t.Errorf("relabelling the project with a task running there: %d %s", w.Code, w.Body)
	}
	// Labels that keep it on the project are fine.
	if w := patchLabels(f.c, c, `["web","extra"]`); w.Code != http.StatusOK {
		t.Errorf("adding a label: %d %s", w.Code, w.Body)
	}

	set(model.StatusReview)
	if _, got := runnerLinksOf(t, f.c, c); got["shared"].Running != 0 || got["shared"].Worktrees != 1 {
		t.Errorf("in review %+v", got["shared"])
	}
	if w := patchLabels(f.c, c, `[]`); w.Code != http.StatusOK {
		t.Fatalf("unlabelling: %d %s", w.Code, w.Body)
	}
	if _, got := runnerLinksOf(t, f.c, c); got["shared"].Linked {
		t.Errorf("c-box still works on the project: %+v", got["shared"])
	}
	if _, got := runnerLinksOf(t, f.c, u); !got["shared"].Linked {
		t.Errorf("u-box left the project too: %+v", got["shared"])
	}
	if got, err := raw(f.s).GetTodo(td.ID); err != nil || got.Status != model.StatusReview {
		t.Errorf("the task after unlabelling: %+v %v", got, err)
	}
}

// Added from the runner's screen, an online runner clones the project at
// once, and a remote it can't read says why, in the same words as the
// project's work would ("runner … cannot access … Permission denied").
func TestRunnerScreenReportsCloneErrors(t *testing.T) {
	testbin.SkipOnWindows(t, "runs a #!/bin/sh stand-in for ssh")
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

	w := httptest.NewRecorder()
	hub.mux().ServeHTTP(w, asOwner(t, hub, httptest.NewRequest("POST", "/api/runners/"+creds.ID+"/links", strings.NewReader(`{"github":"klahr/dash-infra"}`))))
	res := linkResultOf(t, w, http.StatusCreated)
	if want := "runner laptop cannot access klahr/dash-infra over ssh: git@github.com: Permission denied (publickey)"; res.Cloned || !strings.Contains(res.CloneError, want) {
		t.Errorf("clone error %q, want %q", res.CloneError, want)
	}
	// The project stands: its work says why it waits.
	if got, _ := raw(hub).GetProject(res.Project.ID); !slices.Equal(got.RunsOn, []string{"any"}) {
		t.Errorf("runs on %v", got.RunsOn)
	}
}
