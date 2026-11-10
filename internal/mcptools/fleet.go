package mcptools

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/klahr/dash/internal/model"
)

// API calls the REST API of the Dash a client logged in to (the hub). Paths
// start with a computer's prefix: "" for the hub, "/peer/<name>" for another
// computer in its dashboard. out receives the JSON answer, or the text for a
// *string.
type API interface {
	Call(method, path string, body, out any) error
}

// ErrUnauthorized, from API.Call, stops a search across computers.
var ErrUnauthorized = errors.New("not logged in")

// Computer is one computer's task board, as the fleet tools use it.
type Computer interface {
	Projects() ([]model.Project, error)
	// UpdateProject changes a project's settings (ProjectSettings).
	UpdateProject(id string, changes map[string]any) (model.Project, error)
	Todos(projectID string) ([]model.Todo, error)
	CreateTodo(projectID string, t NewTodo) (model.Todo, error)
	Todo(id string) (model.Todo, error)
	// Act moves a task: verb is enqueue, cancel, approve or rework (with
	// feedback).
	Act(id, verb, feedback string) error
	UpdateTodo(id string, u TodoUpdate) (model.Todo, error)
	DeleteTodo(id string) error
	Issues(projectID string) ([]model.Issue, error)
	ReportIssue(projectID string, is model.Issue) (model.Issue, model.UpsertResult, error)
	// Text is a task's "diff" or "log".
	Text(id, kind string) (string, error)
	CreateRelease(projectID string, r NewRelease) (Release, error)
	ReadChecklist(projectID string) (Checklist, error)
	ChangeChecklist(projectID string, c ChecklistChange) (ChecklistSaved, error)
	MoveChecklistItem(projectID string, m ChecklistMove) (ChecklistMoved, error)
}

// Machine is one computer of a fleet. Board is unset while it is
// unreachable (OK false, Error says why).
type Machine struct {
	Name  string
	OK    bool
	Error string
	Board Computer
}

// Fleet is the task board of every computer in a dashboard. It serves both
// cmd/dash-mcp (over the hub's REST API) and the hub's own /mcp endpoint
// (Computers: called in-process, as the signed-in user).
type Fleet struct {
	API API
	// Computers, if set, lists the computers instead of asking API.
	Computers func() ([]Machine, error)
}

// FleetInstructions describe the FleetTools to the model.
const FleetInstructions = "Dash runs Claude Code on tasks in parallel, each in its own git worktree. " +
	"A task goes draft → queued → in_progress → review → merging → merged (or failed). " +
	"Review a task with get_todo and get_diff, then approve_todo or rework_todo with feedback."

type machine struct {
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
	OK     bool   `json:"ok"`
	Error  string `json:"error"`
}

func (f Fleet) machines() ([]Machine, error) {
	if f.Computers != nil {
		return f.Computers()
	}
	var ms []machine
	if err := f.API.Call("GET", "/api/machines", nil, &ms); err != nil {
		return nil, err
	}
	out := make([]Machine, len(ms))
	for i, m := range ms {
		out[i] = Machine{Name: m.Name, OK: m.OK, Error: m.Error}
		if m.OK {
			out[i].Board = RESTComputer{API: f.API, Prefix: m.Prefix}
		}
	}
	return out, nil
}

// projects lists every reachable computer's projects, tagged with the
// computer's name, and the computer each lives on.
func (f Fleet) projects() (ps []model.Project, on map[string]Computer, unreachable []string, err error) {
	ms, err := f.machines()
	if err != nil {
		return nil, nil, nil, err
	}
	on = map[string]Computer{}
	for _, m := range ms {
		var mps []model.Project
		if m.OK && m.Board != nil {
			mps, err = m.Board.Projects()
			if errors.Is(err, ErrUnauthorized) {
				return nil, nil, nil, err
			}
			if err != nil {
				m.OK, m.Error = false, err.Error()
			}
		} else {
			m.OK = false
		}
		if !m.OK {
			unreachable = append(unreachable, m.Name+": "+m.Error)
			continue
		}
		for _, p := range mps {
			p.Machine = m.Name
			on[p.ID] = m.Board
			ps = append(ps, p)
		}
	}
	return ps, on, unreachable, nil
}

