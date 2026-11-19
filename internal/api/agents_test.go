package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// A project picks the agent its tasks run on from the registered ones;
// the dashboard lists them with what each can do.
func TestProjectAgentSetting(t *testing.T) {
	parallel(t)

	s, _, p := boardServer(t)
	s.Git = gitops.New(nil)
	h := loggedIn(t, s.Routes())
	if w := do(t, h, "PATCH", "/api/projects/"+p.ID, `{"agent":" Claude "}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"agent":"claude"`) {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "PATCH", "/api/projects/"+p.ID, `{"agent":"nobody"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "claude") {
		t.Errorf("unknown agent: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "PATCH", "/api/projects/"+p.ID, `{"agent":""}`); w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"agent"`) {
		t.Errorf("back to the default: %d %s", w.Code, w.Body)
	}

	w := do(t, h, "GET", "/api/agents", "")
	var list []struct {
		Name     string   `json:"name"`
		Title    string   `json:"title"`
		Features []string `json:"features"`
		Default  bool     `json:"default"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) == 0 {
		t.Fatalf("agents: %v %s", err, w.Body)
	}
	if c := list[0]; c.Name != "claude" || c.Title != "Claude Code" || !c.Default || len(c.Features) != 5 {
		t.Errorf("claude: %+v", c)
	}
	// Codex: sessions and MCP, no permission prompts, tool rules or usage.
	if len(list) < 2 || list[1].Name != "codex" || list[1].Title != "Codex" || list[1].Default ||
		!slices.Equal(list[1].Features, []string{"resume", "mcp"}) {
		t.Errorf("codex: %+v", list)
	}
	if w := do(t, h, "PATCH", "/api/projects/"+p.ID, `{"agent":"codex"}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"agent":"codex"`) {
		t.Errorf("pick codex: %d %s", w.Code, w.Body)
	}
}

// A project's chat runs on its agent's chatter (ChatFor); the general chat
// and a project on the default agent on Chat. A runner's GET /api/agents
// says what it found of each CLI.
func TestProjectChatRunsOnItsAgent(t *testing.T) {
	s, _, p := boardServer(t)
	def, codex := &echoChat{}, &echoChat{}
	s.Chat = def
	s.ChatFor = func(p model.Project) agent.Chatter {
		if p.AgentName() == agent.CodexProvider {
			return codex
		}
		return nil
	}
	turn := func(msg map[string]any) {
		t.Helper()
		conn := chatSocket(t, s)
		if err := conn.WriteJSON(msg); err != nil {
			t.Fatal(err)
		}
		if k := kindsOf(readTurn(t, conn, 0)); k[len(k)-1] != "done" {
			t.Fatalf("turn: %v", k)
		}
	}
	project := map[string]any{"type": "send", "project": p.ID, "messages": []agent.ChatMessage{cmdMsg("q")}}
	turn(project)
	if def.calls.Load() != 1 || codex.calls.Load() != 0 {
		t.Fatalf("default agent: default %d, codex %d", def.calls.Load(), codex.calls.Load())
	}
	if _, err := raw(s).UpdateProject(p.ID, func(x *model.Project) error { x.Agent = agent.CodexProvider; return nil }); err != nil {
		t.Fatal(err)
	}
	// The next turn carries the conversation so far.
	project["messages"] = []agent.ChatMessage{cmdMsg("q"), {Role: "assistant", Content: "hi"}, cmdMsg("again")}
	turn(project)
	if def.calls.Load() != 1 || codex.calls.Load() != 1 {
		t.Errorf("codex project: default %d, codex %d", def.calls.Load(), codex.calls.Load())
	}
	turn(map[string]any{"type": "send", "messages": []agent.ChatMessage{cmdMsg("q")}})
	if def.calls.Load() != 2 || codex.calls.Load() != 1 {
		t.Errorf("general chat: default %d, codex %d", def.calls.Load(), codex.calls.Load())
	}

	s.Agents = func() []model.AgentStatus {
		return []model.AgentStatus{{Name: "codex", Installed: true, LoggedIn: false, CheckedAt: time.Now()}}
	}
	w := do(t, loggedIn(t, s.Routes()), "GET", "/api/agents", "")
	var list []agent.ProviderInfo
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(list, func(a agent.ProviderInfo) bool { return a.Name == "codex" })
	if i < 0 || list[i].Status == nil || !list[i].Status.Installed || list[i].Status.LoggedIn || list[0].Status != nil {
		t.Errorf("agents %s", w.Body)
	}
}
