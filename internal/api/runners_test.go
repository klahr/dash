package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// fleetServer is a hub whose Default org has u (owner, platform admin), c
// (a member) and d (an admin), and whose Beta org has b. Each of u, c and
// b has registered a runner ("u-box", "c-box", "b-box").
type fleetServer struct {
	s          *Server
	h          http.Handler
	u, c, d, b http.Handler // h signed in as each
	rh         *tunnel.Hub
	ids        map[string]string // runner name → id
	web        model.Project     // Default's, a git repo with an origin
	beta       store.Org
}

func newFleetServer(t *testing.T) *fleetServer {
	t.Helper()
	s, _, _ := boardServer(t)
	acc := s.Auth.Accounts()
	s.Git = gitops.New(s.Hub)
	rh := &tunnel.Hub{Accounts: acc}
	s.Peers = &peer.Hub{Self: "hub", Runners: rh}
	s.Dispatch = &dispatch.Dispatcher{Store: raw(s), Fleet: rh}
	owner := testOwner(t, s)
	cID := member(t, s, owner.Org, "c", store.RoleMember)
	member(t, s, owner.Org, "d", store.RoleAdmin)
	beta, err := acc.CreateOrg(owner.User, "Beta")
	if err != nil {
		t.Fatal(err)
	}
	bID := member(t, s, beta.ID, "b", store.RoleMember)

	f := &fleetServer{s: s, rh: rh, ids: map[string]string{}, beta: beta}
	for name, o := range map[string]model.Owner{"u-box": owner, "c-box": {User: cID, Org: owner.Org}, "b-box": {User: bID, Org: beta.ID}} {
		code, _, err := rh.NewCode(o)
		if err != nil {
			t.Fatal(err)
		}
		id, _, err := rh.Register(code, name)
		if err != nil {
			t.Fatal(err)
		}
		f.ids[name] = id
	}

	repo := filepath.Join(t.TempDir(), "web")
	if err := s.Git.InitRepo(context.Background(), "web", repo, "main"); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", "https://github.com/x/web.git").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if f.web, err = raw(s).CreateProject(model.Project{Name: "shared", Path: repo, DefaultBranch: "main", OrgID: owner.Org}); err != nil {
		t.Fatal(err)
	}

	f.h = s.Routes()
	f.u = loginAs(t, f.h, "u", "p")
	f.c = loginAs(t, f.h, "c", "long-enough-c")
	f.d = loginAs(t, f.h, "d", "long-enough-d")
	f.b = loginAs(t, f.h, "b", "long-enough-b")
	return f
}

func send(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, r)
	return w
}

func runnerNames(t *testing.T, w *httptest.ResponseRecorder) map[string]runnerView {
	t.Helper()
	var out struct{ Runners []runnerView }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("list runners: %d %s", w.Code, w.Body)
	}
	m := map[string]runnerView{}
	for _, r := range out.Runners {
		m[r.Name] = r
	}
	return m
}

// An org sees, configures and revokes only its own runners: another org's
// don't exist for it.
func TestRunnersAreScopedToTheirOrg(t *testing.T) {
	f := newFleetServer(t)
	if got := runnerNames(t, send(f.u, "GET", "/api/runners", "")); len(got) != 2 || got["b-box"].ID != "" || !got["u-box"].Mine || !got["c-box"].CanManage {
		t.Errorf("Default's runners: %+v", got)
	}
	if got := runnerNames(t, send(f.b, "GET", "/api/runners", "")); len(got) != 1 || got["b-box"].ID == "" {
		t.Errorf("Beta's runners: %+v", got)
	}
	for _, c := range []struct{ method, body string }{{"PATCH", `{"paused":true}`}, {"DELETE", ""}} {
		if w := send(f.b, c.method, "/api/runners/"+f.ids["u-box"], c.body); w.Code != http.StatusNotFound {
			t.Errorf("%s another org's runner: %d %s", c.method, w.Code, w.Body)
		}
		if w := send(f.u, c.method, "/api/runners/"+f.ids["b-box"], c.body); w.Code != http.StatusNotFound {
			t.Errorf("%s another org's runner as the platform admin: %d %s", c.method, w.Code, w.Body)
		}
	}
	if _, ok := f.rh.Get(f.ids["u-box"]); !ok {
		t.Fatal("u-box was revoked from another org")
	}
	// Linking another org's runner to a project is refused too.
	if w := send(f.u, "PATCH", "/api/projects/"+f.web.ID, `{"runners":["`+f.ids["b-box"]+`"]}`); w.Code != http.StatusBadRequest {
		t.Errorf("linking Beta's runner: %d %s", w.Code, w.Body)
	}
}

