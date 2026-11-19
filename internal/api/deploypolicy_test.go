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
	"github.com/klahr/dash/internal/store"
)

// joinAs invites name to the default org with role and returns the router
// signed in as them.
func joinAs(t *testing.T, s *Server, h http.Handler, name string, role store.Role) http.Handler {
	t.Helper()
	acc := raw(s).Accounts()
	owner := ownerSession(t, s)
	tok, _, err := acc.InviteWith(owner.User.ID, owner.Org.ID, name+"@example.com", role, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := acc.RegisterWithInvitation(tok, name, name+"-password"); err != nil {
		t.Fatal(err)
	}
	return loginAs(t, h, name, name+"-password")
}

// An environment's policy, on a Dash that deploys on its own computer: who
// may deploy (staging: members, production: admins), production's
// approval (not one's own, unless the org has a single admin), its
// requirements (live on staging first, passing tests: Dash runs them),
// freezes, Promote (the same commit and artifact, no build), who may
// change settings, the settings history, and planners looking on.
func TestDeployPolicy(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	s, _, _ := boardServer(t)
	s.Git = gitops.New(s.Hub)
	s.DeployPoll = 20 * time.Millisecond
	s.DeployRoot, s.EnvDir = t.TempDir(), t.TempDir()
	h := s.Routes()
	owner := loggedIn(t, h)

	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	var commits []string
	for _, f := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(repo, f), []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "add", f)
		git(t, repo, "commit", "-q", "-m", "add "+f)
		commits = append(commits, git(t, repo, "rev-parse", "HEAD"))
	}
	marks := t.TempDir()
	mark := func(name string) string { return "'" + filepath.ToSlash(filepath.Join(marks, name)) + "'" }
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(marks, name))
		return strings.TrimSpace(string(b))
	}
	p, err := raw(s).CreateProject(model.Project{Name: "shop", OrgID: testOwner(t, s).Org, Path: repo, DefaultBranch: "main",
		TestCommand: "echo $DASH_ENV >> " + mark("tested")})
	if err != nil {
		t.Fatal(err)
	}
	staging := model.Environment{Name: "staging", BuildCommand: "echo built >> " + mark("built-staging"),
		DeployCommand: `echo "artifact=art-$DASH_COMMIT" > "$DASH_OUTPUT"`}
	production := model.Environment{Name: "production", BuildCommand: "echo built >> " + mark("built-production"),
		DeployCommand: "echo $DASH_COMMIT $DASH_ARTIFACT > " + mark("production"), RequireTests: true, RequireLiveOn: "staging"}
	setEnvs := func(as http.Handler, envs ...model.Environment) *httptest.ResponseRecorder {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"environments": envs})
		return do(t, as, "PATCH", "/api/projects/"+p.ID, string(b))
	}
	if w := setEnvs(owner, staging, production); w.Code != http.StatusOK {
		t.Fatalf("set the environments: %d %s", w.Code, w.Body)
	}
	got, _ := raw(s).GetProject(p.ID)
	if e, _ := got.Environment("production"); e.DeployRole != "admin" || e.ApproveRole != "admin" || e.SettingsRole != "admin" {
		t.Errorf("production's default roles: %+v", e)
	}
	if e, _ := got.Environment("staging"); e.DeployRole != "member" || e.ApproveRole != "none" || e.SettingsRole != "admin" {
		t.Errorf("staging's default roles: %+v", e)
	}

	created := func(w *httptest.ResponseRecorder) model.Deploy {
		t.Helper()
		if w.Code != http.StatusCreated {
			t.Fatalf("deploy: %d %s", w.Code, w.Body)
		}
		var d model.Deploy
		_ = json.Unmarshal(w.Body.Bytes(), &d)
		return d
	}
	finished := func(id string) model.Deploy {
		t.Helper()
		var d model.Deploy
		eventually(t, "the deploy to finish", func() bool {
			d, _ = raw(s).GetDeploy(id)
			return d.Status.Done()
		})
		return d
	}
	deploy := func(as http.Handler, body string) *httptest.ResponseRecorder {
		return do(t, as, "POST", "/api/projects/"+p.ID+"/deploys", body)
	}

	// Alone, the owner is the org's only admin: they approve their own.
	d0 := created(deploy(owner, `{"environment":"staging","commit":"`+commits[0]+`"}`))
	if d0 = finished(d0.ID); d0.Status != model.DeployLive || d0.Artifact != "art-"+commits[0] {
		t.Fatalf("staging deploy: %+v", d0)
	}
	self := created(deploy(owner, `{"environment":"production","promote_from":"staging"}`))
	if self.Status != model.DeployAwaiting || self.NeedsApproval != "admin" || !self.Promote || self.Ref != commits[0] {
		t.Fatalf("production deploy: %+v", self)
	}
	if w := do(t, owner, "POST", "/api/deploys/"+self.ID+"/approve", ""); w.Code != http.StatusOK {
		t.Fatalf("the only admin approves their own: %d %s", w.Code, w.Body)
	}
	self = finished(self.ID)
	if self.Status != model.DeployLive || !self.TestsPassed || self.Approval == nil || !self.Approval.Approved || self.Approval.ByName != "u" {
		t.Fatalf("approved promotion: %+v", self)
	}
	if read("production") != commits[0]+" art-"+commits[0] || read("built-production") != "" || read("tested") != "production" {
		t.Errorf("promotion ran with %q, built %q, tested %q", read("production"), read("built-production"), read("tested"))
	}

	member := joinAs(t, s, h, "mia", store.RoleMember)
	admin := joinAs(t, s, h, "ada", store.RoleAdmin)
	planner := joinAs(t, s, h, "pat", store.RolePlanner)

	// Who may deploy where.
	d1 := created(deploy(member, `{"environment":"staging","commit":"`+commits[1]+`"}`))
	if d1 = finished(d1.ID); d1.Status != model.DeployLive || d1.ByName != "mia" {
		t.Fatalf("a member's staging deploy: %+v", d1)
	}
	if w := deploy(member, `{"environment":"production","promote_from":"staging"}`); w.Code != http.StatusForbidden {
		t.Errorf("a member deploys to production: %d %s", w.Code, w.Body)
	}
	if w := do(t, member, "POST", "/api/deploys/"+d0.ID+"/rollback", ""); w.Code == http.StatusForbidden {
		t.Errorf("a member may roll staging back: %d %s", w.Code, w.Body)
	}
	if w := deploy(admin, `{"environment":"production"}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "live on staging") {
		t.Errorf("production's tip, not live on staging: %d %s", w.Code, w.Body)
	}

	// Approvals: not one's own (two admins now), not by a member.
	d2 := created(deploy(admin, `{"environment":"production","promote_from":"staging"}`))
	if d2.Status != model.DeployAwaiting {
		t.Fatalf("awaiting: %+v", d2)
	}
	if w := do(t, admin, "POST", "/api/deploys/"+d2.ID+"/approve", ""); w.Code != http.StatusForbidden {
		t.Errorf("an admin approves their own: %d %s", w.Code, w.Body)
	}
	if w := do(t, member, "POST", "/api/deploys/"+d2.ID+"/approve", ""); w.Code != http.StatusForbidden {
		t.Errorf("a member approves: %d %s", w.Code, w.Body)
	}
	var envs []envStatus
	_ = json.Unmarshal(do(t, owner, "GET", "/api/projects/"+p.ID+"/environments", "").Body.Bytes(), &envs)
	if len(envs) != 2 || len(envs[1].Policy.Awaiting) != 1 || !envs[1].Policy.CanApprove || envs[0].Policy.PromoteTo != "production" {
		t.Errorf("environments: %+v", envs)
	}
	if w := do(t, owner, "POST", "/api/deploys/"+d2.ID+"/reject", `{"note":"not today"}`); w.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", w.Code, w.Body)
	}
	if x, _ := raw(s).GetDeploy(d2.ID); x.Status != model.DeployRejected || !strings.Contains(x.Error, "not today") || x.Approval.Approved {
		t.Errorf("rejected: %+v", x)
	}
	if w := do(t, owner, "POST", "/api/deploys/"+d2.ID+"/approve", ""); w.Code != http.StatusConflict {
		t.Errorf("approve a rejected deploy: %d %s", w.Code, w.Body)
	}

	// A freeze blocks deploys and approvals, not rollbacks.
	d3 := created(deploy(admin, `{"environment":"production","promote_from":"staging"}`))
	now := time.Now().UTC()
	production.Freezes = []model.Freeze{{From: now.Add(-time.Hour).Format("2006-01-02 15:04"), To: now.Add(time.Hour).Format("2006-01-02 15:04"), Note: "release party"}}
	if w := setEnvs(owner, staging, production); w.Code != http.StatusOK {
		t.Fatalf("freeze: %d %s", w.Code, w.Body)
	}
	if w := do(t, owner, "POST", "/api/deploys/"+d3.ID+"/approve", ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "frozen until") ||
		!strings.Contains(w.Body.String(), "release party") {
		t.Errorf("approve in a freeze: %d %s", w.Code, w.Body)
	}
	if w := deploy(admin, `{"environment":"production","promote_from":"staging"}`); w.Code != http.StatusConflict {
		t.Errorf("deploy in a freeze: %d %s", w.Code, w.Body)
	}
	_ = json.Unmarshal(do(t, member, "GET", "/api/projects/"+p.ID+"/environments", "").Body.Bytes(), &envs)
	if !strings.Contains(envs[1].Policy.Frozen, "frozen until") || envs[1].Policy.CanDeploy || len(envs[0].Policy.PromoteBlocked) == 0 {
		t.Errorf("a member's view of production: %+v", envs[1].Policy)
	}
	if w := do(t, owner, "DELETE", "/api/deploys/"+d3.ID, ""); w.Code != http.StatusAccepted {
		t.Errorf("cancel an awaiting deploy: %d %s", w.Code, w.Body)
	}

	// Settings: admins by default; never a role above one's own.
	staging.URL = "https://staging.example.com"
	if w := setEnvs(member, staging, production); w.Code != http.StatusForbidden {
		t.Errorf("a member changes staging: %d %s", w.Code, w.Body)
	}
	staging.SettingsRole = model.RoleMember
	if w := setEnvs(admin, staging, production); w.Code != http.StatusOK {
		t.Fatalf("an admin lets members change staging: %d %s", w.Code, w.Body)
	}
	staging.URL = "https://stage.example.com"
	if w := setEnvs(member, staging, production); w.Code != http.StatusOK {
		t.Errorf("a member changes staging now: %d %s", w.Code, w.Body)
	}
	staging.DeployRole = model.RoleAdmin
	if w := setEnvs(member, staging, production); w.Code != http.StatusForbidden {
		t.Errorf("a member names a role above their own: %d %s", w.Code, w.Body)
	}
	production.Freezes = nil
	production.URL = "https://example.com"
	staging.DeployRole = ""
	if w := setEnvs(member, staging, production); w.Code != http.StatusForbidden {
		t.Errorf("a member changes production: %d %s", w.Code, w.Body)
	}

	// The history: who changed what, newest first.
	var hist []model.EnvChange
	_ = json.Unmarshal(do(t, owner, "GET", "/api/projects/"+p.ID+"/environments/history", "").Body.Bytes(), &hist)
	if len(hist) < 4 {
		t.Fatalf("history: %+v", hist)
	}
	if c := hist[0]; c.Environment != "staging" || c.ByName != "mia" || c.Action != model.EnvChanged || len(c.Diff) != 1 ||
		c.Diff[0] != (model.EnvFieldChange{Field: "url", From: "https://staging.example.com", To: "https://stage.example.com"}) {
		t.Errorf("the newest change: %+v", c)
	}
	if c := hist[len(hist)-1]; c.Action != model.EnvAdded || c.ByName != "u" {
		t.Errorf("the first change: %+v", c)
	}

	// A planner looks, and does nothing else.
	for _, path := range []string{"/api/projects/" + p.ID + "/environments", "/api/projects/" + p.ID + "/deploys",
		"/api/projects/" + p.ID + "/environments/history", "/api/deploys/" + d1.ID} {
		if w := do(t, planner, "GET", path, ""); w.Code != http.StatusOK {
			t.Errorf("a planner's GET %s: %d %s", path, w.Code, w.Body)
		}
	}
	if w := do(t, planner, "GET", "/api/deploys/"+d1.ID, ""); strings.Contains(w.Body.String(), `"log"`) {
		t.Errorf("a planner sees the log: %s", w.Body)
	}
	for _, e := range []endpoint{
		{"POST", "/api/projects/" + p.ID + "/deploys", `{"environment":"staging"}`},
		{"POST", "/api/deploys/" + d1.ID + "/approve", ""},
		{"POST", "/api/deploys/" + d1.ID + "/rollback", ""},
		{"DELETE", "/api/deploys/" + d1.ID, ""},
		{"PATCH", "/api/projects/" + p.ID, `{"environments":[]}`},
	} {
		if w := do(t, planner, e.method, e.path, e.body); w.Code != http.StatusForbidden {
			t.Errorf("a planner's %s %s: %d %.200s", e.method, e.path, w.Code, w.Body)
		}
	}
}
