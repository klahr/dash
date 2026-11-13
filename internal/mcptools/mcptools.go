// Package mcptools serves Dash's task board as MCP tools: over stdio for
// cmd/dash-mcp, and over streamable HTTP (JSON responses) for the chat's
// Claude Code runs, authenticated by a bearer token per run.
package mcptools

import (
	"bufio"
	"context"
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

// Has reports whether k was passed (and isn't null), to tell "not passed"
// from an empty value.
func (a Args) Has(k string) bool {
	v, ok := a[k]
	return ok && v != nil
}

// OptStr is Str, or nil when k wasn't passed.
func (a Args) OptStr(k string) *string {
	if !a.Has(k) {
		return nil
	}
	s := a.Str(k)
	return &s
}

// OptNum is Num, or nil when k wasn't passed.
func (a Args) OptNum(k string) *int {
	if !a.Has(k) {
		return nil
	}
	n := a.Num(k, 0)
	return &n
}

// OptList is List, or nil when k wasn't passed; an empty array gives an
// empty (non-nil) list.
func (a Args) OptList(k string) *[]string {
	if !a.Has(k) {
		return nil
	}
	l := a.List(k)
	if l == nil {
		l = []string{}
	}
	return &l
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

// priorityDesc describes a task's priority to Claude.
const priorityDesc = "Prioritised tasks run first, lower before higher (0 is a real priority); unprioritised tasks run after all of them, oldest first."

// ---- board tools ----

// NewTodo is a task to create.
type NewTodo struct {
	Title, Prompt string
	Priority      *int     // nil: unprioritised (runs after every prioritised task)
	Enqueue       bool     // queue it to run instead of keeping a draft
	DependsOn     []string // ids of tasks that must be merged before it starts
	Model         string   // claude --model: an alias or model id; empty: the default
}

// TodoUpdate changes a task; nil fields stay as they are. ClearPriority
// unsets the priority (Priority must then be nil).
type TodoUpdate struct {
	Title, Prompt *string
	Priority      *int
	ClearPriority bool
	DependsOn     *[]string // replaces the list; empty clears it
	Model         *string   // "" goes back to the default model
}

// NewRelease is a release to tag: a version ("1.4.0", or a tag in the
// project's tag format) or a bump (patch, minor or major) of the latest
// release, on a commit (default: the tip of origin's default branch).
type NewRelease struct {
	Version, Bump, SHA, Notes string
}

// Release is a release tag that was created and pushed.
type Release struct {
	Tag      string `json:"tag"`
	SHA      string `json:"sha"`
	Previous string `json:"previous,omitempty"` // the release before it
	Notes    string `json:"notes,omitempty"`
	URL      string `json:"url,omitempty"`   // its GitHub release page
	Build    string `json:"build,omitempty"` // "building" while Dash follows its workflow runs
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
	// UpdateTodo changes a task's title, prompt, priority or dependencies.
	UpdateTodo(id string, u TodoUpdate) (model.Todo, error)
	// DeleteTodo deletes a task and its worktree.
	DeleteTodo(id string) error
	// Issues lists a project's issues, most severe first.
	Issues(projectID string) ([]model.Issue, error)
	// ReportIssue adds an issue, or updates the one with its fingerprint.
	ReportIssue(projectID string, is model.Issue) (model.Issue, model.UpsertResult, error)
	// CreateRelease tags a commit in the project's tag format and pushes the tag.
	CreateRelease(projectID string, r NewRelease) (Release, error)
	// ReadChecklist reads the project's TODO.md as a checklist.
	ReadChecklist(projectID string) (Checklist, error)
	// ChangeChecklist changes TODO.md and saves it: committed straight onto
	// the default branch (or written in the checkout when git doesn't track it).
	ChangeChecklist(projectID string, c ChecklistChange) (ChecklistSaved, error)
	// MoveChecklistItem creates a task from an item, then removes the item.
	MoveChecklistItem(projectID string, m ChecklistMove) (ChecklistMoved, error)
}

// Restarter is a Board that stops tasks and queues them again, for
// stop_todo, restart_todo and queue_todo's fresh.
type Restarter interface {
	// StopTodo stops a queued or running task like the board's Cancel, and
	// returns it once it has stopped: a running one failed ("canceled"), a
	// queued one back in draft.
	StopTodo(id string) (model.Todo, error)
	// RequeueTodo queues a draft or failed task. It continues in its
	// worktree and Claude session, with note (if any) as a new note for
	// Claude; fresh drops them and the earlier notes first, so it starts
	// over from the prompt.
	RequeueTodo(id string, fresh bool, note string) (model.Todo, error)
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
	ReadTools  = []string{"list_projects", "list_todos", "get_todo", "list_issues", "read_try_terminal", "read_checklist"}
	BoardTools = []string{"list_projects", "list_todos", "get_todo", "create_todo", "queue_todo", "stop_todo", "restart_todo", "update_todo", "create_release", "delete_todo",
		"read_checklist", "add_checklist_item", "check_checklist_item", "edit_checklist_item", "delete_checklist_item", "reorder_checklist_item", "move_checklist_item_to_task",
		"list_issues", "report_issue", "read_try_terminal"}
	// AskTools are board tools a chat never gets without asking: they publish
	// something (a release tag pushed to origin, or a TODO.md change committed
	// onto the default branch, which may be pushed to origin).
	AskTools = []string{"create_release",
		"add_checklist_item", "check_checklist_item", "edit_checklist_item", "delete_checklist_item", "reorder_checklist_item", "move_checklist_item_to_task"}
	// IssueToolNames are what a running task gets (IssueTools).
	IssueToolNames = []string{"list_issues", "report_issue"}
)

// Tools returns list_projects, list_todos, get_todo, create_todo,
// queue_todo, stop_todo and restart_todo (when b is a Restarter),
// update_todo, create_release and delete_todo; the checklist
// tools on the project's TODO.md (read_checklist, add_checklist_item,
// check_checklist_item, edit_checklist_item, delete_checklist_item,
// reorder_checklist_item and move_checklist_item_to_task); the IssueTools; and read_try_terminal when
// b reads Try terminals (TryReader).
func Tools(b Board, o Options) []Tool {
	tools := append(boardTools(b, o), IssueTools(b, o)...)
	if tr, ok := b.(TryReader); ok {
		tools = append(tools, TryTool(b, tr, o))
	}
	return tools
}

// TryOutput is the end of a project's Try terminal and what it runs.
type TryOutput struct {
	Runner   string `json:"runner"`
	Commit   string `json:"commit"`
	Subject  string `json:"subject"`
	Ref      string `json:"ref,omitempty"` // what was asked for; "" for the default branch's tip
	Branch   string `json:"branch"`
	State    string `json:"state"` // live, or crashed once its command ended
	ExitCode *int   `json:"exit_code,omitempty"`
	Text     string `json:"output"`
}

// TryReader is a Board that can read a project's Try terminal: the
// terminal the user tries the project's CLI in, on a runner, at a commit.
type TryReader interface {
	// ReadTry returns the last lines of the project's Try terminal as plain text.
	ReadTry(projectID string, lines int) (TryOutput, error)
}

// TryTool returns read_try_terminal.
func TryTool(b Board, tr TryReader, o Options) Tool {
	pProject, project := projectParam(o)
	return Tool{"read_try_terminal",
		"Read the last lines of the project's Try terminal as plain text: the terminal next to the chat where the user tries the project's CLI, " +
			"with which runner and commit it runs. Call it whenever the user refers to what just happened there (\"that crashed\", \"the last command\", \"this error\"), " +
			"and before filing what they found with report_issue or create_todo, to quote the command and its output and name the commit.",
		[]Param{pProject, {"lines", "integer", "How many of the last lines to return (default 80, at most 5000).", false}},
		func(a Args) (any, error) {
			p, err := project(b, a)
			if err != nil {
				return nil, err
			}
			return tr.ReadTry(p.ID, a.Num("lines", 80))
		}}
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
			{"type", "string", "Only this type: bug, vuln or issue.", false},
			{"kind", "string", "Only issues filed by this kind of scan: " + strings.Join(model.ScanKinds(), ", ") + " (aliases such as perf, a11y or dead-code work too).", false}},
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
					Kind     string            `json:"kind,omitempty"`
					Tasks    []string          `json:"tasks,omitempty"`
				}
				kind := model.NormalizeScanKind(a.Str("kind"))
				if kind != "" && !slices.Contains(model.ScanKinds(), kind) {
					return nil, fmt.Errorf("unknown kind %q (%s)", a.Str("kind"), strings.Join(model.ScanKinds(), ", "))
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
					if kind != "" && i.ScanKind != kind {
						continue
					}
					out = append(out, row{i.ID, i.Title, i.Type, i.Severity, i.Location(), i.Status, i.Source, i.ScanKind, i.TodoIDs})
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
	modelDesc := "Claude model to run the task on: opus for hard tasks, sonnet or haiku for small ones, or a full model id. " +
		"Omit it for the default."
	if o.Draft {
		createDesc = "Create a task for Claude to work on. It stays a draft on the board unless draft is false."
		draftDesc = "false queues it to run right away; only when the user asks for that (default true)."
	}
	queueDesc := "Queue a draft or failed task to run; only when the user asks to start, run or retry it. " +
		"A failed task continues in its worktree and Claude session, like dragging its card from Review to Queued."
	queueParams := []Param{PTodo}
	restarter, _ := b.(Restarter)
	if restarter != nil {
		queueDesc = "Queue a draft or failed task to run; only when the user asks to start, run or retry it."
		queueParams = append(queueParams, Param{"fresh", "boolean", freshDesc, false})
	}
	tools := []Tool{
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
					Priority  *int             `json:"priority"` // null: unprioritised
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
		{"get_todo", "Show a task: prompt, status, dependencies (depends_on), feedback, test results, diff size, pull request, and each Claude run's kind, tokens, turns and cost (run_stats).", []Param{PTodo},
			func(a Args) (any, error) {
				if a.Str("todo") == "" {
					return nil, errors.New("todo is required")
				}
				return b.Todo(a.Str("todo"))
			}},
		{"create_todo", createDesc, []Param{pProject,
			{"prompt", "string", "The task for Claude (the title alone is enough for small tasks).", false},
			{"title", "string", "Short title (generated from the prompt if empty).", false},
			{"priority", "integer", priorityDesc + " Omit it to leave the task unprioritised.", false},
			{"depends_on", "array", "Ids of tasks in the same project that must be merged before this one starts; it waits queued until then.", false},
			{"model", "string", modelDesc, false},
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
					Priority: a.OptNum("priority"), Enqueue: !a.FlagOr("draft", o.Draft),
					DependsOn: a.List("depends_on"), Model: a.Str("model"),
				})
			}},
		{"queue_todo", queueDesc, queueParams,
			func(a Args) (any, error) {
				if a.Str("todo") == "" {
					return nil, errors.New("todo is required")
				}
				t, err := b.Todo(a.Str("todo"))
				if err != nil {
					return nil, err
				}
				if t.Status != model.StatusDraft && t.Status != model.StatusFailed {
					return nil, fmt.Errorf("task %q is %s; only draft and failed tasks can be queued", t.ID, t.Status)
				}
				if a.Flag("fresh") {
					t, err = restarter.RequeueTodo(t.ID, true, "")
				} else {
					t, err = b.QueueTodo(t.ID)
				}
				if err != nil {
					return nil, err
				}
				return queued(t), nil
			}},
	}
	if restarter != nil {
		tools = append(tools, restartTools(b, restarter)...)
	}
	tools = append(tools, []Tool{
		{"update_todo", "Change a task's title, prompt, priority, dependencies or model; only the given fields change. " +
			"The prompt can only be changed while the task is a draft, queued or failed.", []Param{PTodo,
			{"title", "string", "New short title.", false},
			{"prompt", "string", "New task for Claude.", false},
			{"priority", "integer", priorityDesc, false},
			{"clear_priority", "boolean", "Unset the priority: the task runs after every prioritised one, oldest first.", false},
			{"depends_on", "array", "Ids of tasks in the same project that must be merged before this one starts; replaces the list, an empty array clears it.", false},
			{"model", "string", modelDesc + " An empty string goes back to the default; a new model applies from the task's next run.", false}},
			func(a Args) (any, error) {
				if a.Str("todo") == "" {
					return nil, errors.New("todo is required")
				}
				u := TodoUpdate{Title: a.OptStr("title"), Prompt: a.OptStr("prompt"), Priority: a.OptNum("priority"), DependsOn: a.OptList("depends_on"), Model: a.OptStr("model")}
				if v, ok := a["priority"]; ok && v == nil {
					u.ClearPriority = true // priority: null
				}
				if a.Flag("clear_priority") {
					if u.Priority != nil {
						return nil, errors.New("pass priority or clear_priority, not both")
					}
					u.ClearPriority = true
				}
				if u.Title == nil && u.Prompt == nil && u.Priority == nil && !u.ClearPriority && u.DependsOn == nil && u.Model == nil {
					return nil, errors.New("nothing to change: pass title, prompt, priority, clear_priority, depends_on or model")
				}
				t, err := b.UpdateTodo(a.Str("todo"), u)
				if err != nil {
					return nil, err
				}
				deps := t.DependsOn
				if deps == nil {
					deps = []string{}
				}
				return struct {
					ID        string           `json:"id"`
					Title     string           `json:"title"`
					Status    model.TodoStatus `json:"status"`
					Priority  *int             `json:"priority"`
					DependsOn []string         `json:"depends_on"`
					Model     string           `json:"model,omitempty"`
				}{t.ID, t.Title, t.Status, t.Priority, deps, t.Model}, nil
			}},
		{"create_release", "Tag a release: an annotated tag in the project's tag format (e.g. vX.Y.Z), pushed to origin, " +
			"which starts the repository's release workflow. Only when the user asks for a release. Give version or bump.", []Param{pProject,
			{"version", "string", "The version, e.g. \"1.4.0\" (or a full tag in the project's format).", false},
			{"bump", "string", "Instead of version: patch, minor or major, from the latest release tag (the first release is 0.1.0).", false},
			{"sha", "string", "Commit to tag; it must be on origin (default: the tip of origin's default branch).", false},
			{"notes", "string", "Release notes for the tag message (default: the Dash tasks merged since the previous release).", false}},
			func(a Args) (any, error) {
				p, err := project(a)
				if err != nil {
					return nil, err
				}
				if a.Str("version") == "" && a.Str("bump") == "" {
					return nil, errors.New("give a version (e.g. 1.4.0) or a bump (patch, minor or major)")
				}
				return b.CreateRelease(p.ID, NewRelease{Version: a.Str("version"), Bump: a.Str("bump"), SHA: a.Str("sha"), Notes: a.Str("notes")})
			}},
		{"delete_todo", "Delete a draft, queued or failed task; only when the user asks to remove it (e.g. a draft made by mistake).", []Param{PTodo},
			func(a Args) (any, error) {
				if a.Str("todo") == "" {
					return nil, errors.New("todo is required")
				}
				t, err := b.Todo(a.Str("todo"))
				if err != nil {
					return nil, err
				}
				switch t.Status {
				case model.StatusDraft, model.StatusQueued, model.StatusFailed:
				default:
					return nil, fmt.Errorf("task %q is %s; only draft, queued or failed tasks can be deleted here", t.ID, t.Status)
				}
				if err := b.DeleteTodo(t.ID); err != nil {
					return nil, err
				}
				return fmt.Sprintf("Deleted task %s (%s).", t.ID, t.Title), nil
			}},
	}...)
	return append(tools, checklistTools(b, o, project, pProject)...)
}