// Any member makes a code (the runner will be theirs, in their org); only a
// runner's owner and the org's admins pause, limit or revoke it.
func TestRunnerOwnersAndAdminsManageRunners(t *testing.T) {
	f := newFleetServer(t)
	w := send(f.c, "POST", "/api/runners/codes", "")
	var code struct{ Code string }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &code) != nil {
		t.Fatalf("member's code: %d %s", w.Code, w.Body)
	}
	id, _, err := f.rh.Register(code.Code, "c-laptop")
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := f.rh.Get(id); r.Owner == nil || r.Owner.Username != "c" || r.OrgID() != testOwner(t, f.s).Org {
		t.Errorf("c's new runner: %+v", r.Owner)
	}

	if w := send(f.c, "PATCH", "/api/runners/"+f.ids["u-box"], `{"paused":true}`); w.Code != http.StatusForbidden {
		t.Errorf("member pausing someone else's runner: %d", w.Code)
	}
	if w := send(f.c, "DELETE", "/api/runners/"+f.ids["u-box"], ""); w.Code != http.StatusForbidden {
		t.Errorf("member revoking someone else's runner: %d", w.Code)
	}
	w = send(f.c, "PATCH", "/api/runners/"+f.ids["c-box"], `{"parallel":4,"paused":true}`)
	if r, _ := f.rh.Get(f.ids["c-box"]); w.Code != http.StatusOK || r.Parallel != 4 || !r.Paused {
		t.Errorf("owner configuring their runner: %d %s %+v", w.Code, w.Body, r)
	}
	if w := send(f.c, "PATCH", "/api/runners/"+f.ids["c-box"], `{"parallel":-1}`); w.Code != http.StatusBadRequest {
		t.Errorf("negative parallel: %d", w.Code)
	}
	// An org admin revokes a member's runner.
	if w := send(f.d, "DELETE", "/api/runners/"+f.ids["c-box"], ""); w.Code != http.StatusNoContent {
		t.Errorf("admin revoking a member's runner: %d %s", w.Code, w.Body)
	}
	if _, ok := f.rh.Get(f.ids["c-box"]); ok {
		t.Error("c-box is still registered")
	}
}

