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

// ExecFor picks a project's executor: its tasks run on it, and its abilities,
// not the default executor's, decide on resuming, MCP servers and permission
// prompts. A project ExecFor has nothing for runs on Exec.
func TestExecForPicksProjectsExecutor(t *testing.T) {
	parallel(t)
	for _, tc := range []struct {
		agent string
		bare  bool // runs on the bare executor
	}{{agent: "bare", bare: true}, {agent: ""}} {
		t.Run("agent="+tc.agent, func(t *testing.T) {
			ok := func(int, agent.ExecRequest) error { return nil }
			def, other := &fakeExec{fn: ok}, &fakeExec{fn: ok}
			h := newHarness(t, def, model.Project{Name: "p", Agent: tc.agent})
			h.engine.ExecFor = func(p model.Project) agent.Executor {
				if p.Agent == "bare" {
					return bareExec{other}
				}
				return nil
			}
			h.engine.Board, h.engine.BoardURL = &mcptools.Tokens{}, "http://127.0.0.1:1"
			h.engine.Perms, h.engine.BaseURL = perm.NewBroker(nil), "http://127.0.0.1:1"
			h.engine.Owner = func(model.Todo) (model.Owner, error) { return model.Owner{User: 1, Org: 1}, nil }

			// A task with a session to continue, as after an interrupted run.
			td, err := h.st.CreateTodo(model.Todo{ProjectID: h.proj.ID, Title: "make a", Prompt: "do make a"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.st.UpdateTodo(td.ID, func(x *model.Todo) error { x.SessionID = "sess-old"; return nil }); err != nil {
				t.Fatal(err)
			}
			if _, err := h.st.SetStatus(td.ID, model.StatusQueued, ""); err != nil {
				t.Fatal(err)
			}
			h.engine.Kick()
			h.waitFor(td.ID, model.StatusReview)

			ran, idle := def, other
			if tc.bare {
				ran, idle = other, def
			}
			if n := len(idle.Calls()); n != 0 {
				t.Fatalf("the other executor ran %d times", n)
			}
			calls := ran.Calls()
			if len(calls) != 1 {
				t.Fatalf("want 1 run, got %d", len(calls))
			}
			c := calls[0]
			if tc.bare {
				if c.Resume != "" || c.ForkSession {
					t.Errorf("bare executor resumed %q", c.Resume)
				}
				if len(c.MCPServers) > 0 || len(c.AllowMCPTools) > 0 || c.PermissionTool != (agent.MCPTool{}) {
					t.Errorf("bare executor got MCP servers or a permission prompt: %+v", c)
				}
				if !strings.Contains(c.Prompt, "# Task: make a") {
					t.Errorf("bare executor should get the full task, got:\n%s", c.Prompt)
				}
				return
			}
			if c.Resume != "sess-old" {
				t.Errorf("default executor resumed %q, want sess-old", c.Resume)
			}
			if len(c.MCPServers) == 0 || c.PermissionTool == (agent.MCPTool{}) {
				t.Errorf("default executor should get MCP servers and a permission prompt: %+v", c)
			}
		})
	}
}
