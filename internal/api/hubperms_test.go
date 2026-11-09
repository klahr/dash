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
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/tunnel"
)

// A hub task's permission request, asked on its runner, shows on the
// hub's dashboard, and the hub user's answer reaches Claude on the runner.
func TestHubAnswersPermissionRequestsOfTasksOnRunners(t *testing.T) {
	hub, _, _ := boardServer(t)
	hub.RunnerOnly = true
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	d := &dispatch.Dispatcher{Store: raw(hub), Fleet: rh, Grace: time.Minute}
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
	client := &tunnel.Client{Creds: creds, Handler: runnerAPI, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { _ = client.Run(ctx) }()
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })

	p, err := raw(hub).CreateProject(model.Project{Name: "shared", OrgID: owner.Org, RemoteURL: remote, DefaultBranch: "main",
		MergeMode: model.MergePR, Runners: []string{creds.ID}})
	if err != nil {
		t.Fatal(err)
	}
	linked, err := d.Link(ctx, creds.ID, p)
	if err != nil {
		t.Fatal(err)
	}
	// The task, running on the runner.
	rt, err := raw(rs).CreateTodo(model.Todo{ProjectID: linked.ProjectID, Title: "task", Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	ht, err := raw(hub).CreateTodo(model.Todo{ProjectID: p.ID, Title: "task", Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct {
		s interface {
			SetStatus(string, model.TodoStatus, string) (model.Todo, error)
		}
		id string
	}{{raw(rs), rt.ID}, {raw(hub), ht.ID}} {
		if _, err := x.s.SetStatus(x.id, model.StatusQueued, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw(rs).UpdateTodo(rt.ID, func(x *model.Todo) error {
		x.Status, x.HubTodoID = model.StatusInProgress, ht.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := raw(hub).UpdateTodo(ht.ID, func(x *model.Todo) error {
		x.Status, x.Runner, x.RemoteID = model.StatusInProgress, creds.ID, rt.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Claude asks on the runner.
	token, release := rs.Perms.Register(owner, rt.ID, linked.ProjectID)
	defer release()
	answer := make(chan string, 1)
	go func() {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + perm.ToolName +
			`","arguments":{"tool_name":"Bash","input":{"command":"make deploy"}}}}`
		r := httptest.NewRequest("POST", "/mcp/permissions", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		rs.Perms.ServeMCP(w, r)
		answer <- w.Body.String()
	}()
	eventually(t, "the request to wait on the runner", func() bool { return rs.Perms.Waiting(rt.ID) })

	// The runner reports it.
	rep := &dispatch.Reporter{Store: raw(rs), Perms: func() []perm.Request { return rs.Perms.Pending("") },
		Send: func(ctx context.Context, r dispatch.Report) error { return tunnel.Report(ctx, creds, r) }}
	if err := rep.Send(ctx, rep.Build(time.Now())); err != nil {
		t.Fatal(err)
	}

	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		hub.mux().ServeHTTP(w, asOwner(t, hub, httptest.NewRequest(method, path, strings.NewReader(body))))
		return w
	}
	w := call("GET", "/api/permissions?project="+p.ID, "")
	var listed []perm.Request
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &listed) != nil {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	if len(listed) != 1 || listed[0].TodoID != ht.ID || listed[0].ProjectID != p.ID || listed[0].Tool != "Bash" ||
		!strings.Contains(string(listed[0].Input), "make deploy") {
		t.Fatalf("the hub lists: %+v", listed)
	}

	if w := call("POST", "/api/permissions/"+listed[0].ID, `{"decision":"allow"}`); w.Code != http.StatusNoContent {
		t.Fatalf("decide: %d %s", w.Code, w.Body)
	}
	select {
	case got := <-answer:
		if !strings.Contains(got, `\"behavior\":\"allow\"`) {
			t.Errorf("Claude was told: %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the answer never reached the runner")
	}
	if w := call("GET", "/api/permissions", ""); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("still listed: %s", w.Body)
	}
	if w := call("POST", "/api/permissions/"+listed[0].ID, `{"decision":"allow"}`); w.Code != http.StatusNotFound {
		t.Errorf("answered twice: %d %s", w.Code, w.Body)
	}
}
