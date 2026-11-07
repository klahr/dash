package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/store"
)

type events struct {
	mu  sync.Mutex
	evs []model.Event
}

func (e *events) Publish(ev model.Event) { e.mu.Lock(); e.evs = append(e.evs, ev); e.mu.Unlock() }

func boardServer(t *testing.T) (*Server, *events, model.Project) {
	t.Helper()
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

func TestBoardMCPQueueTodoOnlyDrafts(t *testing.T) {
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
	if !isErr || !strings.Contains(text, "not a draft") {
		t.Errorf("queued again: %s", text)
	}
	failed, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "Broke"})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []model.TodoStatus{model.StatusQueued, model.StatusInProgress, model.StatusFailed} {
		if _, err := raw(s).SetStatus(failed.ID, st, ""); err != nil {
			t.Fatal(err)
		}
	}
	if text, isErr := callResult(t, rpc(h, token, "tools/call", `{"name":"queue_todo","arguments":{"todo":"`+failed.ID+`"}}`)); !isErr || !strings.Contains(text, "failed") {
		t.Errorf("queued a failed task: %s", text)
	}
	if text, isErr := callResult(t, rpc(h, token, "tools/call", `{"name":"queue_todo","arguments":{"todo":"nope"}}`)); !isErr {
		t.Errorf("queued a missing task: %s", text)
	}
}

func TestBoardMCPGeneralChatNeedsProject(t *testing.T) {
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
		Priority  int              `json:"priority"`
		DependsOn []string         `json:"depends_on"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != c.ID || got.Title != "C" || got.Status != model.StatusDraft || got.Priority != 3 || strings.Join(got.DependsOn, ",") != a.ID+","+b.ID {
		t.Errorf("update_todo %s", text)
	}
	if td, _ := raw(s).GetTodo(c.ID); td.Prompt != "C" || td.Priority != 3 || len(td.DependsOn) != 2 {
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
