package queue

import (
	"strings"
	"testing"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
)

// bareExec is an agent with none of the optional abilities: it can't
// continue a session, take MCP servers or ask before a tool call.
type bareExec struct{ agent.Executor }

// A provider without the optional abilities still runs tasks: each run
// starts fresh with the full task and every note, without Dash's MCP
// servers or permission prompts.
func TestRunWithoutOptionalAbilities(t *testing.T) {
	parallel(t)

	fe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		if n == 1 {
			write(t, req.Dir, "a.txt", "broken\n")
		} else {
			write(t, req.Dir, "a.txt", "pass\n")
		}
		return nil
	}}
	exe := bareExec{fe}
	if agent.CanResume(exe) || agent.CanMCP(exe) || agent.CanPromptPermissions(exe) {
		t.Fatal("bareExec should have no optional abilities")
	}
	h := newHarness(t, exe, model.Project{Name: "p", TestCommand: "grep -qx pass a.txt", MaxAutoFixes: 2})
	h.engine.Board, h.engine.BoardURL = &mcptools.Tokens{}, "http://127.0.0.1:1"
	h.engine.Perms, h.engine.BaseURL = perm.NewBroker(nil), "http://127.0.0.1:1"
	h.engine.Owner = func(model.Todo) (model.Owner, error) { return model.Owner{User: 1, Org: 1}, nil }
	td := h.waitFor(h.add("make a").ID, model.StatusReview)

	calls := fe.Calls()
	if len(calls) != 2 || td.Runs != 2 {
		t.Fatalf("want 2 runs, got %d (%d)", len(calls), td.Runs)
	}
	for i, c := range calls {
		if c.Resume != "" || c.ForkSession {
			t.Errorf("run %d resumed %q", i+1, c.Resume)
		}
		if len(c.MCPServers) > 0 || len(c.AllowMCPTools) > 0 || c.PermissionTool != (agent.MCPTool{}) || c.ToolTimeout != 0 {
			t.Errorf("run %d got MCP servers or a permission prompt: %+v", i+1, c)
		}
	}
	// The second run has no session to continue: it gets the whole task
	// with the test failure as a note.
	if p := calls[1].Prompt; !strings.Contains(p, "# Task: make a") || !strings.Contains(p, "grep -qx pass a.txt") {
		t.Fatalf("second run should get the full task and the failure, got:\n%s", p)
	}
	if got := runKinds(td); got != "first,retry" {
		t.Errorf("run kinds = %s, want first,retry", got)
	}
}
