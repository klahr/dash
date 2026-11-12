package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/testbin"
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
	tokens     map[string]string // runner name → its token
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

	f := &fleetServer{s: s, rh: rh, ids: map[string]string{}, tokens: map[string]string{}, beta: beta}
	for name, o := range map[string]model.Owner{"u-box": owner, "c-box": {User: cID, Org: owner.Org}, "b-box": {User: bID, Org: beta.ID}} {
		code, _, err := rh.NewCode(o)
		if err != nil {
			t.Fatal(err)
		}
		id, tok := registerRunner(t, rh, code, name)
		f.ids[name], f.tokens[name] = id, tok
	}

	repo := filepath.Join(t.TempDir(), "web")
	if err := s.Git.InitRepo(context.Background(), "web", repo, "main"); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", "https://github.com/x/web.git").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if f.web, err = raw(s).CreateProject(model.Project{Name: "shared", Path: repo, DefaultBranch: "main", OrgID: owner.Org,
		RemoteURL: "https://github.com/x/web.git"}); err != nil {
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
	parallel(t)

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
	parallel(t)

	f := newFleetServer(t)
	w := send(f.c, "POST", "/api/runners/codes", "")
	var code struct{ Code string }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &code) != nil {
		t.Fatalf("member's code: %d %s", w.Code, w.Body)
	}
	id, _ := registerRunner(t, f.rh, code.Code, "c-laptop")
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

// Only a runner's owner and the org's admins update it, turn its
// auto-update on and cancel an update waiting for its tasks; other
// members can't, and other orgs don't see it. A hub built from source has
// no release to update to.
func TestRunnerUpdatesAreForOwnersAndAdmins(t *testing.T) {
	parallel(t)

	f := newFleetServer(t)
	update := func(h http.Handler, method, name string) *httptest.ResponseRecorder {
		return send(h, method, "/api/runners/"+f.ids[name]+"/update", "")
	}
	updating := func(name string) bool { r, _ := f.rh.Get(f.ids[name]); return r.Update != nil }

	if w := update(f.u, "POST", "u-box"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "from source") {
		t.Errorf("update from a hub built from source: %d %s", w.Code, w.Body)
	}
	if got := runnerNames(t, send(f.u, "GET", "/api/runners", "")); got["u-box"].AutoUpdate {
		t.Error("auto-update on by default")
	}
	if w := send(f.u, "GET", "/api/runners", ""); !strings.Contains(w.Body.String(), `"can_update":false`) {
		t.Errorf("list from a hub built from source: %s", w.Body)
	}
	f.rh.Release = "1.3.0"

	// A member can't update or cancel someone else's runner; another org can't see it.
	for _, method := range []string{"POST", "DELETE"} {
		if w := update(f.c, method, "u-box"); w.Code != http.StatusForbidden {
			t.Errorf("member %s on someone else's runner: %d %s", method, w.Code, w.Body)
		}
		if w := update(f.b, method, "u-box"); w.Code != http.StatusNotFound {
			t.Errorf("%s on another org's runner: %d %s", method, w.Code, w.Body)
		}
	}
	if w := send(f.c, "PATCH", "/api/runners/"+f.ids["u-box"], `{"auto_update":true}`); w.Code != http.StatusForbidden {
		t.Errorf("member turning on someone else's auto-update: %d", w.Code)
	}
	if updating("u-box") {
		t.Fatal("u-box is updating")
	}

	// The owner updates their runner; an admin cancels it while it waits.
	w := update(f.c, "POST", "c-box")
	var v runnerView
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Update == nil || v.Update.Phase != tunnel.UpdateWaiting || v.Update.Target != "1.3.0" {
		t.Fatalf("owner updating their runner: %d %s", w.Code, w.Body)
	}
	if w := update(f.d, "DELETE", "c-box"); w.Code != http.StatusOK || updating("c-box") {
		t.Errorf("admin cancelling the update: %d %s", w.Code, w.Body)
	}
	if w := update(f.d, "DELETE", "c-box"); w.Code != http.StatusConflict {
		t.Errorf("cancelling no update: %d %s", w.Code, w.Body)
	}
	// An admin updates a member's runner, the owner cancels it.
	if w := update(f.d, "POST", "c-box"); w.Code != http.StatusOK || !updating("c-box") {
		t.Errorf("admin updating a member's runner: %d %s", w.Code, w.Body)
	}
	if w := update(f.c, "DELETE", "c-box"); w.Code != http.StatusOK || updating("c-box") {
		t.Errorf("owner cancelling: %d %s", w.Code, w.Body)
	}
	// Auto-update: the owner's setting.
	w = send(f.u, "PATCH", "/api/runners/"+f.ids["u-box"], `{"auto_update":true}`)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil || !v.AutoUpdate {
		t.Errorf("owner turning on auto-update: %d %s", w.Code, w.Body)
	}
}

