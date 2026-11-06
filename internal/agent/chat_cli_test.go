package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/perm"
)

// flag returns the value after name in args, and whether name is there.
func flag(args []string, name string) (string, bool) {
	i := slices.Index(args, name)
	if i < 0 || i+1 >= len(args) {
		return "", i >= 0
	}
	return args[i+1], true
}

func TestChatArgsReadOnly(t *testing.T) {
	for _, c := range []ClaudeCodeChat{{}, {Tools: ChatReadOnly, AllowedTools: []string{"Bash(ls *)"}}} {
		args := c.args("", "{}")
		if tools, _ := flag(args, "--tools"); tools != "Read,Grep,Glob" {
			t.Errorf("%q: --tools %q", c.Tools, tools)
		}
		for _, f := range []string{"--mcp-config", "--permission-prompt-tool", "--allowedTools"} {
			if _, ok := flag(args, f); ok {
				t.Errorf("%q: read-only chat should not pass %s", c.Tools, f)
			}
		}
		if _, ok := flag(args, "--strict-mcp-config"); !ok {
			t.Error("--strict-mcp-config missing")
		}
		if sys, _ := flag(args, "--system-prompt"); !strings.Contains(sys, "cannot edit files or run commands") {
			t.Errorf("system prompt: %q", sys)
		}
	}
}

func TestChatArgsShell(t *testing.T) {
	c := ClaudeCodeChat{Tools: ChatShell, Model: "opus", AllowedTools: []string{"Bash(ssh defaultuser@phone *)", "Read"}}
	cfg := perm.MCPConfig("http://127.0.0.1:1", "tok")
	args := c.args("/repo", cfg)
	// Same access as `claude` in a terminal: all tools, the user's settings and MCP servers.
	for _, f := range []string{"--tools", "--strict-mcp-config", "--setting-sources"} {
		if _, ok := flag(args, f); ok {
			t.Errorf("shell chat should not pass %s", f)
		}
	}
	if got, _ := flag(args, "--mcp-config"); got != cfg {
		t.Errorf("--mcp-config %q", got)
	}
	if got, _ := flag(args, "--permission-prompt-tool"); got != perm.PromptTool {
		t.Errorf("--permission-prompt-tool %q", got)
	}
	var allowed []string
	for i, a := range args {
		if a == "--allowedTools" {
			allowed = append(allowed, args[i+1])
		}
	}
	if !slices.Equal(allowed, c.AllowedTools) {
		t.Errorf("--allowedTools %q", allowed)
	}
	if m, _ := flag(args, "--model"); m != "opus" {
		t.Errorf("--model %q", m)
	}
	sys, _ := flag(args, "--system-prompt")
	if !strings.Contains(sys, "Bash") || strings.Contains(sys, "cannot edit") || !strings.Contains(sys, "project's checkout") {
		t.Errorf("system prompt: %q", sys)
	}

	// Without a prompt server, nothing is wired up to ask.
	if _, ok := flag(c.args("", ""), "--permission-prompt-tool"); ok {
		t.Error("--permission-prompt-tool without --mcp-config")
	}
}

func TestToolBlockDeltas(t *testing.T) {
	var ev struct {
		Message struct{ Content []contentBlock } `json:"message"`
	}
	line := `{"message":{"content":[
		{"type":"text","text":"hi"},
		{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"uname -a"}},
		{"type":"tool_result","tool_use_id":"t1","content":"Linux"},
		{"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"is_error":true}]}}`
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatal(err)
	}
	var got []ChatDelta
	for _, b := range ev.Message.Content {
		if d, ok := b.delta(); ok {
			got = append(got, d)
		}
	}
	if len(got) != 3 {
		t.Fatalf("got %d deltas: %+v", len(got), got)
	}
	if got[0].Kind != "tool_use" || got[0].ID != "t1" || got[0].Tool != "Bash" || string(got[0].Input) != `{"command":"uname -a"}` {
		t.Errorf("tool_use: %+v", got[0])
	}
	if got[1].Kind != "tool_result" || got[1].ID != "t1" || got[1].Text != "Linux" || got[1].IsError {
		t.Errorf("tool_result: %+v", got[1])
	}
	if got[2].Text != "ab" || !got[2].IsError {
		t.Errorf("tool_result parts: %+v", got[2])
	}
}
