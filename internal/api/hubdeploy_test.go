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

// A hub project's deploys to an environment run only on a runner with
// every label the environment deploys on: with none, the deploy fails
// saying so and runs nowhere, and the runner's clone has it held. Once the
// runner has the label, the deploy runs there, in a checkout of the
// commit: the build, then the multi-line deploy script, with the DASH_
// variables and the runner's secrets file, whose values the log masks. It
// goes live, and the next one supersedes it. Only those who may change the
// commands may change the environments, as they pick where deploys run.
func TestHubDeploysOnlyOnRunnersWithItsLabels(t *testing.T) {
	parallel(t)

	hub, _, _ := boardServer(t)
	hub.DeployPoll = 20 * time.Millisecond
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
	rs.DeployRoot, rs.EnvDir = t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(rs.EnvDir, "production.env"), []byte("API_TOKEN=very-secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		hub.mux().ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), sess)))
		return w
	}
	pp := "/api/projects/" + p.ID

	// A multi-line deploy script, on the label gpu, which the runner lacks.
	marker := filepath.Join(t.TempDir(), "deployed")
	// The deploy runs in bash (Git Bash on Windows), where an unquoted
	// C:\Users\... loses its backslashes as escapes: forward slashes, quoted.
	target := "'" + filepath.ToSlash(marker) + "'"
	script := "set -e\r\necho $DASH_ENV $DASH_PREV_COMMIT > " + target + "\r\n\r\n  echo token $API_TOKEN >> " + target + "\r\ncat built.txt\r\necho token $API_TOKEN\r\n"
	envs := []model.Environment{{Name: " Production ", DeployCommand: script, BuildCommand: "echo built $DASH_COMMIT > built.txt", RunsOn: []string{" gpu ", "any", "GPU"},
		ApproveRole: model.RoleNone}}
	b, _ := json.Marshal(map[string]any{"environments": envs})
	w := call(owner.User, "PATCH", pp, string(b))
	if w.Code != http.StatusOK {
		t.Fatalf("set the environments: %d %s", w.Code, w.Body)
	}
	var saved model.Project
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	wantScript := "set -e\necho $DASH_ENV $DASH_PREV_COMMIT > " + target + "\n\n  echo token $API_TOKEN >> " + target + "\ncat built.txt\necho token $API_TOKEN"
	if len(saved.Environments) != 1 || saved.Environments[0].Name != "production" || saved.Environments[0].DeployCommand != wantScript ||
		strings.Join(saved.Environments[0].RunsOn, ",") != "gpu" {
		t.Fatalf("saved environments %+v", saved.Environments)
	}

	deployNow := func() model.Deploy {
		t.Helper()
		w := call(owner.User, "POST", pp+"/deploys", `{"environment":"production"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("deploy: %d %s", w.Code, w.Body)
		}
		var d model.Deploy
		_ = json.Unmarshal(w.Body.Bytes(), &d)
		if d.Status != model.DeployQueued || d.ByName == "" || d.Trigger != model.DeployByUser {
			t.Fatalf("queued: %+v", d)
		}
		return d
	}
	finished := func(id string) model.Deploy {
		t.Helper()
		var d model.Deploy
		eventually(t, "the deploy to finish", func() bool {
			d, _ = raw(hub).GetDeploy(id)
			return d.Status.Done()
		})
		return d
	}
	if w := call(owner.User, "POST", pp+"/deploys", `{"environment":"staging"}`); w.Code != http.StatusBadRequest {
		t.Errorf("deploy to an environment it hasn't: %d %s", w.Code, w.Body)
	}
	first := finished(deployNow().ID)
	if first.Status != model.DeployFailed || !strings.Contains(first.Error, "labels production deploys on (gpu)") {
		t.Fatalf("deploy with no runner labeled gpu: %+v", first)
	}
	// Other commands still go to the runner (which links the project there).
	if w := call(owner.User, "POST", pp+"/processes", `{"kind":"task","command":"true"}`); w.Code != http.StatusCreated {
		t.Fatalf("own command: %d %s", w.Code, w.Body)
	}
	// Nor does the runner deploy if asked: its clone has it held.
	var clone model.Project
	for _, x := range raw(rs).ForOrg(creds.OrgID).ListProjects() {
		if x.LinkedFromHub() {
			clone = x
		}
	}
	if e, _ := clone.Environment("production"); !strings.Contains(clone.DeployHeld("production"), "runner laptop lacks gpu") || e.DeployCommand != "" {
		t.Fatalf("runner's clone: held %v, environments %+v", clone.HeldCommands, clone.Environments)
	}

	// Another member can't move the deploys to other runners.
	moved := []model.Environment{{Name: "production", DeployCommand: wantScript, BuildCommand: envs[0].BuildCommand}}
	b, _ = json.Marshal(map[string]any{"environments": moved})
	if w := call(other, "PATCH", pp, string(b)); w.Code != http.StatusForbidden {
		t.Fatalf("environment changed by another member: %d %s", w.Code, w.Body)
	}
	b, _ = json.Marshal(map[string]any{"environments": envs, "color": "#3a7bd5"})
	if w := call(other, "PATCH", pp, string(b)); w.Code != http.StatusOK {
		t.Fatalf("same environments sent by another member: %d %s", w.Code, w.Body)
	}

	// With the label, it deploys there.
	if err := rh.SetLabels(creds.ID, []string{"gpu"}); err != nil {
		t.Fatal(err)
	}
	live := finished(deployNow().ID)
	if live.Status != model.DeployLive || live.Runner != "laptop" || len(live.Commit) != 40 || live.ExitCode == nil || *live.ExitCode != 0 {
		t.Fatalf("deploy on the runner labeled gpu: %+v", live)
	}
	if got, _ := os.ReadFile(marker); strings.ReplaceAll(string(got), "\r", "") != "production\ntoken very-secret-token\n" {
		t.Fatalf("deployed: %q", got)
	}
	if !strings.Contains(live.Log, "token") || strings.Contains(live.Log, "very-secret-token") || !strings.Contains(live.Log, "built "+live.Commit) {
		t.Errorf("log, secrets masked: %q", live.Log)
	}
	var phases []string
	for _, ph := range live.Phases {
		phases = append(phases, string(ph.Status))
	}
	if strings.Join(phases, ",") != "queued,building,deploying,live" {
		t.Errorf("phases: %v", phases)
	}

	// The next one replaces it, and knows what it replaces.
	next := finished(deployNow().ID)
	if next.Status != model.DeployLive || next.PrevDeploy != live.ID || next.PrevCommit != live.Commit {
		t.Fatalf("second deploy: %+v", next)
	}
	if got, _ := os.ReadFile(marker); !strings.HasPrefix(string(got), "production "+live.Commit) {
		t.Errorf("DASH_PREV_COMMIT: %q", got)
	}
	if old, _ := raw(hub).GetDeploy(live.ID); old.Status != model.DeploySuperseded {
		t.Errorf("the deploy before: %s", old.Status)
	}

	// The Deploys page: what is live and where it deploys. No runner has
	// the secrets file, which is optional: no warning.
	w = call(owner.User, "GET", pp+"/environments", "")
	var list []envStatus
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) != 1 {
		t.Fatalf("environments: %v %s", err, w.Body)
	}
	if e := list[0]; e.Live == nil || e.Live.ID != next.ID || len(e.Runners) != 1 || e.Runners[0].EnvFile || e.Warning != "" {
		t.Errorf("environment: %+v", e)
	}
	w = call(owner.User, "GET", pp+"/deploys", "")
	var deploys []model.Deploy
	if err := json.Unmarshal(w.Body.Bytes(), &deploys); err != nil || len(deploys) != 3 || deploys[0].ID != next.ID || deploys[0].Log != "" {
		t.Errorf("deploys: %v %s", err, w.Body)
	}
	// Release tags come from the runner's clone, which fetches origin's:
	// Deploy's picker, compared with what is live, and a commit's own.
	git(t, remote, "tag", "1.0.0", next.Commit)
	w = call(owner.User, "GET", pp+"/deploys/tags?environment=production", "")
	var tags deployTags
	if err := json.Unmarshal(w.Body.Bytes(), &tags); err != nil || w.Code != http.StatusOK || len(tags.Tags) != 1 ||
		tags.Tags[0].Tag != "1.0.0" || tags.Tags[0].Commit != next.Commit || tags.Live != next.Commit || !tags.Tags[0].Compared {
		t.Errorf("tags on the hub: %d %s", w.Code, w.Body)
	}
	w = call(owner.User, "GET", pp+"/deploys/tags?commit="+next.Commit, "")
	tags = deployTags{}
	if err := json.Unmarshal(w.Body.Bytes(), &tags); err != nil || strings.Join(tags.CommitTags, ",") != "1.0.0" {
		t.Errorf("a commit's tags on the hub: %d %s", w.Code, w.Body)
	}
	// A deploy started there records its tag.
	if tagged := finished(deployNow().ID); tagged.Status != model.DeployLive || tagged.Tag != "1.0.0" {
		t.Errorf("the tag of a deploy on the runner: %+v", tagged)
	}

	// The old way to deploy is gone.
	if w := call(owner.User, "POST", pp+"/processes", `{"kind":"task","preset":"deploy"}`); w.Code != http.StatusBadRequest {
		t.Errorf("the deploy preset: %d %s", w.Code, w.Body)
	}
}

// The environments keep their scripts' lines through saving and loading,
// on a Dash of its own, which deploys on its own computer: runner labels
// are a hub's.
func TestEnvironmentsRoundTrip(t *testing.T) {
	s, _, p := boardServer(t)
	s.Git = gitops.New(nil)
	patch := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest("PATCH", "/api/projects/"+p.ID, strings.NewReader(body))))
		return w
	}
	script := "#!/bin/bash\nset -euo pipefail\n\nnpm run build\nif [ -n \"$X\" ]; then\n  ./deploy.sh \"$X\"\nfi"
	b, _ := json.Marshal(map[string]any{"environments": []model.Environment{
		{Name: "staging", DeployCommand: "\n" + script + "\n\n", URL: "https://staging.example.com"},
		{Name: "production", DeployCommand: "./ship"},
	}})
	if w := patch(string(b)); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	w := httptest.NewRecorder()
	s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest("GET", "/api/projects/"+p.ID, nil)))
	var got model.Project
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("load: %v %s", err, w.Body)
	}
	if len(got.Environments) != 2 || got.Environments[0].DeployCommand != script || got.Environments[0].URL != "https://staging.example.com" {
		t.Errorf("loaded %+v", got.Environments)
	}
	for _, bad := range []string{
		`{"environments":[{"name":"prod","deploy_command":"x","runs_on":["gpu"]}]}`,                                     // labels on a Dash of its own
		`{"environments":[{"name":"../etc","deploy_command":"x"}]}`,                                                     // not a name
		`{"environments":[{"name":"a","deploy_command":"x"},{"name":"A","deploy_command":"y"}]}`,                        // twice
		`{"environments":[{"name":"a","deploy_command":""}]}`,                                                           // nothing to run
		`{"environments":[{"name":"a","deploy_command":"x","url":"ftp://example.com"}]}`,                                // not a web address
		`{"environments":[{"name":"a","deploy_command":"x","deploy_role":"planner"}]}`,                                  // not a role a policy names
		`{"environments":[{"name":"a","deploy_command":"x","require_live_on":"a"}]}`,                                    // itself
		`{"environments":[{"name":"a","deploy_command":"x","require_live_on":"b"}]}`,                                    // no such environment
		`{"environments":[{"name":"a","deploy_command":"x","time_zone":"Mars/Base"}]}`,                                  // no such zone
		`{"environments":[{"name":"a","deploy_command":"x","freezes":[{"from":"fri 15:00","to":"2026-12-24 00:00"}]}]}`, // mixed
	} {
		if w := patch(bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", bad, w.Code, w.Body)
		}
	}
	if w := patch(`{"environments":[{"name":"prod","deploy_command":"x","runs_on":["any"]}]}`); w.Code != http.StatusOK {
		t.Errorf("any runner on a Dash of its own: %d %s", w.Code, w.Body)
	}
}