// Linking: in the org, your own runner (or any as an admin), only with a
// git remote. Work still merges locally (on the runners, which push), and
// pull requests are only ever the user's choice.
func TestLinkingProjectsToRunners(t *testing.T) {
	f := newFleetServer(t)
	path := "/api/projects/" + f.web.ID
	if w := send(f.c, "PATCH", path, `{"runners":["`+f.ids["u-box"]+`"]}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "only they") {
		t.Errorf("member linking someone else's runner: %d %s", w.Code, w.Body)
	}
	w := send(f.c, "PATCH", path, `{"runners":["`+f.ids["c-box"]+`"]}`)
	var p model.Project
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &p) != nil || len(p.Runners) != 1 || p.UsesPR() || p.AutoPush ||
		!p.LandsOnOrigin() || p.MergeModeSet {
		t.Fatalf("member linking their runner: %d %s", w.Code, w.Body)
	}
	// The admin adds u's; c may keep the list as it is (with u-box in it).
	if w := send(f.d, "PATCH", path, `{"runners":["`+f.ids["c-box"]+`","`+f.ids["u-box"]+`"]}`); w.Code != http.StatusOK {
		t.Errorf("admin linking u's runner: %d %s", w.Code, w.Body)
	}
	if w := send(f.c, "PATCH", path, `{"name":"renamed","runners":["`+f.ids["c-box"]+`","`+f.ids["u-box"]+`"]}`); w.Code != http.StatusOK {
		t.Errorf("member saving settings with runners linked: %d %s", w.Code, w.Body)
	}
	// Pull requests are an explicit choice, kept; and merging can come back.
	w = send(f.c, "PATCH", path, `{"merge_mode":"pr"}`)
	if json.Unmarshal(w.Body.Bytes(), &p) != nil || w.Code != http.StatusOK || !p.UsesPR() || !p.MergeModeSet {
		t.Errorf("choosing pull requests with runners linked: %d %s", w.Code, w.Body)
	}
	if names, _ := raw(f.s).LandRunnerProjectsLocally(); len(names) != 0 {
		t.Errorf("the upgrade switched a project whose user chose pull requests: %v", names)
	}
	w = send(f.c, "PATCH", path, `{"merge_mode":"local"}`)
	if json.Unmarshal(w.Body.Bytes(), &p) != nil || w.Code != http.StatusOK || p.UsesPR() {
		t.Errorf("merging locally again with runners linked: %d %s", w.Code, w.Body)
	}
	// Attachments can't follow a task to a runner.
	if _, err := (&service{s: f.s, c: ownerCaller(t, f.s), data: raw(f.s).ForOrg(f.web.OrgID)}).addTodo(model.Todo{
		ProjectID: f.web.ID, Prompt: "x", Attachments: []model.Attachment{{ID: "a"}}}, false); err == nil {
		t.Error("a task with attachments was added to a project on runners")
	}

	// A project without a remote can't be shared.
	other, _ := raw(f.s).CreateProject(model.Project{Name: "no-remote", Path: t.TempDir(), OrgID: f.web.OrgID})
	if w := send(f.u, "PATCH", "/api/projects/"+other.ID, `{"runners":["`+f.ids["u-box"]+`"]}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "remote") {
		t.Errorf("linking a project without a remote: %d %s", w.Code, w.Body)
	}

	// Unlinking all of them is anyone's who can edit the project.
	if w := send(f.c, "PATCH", path, `{"runners":[]}`); w.Code != http.StatusOK {
		t.Errorf("unlinking: %d %s", w.Code, w.Body)
	}
}

// runnerSide is a runner's Dash as its hub reaches it through the tunnel,
// with a git repository to stand in for the shared remote.
func runnerSide(t *testing.T, hubOrg int64) (s *Server, h http.Handler, remote string) {
	t.Helper()
	s, _, _ = boardServer(t)
	s.Git = gitops.New(s.Hub)
	s.ProjectRoot = t.TempDir()
	s.HubOrg = hubOrg
	remote = filepath.Join(t.TempDir(), "remote")
	if err := s.Git.InitRepo(context.Background(), "remote", remote, "main"); err != nil {
		t.Fatal(err)
	}
	gitops.AllowLocalRemotes = true
	t.Cleanup(func() { gitops.AllowLocalRemotes = false })
	return s, s.RunnerRoutes(), remote
}