func (f Fleet) Projects() ([]model.Project, []string, error) {
	ps, _, unreachable, err := f.projects()
	return ps, unreachable, err
}

// project finds a project by id or name on any computer.
func (f Fleet) project(ref string) (model.Project, Computer, error) {
	ps, on, _, err := f.projects()
	if err != nil {
		return model.Project{}, nil, err
	}
	for _, p := range ps {
		if p.ID == ref || strings.EqualFold(p.Name, ref) {
			return p, on[p.ID], nil
		}
	}
	return model.Project{}, nil, fmt.Errorf("no project %q (see list_projects)", ref)
}

func (f Fleet) Project(ref string) (model.Project, error) {
	p, _, err := f.project(ref)
	return p, err
}

// UpdateProject changes a project's settings on its computer.
func (f Fleet) UpdateProject(ref string, changes map[string]any) (model.Project, error) {
	p, c, err := f.project(ref)
	if err != nil {
		return model.Project{}, err
	}
	out, err := c.UpdateProject(p.ID, changes)
	if err != nil {
		return model.Project{}, err
	}
	out.Machine = p.Machine
	return out, nil
}

func (f Fleet) Todos(projectID string) ([]model.Todo, error) {
	p, c, err := f.project(projectID)
	if err != nil {
		return nil, err
	}
	return c.Todos(p.ID)
}

func (f Fleet) CreateTodo(projectID string, t NewTodo) (model.Todo, error) {
	p, c, err := f.project(projectID)
	if err != nil {
		return model.Todo{}, err
	}
	return c.CreateTodo(p.ID, t)
}

// todo finds a task on whichever computer has it, and that computer.
func (f Fleet) todo(id string) (model.Todo, Computer, error) {
	if id == "" {
		return model.Todo{}, nil, errors.New("todo is required")
	}
	ms, err := f.machines()
	if err != nil {
		return model.Todo{}, nil, err
	}
	for _, m := range ms {
		if !m.OK || m.Board == nil {
			continue
		}
		t, err := m.Board.Todo(id)
		if err == nil {
			return t, m.Board, nil
		}
		if errors.Is(err, ErrUnauthorized) {
			return model.Todo{}, nil, err
		}
	}
	return model.Todo{}, nil, fmt.Errorf("no task %q", id)
}

func (f Fleet) Todo(id string) (model.Todo, error) {
	t, _, err := f.todo(id)
	return t, err
}

// Action moves a task (enqueue, cancel, approve, rework) and returns it
// afterwards.
func (f Fleet) Action(id, verb, feedback string) (model.Todo, error) {
	_, c, err := f.todo(id)
	if err != nil {
		return model.Todo{}, err
	}
	if err := c.Act(id, verb, feedback); err != nil {
		return model.Todo{}, err
	}
	return c.Todo(id)
}

func (f Fleet) QueueTodo(id string) (model.Todo, error) { return f.Action(id, "enqueue", "") }

func (f Fleet) UpdateTodo(id string, u TodoUpdate) (model.Todo, error) {
	_, c, err := f.todo(id)
	if err != nil {
		return model.Todo{}, err
	}
	return c.UpdateTodo(id, u)
}

func (f Fleet) DeleteTodo(id string) error {
	_, c, err := f.todo(id)
	if err != nil {
		return err
	}
	return c.DeleteTodo(id)
}

func (f Fleet) Issues(projectID string) ([]model.Issue, error) {
	p, c, err := f.project(projectID)
	if err != nil {
		return nil, err
	}
	return c.Issues(p.ID)
}

func (f Fleet) ReportIssue(projectID string, is model.Issue) (model.Issue, model.UpsertResult, error) {
	p, c, err := f.project(projectID)
	if err != nil {
		return model.Issue{}, "", err
	}
	return c.ReportIssue(p.ID, is)
}

func (f Fleet) CreateRelease(projectID string, r NewRelease) (Release, error) {
	p, c, err := f.project(projectID)
	if err != nil {
		return Release{}, err
	}
	return c.CreateRelease(p.ID, r)
}

func (f Fleet) ReadChecklist(projectID string) (Checklist, error) {
	p, c, err := f.project(projectID)
	if err != nil {
		return Checklist{}, err
	}
	return c.ReadChecklist(p.ID)
}