// freshDesc is the fresh parameter of queue_todo and restart_todo.
const freshDesc = "true starts over: a new worktree from the default branch and a new Claude session, from the prompt alone, without earlier notes for Claude. " +
	"false (the default) continues in the task's worktree and Claude session, " +
	"like dragging the card from Review to Queued."

// queued is what queue_todo and restart_todo answer.
func queued(t model.Todo) any {
	return struct {
		ID     string           `json:"id"`
		Title  string           `json:"title"`
		Status model.TodoStatus `json:"status"`
	}{t.ID, t.Title, t.Status}
}

// restartTools returns stop_todo and restart_todo.
func restartTools(b Board, r Restarter) []Tool {
	return []Tool{
		{"stop_todo", "Stop a queued or running task, like Cancel on the board: a running one ends failed (\"canceled\"), keeping its worktree " +
			"and Claude session; a queued one goes back to draft. Only when the user asks to stop or restart a task.", []Param{PTodo},
			func(a Args) (any, error) {
				if a.Str("todo") == "" {
					return nil, errors.New("todo is required")
				}
				t, err := b.Todo(a.Str("todo"))
				if err != nil {
					return nil, err
				}
				if t.Status != model.StatusQueued && t.Status != model.StatusInProgress {
					return nil, fmt.Errorf("task %q is %s; only queued or running tasks can be stopped", t.ID, t.Status)
				}
				if t, err = r.StopTodo(t.ID); err != nil {
					return nil, err
				}
				return struct {
					ID     string           `json:"id"`
					Title  string           `json:"title"`
					Status model.TodoStatus `json:"status"`
					Error  string           `json:"error,omitempty"`
				}{t.ID, t.Title, t.Status, t.Error}, nil
			}},
		{"restart_todo", "Run a running, queued or failed task again in one call: stop it if needed, change its prompt or title if given, and queue it. " +
			"Only when the user asks to restart or rerun a task.", []Param{PTodo,
			{"prompt", "string", "New task for Claude.", false},
			{"title", "string", "New short title.", false},
			{"fresh", "boolean", freshDesc + " Without fresh, a new prompt reaches Claude as a note in its session.", false}},
			func(a Args) (any, error) {
				if a.Str("todo") == "" {
					return nil, errors.New("todo is required")
				}
				t, err := b.Todo(a.Str("todo"))
				if err != nil {
					return nil, err
				}
				switch t.Status {
				case model.StatusInProgress, model.StatusQueued:
					if t, err = r.StopTodo(t.ID); err != nil {
						return nil, err
					}
				case model.StatusFailed:
				default:
					return nil, fmt.Errorf("task %q is %s; only running, queued or failed tasks can be restarted", t.ID, t.Status)
				}
				u := TodoUpdate{Title: a.OptStr("title"), Prompt: a.OptStr("prompt")}
				if u.Title != nil || u.Prompt != nil {
					if _, err := b.UpdateTodo(t.ID, u); err != nil {
						return nil, err
					}
				}
				fresh, note := a.Flag("fresh"), ""
				if u.Prompt != nil && !fresh && (t.SessionID != "" || t.Runner != "") {
					// A continued session never sees the prompt again.
					note = "The task's instructions have changed. Follow these from now on:\n\n" + *u.Prompt
				}
				if t, err = r.RequeueTodo(t.ID, fresh, note); err != nil {
					return nil, err
				}
				return queued(t), nil
			}},
	}
}

