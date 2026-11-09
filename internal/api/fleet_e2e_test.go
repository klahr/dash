package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/claudecli"
	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/tunnel"
)

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !ok(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A hub sends a linked project's task to a runner registered with a code,
// over the runner's tunnel; the runner reports back; while the runner is
// offline new tasks wait; revoking it unlinks it.
func TestHubDispatchesToARunnerEndToEnd(t *testing.T) {
	// The hub
	hub, _, _ := boardServer(t)
	acc := hub.Auth.Accounts()
	rh := &tunnel.Hub{Accounts: acc}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	d := &dispatch.Dispatcher{Store: raw(hub), Fleet: rh, Grace: 50 * time.Millisecond}
	rh.OnState, rh.OnRevoke, rh.OnReport = d.State, d.Revoked, d.Report
	engine := queue.NewTodoEngine(&queue.TodoEngine{Store: raw(hub), Pub: hub.Hub, Log: &queue.TodoLog{Root: t.TempDir()},
		Remote: d})
	d.Kick = engine.Kick
	hub.Todos, hub.Dispatch = engine, d
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go engine.Run(ctx)
	srv := httptest.NewServer(hub.Routes())
	defer srv.Close()

	// A runner, registered with a code its owner made
	owner := testOwner(t, hub)
	code, _, err := rh.NewCode(owner)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := tunnel.Register(ctx, srv.URL, code, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	if creds.OrgID != owner.Org {
		t.Fatalf("runner registered for org %d, want %d", creds.OrgID, owner.Org)
	}
	runner, runnerAPI, remote := runnerSide(t, creds.OrgID)
	cli := &claudecli.Monitor{}
	cli.Stream().Line(`{"type":"result","subtype":"success"}`) // a changed Claude Code
	reporter := &dispatch.Reporter{Store: raw(runner), Parallel: 2, Claude: cli,
		Send: func(ctx context.Context, r dispatch.Report) error { return tunnel.Report(ctx, creds, r) }}
	rctx, stopRunner := context.WithCancel(ctx)
	client := &tunnel.Client{Creds: creds, Handler: runnerAPI, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	stopped := make(chan struct{})
	go func() { _ = client.Run(rctx); close(stopped) }()
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })

	// A project of the hub linked to it, and a task
	p, err := raw(hub).CreateProject(model.Project{Name: "shared", OrgID: owner.Org, RemoteURL: remote, DefaultBranch: "main",
		MergeMode: model.MergePR, Runners: []string{creds.ID}})
	if err != nil {
		t.Fatal(err)
	}
	td, _ := raw(hub).CreateTodo(model.Todo{ProjectID: p.ID, Title: "add a page", Prompt: "add /about"})
	if _, err := raw(hub).SetStatus(td.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	engine.Kick()
	eventually(t, "the task to reach the runner", func() bool {
		got, _ := raw(hub).GetTodo(td.ID)
		return got.Status == model.StatusInProgress && got.Runner == creds.ID
	})
	sent, _ := raw(hub).GetTodo(td.ID)
	copyOnRunner, err := raw(runner).GetTodo(sent.RemoteID)
	if err != nil || copyOnRunner.HubTodoID != td.ID || copyOnRunner.Prompt != "add /about" {
		t.Fatalf("runner's copy %+v %v", copyOnRunner, err)
	}
	if rp, _ := raw(runner).GetProject(copyOnRunner.ProjectID); rp.HubProjectID != p.ID {
		t.Errorf("runner's project %+v", rp)
	}

	// Claude asks to run a tool: the request waits on the runner's broker,
	// reaches the hub's board in the runner's report, and is answered there.
	token, release := runner.Perms.Register(owner, copyOnRunner.ID, copyOnRunner.ProjectID)
	defer release()
	answered := make(chan string, 1)
	go func() {
		call := httptest.NewRequest("POST", "/mcp/permissions", strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"permission_prompt","arguments":{"tool_name":"Bash","input":{"command":"make"}}}}`))
		call.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		runner.Perms.ServeMCP(w, call)
		answered <- w.Body.String()
	}()
	eventually(t, "the request to wait on the runner", func() bool { return len(runner.Perms.Pending("")) == 1 })
	reporter.Perms = func() []perm.Request { return runner.Perms.Pending("") }
	if err := reporter.Send(ctx, reporter.Build(time.Now())); err != nil {
		t.Fatal(err)
	}
	listPerms := func() []perm.Request {
		w := httptest.NewRecorder()
		hub.mux().ServeHTTP(w, asOwner(t, hub, httptest.NewRequest("GET", "/api/permissions?project="+p.ID, nil)))
		var out []perm.Request
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
			t.Fatalf("hub's permissions: %d %s", w.Code, w.Body)
		}
		return out
	}
	pending := listPerms()
	if len(pending) != 1 || pending[0].TodoID != td.ID || pending[0].ProjectID != p.ID || pending[0].Tool != "Bash" {
		t.Fatalf("hub's board shows %+v", pending)
	}
	w := httptest.NewRecorder()
	hub.mux().ServeHTTP(w, asOwner(t, hub, httptest.NewRequest("POST", "/api/permissions/"+pending[0].ID, strings.NewReader(`{"decision":"allow"}`))))
	if w.Code != http.StatusNoContent {
		t.Fatalf("answering on the hub: %d %s", w.Code, w.Body)
	}
	select {
	case a := <-answered:
		if !strings.Contains(a, `\"behavior\":\"allow\"`) {
			t.Fatalf("Claude was told %s", a)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the runner's request wasn't answered")
	}
	if left := listPerms(); len(left) != 0 {
		t.Errorf("still shown after answering: %+v", left)
	}
	if err := reporter.Send(ctx, reporter.Build(time.Now())); err != nil {
		t.Fatal(err)
	}
	if left := listPerms(); len(left) != 0 {
		t.Errorf("shown again after the next report: %+v", left)
	}

	// The runner finishes it and reports.
	for _, st := range []model.TodoStatus{model.StatusInProgress, model.StatusReview} {
		_, err := raw(runner).UpdateTodo(copyOnRunner.ID, func(x *model.Todo) error {
			x.Status, x.Summary = st, "Added the about page."
			x.Worktree, x.SessionID = &model.Worktree{Branch: "dash/todo-x", Path: "/somewhere"}, "s1"
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := reporter.Send(ctx, reporter.Build(time.Now())); err != nil {
		t.Fatal(err)
	}
	got, _ := raw(hub).GetTodo(td.ID)
	if got.Status != model.StatusReview || got.Summary != "Added the about page." || got.Worktree == nil || got.Worktree.Path != "" {
		t.Fatalf("hub's task after the report: %+v", got)
	}
	if r, _ := rh.Get(creds.ID); r.Reported == nil || r.Reported.Parallel != 2 || r.Limit() != 2 {
		t.Errorf("runner's own limit not reported: %+v", r.Reported)
	}
	if r, _ := rh.Get(creds.ID); r.Reported == nil || r.Reported.Claude == nil || len(r.Reported.Claude.Warnings) != 1 ||
		!strings.Contains(r.Reported.Claude.Warnings[0], "total_cost_usd is missing") {
		t.Errorf("runner's Claude Code warnings not reported: %+v", r.Reported)
	}
	// The hub reads the task's log from the runner.
	w = httptest.NewRecorder()
	hub.mux().ServeHTTP(w, asOwner(t, hub, httptest.NewRequest("GET", "/api/todos/"+td.ID+"/events", nil)))
	if w.Code != http.StatusOK {
		t.Errorf("events from the runner: %d %s", w.Code, w.Body)
	}

	// Offline: a new task waits, saying so.
	stopRunner()
	<-stopped
	eventually(t, "the runner to go offline", func() bool { r, _ := rh.Get(creds.ID); return !r.Online })
	next, _ := raw(hub).CreateTodo(model.Todo{ProjectID: p.ID, Title: "next", Prompt: "next"})
	_, _ = raw(hub).SetStatus(next.ID, model.StatusQueued, "")
	engine.Kick()
	eventually(t, "the task to say it waits", func() bool {
		got, _ := raw(hub).GetTodo(next.ID)
		return strings.Contains(got.Waiting, "laptop is offline")
	})
	if got, _ := raw(hub).GetTodo(next.ID); got.Status != model.StatusQueued {
		t.Errorf("task while the runner is offline: %s", got.Status)
	}

	// Revoked: unlinked, its token stops working.
	if err := rh.Revoke(creds.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := raw(hub).GetProject(p.ID); len(got.Runners) != 0 {
		t.Errorf("revoked runner still linked: %v", got.Runners)
	}
	if err := tunnel.Report(ctx, creds, dispatch.Report{}); !errors.Is(err, tunnel.ErrRevoked) {
		t.Errorf("report with a revoked token: %v", err)
	}
}
