// Command dash-mcp is an MCP server (stdio) for a running Dash server, so a
// regular Claude client (Claude Desktop, Claude Code) can list, create and
// review todos on every computer in the dashboard.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/klahr/dash/internal/auth"
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
	s := &mcpServer{c: c, login: func() error { return c.Login(user, pass) }}
	if err := s.login(); err != nil {
		fail(fmt.Errorf("login to %s: %w", *server, err))
	}
	if err := s.serve(os.Stdin, os.Stdout); err != nil {
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

// ---- JSON-RPC over stdio (one message per line) ----

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpServer struct {
	c     *tui.Client
	login func() error
}

func (s *mcpServer) serve(in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcError{-32700, "parse error"}})
			continue
		}
		if len(req.ID) == 0 { // notification (e.g. notifications/initialized)
			continue
		}
		result, rerr := s.handle(req)
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rerr != nil {
			resp["error"] = rerr
		} else {
			resp["result"] = result
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *mcpServer) handle(req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		return map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "dash", "version": "1"},
			"instructions": "Dash runs Claude Code on tasks in parallel, each in its own git worktree. " +
				"A task goes draft → queued → in_progress → review → merging → merged (or failed). " +
				"Review a task with get_todo and get_diff, then approve_todo or rework_todo with feedback.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		list := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			list = append(list, map[string]any{"name": t.name, "description": t.desc, "inputSchema": t.schema()})
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params"}
		}
		for _, t := range tools {
			if t.name == p.Name {
				return s.call(t, args(p.Arguments)), nil
			}
		}
		return nil, &rpcError{-32602, "unknown tool " + p.Name}
	default:
		return nil, &rpcError{-32601, "method not found"}
	}
}

// call runs a tool, logging in again once if the session expired.
func (s *mcpServer) call(t tool, a args) map[string]any {
	res, err := t.run(s, a)
	if errors.Is(err, tui.ErrUnauthorized) && s.login() == nil {
		res, err = t.run(s, a)
	}
	if err != nil {
		return map[string]any{"isError": true, "content": []any{map[string]string{"type": "text", "text": err.Error()}}}
	}
	text, ok := res.(string)
	if !ok {
		b, _ := json.MarshalIndent(res, "", "  ")
		text = string(b)
	}
	return map[string]any{"content": []any{map[string]string{"type": "text", "text": text}}}
}

// ---- tools ----

type args map[string]any

func (a args) str(k string) string {
	s, _ := a[k].(string)
	return strings.TrimSpace(s)
}

func (a args) num(k string, def int) int {
	if f, ok := a[k].(float64); ok {
		return int(f)
	}
	return def
}

func (a args) flag(k string) bool {
	b, _ := a[k].(bool)
	return b
}

type param struct {
	name, typ, desc string
	required        bool
}

type tool struct {
	name, desc string
	params     []param
	run        func(s *mcpServer, a args) (any, error)
}