// The hub's update reaches only a runner whose hub's tasks are done; the
// route answers nobody but the hub.
func TestRunnerUpdatesOnlyWhenIdleAndOnlyForTheHub(t *testing.T) {
	parallel(t)

	s, h, _ := runnerSide(t, 7)
	var asked []string
	s.SelfUpdate = func(_ context.Context, v string) error { asked = append(asked, v); return nil }
	p, err := raw(s).CreateProject(model.Project{Name: "p", Path: t.TempDir(), HubProjectID: "hub-p"})
	if err != nil {
		t.Fatal(err)
	}
	td, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "t", Prompt: "x"})
	setStatus := func(st model.TodoStatus) {
		t.Helper()
		if _, err := raw(s).SetStatus(td.ID, st, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	setStatus(model.StatusQueued)
	setStatus(model.StatusInProgress)
	if w := send(h, "POST", tunnel.UpdatePath, `{"version":"1.3.0"}`); w.Code != http.StatusConflict || len(asked) != 0 {
		t.Fatalf("update while a task runs: %d %s", w.Code, w.Body)
	}
	setStatus(model.StatusReview)
	if w := send(h, "POST", tunnel.UpdatePath, `{"version":"1.3.0"}`); w.Code != http.StatusOK || len(asked) != 1 || asked[0] != "1.3.0" {
		t.Fatalf("update when idle: %d %s %v", w.Code, w.Body, asked)
	}
	s.SelfUpdate = func(context.Context, string) error { return errors.New("checksum mismatch") }
	if w := send(h, "POST", tunnel.UpdatePath, `{"version":"1.3.0"}`); w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "checksum mismatch") {
		t.Errorf("failed update: %d %s", w.Code, w.Body)
	}

	r := asOwner(t, s, httptest.NewRequest("POST", tunnel.UpdatePath, strings.NewReader(`{"version":"1.3.0"}`)))
	rec := httptest.NewRecorder()
	s.mux().ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("update route for a user: %d %s", rec.Code, rec.Body)
	}
}

