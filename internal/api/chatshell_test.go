package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// shellTurn sends a "!" command (the last of msgs) on conn and reads the
// deltas until it ends: the result, or the error.
func shellTurn(t *testing.T, conn *websocket.Conn, fields map[string]any, msgs []agent.ChatMessage, onOutput func(string)) (deltas []agent.ChatDelta, res *model.ChatShell, errText string) {
	t.Helper()
	msg := map[string]any{"type": "shell", "id": "c1", "messages": msgs}
	for k, v := range fields {
		msg[k] = v
	}
	if err := conn.WriteJSON(msg); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		var d agent.ChatDelta
		if err := conn.ReadJSON(&d); err != nil {
			t.Fatal(err)
		}
		deltas = append(deltas, d)
		switch d.Kind {
		case "shell_output":
			if onOutput != nil {
				onOutput(d.Text)
			}
		case "shell_done":
			if d.ID != "c1" {
				t.Errorf("shell_done for %q", d.ID)
			}
			return deltas, d.Shell, ""
		case "error":
			return deltas, nil, d.Text
		}
	}
}

func cmdMsg(text string) agent.ChatMessage { return agent.ChatMessage{Role: "user", Content: text} }

func realDir(t *testing.T, dir string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A "!" command in a project chat runs in the project's checkout, streams
// its output, reports its exit code, and is saved with its result as the
// conversation's newest message, without asking Claude.
func TestProjectChatRunsShellCommand(t *testing.T) {
	s, _, p := boardServer(t)
	chat := &echoChat{}
	s.Chat = chat
	dir := t.TempDir()
	if _, err := raw(s).UpdateProject(p.ID, func(x *model.Project) error { x.Path = dir; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	prev := []agent.ChatMessage{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}
	if err := raw(s).SetProjectChat(p.ID, prev); err != nil {
		t.Fatal(err)
	}
	conn := chatSocket(t, s)
	var streamed strings.Builder
	deltas, res, errText := shellTurn(t, conn, map[string]any{"project": p.ID},
		append(prev, cmdMsg("!pwd; ls; echo broken >&2; exit 2")), func(s string) { streamed.WriteString(s) })
	if errText != "" || res == nil {
		t.Fatalf("error %q", errText)
	}
	if deltas[0].Kind != "cwd" || deltas[0].Text != dir {
		t.Errorf("first delta %+v, want cwd %s", deltas[0], dir)
	}
	if res.ExitCode != 2 || res.Dir != dir || res.Command != "pwd; ls; echo broken >&2; exit 2" {
		t.Errorf("result %+v", res)
	}
	for _, want := range []string{realDir(t, dir), "go.mod", "broken"} {
		if !strings.Contains(res.Output, want) || !strings.Contains(streamed.String(), want) {
			t.Errorf("output %q (streamed %q) lacks %q", res.Output, streamed.String(), want)
		}
	}
	if chat.calls.Load() != 0 {
		t.Error("the command was sent to Claude")
	}
	got := raw(s).ProjectChat(p.ID)
	if len(got) != 3 || got[2].Shell == nil || got[2].Shell.ExitCode != 2 || got[2].Content != "!pwd; ls; echo broken >&2; exit 2" || got[2].At == nil || got[2].CreatedBy == 0 {
		t.Fatalf("saved %+v", got)
	}

	// The next turn sends it to Claude as a command the user ran.
	seen := &seenLast{}
	s.Chat = seen
	if err := conn.WriteJSON(map[string]any{"type": "send", "project": p.ID, "messages": append(got, cmdMsg("why?"))}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the next turn", func() bool { return seen.got() != nil })
	h := seen.got()
	if len(h) != 4 || h[2].Shell != nil || !strings.Contains(h[2].Content, "I ran this shell command myself") || !strings.Contains(h[2].Content, "broken") {
		t.Errorf("Claude was sent %+v", h)
	}
}

// seenLast keeps the conversation it was asked to answer.
type seenLast struct {
	mu sync.Mutex
	h  []agent.ChatMessage
}

func (c *seenLast) Stream(_ context.Context, _ string, h []agent.ChatMessage, emit func(agent.ChatDelta)) error {
	c.mu.Lock()
	c.h = h
	c.mu.Unlock()
	emit(agent.ChatDelta{Kind: "text", Text: "ok"})
	emit(agent.ChatDelta{Kind: "done"})
	return nil
}

func (c *seenLast) got() []agent.ChatMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.h
}

// A task chat's command runs in the task's worktree.
func TestTodoChatRunsShellCommandInWorktree(t *testing.T) {
	s, _, p := boardServer(t)
	s.Chat = &echoChat{}
	wt := t.TempDir()
	if _, err := raw(s).UpdateProject(p.ID, func(x *model.Project) error { x.Path = t.TempDir(); return nil }); err != nil {
		t.Fatal(err)
	}
	td, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "t", Prompt: "p", Status: model.StatusReview, Worktree: &model.Worktree{Path: wt, Branch: "dash/t"}})
	if err != nil {
		t.Fatal(err)
	}
	_, res, errText := shellTurn(t, chatSocket(t, s), map[string]any{"project": p.ID, "todo": td.ID, "review": true}, []agent.ChatMessage{cmdMsg("! pwd")}, nil)
	if errText != "" || res == nil || res.ExitCode != 0 || strings.TrimSpace(res.Output) != realDir(t, wt) {
		t.Fatalf("result %+v, error %q", res, errText)
	}
	if got := raw(s).TodoChat(td.ID); len(got) != 1 || got[0].Shell == nil {
		t.Fatalf("saved %+v", got)
	}
	if got, _ := raw(s).GetTodo(td.ID); got.Reviewing {
		t.Error("a command marked the task as being reviewed")
	}
}

// Stop kills the command; what it printed is kept, and saved.
func TestChatShellStop(t *testing.T) {
	s, _, p := boardServer(t)
	s.Chat = &echoChat{}
	dir := t.TempDir()
	if _, err := raw(s).UpdateProject(p.ID, func(x *model.Project) error { x.Path = dir; return nil }); err != nil {
		t.Fatal(err)
	}
	conn := chatSocket(t, s)
	start := time.Now()
	_, res, errText := shellTurn(t, conn, map[string]any{"project": p.ID}, []agent.ChatMessage{cmdMsg("!echo started; sleep 60")}, func(out string) {
		if strings.Contains(out, "started") {
			_ = conn.WriteJSON(map[string]any{"type": "cancel"})
		}
	})
	if errText != "" || res == nil || res.Error != "stopped" || res.ExitCode != -1 || !strings.Contains(res.Output, "started") {
		t.Fatalf("result %+v, error %q", res, errText)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("took %s to stop", time.Since(start))
	}
	got := raw(s).ProjectChat(p.ID)
	if len(got) != 1 || got[0].Shell == nil || got[0].Shell.Error != "stopped" {
		t.Fatalf("saved %+v", got)
	}
}

// The timeout applies to chat commands.
func TestChatShellTimeout(t *testing.T) {
	s, _, p := boardServer(t)
	s.Chat = &echoChat{}
	s.ChatCommandTimeout = 200 * time.Millisecond
	if _, err := raw(s).UpdateProject(p.ID, func(x *model.Project) error { x.Path = t.TempDir(); return nil }); err != nil {
		t.Fatal(err)
	}
	_, res, errText := shellTurn(t, chatSocket(t, s), map[string]any{"project": p.ID}, []agent.ChatMessage{cmdMsg("!sleep 60")}, nil)
	if errText != "" || res == nil || !strings.Contains(res.Error, "timed out") {
		t.Fatalf("result %+v, error %q", res, errText)
	}
}

// A read-only chat runs no commands, nor does the general chat; neither
// saves anything, and /api/claude tells the UI.
func TestChatShellRefused(t *testing.T) {
	s, _, p := boardServer(t)
	dir := t.TempDir()
	if _, err := raw(s).UpdateProject(p.ID, func(x *model.Project) error { x.Path = dir; return nil }); err != nil {
		t.Fatal(err)
	}
	s.Chat = agent.ClaudeCodeChat{Tools: agent.ChatReadOnly}
	conn := chatSocket(t, s)
	if _, _, errText := shellTurn(t, conn, map[string]any{"project": p.ID}, []agent.ChatMessage{cmdMsg("!touch ran")}, nil); !strings.Contains(errText, "read-only") {
		t.Errorf("read-only chat: %q", errText)
	}
	if w := send(asOwnerHandler(t, s), "GET", "/api/claude", ""); !strings.Contains(w.Body.String(), `"commands":false`) {
		t.Errorf("/api/claude for a read-only chat: %s", w.Body)
	}

	s.Chat = &echoChat{}
	if w := send(asOwnerHandler(t, s), "GET", "/api/claude", ""); !strings.Contains(w.Body.String(), `"commands":true`) {
		t.Errorf("/api/claude: %s", w.Body)
	}
	if _, _, errText := shellTurn(t, conn, nil, []agent.ChatMessage{cmdMsg("!touch ran")}, nil); !strings.Contains(errText, "general chat") {
		t.Errorf("general chat: %q", errText)
	}
	if _, _, errText := shellTurn(t, conn, map[string]any{"project": p.ID}, []agent.ChatMessage{cmdMsg("touch ran")}, nil); !strings.Contains(errText, `starts with "!"`) {
		t.Errorf("not a command: %q", errText)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); err == nil {
		t.Error("a refused command ran")
	}
	if got := raw(s).ProjectChat(p.ID); len(got) != 0 {
		t.Errorf("saved %+v", got)
	}
}

func asOwnerHandler(t *testing.T, s *Server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.mux().ServeHTTP(w, asOwner(t, s, r)) })
}