// FleetReadTools are the FleetTools that only look.
var FleetReadTools = []string{"list_projects", "list_todos", "get_todo", "list_issues", "get_diff", "get_log", "get_project", "read_checklist"}

// ReadOnly returns tools with those not in read made to fail with err: a
// read-only caller still sees every tool, and learns why one doesn't work.
func ReadOnly(tools []Tool, read []string, err error) []Tool {
	out := make([]Tool, len(tools))
	for i, t := range tools {
		if !slices.Contains(read, t.Name) {
			t.Run = func(Args) (any, error) { return nil, err }
		}
		out[i] = t
	}
	return out
}

// PTodo is the task id parameter.
var PTodo = Param{"todo", "string", "Task id.", true}

// ---- HTTP endpoint for chat runs ----

// Tokens holds the bearer tokens of running chat turns.
type Tokens struct {
	mu sync.Mutex
	m  map[string]Scope
}

// Scope is what a token's tools serve and for whom.
type Scope struct {
	// Org and User are who the run acts as: the chat's user in their active
	// org, or the task's creator in the task's org. Both are required; the
	// tools only reach Org.
	Org, User int64
	Project   string // default project ("" for the general chat)
	Todo      string // set for a running task, which only gets the IssueTools
	// Hub marks a runner's run of its hub's work (a hub task, or a chat the
	// hub asked for): its tools are the hub's board, never this Dash's.
	// HubProject and HubTodo are then the hub's ids for the run's project
	// and task.
	Hub                 bool
	HubProject, HubTodo string
}

