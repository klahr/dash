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

// Fleet is the task board of every computer in a dashboard. It serves both
// cmd/dash-mcp (over HTTP) and the hub's own /mcp endpoint (in-process).
type Fleet struct{ API API }

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

func (f Fleet) machines() ([]machine, error) {
	var ms []machine
	return ms, f.API.Call("GET", "/api/machines", nil, &ms)
}

// projects lists every reachable computer's projects, tagged with the
// computer's name, and the prefix each lives under.
func (f Fleet) projects() (ps []model.Project, prefix map[string]string, unreachable []string, err error) {
	ms, err := f.machines()
	if err != nil {
		return nil, nil, nil, err
	}
	prefix = map[string]string{}
	for _, m := range ms {
		var mps []model.Project
		if m.OK {
			err = f.API.Call("GET", m.Prefix+"/api/projects", nil, &mps)
			if errors.Is(err, ErrUnauthorized) {
				return nil, nil, nil, err
			}
			if err != nil {
				m.OK, m.Error = false, err.Error()
			}
		}
		if !m.OK {
			unreachable = append(unreachable, m.Name+": "+m.Error)
			continue
		}
		for _, p := range mps {
			p.Machine = m.Name
			prefix[p.ID] = m.Prefix
			ps = append(ps, p)
		}
	}
	return ps, prefix, unreachable, nil
}

func (f Fleet) Projects() ([]model.Project, []string, error) {
	ps, _, unreachable, err := f.projects()
	return ps, unreachable, err
}

// project finds a project by id or name on any computer.
func (f Fleet) project(ref string) (model.Project, string, error) {
	ps, prefix, _, err := f.projects()
	if err != nil {
		return model.Project{}, "", err
	}
	for _, p := range ps {
		if p.ID == ref || strings.EqualFold(p.Name, ref) {
			return p, prefix[p.ID], nil
		}
	}
	return model.Project{}, "", fmt.Errorf("no project %q (see list_projects)", ref)
}

func (f Fleet) Project(ref string) (model.Project, error) {
	p, _, err := f.project(ref)
	return p, err
}

// UpdateProject sends changes to the project's PATCH /api/projects/{id}.
func (f Fleet) UpdateProject(ref string, changes map[string]any) (model.Project, error) {
	p, at, err := f.project(ref)
	if err != nil {
		return model.Project{}, err
	}
	var out model.Project
	if err := f.API.Call("PATCH", at+"/api/projects/"+url.PathEscape(p.ID), changes, &out); err != nil {
		return model.Project{}, err
	}
	out.Machine = p.Machine
	return out, nil
}

func (f Fleet) Todos(projectID string) ([]model.Todo, error) {
	_, at, err := f.project(projectID)
	if err != nil {
		return nil, err
	}
	var ts []model.Todo
	return ts, f.API.Call("GET", at+"/api/projects/"+url.PathEscape(projectID)+"/todos", nil, &ts)
}

func (f Fleet) CreateTodo(projectID string, t NewTodo) (model.Todo, error) {
	_, at, err := f.project(projectID)
	if err != nil {
		return model.Todo{}, err
	}
	var out model.Todo
	return out, f.API.Call("POST", at+"/api/projects/"+url.PathEscape(projectID)+"/todos", map[string]any{
		"title": t.Title, "prompt": t.Prompt, "priority": t.Priority, "enqueue": t.Enqueue,
		"depends_on": t.DependsOn,
	}, &out)
}

// todo finds a task on whichever computer has it, and that computer's prefix.
func (f Fleet) todo(id string) (model.Todo, string, error) {
	if id == "" {
		return model.Todo{}, "", errors.New("todo is required")
	}
	ms, err := f.machines()
	if err != nil {
		return model.Todo{}, "", err
	}
	for _, m := range ms {
		if !m.OK {
			continue
		}
		var t model.Todo
		err := f.API.Call("GET", m.Prefix+"/api/todos/"+url.PathEscape(id), nil, &t)
		if err == nil {
			return t, m.Prefix, nil
		}
		if errors.Is(err, ErrUnauthorized) {
			return model.Todo{}, "", err
		}
	}
	return model.Todo{}, "", fmt.Errorf("no task %q", id)
}

func (f Fleet) Todo(id string) (model.Todo, error) {
	t, _, err := f.todo(id)
	return t, err
}

// Action posts to a task's /{verb} (enqueue, cancel, approve, rework) and
// returns the task afterwards.
func (f Fleet) Action(id, verb string, body any) (model.Todo, error) {
	_, at, err := f.todo(id)
	if err != nil {
		return model.Todo{}, err
	}
	if err := f.API.Call("POST", at+"/api/todos/"+url.PathEscape(id)+"/"+verb, body, nil); err != nil {
		return model.Todo{}, err
	}
	return f.Todo(id)
}

func (f Fleet) QueueTodo(id string) (model.Todo, error) { return f.Action(id, "enqueue", nil) }

