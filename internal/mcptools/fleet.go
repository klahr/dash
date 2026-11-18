package mcptools

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/usage"
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
	// Act moves a task: verb is enqueue, start-now, cancel, approve or rework (with
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

	// cache remembers the computers and where projects and tasks live, so
	// one tool call doesn't ask every computer again. FleetTools sets it; a
	// Fleet without one asks every time.
	cache *fleetCache
}

// fleetTTL is how long the cached computer and project lists are used.
const fleetTTL = 15 * time.Second

// fleetCache is what a Fleet remembers between calls. Where a task or
// project lives doesn't change, so that is kept by computer name; the
// computers themselves (who is reachable) are asked again after fleetTTL.
type fleetCache struct {
	mu         sync.Mutex
	now        func() time.Time // for tests; time.Now if nil
	machines   []Machine
	machinesAt time.Time
	projects   []model.Project // tagged with their computer (Machine)
	projectsAt time.Time
	todoOn     map[string]string // task id → computer name
}

func (c *fleetCache) time() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
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

// machines lists the computers, from the cache while it is fresh.
func (f Fleet) machines() ([]Machine, error) {
	if c := f.cache; c != nil {
		c.mu.Lock()
		ms, at := c.machines, c.machinesAt
		c.mu.Unlock()
		if ms != nil && c.time().Sub(at) < fleetTTL {
			return ms, nil
		}
	}
	ms, err := f.listMachines()
	if err != nil {
		return nil, err
	}
	if c := f.cache; c != nil {
		c.mu.Lock()
		c.machines, c.machinesAt = ms, c.time()
		c.mu.Unlock()
	}
	return ms, nil
}