type callerKey struct{}

type hubKey struct{}

// ForHub makes chat turns run with ctx, which run for this runner's hub,
// serve the hub's board: hubProject is the hub's id of the chat's project
// ("" for the general chat).
func ForHub(ctx context.Context, hubProject string) context.Context {
	return context.WithValue(ctx, hubKey{}, hubProject)
}

// HubFrom is the hub project ForHub put in ctx; ok is false for a turn
// that isn't the hub's.
func HubFrom(ctx context.Context) (hubProject string, ok bool) {
	hubProject, ok = ctx.Value(hubKey{}).(string)
	return
}

// Caller is who a run acts as: a user in an org.
type Caller = model.Owner

// WithCaller makes chat turns run with ctx serve the Dash tools as c.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom is the caller WithCaller put in ctx.
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok && c.Valid()
}

// Register issues a token for one chat run of c; projectID is the default
// project for the tools ("" for none). Call release when the run ends.
func (t *Tokens) Register(c Caller, projectID string) (token string, release func()) {
	return t.register(Scope{Org: c.Org, User: c.User, Project: projectID})
}

// RegisterTask issues a token for one run of a task, acting as c (the
// task's user and org): its tools are the IssueTools for the task's project.
// Call release when the run ends.
func (t *Tokens) RegisterTask(c Caller, projectID, todoID string) (token string, release func()) {
	return t.register(Scope{Org: c.Org, User: c.User, Project: projectID, Todo: todoID})
}

