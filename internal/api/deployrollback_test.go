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

// On a Dash that deploys on its own computer: a deploy's health check and
// verify command decide whether it is live. "Roll back" deploys the last
// good deploy's commit again with DASH_ROLLBACK=1, and marks the one it
// replaces rolled back. With automatic rollback on, a deploy that fails its
// checks is rolled back at once. Each failure leaves one draft task "Fix
// failed deploy to <env>", with the log (its secrets masked) attached.
func TestDeployChecksRollbackAndFailure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	s, _, _ := boardServer(t)
	s.Git = gitops.New(s.Hub)
	s.DeployPoll = 20 * time.Millisecond
	s.DeployRoot, s.EnvDir, s.UploadRoot = t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(s.EnvDir, "staging.env"), []byte("TOKEN=supersecret1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
		}
	}))
	defer health.Close()

	// Three commits; the third breaks the smoke test.
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	var commits []string
	for _, f := range []string{"a", "b", "broken"} {
		if err := os.WriteFile(filepath.Join(repo, f), []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "add", f)
		git(t, repo, "commit", "-q", "-m", "add "+f)
		commits = append(commits, git(t, repo, "rev-parse", "HEAD"))
	}
	p, err := raw(s).CreateProject(model.Project{Name: "web2", OrgID: testOwner(t, s).Org, Path: repo, DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "deployed")
	env := model.Environment{Name: "staging", URL: health.URL, HealthPath: "/healthz", HealthStatus: 200, HealthTimeout: 1,
		DeployCommand: "echo $DASH_COMMIT rollback=$DASH_ROLLBACK > '" + filepath.ToSlash(marker) + "'\necho using $TOKEN",
		VerifyCommand: `test ! -f broken || { echo "smoke test failed at $DASH_URL"; exit 1; }`}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest(method, path, strings.NewReader(body))))
		return w
	}
	setEnv := func(e model.Environment) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"environments": []model.Environment{e}})
		if w := call("PATCH", "/api/projects/"+p.ID, string(b)); w.Code != http.StatusOK {
			t.Fatalf("set the environment: %d %s", w.Code, w.Body)
		}
	}
	setEnv(env)
	finished := func(id string) model.Deploy {
		t.Helper()
		var d model.Deploy
		eventually(t, "the deploy to finish", func() bool {
			d, _ = raw(s).GetDeploy(id)
			return d.Status.Done()
		})
		return d
	}
	// settled is deploy id once ok holds, or as it is when the wait runs
	// out: a rollback goes live before the deploys it replaces are marked.
	settled := func(id string, ok func(model.Deploy) bool) model.Deploy {
		t.Helper()
		d, _ := raw(s).GetDeploy(id)
		for deadline := time.Now().Add(waitTimeout); !ok(d) && time.Now().Before(deadline); {
			time.Sleep(10 * time.Millisecond)
			d, _ = raw(s).GetDeploy(id)
		}
		return d
	}
	deploy := func(commit string) model.Deploy {
		t.Helper()
		w := call("POST", "/api/projects/"+p.ID+"/deploys", `{"environment":"staging","commit":"`+commit+`"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("deploy: %d %s", w.Code, w.Body)
		}
		var d model.Deploy
		_ = json.Unmarshal(w.Body.Bytes(), &d)
		return finished(d.ID)
	}
	envStatusNow := func() envStatus {
		t.Helper()
		var list []envStatus
		if err := json.Unmarshal(call("GET", "/api/projects/"+p.ID+"/environments", "").Body.Bytes(), &list); err != nil || len(list) != 1 {
			t.Fatalf("environments: %v", err)
		}
		return list[0]
	}
	deployed := func() string {
		b, _ := os.ReadFile(marker)
		return strings.TrimSpace(string(b))
	}
	phases := func(d model.Deploy) string {
		var out []string
		for _, ph := range d.Phases {
			out = append(out, string(ph.Status))
		}
		return strings.Join(out, ",")
	}

	// Two good deploys: checked, then live.
	d1 := deploy(commits[0])
	if d1.Status != model.DeployLive || !d1.Deployed || len(d1.Checks) != 2 || !d1.Checks[0].OK || !d1.Checks[1].OK ||
		phases(d1) != "queued,deploying,verifying,live" {
		t.Fatalf("first deploy: %+v", d1)
	}
	if envStatusNow().Rollback != nil {
		t.Error("nothing to roll back to after the first deploy")
	}
	d2 := deploy(commits[1])
	if d2.Status != model.DeployLive {
		t.Fatalf("second deploy: %+v", d2)
	}

	// Roll back by hand: to the last good deploy, d1.
	rb := envStatusNow().Rollback
	if rb == nil || rb.Of != d2.ID || rb.To.ID != d1.ID {
		t.Fatalf("rollback offered: %+v", rb)
	}
	var detail deployDetail
	_ = json.Unmarshal(call("GET", "/api/deploys/"+d2.ID, "").Body.Bytes(), &detail)
	if detail.RollbackTo == nil || detail.RollbackTo.ID != d1.ID {
		t.Errorf("the live deploy offers its rollback: %+v", detail.RollbackTo)
	}
	if w := call("POST", "/api/deploys/"+d1.ID+"/rollback", ""); w.Code != http.StatusConflict {
		t.Errorf("roll back a deploy that isn't live: %d %s", w.Code, w.Body)
	}
	w := call("POST", "/api/deploys/"+d2.ID+"/rollback", "")
	if w.Code != http.StatusCreated {
		t.Fatalf("roll back: %d %s", w.Code, w.Body)
	}
	var r1 model.Deploy
	_ = json.Unmarshal(w.Body.Bytes(), &r1)
	r1 = finished(r1.ID)
	if r1.Status != model.DeployLive || !r1.Rollback || r1.Commit != commits[0] || r1.RollbackOf != d2.ID || r1.RollbackTo != d1.ID ||
		r1.Trigger != model.DeployByUser || r1.PrevDeploy != d2.ID {
		t.Fatalf("rollback: %+v", r1)
	}
	if got := deployed(); got != commits[0]+" rollback=1" {
		t.Errorf("the rollback's script ran with %q", got)
	}
	if x := settled(d2.ID, func(x model.Deploy) bool { return x.RolledBackBy != "" }); x.Status != model.DeployRolledBack || x.RolledBackBy != r1.ID {
		t.Errorf("the deploy rolled back: %s by %q", x.Status, x.RolledBackBy)
	}
	if x := settled(d1.ID, func(x model.Deploy) bool { return x.Status != model.DeployLive }); x.Status != model.DeploySuperseded {
		t.Errorf("the deploy rolled back to stays superseded: %s", x.Status)
	}
	// Now only d1's commit went live without being rolled back: nothing else to go to.
	if rb := envStatusNow().Rollback; rb != nil {
		t.Errorf("rollback offered after the rollback: %+v", rb)
	}

	// With automatic rollback, a deploy that fails its checks is rolled
	// back to what was live.
	env.AutoRollback = true
	setEnv(env)
	d3 := deploy(commits[2])
	if d3.Status != model.DeployFailed || !d3.Deployed || !d3.ChecksFailed() || !strings.Contains(d3.Error, "verify command exited with 1") {
		t.Fatalf("the broken deploy: %+v", d3)
	}
	var r2 model.Deploy
	eventually(t, "the automatic rollback", func() bool {
		for _, d := range raw(s).ListDeploys(p.ID, "staging") {
			if d.Rollback && d.RollbackOf == d3.ID && d.Status.Done() {
				r2 = d
				return true
			}
		}
		return false
	})
	if r2.Status != model.DeployLive || r2.Trigger != model.DeployByAutoRollback || r2.Commit != commits[0] || r2.RollbackTo != r1.ID {
		t.Fatalf("automatic rollback: %+v", r2)
	}
	if x := settled(d3.ID, func(x model.Deploy) bool { return x.RolledBackBy != "" }); x.Status != model.DeployFailed || x.RolledBackBy != r2.ID {
		t.Errorf("the failed deploy: %s, rolled back by %q", x.Status, x.RolledBackBy)
	}
	if x := settled(r1.ID, func(x model.Deploy) bool { return x.Status != model.DeployLive }); x.Status != model.DeploySuperseded {
		t.Errorf("the rollback before: %s", x.Status)
	}

	// The failure left a draft task with the log, its secret masked.
	var fix model.Todo
	eventually(t, "the draft task", func() bool {
		x, _ := raw(s).GetDeploy(d3.ID)
		if x.FixTask == "" {
			return false
		}
		fix, err = raw(s).GetTodo(x.FixTask)
		return err == nil
	})
	if fix.Title != "Fix failed deploy to staging" || fix.Status != model.StatusDraft || fix.ProjectID != p.ID ||
		!strings.Contains(fix.Prompt, "smoke test failed at "+health.URL) || !strings.Contains(fix.Prompt, "verify: failed") ||
		!strings.Contains(fix.Prompt, "rolling staging back to commit "+gitops.ShortSHA(commits[0])) || strings.Contains(fix.Prompt, "supersecret1") ||
		!strings.Contains(fix.Prompt, "using ***") {
		t.Errorf("draft task %q: %s", fix.Title, fix.Prompt)
	}
	if len(fix.Attachments) != 1 {
		t.Fatalf("attachments %+v", fix.Attachments)
	}
	if b, err := os.ReadFile(fix.Attachments[0].Path); err != nil || !strings.Contains(string(b), "using ***") || strings.Contains(string(b), "supersecret1") {
		t.Errorf("attached log %q (%v)", b, err)
	}

	// Another failure (its script, this time) doesn't roll back; it uses
	// the same draft.
	env.DeployCommand = "echo $TOKEN; exit 3"
	setEnv(env)
	d4 := deploy(commits[1])
	if d4.Status != model.DeployFailed || d4.Deployed || d4.ChecksFailed() {
		t.Fatalf("failed script: %+v", d4)
	}
	eventually(t, "the draft for it", func() bool { x, _ := raw(s).GetDeploy(d4.ID); return x.FixTask != "" })
	time.Sleep(100 * time.Millisecond)
	n := 0
	for _, x := range raw(s).ListDeploys(p.ID, "staging") {
		if x.Rollback {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d rollbacks: a failed script doesn't roll back", n)
	}
	drafts := 0
	for _, x := range raw(s).ListTodos(p.ID) {
		if x.Title == "Fix failed deploy to staging" {
			drafts++
		}
	}
	if x, _ := raw(s).GetDeploy(d4.ID); drafts != 1 || x.FixTask != fix.ID {
		t.Errorf("%d drafts; the second failure's is %s, want %s", drafts, x.FixTask, fix.ID)
	}

	// A cancelled deploy leaves no draft.
	env.DeployCommand = "sleep 30"
	setEnv(env)
	w = call("POST", "/api/projects/"+p.ID+"/deploys", `{"environment":"staging"}`)
	var d5 model.Deploy
	_ = json.Unmarshal(w.Body.Bytes(), &d5)
	eventually(t, "the deploy to start", func() bool { x, _ := raw(s).GetDeploy(d5.ID); return x.Status.Active() })
	if w := call("DELETE", "/api/deploys/"+d5.ID, ""); w.Code != http.StatusAccepted {
		t.Fatalf("stop: %d %s", w.Code, w.Body)
	}
	if d5 = finished(d5.ID); d5.Status != model.DeployFailed || !d5.Cancelled || d5.FixTask != "" || !strings.Contains(d5.Error, "stopped by") {
		t.Errorf("stopped deploy: %+v", d5)
	}
}
