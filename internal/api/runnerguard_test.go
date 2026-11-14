package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/runner"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// runnerServer is a Dash running as a runner, as its hub reaches it.
func runnerServer(t *testing.T) (*Server, http.Handler, model.Project) {
	t.Helper()
	s, _, p := boardServer(t)
	s.ProjectRoot = t.TempDir()
	s.Git = gitops.New(nil)
	s.Procs = runner.NewManager(hub.New())
	t.Cleanup(s.Procs.StopAll)
	p, err := raw(s).UpdateProject(p.ID, func(p *model.Project) error {
		p.Path, p.TestCommand = t.TempDir(), "go test ./..."
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, s.RunnerRoutes(), p
}

func hubCall(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestRunnerOffersTheHubOnlyItsProjects(t *testing.T) {
	parallel(t)

	_, h, p := runnerServer(t)
	if w := hubCall(h, "GET", "/api/projects", ""); w.Code != http.StatusOK {
		t.Fatalf("list projects (no login needed over the tunnel): %d %s", w.Code, w.Body)
	}
	if w := hubCall(h, "GET", "/api/projects/"+p.ID+"/todos", ""); w.Code != http.StatusOK {
		t.Fatalf("list todos: %d %s", w.Code, w.Body)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/machines"},
		{"GET", "/peer/other/api/projects"},
		{"GET", "/api/connector"},
		{"POST", "/mcp/dash"},
		{"POST", "/mcp/permissions"},
		{"GET", "/"},
		{"GET", "/index.html"},
		{"POST", "/api/login"},
		{"GET", "/api/chat"},
		{"GET", "/api/runners"},
		{"GET", "/peer%2Fother/api/projects"},
	} {
		if w := hubCall(h, c.method, c.path, ""); w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d, want refused", c.method, c.path, w.Code)
		}
	}
}

// A runner takes the hub's commands, but opens terminals only for its owner
// and adds projects only inside its projects root.
func TestRunnerGuardsTheHubsRequests(t *testing.T) {
	parallel(t)

	s, h, p := runnerServer(t)
	procs := "/api/projects/" + p.ID + "/processes"

	if w := hubCall(h, "POST", procs, `{"kind":"pty"}`); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "register it again") {
		t.Fatalf("terminal: %d %s", w.Code, w.Body)
	}
	if w := hubCall(h, "POST", "/api/projects", `{"name":"x","path":"/etc"}`); w.Code != http.StatusForbidden {
		t.Fatalf("adopting a directory outside the projects root: %d %s", w.Code, w.Body)
	}
	if w := hubCall(h, "POST", "/api/projects", `{"name":"x","path":"`+filepath.Join(s.ProjectRoot, "..", "escape")+`"}`); w.Code != http.StatusForbidden {
		t.Fatalf("adopting via ..: %d %s", w.Code, w.Body)
	}

	// Terminals started on the runner itself stay there.
	pty, err := s.Procs.StartPTY(p.ID, p.Path, "sleep 30", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if w := hubCall(h, "GET", "/ws/proc/"+pty.ID, ""); w.Code != http.StatusForbidden {
		t.Fatalf("attaching to a terminal: %d %s", w.Code, w.Body)
	}

	// The hub sets the project's commands.
	if w := hubCall(h, "PATCH", "/api/projects/"+p.ID, `{"test_command":"make test"}`); w.Code != http.StatusOK {
		t.Fatalf("changing a command: %d %s", w.Code, w.Body)
	}
}

// A runner opens terminals for the hub, but only for the user who
// registered it (tunnel.UserHeader, which the hub sets and strips
// from browsers' requests), and only in its project's checkout or a task's
// worktree: the hub doesn't choose the folder.
func TestRunnerOpensTerminalsForItsOwnerOnly(t *testing.T) {
	parallel(t)

	s, h, p := runnerServer(t)
	procs := "/api/projects/" + p.ID + "/processes"
	as := func(user, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if user != "" {
			r.Header.Set(tunnel.UserHeader, user)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if w := as("42", "POST", procs, `{"kind":"pty"}`); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "register it again") {
		t.Fatalf("terminal on a runner that doesn't know its owner: %d %s", w.Code, w.Body)
	}
	s.HubUser = 42
	for _, user := range []string{"", "7"} {
		if w := as(user, "POST", procs, `{"kind":"pty"}`); w.Code != http.StatusForbidden {
			t.Fatalf("terminal for user %q: %d %s", user, w.Code, w.Body)
		}
	}
	w := as("42", "POST", procs, `{"kind":"pty","command":"sleep 30","dir":"/etc","path":"/etc"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("terminal for the owner: %d %s", w.Code, w.Body)
	}
	var proc model.Process
	if err := json.Unmarshal(w.Body.Bytes(), &proc); err != nil {
		t.Fatal(err)
	}
	if proc.Dir != p.Path || proc.Kind != model.ProcPTY {
		t.Fatalf("terminal: %+v, want it in %s", proc, p.Path)
	}

	// Attaching: the owner only.
	srv := httptest.NewServer(h)
	defer srv.Close()
	for user, ok := range map[string]bool{"": false, "7": false, "42": true} {
		hdr := http.Header{}
		if user != "" {
			hdr.Set(tunnel.UserHeader, user)
		}
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/proc/"+proc.ID, hdr)
		if (err == nil) != ok {
			t.Errorf("attaching as %q: %v", user, err)
		}
		if err == nil {
			ws.Close()
		}
	}
}

func TestRunnerSkipsThePasswordTheHubAlreadyAsked(t *testing.T) {
	parallel(t)

	// A missing project is reported after the password check: 404 means
	// the check passed, 403 that it wanted a password.
	_, h, _ := runnerServer(t)
	if w := hubCall(h, "DELETE", "/api/projects/nope", `{}`); w.Code != http.StatusNotFound {
		t.Fatalf("delete from the hub: %d %s", w.Code, w.Body)
	}
	s, _, _ := boardServer(t)
	w := httptest.NewRecorder()
	s.deleteProject(w, asOwner(t, s, httptest.NewRequest("DELETE", "/api/projects/nope", strings.NewReader(`{}`))))
	if w.Code != http.StatusForbidden {
		t.Fatalf("local delete without password: %d %s", w.Code, w.Body)
	}
}

// A runner runs the general chat only for its owner, asked through the
// hub: never for anyone else, nor when the hub doesn't say whom it is for.
// It runs in the runner's scratch directory, and the hub keeps the
// conversation.
func TestRunnerRunsTheGeneralChatOnlyForItsOwner(t *testing.T) {
	parallel(t)

	s, h, _ := runnerServer(t)
	s.HubUser = 42
	s.ChatDir = filepath.Join(t.TempDir(), "chat")
	chat := &echoChat{}
	s.Chat = chat
	srv := httptest.NewServer(h)
	defer srv.Close()
	ask := func(user string) (kinds []string, text string) {
		hdr := http.Header{}
		if user != "" {
			hdr.Set(tunnel.UserHeader, user)
		}
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat", hdr)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.Close()
		if err := ws.WriteJSON(map[string]any{"type": "send", "messages": []map[string]string{{"role": "user", "content": "run ls ~"}}}); err != nil {
			t.Fatal(err)
		}
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			var d struct{ Kind, Text string }
			if err := ws.ReadJSON(&d); err != nil {
				t.Fatal(err)
			}
			kinds, text = append(kinds, d.Kind), text+d.Text
			if d.Kind == "done" || d.Kind == "error" {
				return kinds, text
			}
		}
	}
	for _, user := range []string{"", "7"} {
		if kinds, text := ask(user); kinds[len(kinds)-1] != "error" || !strings.Contains(text, "owner") && !strings.Contains(text, "registered it") {
			t.Fatalf("general chat for user %q: %v %q", user, kinds, text)
		}
	}
	if chat.calls.Load() != 0 {
		t.Fatal("Claude ran for someone other than the runner's owner")
	}
	if kinds, text := ask("42"); kinds[len(kinds)-1] != "done" || text != "hi" {
		t.Fatalf("owner's general chat: %v %q", kinds, text)
	}
	if got := chat.dir(); got != s.ChatDir {
		t.Errorf("ran in %q, want the scratch dir %q", got, s.ChatDir)
	}
	if got := raw(s).Chat(ownerCaller(t, s).Org, ownerCaller(t, s).User); len(got) != 0 {
		t.Errorf("the runner saved the hub's conversation: %+v", got)
	}
}

// runnerChatTurn sends one question to h's /ws/chat as the hub would for
// user, with fields, and returns the kinds and texts of what came back.
func runnerChatTurn(t *testing.T, srv *httptest.Server, user string, fields map[string]any) (kinds, texts []string) {
	t.Helper()
	hdr := http.Header{}
	hdr.Set(tunnel.UserHeader, user)
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat", hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	req := map[string]any{"type": "send", "messages": []map[string]string{{"role": "user", "content": "what's up?"}}}
	for k, v := range fields {
		req[k] = v
	}
	if err := ws.WriteJSON(req); err != nil {
		t.Fatal(err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var d struct{ Kind, Text string }
		if err := ws.ReadJSON(&d); err != nil {
			t.Fatal(err)
		}
		kinds, texts = append(kinds, d.Kind), append(texts, d.Text)
		if d.Kind == "done" || d.Kind == "error" {
			return kinds, texts
		}
	}
}

// The chat of one of the runner's own projects (made or added on it, which
// the hub keeps no record of), opened through the hub: it runs in the
// project's folder for the runner's owner only, and the runner keeps the
// conversation, with its owner as the author, as the hub's UI reads it from
// the runner. So do its own tasks' chats, in their worktrees.
func TestRunnerChatsInItsOwnProjectThroughTheHub(t *testing.T) {
	parallel(t)

	s, h, p := runnerServer(t)
	s.HubUser = 42
	seen := &seenChat{}
	s.Chat = seen
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Someone else: refused before anything runs.
	if kinds, texts := runnerChatTurn(t, srv, "7", map[string]any{"project": p.ID}); kinds[len(kinds)-1] != "error" || !strings.Contains(texts[len(texts)-1], "registered it") {
		t.Fatalf("another user's chat in the runner's project: %v %q", kinds, texts)
	}
	if dir, _ := seen.seen(); dir != "" {
		t.Fatal("Claude ran for someone other than the runner's owner")
	}

	kinds, texts := runnerChatTurn(t, srv, "42", map[string]any{"project": p.ID})
	if kinds[len(kinds)-1] != "done" {
		t.Fatalf("owner's chat in the runner's project: %v %q", kinds, texts)
	}
	if dir, _ := seen.seen(); dir != p.Path {
		t.Errorf("ran in %q, want the project's folder %q", dir, p.Path)
	}
	got := raw(s).ProjectChat(p.ID, 42)
	if len(got) != 2 || got[0].CreatedBy != 42 || got[1].Content != "hi" {
		t.Fatalf("saved project chat: %+v", got)
	}

	// Its own task: in the task's worktree, with the task's context.
	wt := t.TempDir()
	td, err := raw(s).CreateTodo(model.Todo{ID: store.NewID(), ProjectID: p.ID, Title: "Dark mode", Prompt: "Add a dark mode",
		Status: model.StatusReview, Worktree: &model.Worktree{Path: wt, Branch: "dash/todo-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if kinds, texts := runnerChatTurn(t, srv, "42", map[string]any{"project": p.ID, "todo": td.ID}); kinds[len(kinds)-1] != "done" {
		t.Fatalf("owner's chat in the runner's own task: %v %q", kinds, texts)
	}
	dir, prompt := seen.seen()
	if dir != wt {
		t.Errorf("task chat ran in %q, want its worktree %q", dir, wt)
	}
	if !strings.Contains(prompt, "<title>Dark mode</title>") {
		t.Errorf("the chat wasn't told about the task: %q", prompt)
	}
	if got := raw(s).TodoChat(td.ID); len(got) != 2 || got[0].CreatedBy != 42 || got[1].Content != "hi" {
		t.Fatalf("saved task chat: %+v", got)
	}

	// A task of another project, named with this one.
	other, err := raw(s).CreateProject(model.Project{ID: store.NewID(), Name: "other", Path: t.TempDir(), DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if kinds, texts := runnerChatTurn(t, srv, "42", map[string]any{"project": other.ID, "todo": td.ID}); kinds[len(kinds)-1] != "error" || !strings.Contains(texts[len(texts)-1], "no task with that id") {
		t.Fatalf("task chat with the wrong project: %v %q", kinds, texts)
	}

	// No such project: said plainly, with no advice to link it.
	kinds, texts = runnerChatTurn(t, srv, "42", map[string]any{"project": "nope"})
	if last := texts[len(texts)-1]; kinds[len(kinds)-1] != "error" || last != "this runner has no project with that id" {
		t.Fatalf("chat in an unknown project: %v %q", kinds, texts)
	}
	// The hub's own chat in a hub project this runner has no clone of.
	kinds, texts = runnerChatTurn(t, srv, "42", map[string]any{"project": "nope", "hub": true})
	if kinds[len(kinds)-1] != "error" || !strings.Contains(texts[len(texts)-1], "Project settings → Runners") {
		t.Fatalf("hub chat in an unlinked project: %v %q", kinds, texts)
	}
	// A clone of a hub project is reached only by the hub's id, as before.
	clone, err := raw(s).CreateProject(model.Project{ID: store.NewID(), Name: "web", Path: t.TempDir(), DefaultBranch: "main", HubProjectID: "hp1", HubCheckout: model.HubCloned})
	if err != nil {
		t.Fatal(err)
	}
	if kinds, texts := runnerChatTurn(t, srv, "42", map[string]any{"project": clone.ID}); kinds[len(kinds)-1] != "error" {
		t.Fatalf("chat in a hub clone by the runner's id: %v %q", kinds, texts)
	}
	n := len(raw(s).ProjectChat(clone.ID, 42))
	if kinds, texts := runnerChatTurn(t, srv, "42", map[string]any{"project": "hp1", "hub": true, "mode": model.ChatModeAcceptEdits}); kinds[len(kinds)-1] != "done" {
		t.Fatalf("hub chat in its linked project: %v %q", kinds, texts)
	}
	if dir, _ := seen.seen(); dir != clone.Path {
		t.Errorf("hub chat ran in %q, want the clone %q", dir, clone.Path)
	}
	if got := raw(s).ProjectChat(clone.ID, 42); len(got) != n {
		t.Errorf("the runner saved the hub's conversation: %+v", got)
	}
}

func TestRunnerOffersClaudeFiles(t *testing.T) {
	parallel(t)

	_, h, p := runnerServer(t)
	if w := hubCall(h, "GET", "/api/projects/"+p.ID+"/claude-files", ""); w.Code == http.StatusForbidden {
		t.Fatalf("claude-files refused by the guard: %d %s", w.Code, w.Body)
	}
}

// Every route the hub proxies to a runner (viaRunner, processViaRunner)
// must be in hubRoutes, or the runner answers 403.
func TestEveryProxiedRouteIsOnTheAllowlist(t *testing.T) {
	parallel(t)

	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`mux\.HandleFunc\("([^"]+)",\s*s\.(?:viaRunner|processViaRunner)\(`)
	m := re.FindAllStringSubmatch(string(src), -1)
	if len(m) == 0 {
		t.Fatal("found no proxied routes in server.go")
	}
	for _, g := range m {
		if !hubRoutes[g[1]] {
			t.Errorf("%s is proxied to runners but isn't in hubRoutes", g[1])
		}
	}
}