func (f Fleet) ChangeChecklist(projectID string, ch ChecklistChange) (ChecklistSaved, error) {
	p, c, err := f.project(projectID)
	if err != nil {
		return ChecklistSaved{}, err
	}
	return c.ChangeChecklist(p.ID, ch)
}

func (f Fleet) MoveChecklistItem(projectID string, m ChecklistMove) (ChecklistMoved, error) {
	p, c, err := f.project(projectID)
	if err != nil {
		return ChecklistMoved{}, err
	}
	return c.MoveChecklistItem(p.ID, m)
}

// Text returns a task's diff or log, cut to max characters (from the end if tail).
func (f Fleet) Text(id, kind string, max int, tail bool) (string, error) {
	_, c, err := f.todo(id)
	if err != nil {
		return "", err
	}
	out, err := c.Text(id, kind)
	if err != nil || max <= 0 || len(out) <= max {
		return out, err
	}
	if tail {
		return fmt.Sprintf("[…%d earlier characters omitted]\n", len(out)-max) + out[len(out)-max:], nil
	}
	return out[:max] + fmt.Sprintf("\n[…truncated, %d more characters]", len(out)-max), nil
}

// RESTComputer is a computer reached through the hub's REST API: Prefix is
// "" for the hub, "/peer/<name>" for another computer in its dashboard.
type RESTComputer struct {
	API    API
	Prefix string
}

func (c RESTComputer) Projects() ([]model.Project, error) {
	var ps []model.Project
	return ps, c.API.Call("GET", c.Prefix+"/api/projects", nil, &ps)
}

func (c RESTComputer) UpdateProject(id string, changes map[string]any) (model.Project, error) {
	var out model.Project
	return out, c.API.Call("PATCH", c.Prefix+"/api/projects/"+url.PathEscape(id), changes, &out)
}

func (c RESTComputer) Todos(projectID string) ([]model.Todo, error) {
	var ts []model.Todo
	return ts, c.API.Call("GET", c.Prefix+"/api/projects/"+url.PathEscape(projectID)+"/todos", nil, &ts)
}

func (c RESTComputer) CreateTodo(projectID string, t NewTodo) (model.Todo, error) {
	var out model.Todo
	return out, c.API.Call("POST", c.Prefix+"/api/projects/"+url.PathEscape(projectID)+"/todos", map[string]any{
		"title": t.Title, "prompt": t.Prompt, "priority": t.Priority, "enqueue": t.Enqueue,
		"depends_on": t.DependsOn, "model": t.Model,
	}, &out)
}

func (c RESTComputer) Todo(id string) (model.Todo, error) {
	var t model.Todo
	return t, c.API.Call("GET", c.Prefix+"/api/todos/"+url.PathEscape(id), nil, &t)
}

func (c RESTComputer) Act(id, verb, feedback string) error {
	var body any
	if verb == "rework" {
		body = map[string]any{"feedback": feedback}
	}
	return c.API.Call("POST", c.Prefix+"/api/todos/"+url.PathEscape(id)+"/"+verb, body, nil)
}

// UpdateTodo sends the given fields to the task's PATCH /api/todos/{id}.
func (c RESTComputer) UpdateTodo(id string, u TodoUpdate) (model.Todo, error) {
	changes := map[string]any{}
	if u.Title != nil {
		changes["title"] = *u.Title
	}
	if u.Prompt != nil {
		changes["prompt"] = *u.Prompt
	}
	if u.Priority != nil {
		changes["priority"] = *u.Priority
	} else if u.ClearPriority {
		changes["priority"] = nil // null unsets it
	}
	if u.DependsOn != nil {
		deps := *u.DependsOn
		if deps == nil {
			deps = []string{} // [] clears the list; null would leave it
		}
		changes["depends_on"] = deps
	}
	if u.Model != nil {
		changes["model"] = *u.Model
	}
	var out model.Todo
	return out, c.API.Call("PATCH", c.Prefix+"/api/todos/"+url.PathEscape(id), changes, &out)
}

func (c RESTComputer) DeleteTodo(id string) error {
	return c.API.Call("DELETE", c.Prefix+"/api/todos/"+url.PathEscape(id), nil, nil)
}