// On a hub, a project chat's command runs on the caller's runner linked to
// the project, in its clone there, never on the hub; Stop reaches it there;
// the hub keeps the result. Another member can't run one on it.
func TestHubRunsChatShellOnTheUsersRunner(t *testing.T) {
	f := newClaudeFleet(t)
	hp := project(t, f.hub, "web")
	if _, err := raw(f.hub).UpdateProject(hp.ID, func(p *model.Project) error { p.Runners = []string{f.creds.ID}; return nil }); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(f.runner.ProjectRoot, "web")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := raw(f.runner).CreateProject(model.Project{ID: store.NewID(), Name: "web", Path: clone, DefaultBranch: "main", HubProjectID: hp.ID, HubCheckout: model.HubCloned}); err != nil {
		t.Fatal(err)
	}
	report := func(commands bool) {
		b, _ := json.Marshal(map[string]any{"parallel": 1, "chat_commands": commands})
		r := httptest.NewRequest("POST", "/runner/report", strings.NewReader(string(b)))
		r.Header.Set("Authorization", "Bearer "+f.creds.Token)
		w := httptest.NewRecorder()
		f.rh.ServeReport(w, r)
		if w.Code != http.StatusNoContent {
			t.Fatalf("report: %d %s", w.Code, w.Body)
		}
	}
	info := func() string { return send(f.owner, "GET", "/api/claude", "").Body.String() }
	dial := func(url string) *websocket.Conn {
		ws, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ws.Close() })
		return ws
	}

	conn := dial(f.ownerWS)
	// A runner that hasn't said it runs them (an older one) isn't asked.
	if got := info(); !strings.Contains(got, `"commands":false`) {
		t.Errorf("/api/claude before the runner reports: %s", got)
	}
	if _, _, errText := shellTurn(t, conn, map[string]any{"project": hp.ID}, []agent.ChatMessage{cmdMsg("!pwd")}, nil); !strings.Contains(errText, "doesn't run ! commands") {
		t.Errorf("before the report: %q", errText)
	}
	report(true)
	if got := info(); !strings.Contains(got, `"commands":true`) {
		t.Errorf("/api/claude after the runner reports: %s", got)
	}
	if got := send(f.other, "GET", "/api/claude", "").Body.String(); !strings.Contains(got, `"commands":false`) {
		t.Errorf("/api/claude for another member: %s", got)
	}
	deltas, res, errText := shellTurn(t, conn, map[string]any{"project": hp.ID}, []agent.ChatMessage{cmdMsg("!pwd")}, nil)
	if errText != "" || res == nil {
		t.Fatalf("error %q", errText)
	}
	if deltas[0].Kind != "runner" || deltas[0].Text != "laptop" {
		t.Errorf("first delta %+v", deltas[0])
	}
	if res.ExitCode != 0 || strings.TrimSpace(res.Output) != realDir(t, clone) || res.Dir != clone || res.Runner != "laptop" {
		t.Errorf("result %+v", res)
	}
	var cwd string
	for _, d := range deltas {
		if d.Kind == "cwd" {
			cwd = d.Text
		}
	}
	if cwd != clone+" on laptop" {
		t.Errorf("cwd %q", cwd)
	}
	eventually(t, "the hub to save the command", func() bool {
		got := raw(f.hub).ProjectChat(hp.ID)
		return len(got) == 1 && got[0].Shell != nil && got[0].Shell.Runner == "laptop"
	})

	// Stop reaches the runner; what it printed comes back.
	_, res, errText = shellTurn(t, conn, map[string]any{"project": hp.ID}, []agent.ChatMessage{cmdMsg("!echo started; sleep 60")}, func(out string) {
		if strings.Contains(out, "started") {
			_ = conn.WriteJSON(map[string]any{"type": "cancel"})
		}
	})
	if errText != "" || res == nil || res.Error != "stopped" || !strings.Contains(res.Output, "started") {
		t.Fatalf("stopped on the runner: %+v %q", res, errText)
	}

	// Another member: no runner of theirs on the project.
	if _, _, errText := shellTurn(t, dial(f.otherWS), map[string]any{"project": hp.ID}, []agent.ChatMessage{cmdMsg("!touch ran")}, nil); !strings.Contains(errText, "no runner of yours") {
		t.Errorf("other member: %q", errText)
	}
	if _, err := os.Stat(filepath.Join(clone, "ran")); err == nil {
		t.Error("another member's command ran")
	}

	// A read-only chat on the runner refuses it, even when the hub still
	// thinks otherwise; once it reports so, the hub refuses it itself.
	f.runner.Chat = agent.ClaudeCodeChat{Tools: agent.ChatReadOnly}
	if _, _, errText := shellTurn(t, conn, map[string]any{"project": hp.ID}, []agent.ChatMessage{cmdMsg("!touch ran")}, nil); !strings.Contains(errText, "read-only") {
		t.Errorf("read-only runner chat: %q", errText)
	}
	if _, err := os.Stat(filepath.Join(clone, "ran")); err == nil {
		t.Error("a read-only chat ran the command")
	}
	report(false)
	if got := info(); !strings.Contains(got, `"commands":false`) {
		t.Errorf("/api/claude after a read-only report: %s", got)
	}
	if _, _, errText := shellTurn(t, conn, map[string]any{"project": hp.ID}, []agent.ChatMessage{cmdMsg("!touch ran")}, nil); !strings.Contains(errText, "doesn't run ! commands") {
		t.Errorf("after a read-only report: %q", errText)
	}
}