// Linking: in the org, your own runner (or any as an admin), only with a
// git remote. A GitHub project's work lands through pull requests unless
// its user chose otherwise (the runners open them; the hub needs no gh),
// and choosing to merge sticks.
func TestLinkingProjectsToRunners(t *testing.T) {
	parallel(t)

	f := newFleetServer(t)
	f.s.Git.GhBin = filepath.Join(t.TempDir(), "no-gh") // the hub has no gh
	path := "/api/projects/" + f.web.ID
	if w := send(f.c, "PATCH", path, `{"runners":["`+f.ids["u-box"]+`"]}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "only they") {
		t.Errorf("member linking someone else's runner: %d %s", w.Code, w.Body)
	}
	w := send(f.c, "PATCH", path, `{"runners":["`+f.ids["c-box"]+`"]}`)
	var p model.Project
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &p) != nil || len(p.Runners) != 1 || !p.UsesPR() || p.AutoPush ||
		p.LandsOnOrigin() || p.MergeModeSet {
		t.Fatalf("member linking their runner: %d %s", w.Code, w.Body)
	}
	// The admin adds u's; c may keep the list as it is (with u-box in it).
	if w := send(f.d, "PATCH", path, `{"runners":["`+f.ids["c-box"]+`","`+f.ids["u-box"]+`"]}`); w.Code != http.StatusOK {
		t.Errorf("admin linking u's runner: %d %s", w.Code, w.Body)
	}
	if w := send(f.c, "PATCH", path, `{"name":"renamed","merge_mode":"pr","runners":["`+f.ids["c-box"]+`","`+f.ids["u-box"]+`"]}`); w.Code != http.StatusOK {
		t.Errorf("member saving settings with runners linked: %d %s", w.Code, w.Body)
	}
	// Merging is an explicit choice, kept by upgrades; and pull requests
	// can come back. A setting older dashboards still send is ignored.
	w = send(f.c, "PATCH", path, `{"merge_mode":"local","merge_green_prs":false}`)
	if json.Unmarshal(w.Body.Bytes(), &p) != nil || w.Code != http.StatusOK || p.UsesPR() || !p.MergeModeSet || !p.LandsOnOrigin() {
		t.Errorf("choosing to merge with runners linked: %d %s", w.Code, w.Body)
	}
	if names, _ := raw(f.s).DefaultMergeModes(true); slices.Contains(names, p.Name) {
		t.Errorf("the upgrade switched a project whose user chose to merge: %v", names)
	}
	w = send(f.c, "PATCH", path, `{"merge_mode":"pr"}`)
	if json.Unmarshal(w.Body.Bytes(), &p) != nil || w.Code != http.StatusOK || !p.UsesPR() {
		t.Errorf("pull requests again with runners linked: %d %s", w.Code, w.Body)
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
	return s, s.RunnerRoutes(), remote
}

// A runner takes work only for projects its hub linked to it, of the org it
// was registered for, cloned into the org's folder in its projects root
// (<root>/<org folder>/<repo>, named after the remote); it refuses
// anything else, and nobody but the hub reaches these routes.
func TestRunnerOnlyTakesWorkForLinkedProjects(t *testing.T) {
	serial(t) // turns gitops.AllowLocalRemotes off for a moment
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
	w := link(7, remote)
	gitops.AllowLocalRemotes = true
	if w.Code != http.StatusBadRequest {
		t.Errorf("link to a local path: %d %s", w.Code, w.Body)
	}

	w = link(7, remote)
	var linked dispatch.Linked
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &linked) != nil {
		t.Fatalf("link: %d %s", w.Code, w.Body)
	}
	p, err := raw(s).GetProject(linked.ProjectID)
	if err != nil || p.Path != filepath.Join(s.ProjectRoot, "org-7", "remote") || p.MergeMode != model.MergeLocal ||
		!p.LandsOnOrigin() || p.HubProjectID != "hub-p1" || p.HubCheckout != model.HubCloned || linked.Path != p.Path || linked.Adopted {
		t.Fatalf("linked project %+v %v", p, err)
	}
	if w := link(7, remote); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), p.ID) {
		t.Errorf("linking again: %d %s", w.Code, w.Body)
	}
	// The clone lands tasks the way the hub's project says, except that a
	// runner without gh merges when pull requests are only the default.
	gh := filepath.Join(t.TempDir(), "gh")
	testbin.Write(t, gh, "#!/bin/sh\nexit 1\n")
	for _, c := range []struct {
		mode   string
		chosen bool
		hasGh  bool
		wantPR bool
	}{
		{model.MergePR, false, true, true},
		{model.MergePR, false, false, false},
		{model.MergePR, true, false, true},
		{"", false, true, false},
	} {
		s.Git.GhBin = gh
		if !c.hasGh {
			s.Git.GhBin = filepath.Join(t.TempDir(), "no-gh")
		}
		body, _ := json.Marshal(dispatch.Link{Name: "Shared App", RemoteURL: remote, DefaultBranch: "main", OrgID: 7,
			MergeMode: c.mode, MergeModeSet: c.chosen})
		if w := send(h, "PUT", "/api/hub/projects/hub-p1", string(body)); w.Code != http.StatusOK {
			t.Fatalf("linking with %+v: %d %s", c, w.Code, w.Body)
		}
		if got, _ := raw(s).GetProject(p.ID); got.UsesPR() != c.wantPR {
			t.Errorf("hub's link %+v, the clone's merge mode %q", c, got.MergeMode)
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

// The runner's copy of a hub project follows the hub's review settings:
// set when the project is created from the link, updated by every later
// link, and kept when an older hub doesn't send them.
func TestHubLinkCarriesReviewSettings(t *testing.T) {
	parallel(t)

	s, h, remote := runnerSide(t, 7)
	link := func(auto *bool, fixes *int) model.Project {
		t.Helper()
		b, _ := json.Marshal(dispatch.Link{Name: "Shared App", RemoteURL: remote, DefaultBranch: "main", OrgID: 7,
			AutoApprove: auto, MaxAutoFixes: fixes})
		w := send(h, "PUT", "/api/hub/projects/hub-p1", string(b))
		var linked dispatch.Linked
		if w.Code/100 != 2 || json.Unmarshal(w.Body.Bytes(), &linked) != nil {
			t.Fatalf("link: %d %s", w.Code, w.Body)
		}
		p, err := raw(s).GetProject(linked.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	on, off, five, one := true, false, 5, 1
	if p := link(&on, &five); !p.AutoApprove || p.MaxAutoFixes != 5 {
		t.Errorf("created from the link: auto approve %v, fixes %d", p.AutoApprove, p.MaxAutoFixes)
	}
	if p := link(&off, &one); p.AutoApprove || p.MaxAutoFixes != 1 {
		t.Errorf("updated by the link: auto approve %v, fixes %d", p.AutoApprove, p.MaxAutoFixes)
	}
	if p := link(&on, &one); !p.AutoApprove {
		t.Errorf("turned on again on the hub, the runner's copy still has it off")
	}
	// An older hub sends neither: the runner keeps what it has.
	if p := link(nil, nil); !p.AutoApprove || p.MaxAutoFixes != 1 {
		t.Errorf("link from an older hub: auto approve %v, fixes %d", p.AutoApprove, p.MaxAutoFixes)
	}
}

// registerRunner swaps a registration code for a runner's id and token
// through rh's POST /runner/register handler, as `dash runner --code` does.
func registerRunner(t *testing.T, rh *tunnel.Hub, code, name string) (id, token string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": code, "name": name})
	w := httptest.NewRecorder()
	rh.ServeRegister(w, httptest.NewRequest("POST", "/runner/register", bytes.NewReader(body)))
	var res struct{ ID, Token string }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil {
		t.Fatalf("register runner %q: %d %s", name, w.Code, w.Body)
	}
	return res.ID, res.Token
}