func (f Fleet) Issues(projectID string) ([]model.Issue, error) {
	_, at, err := f.project(projectID)
	if err != nil {
		return nil, err
	}
	var is []model.Issue
	return is, f.API.Call("GET", at+"/api/projects/"+url.PathEscape(projectID)+"/issues", nil, &is)
}

func (f Fleet) ReportIssue(projectID string, is model.Issue) (model.Issue, model.UpsertResult, error) {
	_, at, err := f.project(projectID)
	if err != nil {
		return model.Issue{}, "", err
	}
	var out struct {
		Issue  model.Issue        `json:"issue"`
		Result model.UpsertResult `json:"result"`
	}
	err = f.API.Call("POST", at+"/api/projects/"+url.PathEscape(projectID)+"/issues", map[string]any{
		"title": is.Title, "type": is.Type, "severity": is.Severity, "file": is.File, "line": is.Line,
		"details": is.Details, "fix": is.Fix, "source": is.Source,
	}, &out)
	return out.Issue, out.Result, err
}

func (f Fleet) Delete(id string) error {
	_, at, err := f.todo(id)
	if err != nil {
		return err
	}
	return f.API.Call("DELETE", at+"/api/todos/"+url.PathEscape(id), nil, nil)
}

// Text returns a task's diff or log, cut to max characters (from the end if tail).
func (f Fleet) Text(id, kind string, max int, tail bool) (string, error) {
	_, at, err := f.todo(id)
	if err != nil {
		return "", err
	}
	var out string
	if err := f.API.Call("GET", at+"/api/todos/"+url.PathEscape(id)+"/"+kind, nil, &out); err != nil || max <= 0 || len(out) <= max {
		return out, err
	}
	if tail {
		return fmt.Sprintf("[…%d earlier characters omitted]\n", len(out)-max) + out[len(out)-max:], nil
	}
	return out[:max] + fmt.Sprintf("\n[…truncated, %d more characters]", len(out)-max), nil
}

// FleetTools are the board, review and project settings tools, across every computer.
func FleetTools(f Fleet) []Tool {
	return append(append(Tools(f, Options{}), ReviewTools(f)...), ProjectTools(f)...)
}

// ProjectSettings are update_project's optional parameters, sent as given to
// PATCH /api/projects/{id}.
var ProjectSettings = []Param{
	{"name", "string", "Display name.", false},
	{"run_command", "string", "Starts the app, e.g. \"npm run dev\"; gets $PORT.", false},
	{"test_command", "string", "Runs the tests after each task, e.g. \"go test ./...\".", false},
	{"deploy_command", "string", "Deploys, e.g. \"./deploy.sh\".", false},
	{"setup_command", "string", "Runs once in every new worktree before Claude starts, e.g. \"npm ci\".", false},
	{"copy_files", "array", "Untracked files copied from the main checkout into new worktrees, e.g. [\".env\"]; replaces the list.", false},
	{"allowed_tools", "array", "Tools that run without a permission prompt in tasks, e.g. [\"Bash\", \"WebFetch\"]; replaces the list.", false},
	{"chat_permission_mode", "string", "Permission mode of the project's chats: \"\" (ask), \"auto\" or \"acceptEdits\".", false},
	{"review_prompt", "string", "What the task chat's Review button asks Claude (\"\" for the default).", false},
	{"max_auto_fixes", "integer", "How often failing tests or conflicts go back to Claude before asking a human.", false},
	{"auto_push", "boolean", "Push the default branch after every merge.", false},
	{"auto_approve", "boolean", "Finished tasks whose tests pass skip review.", false},
	{"merge_mode", "string", "How approved work lands: \"local\" (squash onto the default branch) or \"pr\" (GitHub pull request).", false},
	{"investigate_ci", "boolean", "Queue a task when a GitHub Actions build on the default branch fails.", false},
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
			func(a Args) (any, error) { return f.Action(a.Str("todo"), "approve", nil) }},
		{"rework_todo", "Send a task back to Claude with feedback.", []Param{PTodo,
			{"feedback", "string", "What to change.", true}},
			func(a Args) (any, error) {
				if a.Str("feedback") == "" {
					return nil, errors.New("feedback is required")
				}
				return f.Action(a.Str("todo"), "rework", map[string]any{"feedback": a.Str("feedback")})
			}},
		{"enqueue_todo", "Queue a draft or failed task to run (also retries failed ones, unlike queue_todo).", []Param{PTodo},
			func(a Args) (any, error) { return f.Action(a.Str("todo"), "enqueue", nil) }},
		{"cancel_todo", "Stop a running or queued task.", []Param{PTodo},
			func(a Args) (any, error) { return f.Action(a.Str("todo"), "cancel", nil) }},
		{"delete_todo", "Delete a task and its worktree.", []Param{PTodo},
			func(a Args) (any, error) { return "deleted", f.Delete(a.Str("todo")) }},
	}
}
