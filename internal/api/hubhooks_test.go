package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/testbin"
	"github.com/klahr/dash/internal/tunnel"
)

// hubToRunner is a hub with one registered runner online that serves
// runnerAPI, and a hub project with remote that runs on it.
func hubToRunner(t *testing.T, runnerAPI http.Handler, remote string, setup func(tunnel.Credentials)) (*Server, model.Project) {
	t.Helper()
	hub, _, _ := boardServer(t)
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	d := &dispatch.Dispatcher{Store: raw(hub), Fleet: rh, Grace: time.Minute}
	rh.OnState, rh.OnRevoke, rh.OnReport = d.State, d.Revoked, d.Report
	hub.Dispatch = d
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	srv := httptest.NewServer(hub.Routes())
	t.Cleanup(srv.Close)

	owner := testOwner(t, hub)
	code, _, err := rh.NewCode(owner)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := tunnel.Register(ctx, srv.URL, code, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(creds)
	}
	client := &tunnel.Client{Creds: creds, Handler: runnerAPI, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { defer close(done); _ = client.Run(ctx) }()
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })
	p, err := raw(hub).CreateProject(model.Project{Name: "shared", OrgID: owner.Org, RemoteURL: remote, DefaultBranch: "main",
		RunsOn: []string{"any"}})
	if err != nil {
		t.Fatal(err)
	}
	return hub, p
}

// The hub registers a repository's webhook through a runner's gh
// (RunnerGitHub, ci.Hooks): each op reaches the runner's gh with the hook's
// id, its secret goes along (on gh's stdin) on create and on update, and
// the hook's id and URL come back to the hub.
func TestHubManagesWebhooksThroughRunnerGitHub(t *testing.T) {
	parallel(t)
	needGit(t)

	ghDir := t.TempDir()
	file := func(name string) string { return filepath.Join(ghDir, name) }
	gh := file("gh")
	const hookURL = "https://dash.example.com/api/github/hook"
	testbin.Write(t, gh, `#!/bin/sh
echo "$*" >> `+file("calls")+`
case "$*" in
"api -X POST repos/{owner}/{repo}/hooks --input -") cat > `+file("create")+`; echo '{"id":9,"config":{"url":"`+hookURL+`"}}' ;;
"api -X PATCH repos/{owner}/{repo}/hooks/9 --input -") cat > `+file("update")+`; echo '{"id":9}' ;;
"api repos/{owner}/{repo}/hooks?per_page=100") echo '[{"id":9,"active":true,"events":["workflow_run"],"config":{"url":"`+hookURL+`","content_type":"json"}}]' ;;
"api -X DELETE repos/{owner}/{repo}/hooks/9") ;;
"api -X POST repos/{owner}/{repo}/hooks/9/pings") ;;
"api repos/{owner}/{repo}/actions/runs/41") echo '{"id":41,"name":"CI","status":"completed","conclusion":"success","head_sha":"abc","html_url":"https://github.com/o/r/actions/runs/41"}' ;;
*) echo "unexpected: $*" >&2; exit 1 ;;
esac
`)
	runner, runnerAPI, remote := runnerSide(t, 0)
	runner.Git.GhBin = gh
	hub, p := hubToRunner(t, runnerAPI, remote, func(c tunnel.Credentials) {
		runner.HubOrg, runner.HubUser = c.OrgID, c.UserID
	})
	g := RunnerGitHub{S: hub}
	ctx := context.Background()
	cfg := gitops.HookConfig{URL: hookURL, Secret: "s3cret", Events: []string{"workflow_run"}}

	id, err := g.CreateHook(ctx, p.ID, "", cfg)
	if err != nil || id != 9 {
		t.Fatalf("CreateHook: %d, %v", id, err)
	}
	if b, _ := os.ReadFile(file("create")); !strings.Contains(string(b), `"secret":"s3cret"`) || !strings.Contains(string(b), `"url":"`+hookURL+`"`) {
		t.Fatalf("the runner's gh got no secret or URL on create: %s", b)
	}

	hooks, err := g.Hooks(ctx, p.ID, "")
	if err != nil || len(hooks) != 1 || hooks[0].ID != 9 || hooks[0].URL != hookURL || !hooks[0].Active ||
		len(hooks[0].Events) != 1 || hooks[0].Events[0] != "workflow_run" {
		t.Fatalf("Hooks: %+v, %v", hooks, err)
	}

	cfg.Secret = "n3w"
	if err := g.UpdateHook(ctx, p.ID, "", 9, cfg); err != nil {
		t.Fatalf("UpdateHook: %v", err)
	}
	if b, _ := os.ReadFile(file("update")); !strings.Contains(string(b), `"secret":"n3w"`) {
		t.Fatalf("the runner's gh got no secret on update: %s", b)
	}
	if err := g.PingHook(ctx, p.ID, "", 9); err != nil {
		t.Fatalf("PingHook: %v", err)
	}
	if err := g.DeleteHook(ctx, p.ID, "", 9); err != nil {
		t.Fatalf("DeleteHook: %v", err)
	}
	run, err := g.RunByID(ctx, p.ID, "", 41)
	if err != nil || run.ID != 41 || run.Conclusion != "success" || run.SHA != "abc" {
		t.Fatalf("RunByID: %+v, %v", run, err)
	}

	calls, _ := os.ReadFile(file("calls"))
	for _, want := range []string{"-X PATCH repos/{owner}/{repo}/hooks/9", "-X DELETE repos/{owner}/{repo}/hooks/9", "-X POST repos/{owner}/{repo}/hooks/9/pings"} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("the runner's gh wasn't called with %q:\n%s", want, calls)
		}
	}
	if strings.Contains(string(calls), "s3cret") || strings.Contains(string(calls), "n3w") {
		t.Errorf("the secret is on gh's command line:\n%s", calls)
	}

	// A create or update without a hook is refused on the runner.
	for _, op := range []string{dispatch.GitHubHookCreate, dispatch.GitHubHookUpdate} {
		w := send(runnerAPI, "POST", "/api/hub/github/"+p.ID, `{"op":"`+op+`","hook_id":9}`)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "no hook") {
			t.Errorf("%s without a hook: %d %s", op, w.Code, w.Body)
		}
	}
}

// A runner that answers a run op without a run is an error on the hub, not
// a zero run.
func TestRunnerGitHubRunByIDWithoutRun(t *testing.T) {
	parallel(t)

	runner := http.NewServeMux()
	runner.HandleFunc("PUT /api/hub/projects/{hpid}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, dispatch.Linked{ProjectID: "rp", DefaultBranch: "main"})
	})
	runner.HandleFunc("POST /api/hub/github/{hpid}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, dispatch.GitHubAnswer{})
	})
	hub, p := hubToRunner(t, runner, "https://github.com/o/r.git", nil)
	if run, err := (RunnerGitHub{S: hub}).RunByID(context.Background(), p.ID, "", 41); err == nil || !strings.Contains(err.Error(), "no run") {
		t.Fatalf("RunByID: %+v, %v", run, err)
	}
}