// A runner takes work only for projects its hub linked to it, of the org it
// was registered for, cloned inside its own projects root; it refuses
// anything else, and nobody but the hub reaches these routes.
func TestRunnerOnlyTakesWorkForLinkedProjects(t *testing.T) {
	s, h, remote := runnerSide(t, 7)
	task := func(project, todo string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(dispatch.Task{HubTodoID: todo, HubProjectID: project, Title: "t", Prompt: "do it"})
		return send(h, "POST", "/api/hub/tasks", string(b))
	}
	if w := task("hub-p1", "hub-t1"); w.Code != http.StatusForbidden {
		t.Fatalf("task for a project never linked: %d %s", w.Code, w.Body)
	}
	link := func(org int64, url string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(dispatch.Link{Name: "Shared App", RemoteURL: url, DefaultBranch: "main", OrgID: org})
		return send(h, "PUT", "/api/hub/projects/hub-p1", string(b))
	}
	if w := link(8, remote); w.Code != http.StatusForbidden {
		t.Errorf("link from another org: %d %s", w.Code, w.Body)
	}
	gitops.AllowLocalRemotes = false
	if w := link(7, remote); w.Code != http.StatusBadRequest {
		t.Errorf("link to a local path: %d %s", w.Code, w.Body)
	}
	gitops.AllowLocalRemotes = true

	w := link(7, remote)
	var linked dispatch.Linked
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &linked) != nil {
		t.Fatalf("link: %d %s", w.Code, w.Body)
	}
	p, err := raw(s).GetProject(linked.ProjectID)
	if err != nil || !within(filepath.Join(s.ProjectRoot, "hub"), p.Path) || p.MergeMode != model.MergeLocal ||
		!p.LandsOnOrigin() || p.HubProjectID != "hub-p1" {
		t.Fatalf("linked project %+v %v", p, err)
	}
	if w := link(7, remote); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), p.ID) {
		t.Errorf("linking again: %d %s", w.Code, w.Body)
	}
	// The clone lands tasks the way the hub's project says.
	for _, mode := range []string{model.MergePR, ""} {
		body, _ := json.Marshal(dispatch.Link{Name: "Shared App", RemoteURL: remote, DefaultBranch: "main", OrgID: 7, MergeMode: mode})
		if w := send(h, "PUT", "/api/hub/projects/hub-p1", string(body)); w.Code != http.StatusOK {
			t.Fatalf("linking with merge mode %q: %d %s", mode, w.Code, w.Body)
		}
		if got, _ := raw(s).GetProject(p.ID); got.UsesPR() != (mode == model.MergePR) {
			t.Errorf("hub's merge mode %q, the clone's %q", mode, got.MergeMode)
		}
	}

	w = task("hub-p1", "hub-t1")
	var acc dispatch.Accepted
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &acc) != nil || acc.Resumed {
		t.Fatalf("task: %d %s", w.Code, w.Body)
	}
	td, _ := raw(s).GetTodo(acc.ID)
	if td.ProjectID != p.ID || td.HubTodoID != "hub-t1" || td.Status != model.StatusQueued || td.CreatedBy != 0 {
		t.Errorf("runner's copy %+v", td)
	}
	if w := task("hub-p1", "hub-t1"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"resumed":true`) {
		t.Errorf("same task again: %d %s", w.Code, w.Body)
	}
	if w := send(h, "GET", "/api/hub/tasks/hub-t1/log", ""); w.Code != http.StatusOK {
		t.Errorf("log of the hub's task: %d %s", w.Code, w.Body)
	}
	if w := send(h, "GET", "/api/hub/tasks/"+td.ID+"/log", ""); w.Code != http.StatusNotFound {
		t.Errorf("the runner's own id reaches a task: %d", w.Code)
	}

	// A project of the runner's own isn't the hub's to use, even by name.
	own, _ := raw(s).CreateProject(model.Project{Name: "mine", Path: filepath.Join(s.ProjectRoot, "mine")})
	if w := task(own.ID, "hub-t2"); w.Code != http.StatusForbidden {
		t.Errorf("task for the runner's own project: %d %s", w.Code, w.Body)
	}

	// Unlinked, the project takes no more tasks.
	if w := send(h, "DELETE", "/api/hub/projects/hub-p1", ""); w.Code != http.StatusNoContent {
		t.Fatalf("unlink: %d", w.Code)
	}
	if w := task("hub-p1", "hub-t3"); w.Code != http.StatusForbidden {
		t.Errorf("task after unlinking: %d %s", w.Code, w.Body)
	}

	// The same routes refuse a signed-in person: only the hub sends work.
	r := httptest.NewRequest("POST", "/api/hub/tasks", strings.NewReader(`{}`))
	r = asOwner(t, s, r)
	rec := httptest.NewRecorder()
	s.mux().ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("hub task route for a user: %d %s", rec.Code, rec.Body)
	}

}
