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
		"title": t.Title, "prompt": t.Prompt, "goal": t.Goal, "priority": t.Priority, "enqueue": t.Enqueue,
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

// FleetTools are the board tools plus the review tools, across every computer.
func FleetTools(f Fleet) []Tool {
	return append(Tools(f, Options{}), ReviewTools(f)...)
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