func (t tool) schema() map[string]any {
	props := map[string]any{}
	required := []string{}
	for _, p := range t.params {
		props[p.name] = map[string]string{"type": p.typ, "description": p.desc}
		if p.required {
			required = append(required, p.name)
		}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

var (
	pProject = param{"project", "string", "Project name or id (see list_projects).", true}
	pTodo    = param{"todo", "string", "Task id.", true}
)

var tools = []tool{
	{"list_projects", "List the projects on every computer in this Dash.", nil,
		func(s *mcpServer, a args) (any, error) {
			ps, ms, err := s.c.AllProjects()
			if err != nil {
				return nil, err
			}
			type row struct {
				ID            string `json:"id"`
				Name          string `json:"name"`
				Machine       string `json:"machine"`
				Path          string `json:"path"`
				DefaultBranch string `json:"default_branch"`
				MergeMode     string `json:"merge_mode,omitempty"`
			}
			out := struct {
				Projects    []row    `json:"projects"`
				Unreachable []string `json:"unreachable,omitempty"`
			}{Projects: []row{}}
			for _, p := range ps {
				out.Projects = append(out.Projects, row{p.ID, p.Name, p.Machine, p.Path, p.DefaultBranch, p.MergeMode})
			}
			for _, m := range ms {
				if !m.OK {
					out.Unreachable = append(out.Unreachable, m.Name+": "+m.Error)
				}
			}
			return out, nil
		}},
	{"list_todos", "List a project's tasks (id, title, status), optionally filtered by status.", []param{pProject,
		{"status", "string", "Only this status: draft, queued, in_progress, review, merging, merged or failed.", false}},
		func(s *mcpServer, a args) (any, error) {
			p, err := s.project(a.str("project"))
			if err != nil {
				return nil, err
			}
			ts, err := s.c.Todos(p.ID)
			if err != nil {
				return nil, err
			}
			type row struct {
				ID       string           `json:"id"`
				Title    string           `json:"title"`
				Status   model.TodoStatus `json:"status"`
				Priority int              `json:"priority"`
				Error    string           `json:"error,omitempty"`
			}
			out := []row{}
			for _, t := range ts {
				if st := a.str("status"); st != "" && string(t.Status) != st {
					continue
				}
				out = append(out, row{t.ID, t.Title, t.Status, t.Priority, t.Error})
			}
			return out, nil
		}},
	{"get_todo", "Show a task: prompt, status, feedback, test results, diff size, pull request.", []param{pTodo},
		func(s *mcpServer, a args) (any, error) { return s.todo(a.str("todo")) }},
	{"create_todo", "Create a task for Claude to work on. It is queued to run unless draft is true.", []param{pProject,
		{"prompt", "string", "The task for Claude (the title alone is enough for small tasks).", false},
		{"title", "string", "Short title (generated from the prompt if empty).", false},
		{"priority", "integer", "Lower runs first (default 0).", false},
		{"goal", "boolean", "Mark as a major milestone.", false},
		{"draft", "boolean", "Keep it as a draft instead of queueing it.", false}},
		func(s *mcpServer, a args) (any, error) {
			p, err := s.project(a.str("project"))
			if err != nil {
				return nil, err
			}
			if a.str("title") == "" && a.str("prompt") == "" {
				return nil, errors.New("title or prompt is required")
			}
			return s.c.CreateTodo(p.ID, tui.NewTodo{
				Title: a.str("title"), Prompt: a.str("prompt"), Goal: a.flag("goal"),
				Priority: a.num("priority", 0), Enqueue: !a.flag("draft"),
			})
		}},
	{"get_diff", "The task's changes against the default branch (or the merged commit).", []param{pTodo,
		{"max_chars", "integer", "Truncate after this many characters (default 100000).", false}},
		func(s *mcpServer, a args) (any, error) {
			return s.text(a.str("todo"), "diff", a.num("max_chars", 100000), false)
		}},
	{"get_log", "The end of Claude's run log for a task.", []param{pTodo,
		{"max_chars", "integer", "Return at most this many characters from the end (default 20000).", false}},
		func(s *mcpServer, a args) (any, error) {
			return s.text(a.str("todo"), "log", a.num("max_chars", 20000), true)
		}},
	{"approve_todo", "Approve a task in review: it goes to the merge queue (or a pull request).", []param{pTodo},
		func(s *mcpServer, a args) (any, error) { return s.action(a.str("todo"), "approve", nil) }},
	{"rework_todo", "Send a task back to Claude with feedback.", []param{pTodo,
		{"feedback", "string", "What to change.", true}},
		func(s *mcpServer, a args) (any, error) {
			if a.str("feedback") == "" {
				return nil, errors.New("feedback is required")
			}
			return s.action(a.str("todo"), "rework", map[string]any{"feedback": a.str("feedback")})
		}},
	{"enqueue_todo", "Queue a draft or failed task to run.", []param{pTodo},
		func(s *mcpServer, a args) (any, error) { return s.action(a.str("todo"), "enqueue", nil) }},
	{"cancel_todo", "Stop a running or queued task.", []param{pTodo},
		func(s *mcpServer, a args) (any, error) { return s.action(a.str("todo"), "cancel", nil) }},
	{"delete_todo", "Delete a task and its worktree.", []param{pTodo},
		func(s *mcpServer, a args) (any, error) {
			if _, err := s.todo(a.str("todo")); err != nil {
				return nil, err
			}
			return "deleted", s.c.DeleteTodo(a.str("todo"))
		}},
	{"list_permissions", "Tool calls Claude is waiting for permission to run, on every computer.", nil,
		func(s *mcpServer, a args) (any, error) {
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
		}},
	{"decide_permission", "Answer a permission request from list_permissions.", []param{
		{"id", "string", "Permission request id.", true},
		{"decision", "string", "allow, always or deny.", true}},
		func(s *mcpServer, a args) (any, error) {
			id, d := a.str("id"), a.str("decision")
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
		}},
	{"get_usage", "Claude subscription usage and whether the queue is paused.", nil,
		func(s *mcpServer, a args) (any, error) { return s.c.Usage() }},
	{"set_queue_paused", "Pause or resume starting new tasks.", []param{
		{"paused", "boolean", "true to pause, false to resume.", true}},
		func(s *mcpServer, a args) (any, error) { return s.c.SetQueuePaused(a.flag("paused")) }},
}

// ---- helpers ----

// project finds a project by id or name on any computer and points the client at it.
func (s *mcpServer) project(ref string) (model.Project, error) {
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
func (s *mcpServer) eachMachine(fn func(tui.Machine) error) error {
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

// todo finds a todo on whichever computer has it and points the client there.
func (s *mcpServer) todo(id string) (model.Todo, error) {
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

func (s *mcpServer) action(id, verb string, body any) (any, error) {
	if _, err := s.todo(id); err != nil {
		return nil, err
	}
	if err := s.c.TodoAction(id, verb, body); err != nil {
		return nil, err
	}
	return s.todo(id)
}

func (s *mcpServer) text(id, kind string, max int, tail bool) (string, error) {
	if _, err := s.todo(id); err != nil {
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