// RegisterHub issues a token for one chat run of c that this runner runs
// for its hub: its tools are the hub's board, defaulting to the hub's
// project hubProject. Call release when the run ends.
func (t *Tokens) RegisterHub(c Caller, hubProject string) (token string, release func()) {
	return t.register(Scope{Org: c.Org, User: c.User, Hub: true, HubProject: hubProject})
}

func (t *Tokens) register(sc Scope) (token string, release func()) {
	buf := make([]byte, 24)
	_, _ = rand.Read(buf)
	token = hex.EncodeToString(buf)
	t.mu.Lock()
	if t.m == nil {
		t.m = map[string]Scope{}
	}
	t.m[token] = sc
	t.mu.Unlock()
	return token, func() {
		t.mu.Lock()
		delete(t.m, token)
		t.mu.Unlock()
	}
}

func (t *Tokens) lookup(token string) (sc Scope, ok bool) {
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
	// Board is the board a token's scope may use: its org's, as its user.
	Board func(Scope) (Board, error)
	// Hub, if set, serves a scope's calls on another Dash (a runner's hub)
	// and reports whether it did; a scope it doesn't take is served here.
	// A Hub scope (Scope.Hub) is never served here.
	Hub func(w http.ResponseWriter, r *http.Request, sc Scope) bool
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "this MCP server only accepts POST", http.StatusMethodNotAllowed)
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	var sc Scope
	if ok && token != "" {
		sc, ok = h.Tokens.lookup(token)
	}
	if !ok {
		http.Error(w, "unknown or expired token", http.StatusUnauthorized)
		return
	}
	if h.Hub != nil && h.Hub(w, r, sc) {
		return
	}
	if sc.Hub {
		// The hub's work changes the hub's board or nothing: a copy here
		// would be on a board nobody sees.
		http.Error(w, "this run's board is its hub's, which this Dash can't reach", http.StatusBadGateway)
		return
	}
	h.Serve(w, r, sc)
}

// Serve answers r with the tools of sc, which the caller vouches for.
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request, sc Scope) {
	if !(Caller{Org: sc.Org, User: sc.User}).Valid() { // every run acts as a user in an org
		http.Error(w, "this run acts for no user in no organization", http.StatusForbidden)
		return
	}
	board, err := h.Board(sc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	srv := &Server{
		Name:         ServerName,
		Instructions: "The tasks on the Dash board. Each runs Claude Code in its own git worktree once queued.",
		Tools:        Tools(board, Options{Project: sc.Project, Draft: true}),
	}
	if sc.Todo != "" {
		srv.Instructions = "The project's Issues list: known bugs, vulnerabilities and other problems the user triages into tasks."
		srv.Tools = IssueTools(board, Options{Project: sc.Project, Todo: sc.Todo})
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
