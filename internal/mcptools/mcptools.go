// Package mcptools serves Dash's task board as MCP tools: over stdio for
// cmd/dash-mcp, and over streamable HTTP (JSON responses) for the chat's
// Claude Code runs, authenticated by a bearer token per run.
package mcptools

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/klahr/dash/internal/model"
)

// ServerName is the chat's name for the Dash tools server; distinct from a
// user-registered "dash" (cmd/dash-mcp) and the permission prompt server.
const ServerName = "dash_board"

// ---- JSON-RPC ----

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Server answers MCP requests with a fixed tool list.
type Server struct {
	Name, Instructions string
	Tools              []Tool
	// Retry, if set, is asked after a tool fails; true runs the tool once more
	// (e.g. after logging in again).
	Retry func(error) bool
}

// ProtocolVersions are the MCP versions Server speaks, newest first.
var ProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// negotiate answers a client's protocol version: the same one if supported,
// else the newest this server has (the client may then disconnect).
func negotiate(v string) string {
	if slices.Contains(ProtocolVersions, v) {
		return v
	}
	return ProtocolVersions[0]
}

func (s *Server) Handle(req Request) (any, *Error) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		res := map[string]any{
			"protocolVersion": negotiate(p.ProtocolVersion),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": s.Name, "version": "1"},
		}
		if s.Instructions != "" {
			res["instructions"] = s.Instructions
		}
		return res, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		list := make([]map[string]any, 0, len(s.Tools))
		for _, t := range s.Tools {
			list = append(list, map[string]any{"name": t.Name, "description": t.Desc, "inputSchema": t.schema()})
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &Error{-32602, "invalid params"}
		}
		for _, t := range s.Tools {
			if t.Name == p.Name {
				return s.call(t, Args(p.Arguments)), nil
			}
		}
		return nil, &Error{-32602, "unknown tool " + p.Name}
	default:
		return nil, &Error{-32601, "method not found"}
	}
}

func (s *Server) call(t Tool, a Args) map[string]any {
	res, err := t.Run(a)
	if err != nil && s.Retry != nil && s.Retry(err) {
		res, err = t.Run(a)
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

func response(id json.RawMessage, result any, rerr *Error) map[string]any {
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if rerr != nil {
		resp["error"] = rerr
	} else {
		resp["result"] = result
	}
	return resp
}

// ServeStdio reads one JSON-RPC message per line from in and answers on out.
func (s *Server) ServeStdio(in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = enc.Encode(response(nil, nil, &Error{-32700, "parse error"}))
			continue
		}
		if len(req.ID) == 0 { // notification (e.g. notifications/initialized)
			continue
		}
		result, rerr := s.Handle(req)
		if err := enc.Encode(response(req.ID, result, rerr)); err != nil {
			return err
		}
	}
	return sc.Err()
}

// ServeHTTP answers one JSON-RPC message posted in r.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !slices.Contains(ProtocolVersions, v) {
		http.Error(w, "unsupported MCP-Protocol-Version "+v, http.StatusBadRequest)
		return
	}
	var req Request
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		_ = json.NewEncoder(w).Encode(response(nil, nil, &Error{-32700, "parse error"}))
		return
	}
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rerr := s.Handle(req)
	_ = json.NewEncoder(w).Encode(response(req.ID, result, rerr))
}

// ---- tools ----

type Args map[string]any

func (a Args) Str(k string) string {
	s, _ := a[k].(string)
	return strings.TrimSpace(s)
}

func (a Args) Num(k string, def int) int {
	if f, ok := a[k].(float64); ok {
		return int(f)
	}
	return def
}

// List returns the strings of an array argument (a lone string counts as one).
func (a Args) List(k string) []string {
	var out []string
	switch v := a[k].(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case []string:
		out = v
	case string:
		if v = strings.TrimSpace(v); v != "" {
			out = []string{v}
		}
	}
	return out
}

func (a Args) Flag(k string) bool {
	b, _ := a[k].(bool)
	return b
}

// FlagOr is Flag with a default for when k is missing.
func (a Args) FlagOr(k string, def bool) bool {
	if b, ok := a[k].(bool); ok {
		return b
	}
	return def
}

type Param struct {
	Name, Type, Desc string
	Required         bool
}

type Tool struct {
	Name, Desc string
	Params     []Param
	Run        func(a Args) (any, error)
}

