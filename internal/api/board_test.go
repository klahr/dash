package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

type events struct {
	mu  sync.Mutex
	evs []model.Event
}

func (e *events) Publish(ev model.Event) { e.mu.Lock(); e.evs = append(e.evs, ev); e.mu.Unlock() }

// testMode records, per test, whether it runs in parallel ("parallel") or
// must not because it changes process-wide state ("serial", e.g. t.Setenv).
var testMode sync.Map // *testing.T → string

// serial keeps t out of the parallel tests; call it before boardServer.
func serial(t *testing.T) {
	testMode.Store(t, "serial")
	t.Cleanup(func() { testMode.Delete(t) })
}

// parallel runs t in parallel with the other tests that build a server
// (each has its own store and folders), unless it is serial or already
// parallel.
func parallel(t *testing.T) {
	if _, set := testMode.LoadOrStore(t, "parallel"); set {
		return
	}
	t.Cleanup(func() { testMode.Delete(t) })
	t.Parallel()
}

func boardServer(t *testing.T) (*Server, *events, model.Project) {
	t.Helper()
	parallel(t)
	evs := &events{}
	st, err := store.Open(filepath.Join(t.TempDir(), "dash.db"), evs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Accounts().Bootstrap("u", "p"); err != nil {
		t.Fatal(err)
	}
	a := auth.New(st.Accounts())
	a.NoFailDelayInTests()
	if _, err := st.CreateProject(model.Project{Name: "other"}); err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProject(model.Project{Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st, Hub: hub.New(), Auth: a, Perms: perm.NewBroker(nil), Board: &mcptools.Tokens{},
		Todos: queue.NewTodoEngine(&queue.TodoEngine{Log: &queue.TodoLog{Root: t.TempDir()}}), Static: fstest.MapFS{"index.html": {Data: []byte("ok")}}}
	return s, evs, p
}

// testOwner is s's first user in the default org, who owns its records.
func testOwner(t *testing.T, s *Server) model.Owner {
	t.Helper()
	u, err := s.Auth.Accounts().EnvUser()
	if err != nil {
		t.Fatal(err)
	}
	org, err := s.Auth.Accounts().DefaultOrg()
	if err != nil {
		t.Fatal(err)
	}
	return model.Owner{User: u.ID, Org: org.ID}
}

// rpc posts one JSON-RPC call to /mcp/dash through the full router.
func rpc(h http.Handler, token, method, params string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s}`, method, params)
	r := httptest.NewRequest("POST", "/mcp/dash", strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func callResult(t *testing.T, w *httptest.ResponseRecorder) (text string, isError bool) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct{ Text string }
		}
		Error *mcptools.Error
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Error != nil || len(resp.Result.Content) != 1 {
		t.Fatalf("response %s (%v)", w.Body, err)
	}
	return resp.Result.Content[0].Text, resp.Result.IsError
}

func TestBoardMCPAuth(t *testing.T) {
	parallel(t)

	s, _, p := boardServer(t)
	h := s.Routes()

	if w := rpc(h, "", "ping", "{}"); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", w.Code)
	}
	if w := rpc(h, "nope", "ping", "{}"); w.Code != http.StatusUnauthorized {
		t.Errorf("unknown token: %d", w.Code)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/mcp/dash", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", w.Code)
	}

	token, release := s.Board.Register(chatCaller(t, s), p.ID)
	if w := rpc(h, token, "ping", "{}"); w.Code != http.StatusOK {
		t.Errorf("valid token: %d %s", w.Code, w.Body)
	}
	// Only the board tools: no reviewing, approving or project settings.
	var list struct {
		Result struct{ Tools []struct{ Name string } }
	}
	_ = json.Unmarshal(rpc(h, token, "tools/list", "{}").Body.Bytes(), &list)
	var names []string
	for _, tl := range list.Result.Tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != strings.Join(mcptools.BoardTools, ",") {
		t.Errorf("tools %v", names)
	}
	release()
	if w := rpc(h, token, "ping", "{}"); w.Code != http.StatusUnauthorized {
		t.Errorf("released token: %d", w.Code)
	}
}

func TestBoardMCPCreateTodoDefaultsToChatProject(t *testing.T) {
	parallel(t)

	s, evs, p := boardServer(t)
	h := s.Routes()
	token, release := s.Board.Register(chatCaller(t, s), p.ID)
	defer release()

	text, isErr := callResult(t, rpc(h, token, "tools/call", `{"name":"create_todo","arguments":{"title":"Fix login"}}`))
	if isErr {
		t.Fatal(text)
	}
	var created model.Todo
	if err := json.Unmarshal([]byte(text), &created); err != nil {
		t.Fatal(err)
	}
	if created.ProjectID != p.ID || created.Status != model.StatusDraft || created.Title != "Fix login" {
		t.Fatalf("created %+v", created)
	}
	// The board learns about it the same way as from POST /api/projects/{pid}/todos.
	evs.mu.Lock()
	last := evs.evs[len(evs.evs)-1]
	evs.mu.Unlock()
	if last.Type != model.EvTodoUpdated || last.ProjectID != p.ID || last.TodoID != created.ID {
		t.Errorf("event %+v", last)
	}

	// Queued only when asked; another project by name.
	text, isErr = callResult(t, rpc(h, token, "tools/call", `{"name":"create_todo","arguments":{"title":"Run it","draft":false,"project":"other"}}`))
	if isErr {
		t.Fatal(text)
	}
	if err := json.Unmarshal([]byte(text), &created); err != nil {
		t.Fatal(err)
	}
	if created.ProjectID == p.ID || created.Status != model.StatusQueued {
		t.Errorf("created %+v", created)
	}

	text, _ = callResult(t, rpc(h, token, "tools/call", `{"name":"list_todos","arguments":{}}`))
	if !strings.Contains(text, "Fix login") || strings.Contains(text, "Run it") {
		t.Errorf("list_todos %s", text)
	}
}

func TestBoardMCPQueueTodo(t *testing.T) {
	parallel(t)

	s, _, p := boardServer(t)
	h := s.Routes()
	token, release := s.Board.Register(chatCaller(t, s), p.ID)
	defer release()
	draft, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "Fix login"})
	if err != nil {
		t.Fatal(err)
	}

	text, isErr := callResult(t, rpc(h, token, "tools/call", `{"name":"queue_todo","arguments":{"todo":"`+draft.ID+`"}}`))
	if isErr {
		t.Fatal(text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != draft.ID || got["title"] != "Fix login" || got["status"] != string(model.StatusQueued) {
		t.Errorf("queue_todo %s", text)
	}
	if td, _ := raw(s).GetTodo(draft.ID); td.Status != model.StatusQueued {
		t.Errorf("status %s", td.Status)
	}

	// Already queued: refused, not re-queued.
	text, isErr = callResult(t, rpc(h, token, "tools/call", `{"name":"queue_todo","arguments":{"todo":"`+draft.ID+`"}}`))
	if !isErr || !strings.Contains(text, "only draft and failed tasks") {
		t.Errorf("queued again: %s", text)
	}

	// A failed task: retried in its session (like the drag from Review to
	// Queued), or from scratch with fresh.
	failed := withStatus(t, s, p.ID, model.StatusFailed)
	if text, isErr := call(t, h, token, "queue_todo", `{"todo":"`+failed.ID+`"}`); isErr {
		t.Fatalf("queue a failed task: %s", text)
	}
	if td, _ := raw(s).GetTodo(failed.ID); td.Status != model.StatusQueued || td.SessionID != "s1" || td.Error != "" {
		t.Errorf("retried %+v", td)
	}
	failed = withStatus(t, s, p.ID, model.StatusFailed)
	if text, isErr := call(t, h, token, "queue_todo", `{"todo":"`+failed.ID+`","fresh":true}`); isErr {
		t.Fatalf("queue a failed task fresh: %s", text)
	}
	if td, _ := raw(s).GetTodo(failed.ID); td.Status != model.StatusQueued || td.SessionID != "" || td.FeedbackSent != 0 || len(td.Feedback) != 0 {
		t.Errorf("started over %+v", td)
	}

	// In review, not failed: refused.
	review := withStatus(t, s, p.ID, model.StatusReview)
	if text, isErr := call(t, h, token, "queue_todo", `{"todo":"`+review.ID+`"}`); !isErr || !strings.Contains(text, "review") {
		t.Errorf("queued a task in review: %s", text)
	}
	if td, _ := raw(s).GetTodo(review.ID); td.Status != model.StatusReview {
		t.Errorf("task in review moved to %s", td.Status)
	}
	if text, isErr := callResult(t, rpc(h, token, "tools/call", `{"name":"queue_todo","arguments":{"todo":"nope"}}`)); !isErr {
		t.Errorf("queued a missing task: %s", text)
	}
}

// withStatus is a task of project pid that ran once (a Claude session, a
// note Claude saw) and is now in status st.
func withStatus(t *testing.T, s *Server, pid string, st model.TodoStatus) model.Todo {
	t.Helper()
	td, err := raw(s).CreateTodo(model.Todo{ProjectID: pid, Title: "Broke", Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	path := []model.TodoStatus{model.StatusQueued, model.StatusInProgress, st}
	if st == model.StatusQueued {
		path = path[:1]
	}
	for _, x := range path {
		if _, err := raw(s).SetStatus(td.ID, x, ""); err != nil {
			t.Fatal(err)
		}
	}
	td, err = raw(s).UpdateTodo(td.ID, func(x *model.Todo) error {
		x.SessionID, x.Feedback, x.FeedbackSent = "s1", []string{"note"}, 1
		if st == model.StatusFailed {
			x.Error = "boom"
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return td
}

func TestBoardMCPGeneralChatNeedsProject(t *testing.T) {
	parallel(t)

	s, _, _ := boardServer(t)
	h := s.Routes()
	token, release := s.Board.Register(chatCaller(t, s), "")
	defer release()
	if text, isErr := callResult(t, rpc(h, token, "tools/call", `{"name":"create_todo","arguments":{"title":"x"}}`)); !isErr {
		t.Errorf("created without a project: %s", text)
	}
	if text, _ := callResult(t, rpc(h, token, "tools/call", `{"name":"list_projects","arguments":{}}`)); !strings.Contains(text, `"web"`) {
		t.Errorf("list_projects %s", text)
	}
}

func TestListProjectsActive(t *testing.T) {
	parallel(t)

	s, _, p := boardServer(t)
	run, _ := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "a"})
	draft, _ := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "b"})
	for _, st := range []model.TodoStatus{model.StatusQueued, model.StatusInProgress} {
		if _, err := raw(s).SetStatus(run.ID, st, ""); err != nil {
			t.Fatal(err)
		}
	}
	w := httptest.NewRecorder()
	s.listProjects(w, asOwner(t, s, httptest.NewRequest("GET", "/api/projects", nil)))
	var got []struct {
		ID     string                      `json:"id"`
		Active map[string]model.TodoStatus `json:"active"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, g := range got {
		if g.ID != p.ID {
			if len(g.Active) != 0 {
				t.Errorf("other project active: %v", g.Active)
			}
			continue
		}
		if len(g.Active) != 1 || g.Active[run.ID] != model.StatusInProgress || g.Active[draft.ID] != "" {
			t.Errorf("active = %v", g.Active)
		}
	}
}

// call runs one board tool and returns its text and whether it failed.
func call(t *testing.T, h http.Handler, token, name, args string) (string, bool) {
	t.Helper()
	return callResult(t, rpc(h, token, "tools/call", `{"name":"`+name+`","arguments":`+args+`}`))
}

func TestBoardMCPUpdateTodo(t *testing.T) {
	parallel(t)

	s, _, p := boardServer(t)
	h := s.Routes()
	token, release := s.Board.Register(chatCaller(t, s), p.ID)
	defer release()
	mk := func(projectID, title string) model.Todo {
		t.Helper()
		td, err := raw(s).CreateTodo(model.Todo{ProjectID: projectID, Title: title, Prompt: title})
		if err != nil {
			t.Fatal(err)
		}
		return td
	}
	a, b, c := mk(p.ID, "A"), mk(p.ID, "B"), mk(p.ID, "C")
	other, _ := board{s.as(ownerCaller(t, s))}.Project("other")
	elsewhere := mk(other.ID, "Elsewhere")

	// Set dependencies and priority; title and prompt stay.
	text, isErr := call(t, h, token, "update_todo", `{"todo":"`+c.ID+`","depends_on":["`+a.ID+`","`+b.ID+`"],"priority":3}`)
	if isErr {
		t.Fatal(text)
	}
	var got struct {
		ID        string           `json:"id"`
		Title     string           `json:"title"`
		Status    model.TodoStatus `json:"status"`
		Priority  *int             `json:"priority"`
		DependsOn []string         `json:"depends_on"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != c.ID || got.Title != "C" || got.Status != model.StatusDraft || got.Priority == nil || *got.Priority != 3 || strings.Join(got.DependsOn, ",") != a.ID+","+b.ID {
		t.Errorf("update_todo %s", text)
	}
	if td, _ := raw(s).GetTodo(c.ID); td.Prompt != "C" || td.Priority == nil || *td.Priority != 3 || len(td.DependsOn) != 2 {
		t.Errorf("stored %+v", td)
	}

	// Leaving depends_on out keeps it; an empty array clears it.
	if text, isErr := call(t, h, token, "update_todo", `{"todo":"`+c.ID+`","title":"C2","prompt":"do C"}`); isErr {
		t.Fatal(text)
	}
	if td, _ := raw(s).GetTodo(c.ID); td.Title != "C2" || td.Prompt != "do C" || len(td.DependsOn) != 2 {
		t.Errorf("after title/prompt %+v", td)
	}
	if text, isErr := call(t, h, token, "update_todo", `{"todo":"`+c.ID+`","depends_on":[]}`); isErr || !strings.Contains(strings.Join(strings.Fields(text), ""), `"depends_on":[]`) {
		t.Fatalf("clear deps: %s", text)
	}
	if td, _ := raw(s).GetTodo(c.ID); len(td.DependsOn) != 0 {
		t.Errorf("deps not cleared: %v", td.DependsOn)
	}

	// Leaving priority out keeps it; clear_priority or null unsets it, and
	// 0 is a real priority.
	if td, _ := raw(s).GetTodo(c.ID); td.Priority == nil || *td.Priority != 3 {
		t.Errorf("priority lost by an update without it: %v", td.Priority)
	}
	if text, isErr := call(t, h, token, "update_todo", `{"todo":"`+c.ID+`","clear_priority":true}`); isErr || !strings.Contains(strings.Join(strings.Fields(text), ""), `"priority":null`) {
		t.Fatalf("clear_priority: %s", text)
	}
	if td, _ := raw(s).GetTodo(c.ID); td.Priority != nil {
		t.Errorf("priority not cleared: %d", *td.Priority)
	}
	if text, isErr := call(t, h, token, "update_todo", `{"todo":"`+c.ID+`","priority":0}`); isErr {
		t.Fatal(text)
	}
	if td, _ := raw(s).GetTodo(c.ID); td.Priority == nil || *td.Priority != 0 {
		t.Errorf("priority 0 not set: %v", td.Priority)
	}
	if text, isErr := call(t, h, token, "update_todo", `{"todo":"`+c.ID+`","priority":null}`); isErr {
		t.Fatal(text)
	}
	if td, _ := raw(s).GetTodo(c.ID); td.Priority != nil {
		t.Errorf("priority null did not clear: %d", *td.Priority)
	}
	if text, isErr := call(t, h, token, "update_todo", `{"todo":"`+c.ID+`","priority":2,"clear_priority":true}`); !isErr {
		t.Errorf("priority and clear_priority together accepted: %s", text)
	}

	// The store's rules: no cycles, no tasks in other projects.
	if text, isErr := call(t, h, token, "update_todo", `{"todo":"`+a.ID+`","depends_on":["`+b.ID+`"]}`); isErr {
		t.Fatal(text)
	}
	if text, isErr := call(t, h, token, "update_todo", `{"todo":"`+b.ID+`","depends_on":["`+a.ID+`"]}`); !isErr || !strings.Contains(text, "cycle") {
		t.Errorf("cycle accepted: %s", text)
	}
	if text, isErr := call(t, h, token, "update_todo", `{"todo":"`+a.ID+`","depends_on":["`+elsewhere.ID+`"]}`); !isErr {
		t.Errorf("other project's task accepted: %s", text)
	}
	if text, isErr := call(t, h, token, "update_todo", `{"todo":"`+a.ID+`"}`); !isErr {
		t.Errorf("empty update accepted: %s", text)
	}

	// A running task's prompt is fixed; its priority isn't.
	for _, st := range []model.TodoStatus{model.StatusQueued, model.StatusInProgress} {
		if _, err := raw(s).SetStatus(b.ID, st, ""); err != nil {
			t.Fatal(err)
		}
	}
	if text, isErr := call(t, h, token, "update_todo", `{"todo":"`+b.ID+`","prompt":"other"}`); !isErr || !strings.Contains(text, "draft, queued or failed") {
		t.Errorf("prompt of a running task changed: %s", text)
	}
	if td, _ := raw(s).GetTodo(b.ID); td.Prompt != "B" {
		t.Errorf("prompt %q", td.Prompt)
	}
	if text, isErr := call(t, h, token, "update_todo", `{"todo":"`+b.ID+`","priority":-1}`); isErr {
		t.Errorf("priority of a running task: %s", text)
	}
}

func TestBoardMCPDeleteTodo(t *testing.T) {
	parallel(t)

	s, _, p := boardServer(t)
	h := s.Routes()
	token, release := s.Board.Register(chatCaller(t, s), p.ID)
	defer release()
	draft, _ := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "Oops"})
	running, _ := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "Busy"})
	for _, st := range []model.TodoStatus{model.StatusQueued, model.StatusInProgress} {
		if _, err := raw(s).SetStatus(running.ID, st, ""); err != nil {
			t.Fatal(err)
		}
	}

	if text, isErr := call(t, h, token, "delete_todo", `{"todo":"`+draft.ID+`"}`); isErr {
		t.Fatal(text)
	}
	if _, err := raw(s).GetTodo(draft.ID); err == nil {
		t.Error("draft still there")
	}
	if text, isErr := call(t, h, token, "delete_todo", `{"todo":"`+running.ID+`"}`); !isErr || !strings.Contains(text, "in_progress") {
		t.Errorf("deleted a running task: %s", text)
	}
	if _, err := raw(s).GetTodo(running.ID); err != nil {
		t.Error("running task deleted")
	}
	if text, isErr := call(t, h, token, "delete_todo", `{"todo":"nope"}`); !isErr {
		t.Errorf("deleted a missing task: %s", text)
	}
}

// blockExec is Claude working until it is stopped; each run is sent on runs.
type blockExec struct{ runs chan agent.ExecRequest }

func (e blockExec) Run(ctx context.Context, req agent.ExecRequest, emit func(model.RunEvent)) (agent.ExecResult, error) {
	e.runs <- req
	<-ctx.Done()
	return agent.ExecResult{SessionID: "s-" + req.Resume + "1"}, ctx.Err()
}

// The chat stops a running task the way Cancel does, then reruns it with a
// new prompt: in its session, which gets the prompt as a note, or from
// scratch in a new worktree with fresh.
func TestBoardMCPStopAndRestartARunningTask(t *testing.T) {
	parallel(t)

	s, _, _ := boardServer(t)
	s.Git = gitops.New(s.Hub)
	repo := filepath.Join(t.TempDir(), "app")
	if err := s.Git.InitRepo(context.Background(), "app", repo, "main"); err != nil {
		t.Fatal(err)
	}
	p, err := raw(s).CreateProject(model.Project{Name: "app", Path: repo, DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	runs := make(chan agent.ExecRequest, 4)
	s.Todos = queue.NewTodoEngine(&queue.TodoEngine{Store: raw(s), Git: s.Git, Exec: blockExec{runs}, Pub: s.Hub,
		Log: &queue.TodoLog{Pub: s.Hub, Root: t.TempDir()}, WorktreeRoot: t.TempDir(), MaxParallel: 1})
	ctx, cancel := context.WithCancel(context.Background())
	go s.Todos.Run(ctx)
	t.Cleanup(func() { cancel(); s.Todos.Wait(10 * time.Second) })
	h := s.Routes()
	token, release := s.Board.Register(chatCaller(t, s), p.ID)
	defer release()

	td, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "Page", Prompt: "add a page"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw(s).SetStatus(td.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	s.Todos.Kick()
	running := func() agent.ExecRequest {
		t.Helper()
		select {
		case req := <-runs:
			return req
		case <-time.After(10 * time.Second):
			t.Fatal("the task didn't start")
		}
		return agent.ExecRequest{}
	}
	first := running()

	text, isErr := call(t, h, token, "stop_todo", `{"todo":"`+td.ID+`"}`)
	if isErr || !strings.Contains(text, `"failed"`) || !strings.Contains(text, "canceled") {
		t.Fatalf("stop_todo: %s", text)
	}
	got, _ := raw(s).GetTodo(td.ID)
	if got.Worktree == nil || got.SessionID != "s-1" {
		t.Fatalf("stopped task lost its work: %+v", got)
	}

	// Without fresh: the same session, which learns the new prompt.
	if text, isErr := call(t, h, token, "restart_todo", `{"todo":"`+td.ID+`","prompt":"add an about page"}`); isErr {
		t.Fatalf("restart_todo: %s", text)
	}
	again := running()
	if again.Resume != "s-1" || !strings.Contains(again.Prompt, "add an about page") || again.Dir != first.Dir {
		t.Errorf("continued run %+v", again)
	}
	if got, _ := raw(s).GetTodo(td.ID); got.Prompt != "add an about page" || got.Status != model.StatusInProgress {
		t.Errorf("restarted %+v", got)
	}

	// fresh: stopped while running, then a new worktree and session.
	if err := os.WriteFile(filepath.Join(first.Dir, "first.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if text, isErr := call(t, h, token, "restart_todo", `{"todo":"`+td.ID+`","prompt":"add a contact page","fresh":true}`); isErr {
		t.Fatalf("restart_todo fresh: %s", text)
	}
	over := running()
	if over.Resume != "" || !strings.Contains(over.Prompt, "add a contact page") || strings.Contains(over.Prompt, "about") {
		t.Errorf("fresh run %+v", over)
	}
	if _, err := os.Stat(filepath.Join(over.Dir, "first.txt")); !os.IsNotExist(err) {
		t.Errorf("fresh run reused the old worktree: %v", err)
	}
}

// stop_todo and restart_todo refuse tasks that aren't running, queued or
// (restart_todo) failed, and leave them as they are.
func TestBoardMCPStopRestartRefusals(t *testing.T) {
	parallel(t)

	s, _, p := boardServer(t)
	h := s.Routes()
	token, release := s.Board.Register(chatCaller(t, s), p.ID)
	defer release()
	merged := withStatus(t, s, p.ID, model.StatusReview)
	if _, err := raw(s).SetStatus(merged.ID, model.StatusMerged, ""); err != nil {
		t.Fatal(err)
	}
	review := withStatus(t, s, p.ID, model.StatusReview)
	failed := withStatus(t, s, p.ID, model.StatusFailed)
	draft, _ := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "d"})
	for _, c := range []struct{ tool, id, want string }{
		{"stop_todo", merged.ID, "merged"},
		{"stop_todo", review.ID, "review"},
		{"stop_todo", failed.ID, "failed"},
		{"restart_todo", merged.ID, "merged"},
		{"restart_todo", review.ID, "review"},
		{"restart_todo", draft.ID, "draft"},
		{"stop_todo", "nope", "nope"},
	} {
		before, _ := raw(s).GetTodo(c.id)
		if text, isErr := call(t, h, token, c.tool, `{"todo":"`+c.id+`","prompt":"x"}`); !isErr || !strings.Contains(text, c.want) {
			t.Errorf("%s of a %s task: %s", c.tool, before.Status, text)
		}
		if after, _ := raw(s).GetTodo(c.id); after.Status != before.Status || after.Prompt != before.Prompt {
			t.Errorf("%s changed a %s task: %+v", c.tool, before.Status, after)
		}
	}

	// A queued task stops back into draft, like Cancel; restarting a failed
	// one changes its prompt and queues it.
	queued := withStatus(t, s, p.ID, model.StatusQueued)
	if text, isErr := call(t, h, token, "stop_todo", `{"todo":"`+queued.ID+`"}`); isErr || !strings.Contains(text, `"draft"`) {
		t.Errorf("stop a queued task: %s", text)
	}
	if text, isErr := call(t, h, token, "restart_todo", `{"todo":"`+failed.ID+`","prompt":"again","title":"Retry"}`); isErr {
		t.Fatalf("restart a failed task: %s", text)
	}
	if got, _ := raw(s).GetTodo(failed.ID); got.Status != model.StatusQueued || got.Prompt != "again" || got.Title != "Retry" ||
		got.SessionID != "s1" || len(got.Feedback) != 2 || !strings.Contains(got.Feedback[1], "again") {
		t.Errorf("restarted %+v", got)
	}
}

// hubFleet stands in for a hub's runners: a cancel is reported back as
// the runner would, failed ("canceled").
type hubFleet struct {
	hub   *Server
	mu    sync.Mutex
	calls []string
}

func (f *hubFleet) Get(string) (tunnel.Runner, bool) { return tunnel.Runner{}, false }

func (f *hubFleet) Call(_ context.Context, id, method, path string, in, out any) error {
	f.mu.Lock()
	f.calls = append(f.calls, method+" "+path)
	f.mu.Unlock()
	if tid, ok := strings.CutSuffix(strings.TrimPrefix(path, "/api/hub/tasks/"), "/cancel"); ok {
		_, err := raw(f.hub).SetStatus(tid, model.StatusFailed, "canceled")
		return err
	}
	return nil
}

// A chat the hub runs on a runner stops and restarts the hub's tasks on
// their runners, through the hub's board.
func TestRunnerChatStopsAndRestartsHubTasks(t *testing.T) {
	parallel(t)

	hub, runner, creds, linked, _ := hubWithRunner(t)
	fleet := &hubFleet{hub: hub}
	hub.Dispatch = &dispatch.Dispatcher{Store: raw(hub), Fleet: fleet}
	h := runner.BoardMCP()
	token, release := runner.Board.RegisterHub(chatCaller(t, runner), linked.ID)
	defer release()

	td := withStatus(t, hub, linked.ID, model.StatusInProgress)
	if _, err := raw(hub).UpdateTodo(td.ID, func(x *model.Todo) error { x.Runner, x.RemoteID = creds.ID, "r1"; return nil }); err != nil {
		t.Fatal(err)
	}
	if text, isErr := call(t, h, token, "stop_todo", `{"todo":"`+td.ID+`"}`); isErr || !strings.Contains(text, "canceled") {
		t.Fatalf("stop_todo: %s", text)
	}
	if got, _ := raw(hub).GetTodo(td.ID); got.Status != model.StatusFailed || got.Runner != creds.ID {
		t.Errorf("stopped %+v", got)
	}

	// Retried on its runner, where its session is.
	if text, isErr := call(t, h, token, "queue_todo", `{"todo":"`+td.ID+`"}`); isErr {
		t.Fatalf("queue_todo: %s", text)
	}
	if got, _ := raw(hub).GetTodo(td.ID); got.Status != model.StatusQueued || got.Runner != creds.ID {
		t.Errorf("retried %+v", got)
	}

	// Restarted fresh: the runner forgets its copy, and the task starts
	// over wherever it is sent next.
	if _, err := raw(hub).SetStatus(td.ID, model.StatusInProgress, ""); err != nil {
		t.Fatal(err)
	}
	if text, isErr := call(t, h, token, "restart_todo", `{"todo":"`+td.ID+`","prompt":"v2","fresh":true}`); isErr {
		t.Fatalf("restart_todo: %s", text)
	}
	got, _ := raw(hub).GetTodo(td.ID)
	if got.Status != model.StatusQueued || got.Prompt != "v2" || got.Runner != "" || got.RemoteID != "" {
		t.Errorf("restarted %+v", got)
	}
	fleet.mu.Lock()
	defer fleet.mu.Unlock()
	want := []string{"POST /api/hub/tasks/" + td.ID + "/cancel", "POST /api/hub/tasks/" + td.ID + "/cancel", "DELETE /api/hub/tasks/" + td.ID}
	if strings.Join(fleet.calls, ",") != strings.Join(want, ",") {
		t.Errorf("runner calls %v", fleet.calls)
	}
	if n := len(raw(runner).ListTodos("")); n != 0 {
		t.Errorf("the runner's own board got %d tasks", n)
	}
}
