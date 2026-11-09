package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/tunnel"
)

// hubWithRunner is a hub serving over HTTP with one registered runner, a
// project linked to that runner and one that isn't, and the runner's Dash
// with its board calls for hub work going to that hub.
func hubWithRunner(t *testing.T) (hub, runner *Server, creds tunnel.Credentials, linked, unlinked model.Project) {
	t.Helper()
	hub, _, _ = boardServer(t)
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	srv := httptest.NewServer(hub.Routes())
	t.Cleanup(srv.Close)
	owner := testOwner(t, hub)
	code, _, err := rh.NewCode(owner)
	if err != nil {
		t.Fatal(err)
	}
	if creds, err = tunnel.Register(context.Background(), srv.URL, code, "laptop", ""); err != nil {
		t.Fatal(err)
	}
	if linked, err = raw(hub).CreateProject(model.Project{Name: "shared", OrgID: owner.Org, Runners: []string{creds.ID}}); err != nil {
		t.Fatal(err)
	}
	if unlinked, err = raw(hub).CreateProject(model.Project{Name: "elsewhere", OrgID: owner.Org}); err != nil {
		t.Fatal(err)
	}
	runner, _, _ = boardServer(t)
	runner.HubBoard = func(ctx context.Context, hubTodo, hubProject string, body []byte) (*http.Response, error) {
		return tunnel.Board(ctx, creds, hubTodo, hubProject, body)
	}
	return
}

// A chat the hub runs on a runner creates its tasks on the hub's board, in
// the hub's project: nothing lands on the runner's own board.
func TestRunnerChatBoardToolsActOnTheHub(t *testing.T) {
	hub, runner, _, linked, unlinked := hubWithRunner(t)
	h := runner.BoardMCP()
	runnerTodos := len(raw(runner).ListTodos(""))

	token, release := runner.Board.RegisterHub(chatCaller(t, runner), linked.ID)
	defer release()
	text, isErr := call(t, h, token, "create_todo", `{"title":"From the runner's chat","prompt":"do it"}`)
	if isErr {
		t.Fatalf("create_todo: %s", text)
	}
	got := raw(hub).ListTodos(linked.ID)
	if len(got) != 1 || got[0].Title != "From the runner's chat" || got[0].Status != model.StatusDraft {
		t.Fatalf("hub's tasks in the chat's project: %+v", got)
	}
	if n := len(raw(runner).ListTodos("")); n != runnerTodos {
		t.Errorf("the runner's own board got %d tasks", n-runnerTodos)
	}

	// The general chat sees the hub's projects.
	general, release2 := runner.Board.RegisterHub(chatCaller(t, runner), "")
	defer release2()
	if text, isErr := call(t, h, general, "list_projects", `{}`); isErr || !strings.Contains(text, "shared") {
		t.Errorf("list_projects from the hub's general chat: %s", text)
	}

	// A project not linked to this runner is refused.
	other, release3 := runner.Board.RegisterHub(chatCaller(t, runner), unlinked.ID)
	defer release3()
	if w := rpc(h, other, "tools/list", `{}`); w.Code != http.StatusForbidden {
		t.Errorf("chat in a project not linked to the runner: %d %s", w.Code, w.Body)
	}
}

// A hub task running on a runner reports issues to the hub's project,
// credited to the hub's task; a task the hub didn't send that runner is
// refused.
func TestRunnerTaskReportsIssuesToTheHub(t *testing.T) {
	hub, runner, creds, linked, _ := hubWithRunner(t)
	h := runner.BoardMCP()
	rp, err := raw(runner).CreateProject(model.Project{Name: "clone", HubProjectID: linked.ID, HubCheckout: model.HubCloned})
	if err != nil {
		t.Fatal(err)
	}
	sentHere := func(runnerID string) (hubTodo, local model.Todo) {
		hubTodo, _ = raw(hub).CreateTodo(model.Todo{ProjectID: linked.ID, Title: "t", Prompt: "p"})
		if _, err := raw(hub).UpdateTodo(hubTodo.ID, func(x *model.Todo) error { x.Runner = runnerID; return nil }); err != nil {
			t.Fatal(err)
		}
		local, err := raw(runner).CreateTodo(model.Todo{ProjectID: rp.ID, Title: "t", Prompt: "p", HubTodoID: hubTodo.ID})
		if err != nil {
			t.Fatal(err)
		}
		return hubTodo, local
	}

	hubTodo, local := sentHere(creds.ID)
	token, release := runner.Board.RegisterTask(chatCaller(t, runner), rp.ID, local.ID)
	defer release()
	if text, isErr := call(t, h, token, "report_issue", `{"title":"CLA fails on every PR","type":"bug"}`); isErr {
		t.Fatalf("report_issue: %s", text)
	}
	is := raw(hub).ListIssues(linked.ID)
	if len(is) != 1 || is[0].Title != "CLA fails on every PR" || is[0].Source != "agent:"+hubTodo.ID {
		t.Fatalf("hub's issues: %+v", is)
	}
	if n := len(raw(runner).ListIssues(rp.ID)); n != 0 {
		t.Errorf("the runner's own board got %d issues", n)
	}

	_, notSent := sentHere("another-runner")
	token2, release2 := runner.Board.RegisterTask(chatCaller(t, runner), rp.ID, notSent.ID)
	defer release2()
	if w := rpc(h, token2, "tools/list", `{}`); w.Code != http.StatusForbidden {
		t.Errorf("a task the hub sent another runner: %d %s", w.Code, w.Body)
	}

	// The runner's own tasks keep the runner's board.
	own, _ := raw(runner).CreateTodo(model.Todo{ProjectID: rp.ID, Title: "own", Prompt: "p"})
	token3, release3 := runner.Board.RegisterTask(chatCaller(t, runner), rp.ID, own.ID)
	defer release3()
	if text, isErr := call(t, h, token3, "report_issue", `{"title":"local"}`); isErr {
		t.Fatalf("report_issue of the runner's own task: %s", text)
	}
	if n := len(raw(runner).ListIssues(rp.ID)); n != 1 {
		t.Errorf("runner's own task's issue: %d issues here", n)
	}
}

// When the hub can't be reached, the hub's work fails visibly instead of
// writing to the runner's own board.
func TestRunnerHubBoardUnreachable(t *testing.T) {
	_, runner, _, linked, _ := hubWithRunner(t)
	runner.HubBoard = func(context.Context, string, string, []byte) (*http.Response, error) {
		return nil, errors.New("connection refused")
	}
	before := len(raw(runner).ListTodos(""))
	token, release := runner.Board.RegisterHub(chatCaller(t, runner), linked.ID)
	defer release()
	if w := rpc(runner.BoardMCP(), token, "tools/call", `{"name":"create_todo","arguments":{"title":"x","prompt":"y"}}`); w.Code != http.StatusBadGateway {
		t.Errorf("hub unreachable: %d %s", w.Code, w.Body)
	}
	runner.HubBoard = nil // not connected to a hub at all
	if w := rpc(runner.BoardMCP(), token, "tools/list", `{}`); w.Code != http.StatusBadGateway {
		t.Errorf("no hub: %d %s", w.Code, w.Body)
	}
	if n := len(raw(runner).ListTodos("")); n != before {
		t.Errorf("the runner's own board got %d tasks", n-before)
	}
}
