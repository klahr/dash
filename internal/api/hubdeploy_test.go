package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/runner"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// A hub project's deploy runs only on a runner with every label it deploys
// on (deploy_runs_on): with none, it fails saying so and runs nowhere;
// once the runner has the label, the deploy runs there, a multi-line
// command as one bash script. Only those who may change the commands may
// change the labels, as they pick where the deploy runs.
func TestHubDeploysOnlyOnRunnersWithItsLabels(t *testing.T) {
	parallel(t)

	hub, _, _ := boardServer(t)
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	d := &dispatch.Dispatcher{Store: raw(hub), Fleet: rh, Grace: 50 * time.Millisecond}
	rh.OnState, rh.OnRevoke, rh.OnReport = d.State, d.Revoked, d.Report
	hub.Dispatch = d
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(hub.Routes())
	defer srv.Close()

	owner := testOwner(t, hub)
	other := member(t, hub, owner.Org, "bob", store.RoleMember)
	code, _, err := rh.NewCode(owner)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := tunnel.Register(ctx, srv.URL, code, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	rs, runnerAPI, remote := runnerSide(t, creds.OrgID)
	rs.Procs, rs.HubUser = runner.NewManager(rs.Hub), creds.UserID
	t.Cleanup(rs.Procs.StopAll)
	client := &tunnel.Client{Creds: creds, Handler: runnerAPI, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { _ = client.Run(ctx) }()
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })

	p, err := raw(hub).CreateProject(model.Project{Name: "shared", OrgID: owner.Org, Path: "/hub/has/its/own/path",
		RemoteURL: remote, DefaultBranch: "main", MergeMode: model.MergePR, RunsOn: []string{"any"}})
	if err != nil {
		t.Fatal(err)
	}
	call := func(uid int64, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		sess := sessionOf(t, hub, uid, owner.Org)
		r := httptest.NewRequest(method, "/api/projects/"+p.ID+path, strings.NewReader(body))
		w := httptest.NewRecorder()
		hub.mux().ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), sess)))
		return w
	}

	// A multi-line deploy command, on the label gpu, which the runner lacks.
	marker := filepath.Join(t.TempDir(), "deployed")
	script := "set -e\r\necho one > " + marker + "\r\n\r\n  echo two >> " + marker + "\r\n"
	b, _ := json.Marshal(map[string]any{"deploy_command": script, "deploy_runs_on": []string{" gpu ", "any", "GPU"}})
	w := call(owner.User, "PATCH", "", string(b))
	if w.Code != http.StatusOK {
		t.Fatalf("set the deploy command: %d %s", w.Code, w.Body)
	}
	var saved model.Project
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	wantScript := "set -e\necho one > " + marker + "\n\n  echo two >> " + marker
	if saved.DeployCommand != wantScript || strings.Join(saved.DeployRunsOn, ",") != "gpu" {
		t.Fatalf("saved deploy %q on %v", saved.DeployCommand, saved.DeployRunsOn)
	}
	got, _ := raw(hub).GetProject(p.ID)
	if got.DeployCommand != wantScript {
		t.Fatalf("stored deploy %q", got.DeployCommand)
	}

	w = call(owner.User, "POST", "/processes", `{"kind":"task","preset":"deploy"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "labels it deploys on (gpu)") {
		t.Fatalf("deploy with no runner labeled gpu: %d %s", w.Code, w.Body)
	}
	// Other commands still go to the runner.
	if w := call(owner.User, "POST", "/processes", `{"kind":"task","command":"true"}`); w.Code != http.StatusCreated {
		t.Fatalf("own command: %d %s", w.Code, w.Body)
	}
	// Nor does the runner deploy if asked: its clone has the deploy held.
	var clone model.Project
	for _, x := range raw(rs).ForOrg(creds.OrgID).ListProjects() {
		if x.LinkedFromHub() {
			clone = x
		}
	}
	if !strings.Contains(clone.Held(model.CmdDeploy), "runner laptop lacks gpu") || clone.DeployCommand != "" {
		t.Fatalf("runner's clone: held %v, deploy %q", clone.HeldCommands, clone.DeployCommand)
	}

	// Another member can't move the deploy to other runners.
	if w := call(other, "PATCH", "", `{"deploy_runs_on":[]}`); w.Code != http.StatusForbidden {
		t.Fatalf("deploy labels changed by another member: %d %s", w.Code, w.Body)
	}
	if w := call(other, "PATCH", "", `{"deploy_runs_on":["GPU"],"color":"#3a7bd5"}`); w.Code != http.StatusOK {
		t.Fatalf("same deploy labels sent by another member: %d %s", w.Code, w.Body)
	}

	// With the label, it deploys there: the lines run as one script.
	if err := rh.SetLabels(creds.ID, []string{"gpu"}); err != nil {
		t.Fatal(err)
	}
	w = call(owner.User, "POST", "/processes", `{"kind":"task","preset":"deploy"}`)
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"runner":"laptop"`) {
		t.Fatalf("deploy on the runner labeled gpu: %d %s", w.Code, w.Body)
	}
	eventually(t, "the deploy to run", func() bool {
		b, _ := os.ReadFile(marker)
		return string(b) == "one\ntwo\n"
	})
	if b, _ := os.ReadFile(marker); string(b) != "one\ntwo\n" {
		t.Fatalf("deployed: %q", b)
	}
}

// The deploy command keeps its lines through saving and loading, on a Dash
// of its own, which deploys on its own computer: deploy_runs_on is a hub's.
func TestMultilineDeployCommandRoundTrips(t *testing.T) {
	s, _, p := boardServer(t)
	s.Git = gitops.New(nil)
	patch := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest("PATCH", "/api/projects/"+p.ID, strings.NewReader(body))))
		return w
	}
	script := "#!/bin/bash\nset -euo pipefail\n\nnpm run build\nif [ -n \"$X\" ]; then\n  ./deploy.sh \"$X\"\nfi"
	b, _ := json.Marshal(map[string]string{"deploy_command": "\n" + script + "\n\n"})
	if w := patch(string(b)); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	w := httptest.NewRecorder()
	s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest("GET", "/api/projects/"+p.ID, nil)))
	var got model.Project
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("load: %v %s", err, w.Body)
	}
	if got.DeployCommand != script {
		t.Errorf("loaded %q, want %q", got.DeployCommand, script)
	}
	if w := patch(`{"deploy_runs_on":["gpu"]}`); w.Code != http.StatusBadRequest {
		t.Errorf("deploy labels on a Dash of its own: %d %s", w.Code, w.Body)
	}
	if w := patch(`{"deploy_runs_on":[]}`); w.Code != http.StatusOK {
		t.Errorf("no deploy labels on a Dash of its own: %d %s", w.Code, w.Body)
	}
}
