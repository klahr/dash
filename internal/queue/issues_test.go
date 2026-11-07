package queue

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
)

// Every run gets the issue tools, so Claude can report problems outside its task.
func TestRunGetsIssueTools(t *testing.T) {
	exe := &fakeExec{fn: func(_ int, req agent.ExecRequest) error {
		write(t, req.Dir, "a.txt", "a")
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p"})
	h.engine.Board, h.engine.BoardURL = &mcptools.Tokens{}, "http://127.0.0.1:1"
	h.engine.Owner = func(model.Todo) (model.Owner, error) { return model.Owner{User: 1, Org: 1}, nil }
	h.waitFor(h.add("work").ID, model.StatusReview)

	args := exe.Calls()[0].ExtraArgs
	i := slices.Index(args, "--mcp-config")
	if i < 0 {
		t.Fatalf("no --mcp-config in %v", args)
	}
	var cfg struct{ MCPServers map[string]any }
	if err := json.Unmarshal([]byte(args[i+1]), &cfg); err != nil || cfg.MCPServers[mcptools.ServerName] == nil {
		t.Fatalf("--mcp-config %s (%v)", args[i+1], err)
	}
	if !slices.Contains(args, "mcp__"+mcptools.ServerName+"__report_issue") || !slices.Contains(args, "--append-system-prompt") {
		t.Fatalf("issue tools not allowed or explained: %v", args)
	}
}

// A task with no user and org to act as runs without Dash's MCP servers:
// a per-run token always carries both.
func TestRunWithoutOwnerGetsNoTools(t *testing.T) {
	exe := &fakeExec{fn: func(_ int, req agent.ExecRequest) error {
		write(t, req.Dir, "a.txt", "a")
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p"})
	h.engine.Board, h.engine.BoardURL = &mcptools.Tokens{}, "http://127.0.0.1:1"
	h.engine.Owner = func(model.Todo) (model.Owner, error) { return model.Owner{}, errors.New("nobody") }
	h.waitFor(h.add("work").ID, model.StatusReview)
	if args := exe.Calls()[0].ExtraArgs; slices.Contains(args, "--mcp-config") {
		t.Fatalf("unowned run got MCP servers: %v", args)
	}
}