func (t Tool) schema() map[string]any {
	props := map[string]any{}
	required := []string{}
	for _, p := range t.Params {
		prop := map[string]any{"type": p.Type, "description": p.Desc}
		if p.Type == "array" {
			prop["items"] = map[string]string{"type": "string"}
		}
		props[p.Name] = prop
		if p.Required {
			required = append(required, p.Name)
		}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

// ---- board tools ----

// NewTodo is a task to create.
type NewTodo struct {
	Title, Prompt string
	Priority      int
	Enqueue       bool     // queue it to run instead of keeping a draft
	DependsOn     []string // ids of tasks that must be merged before it starts
}

// Board is what the board tools need from Dash.
type Board interface {
	// Projects lists projects, and computers that couldn't be reached.
	Projects() (ps []model.Project, unreachable []string, err error)
	// Project finds a project by id or name.
	Project(ref string) (model.Project, error)
	Todos(projectID string) ([]model.Todo, error)
	Todo(id string) (model.Todo, error)
	CreateTodo(projectID string, t NewTodo) (model.Todo, error)
	// QueueTodo queues a task to run.
	QueueTodo(id string) (model.Todo, error)
	// Issues lists a project's issues, most severe first.
	Issues(projectID string) ([]model.Issue, error)
	// ReportIssue adds an issue, or updates the one with its fingerprint.
	ReportIssue(projectID string, is model.Issue) (model.Issue, model.UpsertResult, error)
}

// Options adapt the board tools to where they run.
type Options struct {
	// Project is used when a call names none (the project chat's project).
	Project string
	// Draft makes create_todo keep drafts unless called with draft: false.
	Draft bool
	// Todo is the running task the tools serve, if any: report_issue
	// credits issues to it ("agent:<todo>").
	Todo string
}

// Tool names the chat may call; ReadTools never change anything.
var (
	ReadTools  = []string{"list_projects", "list_todos", "get_todo", "list_issues"}
	BoardTools = []string{"list_projects", "list_todos", "get_todo", "create_todo", "queue_todo", "list_issues", "report_issue"}
	// IssueToolNames are what a running task gets (IssueTools).
	IssueToolNames = []string{"list_issues", "report_issue"}
)

// Tools returns list_projects, list_todos, get_todo, create_todo and
// queue_todo, and the IssueTools.
func Tools(b Board, o Options) []Tool {
	return append(boardTools(b, o), IssueTools(b, o)...)
}

func projectParam(o Options) (Param, func(b Board, a Args) (model.Project, error)) {
	p := Param{"project", "string", "Project name or id (see list_projects).", true}
	if o.Project != "" {
		p = Param{"project", "string", "Project name or id; defaults to this one.", false}
	}
	return p, func(b Board, a Args) (model.Project, error) {
		ref := a.Str("project")
		if ref == "" {
			ref = o.Project
		}
		return b.Project(ref)
	}
}

// IssueTools returns list_issues and report_issue: the project's known bugs,
// vulnerabilities and other problems, which the user triages into tasks.
func IssueTools(b Board, o Options) []Tool {
	pProject, project := projectParam(o)
	reportDesc := "Report a bug, vulnerability or other problem in the project to its Issues list, for the user to triage. " +
		"Use it for problems you notice that are outside what you were asked to do, instead of ignoring them or fixing them here. " +
		"Reporting the same problem again updates it rather than duplicating it."
	return []Tool{
		{"list_issues", "List the project's known issues (bugs, vulnerabilities, other problems), most severe first. Check it before reporting one.", []Param{pProject,
			{"status", "string", "Only this status: open, task_created, resolved or dismissed (default: open and task_created).", false},
			{"type", "string", "Only this type: bug, vuln or issue.", false}},
			func(a Args) (any, error) {
				p, err := project(b, a)
				if err != nil {
					return nil, err
				}
				is, err := b.Issues(p.ID)
				if err != nil {
					return nil, err
				}
				type row struct {
					ID       string            `json:"id"`
					Title    string            `json:"title"`
					Type     model.IssueType   `json:"type"`
					Severity model.Severity    `json:"severity"`
					Location string            `json:"location,omitempty"`
					Status   model.IssueStatus `json:"status"`
					Source   string            `json:"source"`
					Tasks    []string          `json:"tasks,omitempty"`
				}
				out := []row{}
				for _, i := range is {
					if st := a.Str("status"); st != "" {
						if string(i.Status) != st {
							continue
						}
					} else if i.Status != model.IssueOpen && i.Status != model.IssueTaskCreated {
						continue
					}
					if ty := a.Str("type"); ty != "" && string(i.Type) != ty {
						continue
					}
					out = append(out, row{i.ID, i.Title, i.Type, i.Severity, i.Location(), i.Status, i.Source, i.TodoIDs})
				}
				return out, nil
			}},
		{"report_issue", reportDesc, []Param{pProject,
			{"title", "string", "One line saying what is wrong.", true},
			{"type", "string", "bug, vuln (security vulnerability) or issue (anything else). Default issue.", false},
			{"severity", "string", "critical, high, medium, low or info. Default medium.", false},
			{"file", "string", "Path of the file, relative to the repository root.", false},
			{"line", "integer", "Line number in file.", false},
			{"details", "string", "Markdown: what happens, why it is wrong, how to reproduce.", false},
			{"fix", "string", "Markdown: how it could be fixed, if you know.", false}},
			func(a Args) (any, error) {
				p, err := project(b, a)
				if err != nil {
					return nil, err
				}
				source := "agent:chat"
				if o.Todo != "" {
					source = "agent:" + o.Todo
				}
				is, res, err := b.ReportIssue(p.ID, model.Issue{
					Title: a.Str("title"), Type: model.IssueType(a.Str("type")), Severity: model.Severity(a.Str("severity")),
					File: a.Str("file"), Line: a.Num("line", 0), Details: a.Str("details"), Fix: a.Str("fix"),
					Source: source,
				})
				if err != nil {
					return nil, err
				}
				msg := map[model.UpsertResult]string{
					model.UpsertNew:       "Reported as a new issue.",
					model.UpsertUpdated:   "This issue was already known; updated it.",
					model.UpsertKnown:     "This issue is already known; nothing changed.",
					model.UpsertReopened:  "This issue had been resolved; reopened it.",
					model.UpsertDismissed: "The user already dismissed this issue (" + is.DismissReason + "); leave it.",
				}[res]
				return fmt.Sprintf("%s id %s, status %s.", msg, is.ID, is.Status), nil
			}},
	}
}

func boardTools(b Board, o Options) []Tool {
	pProject := Param{"project", "string", "Project name or id (see list_projects).", true}
	if o.Project != "" {
		pProject = Param{"project", "string", "Project name or id (see list_projects); defaults to this chat's project.", false}
	}
	project := func(a Args) (model.Project, error) {
		ref := a.Str("project")
		if ref == "" {
			ref = o.Project
		}
		return b.Project(ref)
	}
	createDesc := "Create a task for Claude to work on. It is queued to run unless draft is true."
	draftDesc := "Keep it as a draft instead of queueing it."
	if o.Draft {
		createDesc = "Create a task for Claude to work on. It stays a draft on the board unless draft is false."
		draftDesc = "false queues it to run right away; only when the user asks for that (default true)."
	}
	return []Tool{
		{"list_projects", "List the projects on every computer in this Dash.", nil,
			func(a Args) (any, error) {
				ps, unreachable, err := b.Projects()
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
				}{Projects: []row{}, Unreachable: unreachable}
				for _, p := range ps {
					out.Projects = append(out.Projects, row{p.ID, p.Name, p.Machine, p.Path, p.DefaultBranch, p.MergeMode})
				}
				return out, nil
			}},
		{"list_todos", "List a project's tasks (id, title, status), optionally filtered by status.", []Param{pProject,
			{"status", "string", "Only this status: draft, queued, in_progress, review, merging, merged or failed.", false}},
			func(a Args) (any, error) {
				p, err := project(a)
				if err != nil {
					return nil, err
				}
				ts, err := b.Todos(p.ID)
				if err != nil {
					return nil, err
				}
				type row struct {
					ID        string           `json:"id"`
					Title     string           `json:"title"`
					Status    model.TodoStatus `json:"status"`
					Priority  int              `json:"priority"`
					DependsOn []string         `json:"depends_on,omitempty"`
					WaitingOn []string         `json:"waiting_on,omitempty"` // dependencies not merged yet
					Error     string           `json:"error,omitempty"`
				}
				out := []row{}
				for _, t := range ts {
					if st := a.Str("status"); st != "" && string(t.Status) != st {
						continue
					}
					var waiting []string
					for _, d := range t.Blockers(ts) {
						waiting = append(waiting, d.ID)
					}
					out = append(out, row{t.ID, t.Title, t.Status, t.Priority, t.DependsOn, waiting, t.Error})
				}
				return out, nil
			}},
		{"get_todo", "Show a task: prompt, status, dependencies (depends_on), feedback, test results, diff size, pull request.", []Param{PTodo},
			func(a Args) (any, error) {
				if a.Str("todo") == "" {
					return nil, errors.New("todo is required")
				}
				return b.Todo(a.Str("todo"))
			}},
		{"create_todo", createDesc, []Param{pProject,
			{"prompt", "string", "The task for Claude (the title alone is enough for small tasks).", false},
			{"title", "string", "Short title (generated from the prompt if empty).", false},
			{"priority", "integer", "Lower runs first (default 0).", false},
			{"depends_on", "array", "Ids of tasks in the same project that must be merged before this one starts; it waits queued until then.", false},
			{"draft", "boolean", draftDesc, false}},
			func(a Args) (any, error) {
				p, err := project(a)
				if err != nil {
					return nil, err
				}
				if a.Str("title") == "" && a.Str("prompt") == "" {
					return nil, errors.New("title or prompt is required")
				}
				return b.CreateTodo(p.ID, NewTodo{
					Title: a.Str("title"), Prompt: a.Str("prompt"),
					Priority: a.Num("priority", 0), Enqueue: !a.FlagOr("draft", o.Draft),
					DependsOn: a.List("depends_on"),
				})
			}},
		{"queue_todo", "Queue a draft task to run; only when the user asks to start or run it.", []Param{PTodo},
			func(a Args) (any, error) {
				if a.Str("todo") == "" {
					return nil, errors.New("todo is required")
				}
				t, err := b.Todo(a.Str("todo"))
				if err != nil {
					return nil, err
				}
				if t.Status != model.StatusDraft {
					return nil, fmt.Errorf("task %q is %s, not a draft; only drafts can be queued", t.ID, t.Status)
				}
				if t, err = b.QueueTodo(t.ID); err != nil {
					return nil, err
				}
				return struct {
					ID     string           `json:"id"`
					Title  string           `json:"title"`
					Status model.TodoStatus `json:"status"`
				}{t.ID, t.Title, t.Status}, nil
			}},
	}
}

// PTodo is the task id parameter.
var PTodo = Param{"todo", "string", "Task id.", true}

// ---- HTTP endpoint for chat runs ----

// Tokens holds the bearer tokens of running chat turns.
type Tokens struct {
	mu sync.Mutex
	m  map[string]tokenScope
}

// tokenScope is what a token's tools serve.
type tokenScope struct {
	project string // default project ("" for the general chat)
	todo    string // set for a running task, which only gets the IssueTools
}

// Register issues a token for one chat run; projectID is the default project
// for the tools ("" for none). Call release when the run ends.
func (t *Tokens) Register(projectID string) (token string, release func()) {
	return t.register(tokenScope{project: projectID})
}

// RegisterTask issues a token for one run of a task: its tools are the
// IssueTools for the task's project. Call release when the run ends.
func (t *Tokens) RegisterTask(projectID, todoID string) (token string, release func()) {
	return t.register(tokenScope{project: projectID, todo: todoID})
}

func (t *Tokens) register(sc tokenScope) (token string, release func()) {
	buf := make([]byte, 24)
	_, _ = rand.Read(buf)
	token = hex.EncodeToString(buf)
	t.mu.Lock()
	if t.m == nil {
		t.m = map[string]tokenScope{}
	}
	t.m[token] = sc
	t.mu.Unlock()
	return token, func() {
		t.mu.Lock()
		delete(t.m, token)
		t.mu.Unlock()
	}
}

func (t *Tokens) lookup(token string) (sc tokenScope, ok bool) {
	if t == nil {
		return sc, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	sc, ok = t.m[token]
	return
}

// Handler serves POST /mcp/dash for the chat's Claude Code runs.
type Handler struct {
	Tokens *Tokens
	Board  Board
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "this MCP server only accepts POST", http.StatusMethodNotAllowed)
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	var sc tokenScope
	if ok && token != "" {
		sc, ok = h.Tokens.lookup(token)
	}
	if !ok {
		http.Error(w, "unknown or expired token", http.StatusUnauthorized)
		return
	}
	srv := &Server{
		Name:         ServerName,
		Instructions: "The tasks on the Dash board. Each runs Claude Code in its own git worktree once queued.",
		Tools:        Tools(h.Board, Options{Project: sc.project, Draft: true}),
	}
	if sc.todo != "" {
		srv.Instructions = "The project's Issues list: known bugs, vulnerabilities and other problems the user triages into tasks."
		srv.Tools = IssueTools(h.Board, Options{Project: sc.project, Todo: sc.todo})
	}
	srv.ServeHTTP(w, r)
}

// ServerConfig is the --mcp-config entry for the endpoint at baseURL.
func ServerConfig(baseURL, token string) map[string]any {
	return map[string]any{
		"type":    "http",
		"url":     strings.TrimRight(baseURL, "/") + "/mcp/dash",
		"headers": map[string]string{"Authorization": "Bearer " + token},
	}
}
