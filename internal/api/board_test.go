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
	a, err := auth.New("u", "p")
	if err != nil {
		t.Fatal(err)
	}
	evs := &events{}
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"), evs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateProject(model.Project{Name: "other"}); err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProject(model.Project{Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st, Hub: hub.New(), Auth: a, Perms: perm.NewBroker(nil), Board: &mcptools.Tokens{},
		Todos: queue.NewTodoEngine(&queue.TodoEngine{}), Static: fstest.MapFS{"index.html": {Data: []byte("ok")}}}
	return s, evs, p
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

	token, release := s.Board.Register(p.ID)
	if w := rpc(h, token, "ping", "{}"); w.Code != http.StatusOK {
		t.Errorf("valid token: %d %s", w.Code, w.Body)
	}
	// Only the board tools: nothing that changes existing tasks.
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
	token, release := s.Board.Register(p.ID)
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

func TestBoardMCPGeneralChatNeedsProject(t *testing.T) {
	s, _, _ := boardServer(t)
	h := s.Routes()
	token, release := s.Board.Register("")
	defer release()
	if text, isErr := callResult(t, rpc(h, token, "tools/call", `{"name":"create_todo","arguments":{"title":"x"}}`)); !isErr {
		t.Errorf("created without a project: %s", text)
	}
	if text, _ := callResult(t, rpc(h, token, "tools/call", `{"name":"list_projects","arguments":{}}`)); !strings.Contains(text, `"web"`) {
		t.Errorf("list_projects %s", text)
	}
}