func (c RESTComputer) Issues(projectID string) ([]model.Issue, error) {
	var is []model.Issue
	return is, c.API.Call("GET", c.Prefix+"/api/projects/"+url.PathEscape(projectID)+"/issues", nil, &is)
}

func (c RESTComputer) ReportIssue(projectID string, is model.Issue) (model.Issue, model.UpsertResult, error) {
	var out struct {
		Issue  model.Issue        `json:"issue"`
		Result model.UpsertResult `json:"result"`
	}
	err := c.API.Call("POST", c.Prefix+"/api/projects/"+url.PathEscape(projectID)+"/issues", map[string]any{
		"title": is.Title, "type": is.Type, "severity": is.Severity, "file": is.File, "line": is.Line,
		"details": is.Details, "fix": is.Fix, "source": is.Source,
	}, &out)
	return out.Issue, out.Result, err
}

func (c RESTComputer) CreateRelease(projectID string, r NewRelease) (Release, error) {
	var out Release
	return out, c.API.Call("POST", c.Prefix+"/api/projects/"+url.PathEscape(projectID)+"/tags", map[string]any{
		"version": r.Version, "bump": r.Bump, "sha": r.SHA, "notes": r.Notes,
	}, &out)
}

func (c RESTComputer) ReadChecklist(projectID string) (Checklist, error) {
	var out Checklist
	return out, c.API.Call("GET", c.Prefix+"/api/projects/"+url.PathEscape(projectID)+"/checklist", nil, &out)
}

func (c RESTComputer) ChangeChecklist(projectID string, ch ChecklistChange) (ChecklistSaved, error) {
	var out ChecklistSaved
	return out, c.API.Call("POST", c.Prefix+"/api/projects/"+url.PathEscape(projectID)+"/checklist", ch, &out)
}

func (c RESTComputer) MoveChecklistItem(projectID string, m ChecklistMove) (ChecklistMoved, error) {
	var out ChecklistMoved
	return out, c.API.Call("POST", c.Prefix+"/api/projects/"+url.PathEscape(projectID)+"/checklist/move", m, &out)
}

func (c RESTComputer) Text(id, kind string) (string, error) {
	var out string
	return out, c.API.Call("GET", c.Prefix+"/api/todos/"+url.PathEscape(id)+"/"+kind, nil, &out)
}

// FleetTools are the board, review and project settings tools, across every
// computer. ReviewTools' delete_todo, which deletes a task in any status but
// running or merging, replaces the board's draft/queued/failed-only one: here
// the user is driving, not the board chat.
func FleetTools(f Fleet) []Tool {
	review := ReviewTools(f)
	names := map[string]bool{}
	for _, t := range review {
		names[t.Name] = true
	}
	var out []Tool
	for _, t := range Tools(f, Options{}) {
		if !names[t.Name] {
			out = append(out, t)
		}
	}
	return append(append(out, review...), ProjectTools(f)...)
}

// ProjectSettings are update_project's optional parameters, sent as given to
// PATCH /api/projects/{id}.
var ProjectSettings = []Param{
	{"name", "string", "Display name.", false},
	{"run_command", "string", "Starts the app, e.g. \"npm run dev\"; gets $PORT.", false},
	{"test_command", "string", "Runs the tests after each task, e.g. \"go test ./...\".", false},
	{"deploy_command", "string", "Deploys, e.g. \"./deploy.sh\".", false},
	{"preview_kind", "string", "How tasks in review are previewed: \"web\" (run_command, opened in a browser; the default) or \"terminal\" (a terminal in the task's worktree, for CLI tools; run_command not needed).", false},
	{"preview_command", "string", "Terminal previews: runs in the task's worktree, with ./.dash-bin on PATH and $DASH_BIN set, e.g. \"go build -o $DASH_BIN/tool . && exec $SHELL\" (\"\" for a plain shell).", false},
	{"setup_command", "string", "Runs once in every new worktree before Claude starts, e.g. \"npm ci\".", false},
	{"copy_files", "array", "Untracked files copied from the main checkout into new worktrees, e.g. [\".env\"]; replaces the list.", false},
	{"allowed_tools", "array", "Tools that run without a permission prompt in tasks, e.g. [\"Bash\", \"WebFetch\"]; replaces the list.", false},
	{"chat_permission_mode", "string", "Permission mode of the project's chats: \"\" (ask), \"auto\" or \"acceptEdits\".", false},
	{"review_prompt", "string", "What the task chat's Review button asks Claude (\"\" for the default).", false},
	{"task_instructions", "string", "Extra instructions appended to Claude's system prompt in every task of the project (\"\" for none).", false},
	{"max_auto_fixes", "integer", "How often failing tests or conflicts go back to Claude before asking a human.", false},
	{"auto_push", "boolean", "Push the default branch after every merge.", false},
	{"auto_approve", "boolean", "Finished tasks whose tests pass skip review.", false},
	{"merge_mode", "string", "How approved work lands: \"pr\" (GitHub pull request, the default on GitHub) or \"local\" (squash onto the default branch).", false},
	{"merge_green_prs", "boolean", "Merge a task's pull request once its checks pass (default on); off, it waits to be merged on GitHub.", false},
	{"investigate_ci", "boolean", "Queue a task when a GitHub Actions build on the default branch fails.", false},
	{"tag_format", "string", "How release tags are named: X, Y, Z are major, minor, patch, e.g. \"vX.Y.Z\" (default \"X.Y.Z\").", false},
	{"max_run_minutes", "integer", "Time limit for one Claude run (0 for the default).", false},
	{"max_turns", "integer", "Turn limit for one Claude run (0 for the default).", false},
	{"color", "string", "Color marking the project, like \"#3a7bd5\" (\"\" for none).", false},
}