// A runner runs a "!" command the hub sends only for its owner, and only in
// a chat that runs commands.
func TestRunnerRunsChatShellOnlyForItsOwner(t *testing.T) {
	s, h, _ := runnerServer(t)
	s.HubUser, s.Chat = 42, &echoChat{}
	clone := filepath.Join(s.ProjectRoot, "web")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := raw(s).CreateProject(model.Project{ID: store.NewID(), Name: "web", Path: clone, DefaultBranch: "main", HubProjectID: "hub-p", HubCheckout: model.HubCloned}); err != nil {
		t.Fatal(err)
	}
	var user string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set(tunnel.UserHeader, user)
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	run := func(u string) (*model.ChatShell, string) {
		user = u
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.Close()
		_, res, errText := shellTurn(t, ws, map[string]any{"project": "hub-p", "hub": true}, []agent.ChatMessage{cmdMsg("!touch ran-" + u)}, nil)
		return res, errText
	}
	// Not in an API chat, which runs no commands.
	s.Chat = agent.NewChat()
	if res, errText := run("42"); res != nil || !strings.Contains(errText, "read-only") {
		t.Errorf("API chat: %+v %q", res, errText)
	}
	s.Chat = &echoChat{}
	// Nor when it doesn't know its owner.
	s.HubUser = 0
	if res, errText := run("42"); res != nil || !strings.Contains(errText, "doesn't know who registered it") {
		t.Errorf("unknown owner: %+v %q", res, errText)
	}
	s.HubUser = 42
	if _, err := os.Stat(filepath.Join(clone, "ran-42")); err == nil {
		t.Fatal("a refused command ran")
	}
	for _, u := range []string{"7", ""} {
		if res, errText := run(u); res != nil || !strings.Contains(errText, "only for") {
			t.Errorf("user %q: %+v %q", u, res, errText)
		}
		if _, err := os.Stat(filepath.Join(clone, "ran-"+u)); err == nil {
			t.Errorf("ran for user %q", u)
		}
	}
	if res, errText := run("42"); res == nil || res.ExitCode != 0 {
		t.Fatalf("owner: %+v %q", res, errText)
	}
	if _, err := os.Stat(filepath.Join(clone, "ran-42")); err != nil {
		t.Error("didn't run for the owner")
	}
}