func (f Fleet) listMachines() ([]Machine, error) {
	if f.Computers != nil {
		ms, err := f.Computers()
		if ms == nil && err == nil {
			ms = []Machine{}
		}
		return ms, err
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

// board is the reachable computer named name, or nil.
func board(ms []Machine, name string) Computer {
	for _, m := range ms {
		if m.Name == name && m.OK {
			return m.Board
		}
	}
	return nil
}

// projects lists every reachable computer's projects, tagged with the
// computer's name, and the computer each lives on. It asks the computers
// in parallel.
func (f Fleet) projects() (ps []model.Project, on map[string]Computer, unreachable []string, err error) {
	ms, err := f.machines()
	if err != nil {
		return nil, nil, nil, err
	}
	type answer struct {
		ps  []model.Project
		err error
	}
	answers := make([]answer, len(ms))
	var wg sync.WaitGroup
	for i, m := range ms {
		if !m.OK || m.Board == nil {
			continue
		}
		wg.Add(1)
		go func(i int, b Computer) {
			defer wg.Done()
			answers[i].ps, answers[i].err = b.Projects()
		}(i, m.Board)
	}
	wg.Wait()
	on = map[string]Computer{}
	ps = []model.Project{}
	for i, m := range ms {
		a := answers[i]
		if errors.Is(a.err, ErrUnauthorized) {
			return nil, nil, nil, a.err
		}
		if m.Board == nil {
			m.OK = false
		}
		if a.err != nil {
			m.OK, m.Error = false, a.err.Error()
		}
		if !m.OK {
			unreachable = append(unreachable, m.Name+": "+m.Error)
			continue
		}
		for _, p := range a.ps {
			p.Machine = m.Name
			on[p.ID] = m.Board
			ps = append(ps, p)
		}
	}
	if c := f.cache; c != nil {
		c.mu.Lock()
		c.projects, c.projectsAt = ps, c.time()
		c.mu.Unlock()
	}
	return ps, on, unreachable, nil
}

func (f Fleet) Projects() ([]model.Project, []string, error) {
	ps, _, unreachable, err := f.projects()
	return ps, unreachable, err
}

// findProject is the project ref names (by id or name) in ps.
func findProject(ps []model.Project, ref string) (model.Project, bool) {
	for _, p := range ps {
		if p.ID == ref || strings.EqualFold(p.Name, ref) {
			return p, true
		}
	}
	return model.Project{}, false
}

// project finds a project by id or name on any computer, freshly asked.
func (f Fleet) project(ref string) (model.Project, Computer, error) {
	ps, on, _, err := f.projects()
	if err != nil {
		return model.Project{}, nil, err
	}
	if p, ok := findProject(ps, ref); ok {
		return p, on[p.ID], nil
	}
	return model.Project{}, nil, fmt.Errorf("no project %q (see list_projects)", ref)
}

// projectOn is the project ref names and its computer, for a call to that
// computer: from the cached project list while it is fresh, else asked.
func (f Fleet) projectOn(ref string) (model.Project, Computer, error) {
	if c := f.cache; c != nil {
		c.mu.Lock()
		ps, at := c.projects, c.projectsAt
		c.mu.Unlock()
		if ps != nil && c.time().Sub(at) < fleetTTL {
			if p, ok := findProject(ps, ref); ok {
				if ms, err := f.machines(); err == nil {
					if b := board(ms, p.Machine); b != nil {
						return p, b, nil
					}
				}
			}
		}
	}
	return f.project(ref)
}

// Project finds a project by id or name, from the cached list while it is
// fresh: the board tools use it to find a project for a call by its id.
func (f Fleet) Project(ref string) (model.Project, error) {
	p, _, err := f.projectOn(ref)
	return p, err
}

// UpdateProject changes a project's settings on its computer.
func (f Fleet) UpdateProject(ref string, changes map[string]any) (model.Project, error) {
	p, c, err := f.projectOn(ref)
	if err != nil {
		return model.Project{}, err
	}
	out, err := c.UpdateProject(p.ID, changes)
	if err != nil {
		return model.Project{}, err
	}
	out.Machine = p.Machine
	if f.cache != nil { // a renamed project is found by its new name
		f.cache.mu.Lock()
		f.cache.projects = nil
		f.cache.mu.Unlock()
	}
	return out, nil
}

func (f Fleet) Todos(projectID string) ([]model.Todo, error) {
	p, c, err := f.projectOn(projectID)
	if err != nil {
		return nil, err
	}
	ts, err := c.Todos(p.ID)
	for _, t := range ts {
		f.remember(t.ID, p.Machine)
	}
	return ts, err
}

func (f Fleet) CreateTodo(projectID string, t NewTodo) (model.Todo, error) {
	p, c, err := f.projectOn(projectID)
	if err != nil {
		return model.Todo{}, err
	}
	out, err := c.CreateTodo(p.ID, t)
	if err == nil {
		f.remember(out.ID, p.Machine)
	}
	return out, err
}

// remember notes that task id lives on the computer named machine.
func (f Fleet) remember(id, machine string) {
	c := f.cache
	if c == nil || id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.todoOn == nil {
		c.todoOn = map[string]string{}
	}
	c.todoOn[id] = machine
}

// todo finds a task on whichever computer has it, and that computer. It
// asks the computer the task was last seen on, else every computer in
// parallel.
func (f Fleet) todo(id string) (model.Todo, Computer, error) {
	if id == "" {
		return model.Todo{}, nil, errors.New("todo is required")
	}
	ms, err := f.machines()
	if err != nil {
		return model.Todo{}, nil, err
	}
	if name, ok := f.seenOn(id); ok {
		if b := board(ms, name); b != nil {
			t, err := b.Todo(id)
			if err == nil {
				return t, b, nil
			}
			if errors.Is(err, ErrUnauthorized) {
				return model.Todo{}, nil, err
			}
		}
	}
	type answer struct {
		m   Machine
		t   model.Todo
		err error
	}
	answers := make(chan answer, len(ms)) // buffered: no goroutine waits once one is found
	asked := 0
	for _, m := range ms {
		if !m.OK || m.Board == nil {
			continue
		}
		asked++
		go func(m Machine) {
			t, err := m.Board.Todo(id)
			answers <- answer{m, t, err}
		}(m)
	}
	var unauthorized error
	for range asked {
		a := <-answers
		if a.err == nil {
			f.remember(id, a.m.Name)
			return a.t, a.m.Board, nil
		}
		if errors.Is(a.err, ErrUnauthorized) {
			unauthorized = a.err
		}
	}
	if unauthorized != nil {
		return model.Todo{}, nil, unauthorized
	}
	return model.Todo{}, nil, fmt.Errorf("no task %q", id)
}

// seenOn is the computer task id was last seen on.
func (f Fleet) seenOn(id string) (string, bool) {
	c := f.cache
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	name, ok := c.todoOn[id]
	return name, ok
}

// todoOn is the computer task id lives on, for a call to it: the one it was
// last seen on, without asking it, else found by todo.
func (f Fleet) todoOn(id string) (Computer, error) {
	if name, ok := f.seenOn(id); ok {
		if ms, err := f.machines(); err == nil {
			if b := board(ms, name); b != nil {
				return b, nil
			}
		}
	}
	_, c, err := f.todo(id)
	return c, err
}

func (f Fleet) Todo(id string) (model.Todo, error) {
	t, _, err := f.todo(id)
	return t, err
}

// Action moves a task (enqueue, cancel, approve, rework) and returns it
// afterwards.
func (f Fleet) Action(id, verb, feedback string) (model.Todo, error) {
	c, err := f.todoOn(id)
	if err != nil {
		return model.Todo{}, err
	}
	if err := c.Act(id, verb, feedback); err != nil {
		return model.Todo{}, err
	}
	return c.Todo(id)
}

func (f Fleet) QueueTodo(id string) (model.Todo, error) { return f.Action(id, "enqueue", "") }

// StartNow lets a queued task start despite the weekly pace, on its computer.
func (f Fleet) StartNow(id string) (model.Todo, error) { return f.Action(id, "start-now", "") }

// PaceWaiter is a Computer that tells whether a queued task waits for the
// weekly pace (see StartNower).
type PaceWaiter interface {
	PaceWait(t model.Todo) string
}

// PaceWait is why queued task t waits for the weekly pace on its computer, or "".
func (f Fleet) PaceWait(t model.Todo) string {
	c, err := f.todoOn(t.ID)
	if err != nil {
		return ""
	}
	if pw, ok := c.(PaceWaiter); ok {
		return pw.PaceWait(t)
	}
	return ""
}

func (f Fleet) UpdateTodo(id string, u TodoUpdate) (model.Todo, error) {
	c, err := f.todoOn(id)
	if err != nil {
		return model.Todo{}, err
	}
	return c.UpdateTodo(id, u)
}

func (f Fleet) DeleteTodo(id string) error {
	c, err := f.todoOn(id)
	if err != nil {
		return err
	}
	return c.DeleteTodo(id)
}

func (f Fleet) Issues(projectID string) ([]model.Issue, error) {
	p, c, err := f.projectOn(projectID)
	if err != nil {
		return nil, err
	}
	return c.Issues(p.ID)
}

func (f Fleet) ReportIssue(projectID string, is model.Issue) (model.Issue, model.UpsertResult, error) {
	p, c, err := f.projectOn(projectID)
	if err != nil {
		return model.Issue{}, "", err
	}
	return c.ReportIssue(p.ID, is)
}

func (f Fleet) CreateRelease(projectID string, r NewRelease) (Release, error) {
	p, c, err := f.projectOn(projectID)
	if err != nil {
		return Release{}, err
	}
	return c.CreateRelease(p.ID, r)
}

func (f Fleet) ReadChecklist(projectID string) (Checklist, error) {
	p, c, err := f.projectOn(projectID)
	if err != nil {
		return Checklist{}, err
	}
	return c.ReadChecklist(p.ID)
}

func (f Fleet) ChangeChecklist(projectID string, ch ChecklistChange) (ChecklistSaved, error) {
	p, c, err := f.projectOn(projectID)
	if err != nil {
		return ChecklistSaved{}, err
	}
	return c.ChangeChecklist(p.ID, ch)
}

func (f Fleet) MoveChecklistItem(projectID string, m ChecklistMove) (ChecklistMoved, error) {
	p, c, err := f.projectOn(projectID)
	if err != nil {
		return ChecklistMoved{}, err
	}
	out, err := c.MoveChecklistItem(p.ID, m)
	if err == nil {
		f.remember(out.Task.ID, p.Machine)
	}
	return out, err
}

// Text returns a task's diff or log, cut to max characters (from the end if tail).
func (f Fleet) Text(id, kind string, max int, tail bool) (string, error) {
	c, err := f.todoOn(id)
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
		"depends_on": t.DependsOn, "builds_on": t.BuildsOn, "model": t.Model, "run_on": t.RunOn,
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

// PaceWait is the computer's pace reason (GET /api/usage) when its queue
// waits only for the weekly pace and t is a queued task the pace holds
// back, or "".
func (c RESTComputer) PaceWait(t model.Todo) string {
	if t.Status != model.StatusQueued || t.Paused || t.StartNow {
		return ""
	}
	var u usage.Snapshot
	if c.API.Call("GET", c.Prefix+"/api/usage", nil, &u) != nil || !u.PaceOnly() {
		return ""
	}
	return u.Reason
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
	if u.BuildsOn != nil {
		changes["builds_on"] = *u.BuildsOn
	}
	if u.Model != nil {
		changes["model"] = *u.Model
	}
	if u.RunOn != nil {
		changes["run_on"] = *u.RunOn
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
	if f.cache == nil {
		f.cache = &fleetCache{}
	}
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
	{"merge_mode", "string", "How finished work lands: \"pr\" (a GitHub pull request opened when the task finishes, reviewed on GitHub and merged by a person, there or in the Dash web UI; the default on GitHub) or \"local\" (approved in Dash, then squashed onto the default branch).", false},
	{"investigate_ci", "boolean", "Queue a task when a GitHub Actions build on the default branch fails.", false},
	{"tag_format", "string", "How release tags are named: X, Y, Z are major, minor, patch, e.g. \"vX.Y.Z\" (default \"X.Y.Z\").", false},
	{"max_run_minutes", "integer", "Time limit for one Claude run (0 for the default).", false},
	{"max_turns", "integer", "Turn limit for one Claude run (0 for the default).", false},
	{"max_parallel", "integer", "Most of the project's tasks in progress at once (0 for no limit of its own).", false},
	{"color", "string", "Color marking the project, like \"#3a7bd5\" (\"\" for none).", false},
}

// ProjectTools read and change a project's settings: get_project and update_project.
func ProjectTools(f Fleet) []Tool {
	pProject := Param{"project", "string", "Project name or id (see list_projects).", true}
	return []Tool{
		{"get_project", "Show a project's settings.", []Param{pProject},
			func(a Args) (any, error) { // asked afresh: settings change elsewhere too
				p, _, err := f.project(a.Str("project"))
				return p, err
			}},
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
		{"approve_todo", "Approve a task in review: it goes to the merge queue. In pull request mode a task's pull request opens when it finishes and is reviewed and merged on GitHub; approving only opens one for a task in review without one.", []Param{PTodo},
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