// ProjectTools read and change a project's settings: get_project and update_project.
func ProjectTools(f Fleet) []Tool {
	pProject := Param{"project", "string", "Project name or id (see list_projects).", true}
	return []Tool{
		{"get_project", "Show a project's settings.", []Param{pProject},
			func(a Args) (any, error) { return f.Project(a.Str("project")) }},
		{"update_project", "Change a project's settings; only the given ones change. Returns the project afterwards.",
			append([]Param{pProject}, ProjectSettings...),
			func(a Args) (any, error) {
				changes := map[string]any{}
				for _, p := range ProjectSettings {
					if v, ok := a[p.Name]; ok {
						changes[p.Name] = v
					}
				}
				if len(changes) == 0 {
					return nil, errors.New("no settings to change")
				}
				return f.UpdateProject(a.Str("project"), changes)
			}},
	}
}

// ReviewTools inspect and move tasks: get_diff, get_log, approve_todo,
// rework_todo, enqueue_todo, cancel_todo and delete_todo.
func ReviewTools(f Fleet) []Tool {
	return []Tool{
		{"get_diff", "The task's changes against the default branch (or the merged commit).", []Param{PTodo,
			{"max_chars", "integer", "Truncate after this many characters (default 100000).", false}},
			func(a Args) (any, error) { return f.Text(a.Str("todo"), "diff", a.Num("max_chars", 100000), false) }},
		{"get_log", "The end of Claude's run log for a task.", []Param{PTodo,
			{"max_chars", "integer", "Return at most this many characters from the end (default 20000).", false}},
			func(a Args) (any, error) { return f.Text(a.Str("todo"), "log", a.Num("max_chars", 20000), true) }},
		{"approve_todo", "Approve a task in review: it goes to the merge queue (or a pull request).", []Param{PTodo},
			func(a Args) (any, error) { return f.Action(a.Str("todo"), "approve", "") }},
		{"rework_todo", "Send a task back to Claude with feedback.", []Param{PTodo,
			{"feedback", "string", "What to change.", true}},
			func(a Args) (any, error) {
				if a.Str("feedback") == "" {
					return nil, errors.New("feedback is required")
				}
				return f.Action(a.Str("todo"), "rework", a.Str("feedback"))
			}},
		{"enqueue_todo", "Queue a draft or failed task to run (also retries failed ones, unlike queue_todo).", []Param{PTodo},
			func(a Args) (any, error) { return f.Action(a.Str("todo"), "enqueue", "") }},
		{"cancel_todo", "Stop a running or queued task.", []Param{PTodo},
			func(a Args) (any, error) { return f.Action(a.Str("todo"), "cancel", "") }},
		{"delete_todo", "Delete a task and its worktree.", []Param{PTodo},
			func(a Args) (any, error) { return "deleted", f.DeleteTodo(a.Str("todo")) }},
	}
}
