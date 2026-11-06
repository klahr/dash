// Command dash-mcp is an MCP server (stdio) for a running Dash server, so a
// regular Claude client (Claude Desktop, Claude Code) can list, create and
// review todos on every computer in the dashboard.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tui"
)

func main() {
	server := flag.String("server", envOr("DASH_SERVER", "http://127.0.0.1:7777"), "Dash server URL (or $DASH_SERVER)")
	envFile := flag.String("env", ".env", "file with DASH_USERNAME / DASH_PASSWORD")
	flag.Parse()

	if err := auth.LoadDotEnv(*envFile); err != nil {
		fail(err)
	}
	user, pass := os.Getenv("DASH_USERNAME"), os.Getenv("DASH_PASSWORD")
	if user == "" || pass == "" {
		fail(errors.New("set DASH_USERNAME and DASH_PASSWORD (environment or -env file)"))
	}
	c, err := tui.NewClient(*server)
	if err != nil {
		fail(err)
	}
	login := func() error { return c.Login(user, pass) }
	if err := login(); err != nil {
		fail(fmt.Errorf("login to %s: %w", *server, err))
	}
	s := &mcptools.Server{
		Name: "dash",
		Instructions: "Dash runs Claude Code on tasks in parallel, each in its own git worktree. " +
			"A task goes draft → queued → in_progress → review → merging → merged (or failed). " +
			"Review a task with get_todo and get_diff, then approve_todo or rework_todo with feedback.",
		Tools: tools(client{c}),
		// Log in again once if the session expired.
		Retry: func(err error) bool { return errors.Is(err, tui.ErrUnauthorized) && login() == nil },
	}
	if err := s.ServeStdio(os.Stdin, os.Stdout); err != nil {
		fail(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "dash-mcp:", err)
	os.Exit(1)
}

// client is the Board of a Dash server reached through its HTTP API.
type client struct{ c *tui.Client }

func (cl client) Projects() ([]model.Project, []string, error) {
	ps, ms, err := cl.c.AllProjects()
	if err != nil {
		return nil, nil, err
	}
	var unreachable []string
	for _, m := range ms {
		if !m.OK {
			unreachable = append(unreachable, m.Name+": "+m.Error)
		}
	}
	return ps, unreachable, nil
}

func (cl client) Todos(projectID string) ([]model.Todo, error) { return cl.c.Todos(projectID) }

func (cl client) CreateTodo(projectID string, t mcptools.NewTodo) (model.Todo, error) {
	return cl.c.CreateTodo(projectID, tui.NewTodo{
		Title: t.Title, Prompt: t.Prompt, Goal: t.Goal, Priority: t.Priority, Enqueue: t.Enqueue,
	})
}

func (cl client) QueueTodo(id string) (model.Todo, error) {
	t, err := cl.action(id, "enqueue", nil)
	if err != nil {
		return model.Todo{}, err
	}
	return t.(model.Todo), nil
}

func tools(s client) []mcptools.Tool {
	pTodo := mcptools.PTodo
	return append(mcptools.Tools(s, mcptools.Options{}), []mcptools.Tool{
		tool("get_diff", "The task's changes against the default branch (or the merged commit).", []mcptools.Param{pTodo,
			param("max_chars", "integer", "Truncate after this many characters (default 100000).", false)},
			func(a mcptools.Args) (any, error) {
				return s.text(a.Str("todo"), "diff", a.Num("max_chars", 100000), false)
			}),
		tool("get_log", "The end of Claude's run log for a task.", []mcptools.Param{pTodo,
			param("max_chars", "integer", "Return at most this many characters from the end (default 20000).", false)},
			func(a mcptools.Args) (any, error) {
				return s.text(a.Str("todo"), "log", a.Num("max_chars", 20000), true)
			}),
		tool("approve_todo", "Approve a task in review: it goes to the merge queue (or a pull request).", []mcptools.Param{pTodo},
			func(a mcptools.Args) (any, error) { return s.action(a.Str("todo"), "approve", nil) }),
		tool("rework_todo", "Send a task back to Claude with feedback.", []mcptools.Param{pTodo,
			param("feedback", "string", "What to change.", true)},
			func(a mcptools.Args) (any, error) {
				if a.Str("feedback") == "" {
					return nil, errors.New("feedback is required")
				}
				return s.action(a.Str("todo"), "rework", map[string]any{"feedback": a.Str("feedback")})
			}),
		tool("enqueue_todo", "Queue a draft or failed task to run (also retries failed ones, unlike queue_todo).", []mcptools.Param{pTodo},
			func(a mcptools.Args) (any, error) { return s.action(a.Str("todo"), "enqueue", nil) }),
		tool("cancel_todo", "Stop a running or queued task.", []mcptools.Param{pTodo},
			func(a mcptools.Args) (any, error) { return s.action(a.Str("todo"), "cancel", nil) }),
		tool("delete_todo", "Delete a task and its worktree.", []mcptools.Param{pTodo},
			func(a mcptools.Args) (any, error) {
				if _, err := s.Todo(a.Str("todo")); err != nil {
					return nil, err
				}
				return "deleted", s.c.DeleteTodo(a.Str("todo"))
			}),
		tool("list_permissions", "Tool calls Claude is waiting for permission to run, on every computer.", nil,
			func(a mcptools.Args) (any, error) {
				type row struct {
					Machine string `json:"machine"`
					tui.PermRequest
				}
				out := []row{}
				err := s.eachMachine(func(m tui.Machine) error {
					ps, err := s.c.Permissions("")
					for _, p := range ps {
						out = append(out, row{m.Name, p})
					}
					return err
				})
				return out, err
			}),
		tool("decide_permission", "Answer a permission request from list_permissions.", []mcptools.Param{
			param("id", "string", "Permission request id.", true),
			param("decision", "string", "allow, always or deny.", true)},
			func(a mcptools.Args) (any, error) {
				id, d := a.Str("id"), a.Str("decision")
				if d != "allow" && d != "always" && d != "deny" {
					return nil, errors.New("decision must be allow, always or deny")
				}
				found := false
				err := s.eachMachine(func(m tui.Machine) error {
					ps, err := s.c.Permissions("")
					for _, p := range ps {
						if p.ID == id && !found {
							found = true
							return s.c.Decide(id, d)
						}
					}
					return err
				})
				if err == nil && !found {
					err = fmt.Errorf("no pending permission request %q", id)
				}
				return d, err
			}),
		tool("get_usage", "Claude subscription usage and whether the queue is paused.", nil,
			func(a mcptools.Args) (any, error) { return s.c.Usage() }),
		tool("set_queue_paused", "Pause or resume starting new tasks.", []mcptools.Param{
			param("paused", "boolean", "true to pause, false to resume.", true)},
			func(a mcptools.Args) (any, error) { return s.c.SetQueuePaused(a.Flag("paused")) }),
	}...)
}

func tool(name, desc string, params []mcptools.Param, run func(mcptools.Args) (any, error)) mcptools.Tool {
	return mcptools.Tool{Name: name, Desc: desc, Params: params, Run: run}
}

func param(name, typ, desc string, required bool) mcptools.Param {
	return mcptools.Param{Name: name, Type: typ, Desc: desc, Required: required}
}

// ---- helpers ----

// Project finds a project by id or name on any computer and points the client at it.
func (s client) Project(ref string) (model.Project, error) {
	ps, ms, err := s.c.AllProjects()
	if err != nil {
		return model.Project{}, err
	}
	for _, p := range ps {
		if p.ID == ref || strings.EqualFold(p.Name, ref) {
			for _, m := range ms {
				if m.Name == p.Machine {
					s.c.SetPrefix(m.Prefix)
				}
			}
			return p, nil
		}
	}
	return model.Project{}, fmt.Errorf("no project %q (see list_projects)", ref)
}

// eachMachine points the client at every reachable computer in turn.
func (s client) eachMachine(fn func(tui.Machine) error) error {
	ms, err := s.c.Machines()
	if err != nil {
		return err
	}
	defer s.c.SetPrefix("")
	for _, m := range ms {
		if !m.OK {
			continue
		}
		s.c.SetPrefix(m.Prefix)
		if err := fn(m); err != nil {
			return err
		}
	}
	return nil
}

// Todo finds a todo on whichever computer has it and points the client there.
func (s client) Todo(id string) (model.Todo, error) {
	if id == "" {
		return model.Todo{}, errors.New("todo is required")
	}
	ms, err := s.c.Machines()
	if err != nil {
		return model.Todo{}, err
	}
	for _, m := range ms {
		if !m.OK {
			continue
		}
		s.c.SetPrefix(m.Prefix)
		if t, err := s.c.Todo(id); err == nil {
			return t, nil
		} else if errors.Is(err, tui.ErrUnauthorized) {
			return model.Todo{}, err
		}
	}
	return model.Todo{}, fmt.Errorf("no task %q", id)
}

func (s client) action(id, verb string, body any) (any, error) {
	if _, err := s.Todo(id); err != nil {
		return nil, err
	}
	if err := s.c.TodoAction(id, verb, body); err != nil {
		return nil, err
	}
	return s.Todo(id)
}

func (s client) text(id, kind string, max int, tail bool) (string, error) {
	if _, err := s.Todo(id); err != nil {
		return "", err
	}
	out, err := s.c.TodoText(id, kind)
	if err != nil || max <= 0 || len(out) <= max {
		return out, err
	}
	if tail {
		return fmt.Sprintf("[…%d earlier characters omitted]\n", len(out)-max) + out[len(out)-max:], nil
	}
	return out[:max] + fmt.Sprintf("\n[…truncated, %d more characters]", len(out)-max), nil
}
