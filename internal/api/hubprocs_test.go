package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/runner"
	"github.com/klahr/dash/internal/tunnel"
)

// The hub's dashboard finds a project's quick actions (Run, Run Tests,
// Deploy) and its shell among the processes the hub lists from its
// runners by their preset and Main, since their dir is the runner's clone,
// never the hub project's path. The runner marks them, and the hub keeps
// the marks, also once the command has finished (as after a deploy that
// restarted the hub).
func TestHubListsRunnerQuickActionsWithTheirPreset(t *testing.T) {
	hub, _, _ := boardServer(t)
	hub.RunnerOnly = true
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
	code, _, err := rh.NewCode(owner)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := tunnel.Register(ctx, srv.URL, code, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	rs, runnerAPI, remote := runnerSide(t, creds.OrgID)
	rs.Procs, rs.HubCommands = runner.NewManager(rs.Hub), true
	client := &tunnel.Client{Creds: creds, Handler: runnerAPI, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { _ = client.Run(ctx) }()
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })

	p, err := raw(hub).CreateProject(model.Project{Name: "shared", OrgID: owner.Org, Path: "/hub/has/its/own/path",
		RemoteURL: remote, DefaultBranch: "main", MergeMode: model.MergePR, Runners: []string{creds.ID},
		TestCommand: "true", DeployCommand: "exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		hub.mux().ServeHTTP(w, asOwner(t, hub, httptest.NewRequest(method, "/api/projects/"+p.ID+path, strings.NewReader(body))))
		return w
	}
	start := func(body string) model.Process {
		t.Helper()
		w := call("POST", "/processes", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("start %s: %d %s", body, w.Code, w.Body)
		}
		var proc model.Process
		if err := json.Unmarshal(w.Body.Bytes(), &proc); err != nil {
			t.Fatal(err)
		}
		return proc
	}

	test := start(`{"kind":"task","preset":"test"}`)
	if test.Preset != "test" || !test.Main || test.ProjectID != p.ID || test.Dir == p.Path {
		t.Errorf("started test run: %+v", test)
	}
	deploy := start(`{"kind":"task","preset":"deploy"}`)
	own := start(`{"kind":"task","command":"true"}`) // not a quick action
	if own.Preset != "" || !own.Main {
		t.Errorf("own command: %+v", own)
	}

	list := func() map[string]model.Process {
		w := call("GET", "/processes", "")
		var ps []model.Process
		if err := json.Unmarshal(w.Body.Bytes(), &ps); err != nil {
			t.Fatalf("list: %v %s", err, w.Body)
		}
		out := map[string]model.Process{}
		for _, x := range ps {
			out[x.ID] = x
		}
		return out
	}
	eventually(t, "the commands to finish", func() bool {
		ps := list()
		return len(ps) == 3 && !ps[test.ID].Running && !ps[deploy.ID].Running && !ps[own.ID].Running
	})
	ps := list()
	if x := ps[test.ID]; x.Preset != "test" || !x.Main || x.ExitCode == nil || *x.ExitCode != 0 {
		t.Errorf("listed test run: %+v", x)
	}
	if x := ps[deploy.ID]; x.Preset != "deploy" || !x.Main || x.ExitCode == nil || *x.ExitCode != 3 {
		t.Errorf("listed deploy: %+v", x)
	}
	if x := ps[own.ID]; x.Preset != "" {
		t.Errorf("listed own command: %+v", x)
	}
	if !strings.Contains(call("GET", "/processes", "").Body.String(), `"runner":"laptop"`) {
		t.Errorf("listed processes don't name their runner")
	}
}

// rewriteProcs gives a runner's processes the hub's ids and keeps the rest,
// the quick-action marks among them.
func TestRewriteProcsKeepsQuickActionMarks(t *testing.T) {
	body := []byte(`[{"id":"p1","kind":"task","preset":"deploy","main":true,"todo_id":"rt"},{"id":"p2","kind":"pty"}]`)
	var got []map[string]any
	if err := json.Unmarshal(rewriteProcs(body, tunnel.Runner{ID: "r1", Name: "laptop"}, map[string]string{"rt": "ht"}), &got); err != nil {
		t.Fatal(err)
	}
	if got[0]["id"] != "r1~p1" || got[0]["preset"] != "deploy" || got[0]["main"] != true || got[0]["todo_id"] != "ht" || got[0]["runner"] != "laptop" {
		t.Errorf("rewritten: %v", got[0])
	}
	if _, ok := got[1]["main"]; ok {
		t.Errorf("a process outside the main checkout got main: %v", got[1])
	}
}
