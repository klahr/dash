package mcptools

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/klahr/dash/internal/model"
)

// The user's goal tools: what a person does with goals (list, start, change,
// pause, resume, stop them, accept or reject a plan, write to the
// coordinator), for dash-mcp, the connector and the chat. A goal's own
// coordinator has GoalTools instead.

// GoalTask is one of a goal's tasks, as a GoalView lists it.
type GoalTask struct {
	ID     string           `json:"id"`
	Title  string           `json:"title"`
	Status model.TodoStatus `json:"status"`
}

// GoalView is a goal as the API and the goal tools answer it: with its
// tasks and how much of its budget they used.
type GoalView struct {
	model.Goal
	Tasks []GoalTask `json:"tasks"`
	// WeeklyUsed is the goal's estimated share of the weekly window, in
	// percent, when it has a weekly share and the window is known.
	WeeklyUsed *float64 `json:"weekly_used,omitempty"`
	// Machine is the computer the goal is on, set by the fleet tools.
	Machine string `json:"machine,omitempty"`
}

// NewGoal is a goal to start in Project.
type NewGoal struct {
	Project, Title, Description string
	Mode                        string // model.GoalAsk (the default) or model.GoalQueue
	Budget                      model.GoalBudget
}

// GoalChange changes a goal; nil fields stay as they are.
type GoalChange struct {
	Title, Description, Mode *string
	Budget                   *model.GoalBudget
}

// GoalManager is a Board on which the user manages goals: Tools then has
// the goal tools (GoalManagerToolNames).
type GoalManager interface {
	// Goals lists a project's goals, or every goal for projectID "".
	Goals(projectID string) ([]GoalView, error)
	// GetGoal is one goal, with its log.
	GetGoal(id string) (GoalView, error)
	// CreateGoal starts a goal: its coordinator reads the code and proposes a plan.
	CreateGoal(g NewGoal) (GoalView, error)
	UpdateGoal(id string, c GoalChange) (GoalView, error)
	// GoalAction pauses, resumes or stops a goal (action: pause, resume, stop).
	GoalAction(id, action string) (GoalView, error)
	// AcceptGoalPlan turns the plan waiting for the user (or plan, their
	// edit of it, if not nil) into the goal's tasks: drafts, or queued.
	AcceptGoalPlan(id string, plan []model.GoalPlanItem, queue bool) (GoalView, []model.Todo, error)
	// RejectGoalPlan wakes the coordinator to propose another plan.
	RejectGoalPlan(id, feedback string) (GoalView, error)
	// MessageGoal wakes the coordinator with a message from the user (an
	// answer to its question, or a change of direction).
	MessageGoal(id, text string) (GoalView, error)
}

// GoalManagerToolNames are the goal tools; GoalManagerReadTools only look.
var (
	GoalManagerToolNames = []string{"list_goals", "get_goal", "create_goal", "update_goal", "pause_goal", "resume_goal", "stop_goal",
		"accept_goal_plan", "reject_goal_plan", "message_goal"}
	GoalManagerReadTools = []string{"list_goals", "get_goal"}
)

// PGoal is the goal id parameter.
var PGoal = Param{"goal", "string", "Goal id (see list_goals).", true}

// goalParams are create_goal's and update_goal's optional settings.
var goalParams = []Param{
	{"description", "string", "What you want done, in as much detail as helps: the coordinator plans from it.", false},
	{"mode", "string", "\"ask\" (the default): its plan and new tasks wait for the user to accept and queue them; " +
		"\"queue\": they are queued on their own.", false},
	{"max_tasks", "integer", "The most tasks the coordinator may create (0 for no limit).", false},
	{"weekly_share", "integer", "The share of the weekly usage window, in percent (1-100), its tasks may use (0 for no limit).", false},
}

// GoalManagerTools returns the goal tools on gm.
func GoalManagerTools(b Board, gm GoalManager, o Options) []Tool {
	pProject, project := projectParam(o)
	optProject := pProject
	optProject.Required = false
	optProject.Desc = "Only this project's goals (name or id); default every project's."
	if o.Project != "" {
		optProject.Desc = "Project name or id; defaults to this one. \"*\" lists every project's goals."
	}
	need := func(a Args, k string) (string, error) {
		if v := a.Str(k); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("%s is required", k)
	}
	// do runs a goal action on a: the goal argument.
	do := func(action string) func(Args) (any, error) {
		return func(a Args) (any, error) {
			id, err := need(a, "goal")
			if err != nil {
				return nil, err
			}
			v, err := gm.GoalAction(id, action)
			return goalSummary(v), err
		}
	}
	return []Tool{
		{"list_goals", "List goals: something bigger than a task, which a coordinator (Claude) plans into tasks, dispatches and reviews. " +
			"Each shows its status (planning, working, waiting_for_you, paused, done, out_of_budget), what it waits for and how many of its tasks are merged.",
			[]Param{optProject, {"status", "string", "Only this status: planning, working, waiting_for_you, paused, done or out_of_budget.", false}},
			func(a Args) (any, error) {
				pid := ""
				if ref := a.Str("project"); ref != "*" && (ref != "" || o.Project != "") {
					p, err := project(b, a)
					if err != nil {
						return nil, err
					}
					pid = p.ID
				}
				gs, err := gm.Goals(pid)
				if err != nil {
					return nil, err
				}
				out := []goalRow{}
				for _, g := range gs {
					if st := a.Str("status"); st != "" && string(g.Status) != st {
						continue
					}
					out = append(out, goalSummary(g))
				}
				return out, nil
			}},
		{"get_goal", "Show a goal: what it is for, its mode, budget and what it used, its status and what it waits for (a plan to accept, " +
			"a question, drafts to queue), the plan waiting for the user, its tasks and the latest entries of its log.",
			[]Param{PGoal, {"log", "integer", "How many of the latest log entries to show (default 20).", false}},
			func(a Args) (any, error) {
				id, err := need(a, "goal")
				if err != nil {
					return nil, err
				}
				g, err := gm.GetGoal(id)
				if err != nil {
					return nil, err
				}
				if n := len(g.Log) - max(a.Num("log", 20), 0); n > 0 {
					g.Log = g.Log[n:]
				}
				g.Pending = nil
				return g, nil
			}},
		{"create_goal", "Start a goal in a project: a coordinator (Claude) reads the code and proposes a plan of tasks, then dispatches them " +
			"and reviews what each did. It never approves or merges a task. Only when the user asks for a goal.",
			append([]Param{pProject, {"title", "string", "Short title (taken from the description if empty).", false}}, goalParams...),
			func(a Args) (any, error) {
				p, err := project(b, a)
				if err != nil {
					return nil, err
				}
				if a.Str("title") == "" && a.Str("description") == "" {
					return nil, errors.New("title or description is required")
				}
				v, err := gm.CreateGoal(NewGoal{Project: p.ID, Title: a.Str("title"), Description: a.Str("description"), Mode: a.Str("mode"),
					Budget: model.GoalBudget{MaxTasks: a.Num("max_tasks", 0), WeeklyShare: a.Num("weekly_share", 0)}})
				return goalSummary(v), err
			}},
		{"update_goal", "Change a goal's title, description, mode or budget; only the given ones change. Raise the budget of a goal out of budget, then resume_goal.",
			append([]Param{PGoal, {"title", "string", "New short title.", false}}, goalParams...),
			func(a Args) (any, error) {
				id, err := need(a, "goal")
				if err != nil {
					return nil, err
				}
				c := GoalChange{Title: a.OptStr("title"), Description: a.OptStr("description"), Mode: a.OptStr("mode")}
				if a.Has("max_tasks") || a.Has("weekly_share") {
					cur, err := gm.GetGoal(id)
					if err != nil {
						return nil, err
					}
					bud := cur.Budget
					if n := a.OptNum("max_tasks"); n != nil {
						bud.MaxTasks = *n
					}
					if n := a.OptNum("weekly_share"); n != nil {
						bud.WeeklyShare = *n
					}
					c.Budget = &bud
				}
				if c.Title == nil && c.Description == nil && c.Mode == nil && c.Budget == nil {
					return nil, errors.New("nothing to change: pass title, description, mode, max_tasks or weekly_share")
				}
				v, err := gm.UpdateGoal(id, c)
				return goalSummary(v), err
			}},
		{"pause_goal", "Pause a planning, working or waiting goal: its coordinator isn't woken until it resumes (task events are kept). Its tasks carry on.",
			[]Param{PGoal}, do("pause")},
		{"resume_goal", "Resume a paused goal, or one out of budget once its budget allows more (see update_goal).", []Param{PGoal}, do("resume")},
		{"stop_goal", "Stop a goal for good: it is done and its coordinator never woken again. Its tasks stay on the board as they are. " +
			"Only when the user asks to stop it.", []Param{PGoal}, do("stop")},
		{"accept_goal_plan", "Accept the plan a goal waits for you on (get_goal shows it): its tasks are made, as drafts, or queued with queue. " +
			"Only when the user accepts the plan.",
			[]Param{PGoal,
				{"queue", "boolean", "Queue the tasks to run instead of keeping drafts.", false},
				{"tasks", ObjectArray, "The user's edit of the plan, if they changed it: JSON objects {\"title\", \"prompt\", \"after\": [indexes of earlier tasks], \"model\"}. " +
					"Omit it to accept the plan as proposed.", false}},
			func(a Args) (any, error) {
				id, err := need(a, "goal")
				if err != nil {
					return nil, err
				}
				var plan []model.GoalPlanItem
				if a.Has("tasks") {
					if plan, err = planItems(a["tasks"]); err != nil {
						return nil, err
					}
				}
				v, made, err := gm.AcceptGoalPlan(id, plan, a.Flag("queue"))
				if err != nil {
					return nil, err
				}
				tasks := []GoalTask{}
				for _, t := range made {
					tasks = append(tasks, GoalTask{t.ID, t.Title, t.Status})
				}
				return struct {
					goalRow
					Created []GoalTask `json:"created"`
				}{goalSummary(v), tasks}, nil
			}},
		{"reject_goal_plan", "Reject the plan a goal waits for you on: the coordinator proposes another, with the user's feedback.",
			[]Param{PGoal, {"feedback", "string", "What to change in the plan.", false}},
			func(a Args) (any, error) {
				id, err := need(a, "goal")
				if err != nil {
					return nil, err
				}
				v, err := gm.RejectGoalPlan(id, a.Str("feedback"))
				return goalSummary(v), err
			}},
		{"message_goal", "Write to a goal's coordinator: answer the question it waits on, or tell it what to change. " +
			"It is woken with the message and answers by acting (see get_goal's log); a paused goal gets it when it resumes.",
			[]Param{PGoal, {"text", "string", "The message, from the user.", true}},
			func(a Args) (any, error) {
				id, err := need(a, "goal")
				if err != nil {
					return nil, err
				}
				text, err := need(a, "text")
				if err != nil {
					return nil, err
				}
				v, err := gm.MessageGoal(id, text)
				return goalSummary(v), err
			}},
	}
}

// goalRow is a goal in short, as the goal tools list it.
type goalRow struct {
	ID         string           `json:"id"`
	Title      string           `json:"title"`
	Project    string           `json:"project_id"`
	Machine    string           `json:"machine,omitempty"`
	Mode       string           `json:"mode"`
	Status     model.GoalStatus `json:"status"`
	WaitingFor string           `json:"waiting_for,omitempty"`
	Note       string           `json:"note,omitempty"`
	Running    bool             `json:"running,omitempty"`
	Budget     model.GoalBudget `json:"budget"`
	WeeklyUsed *float64         `json:"weekly_used,omitempty"`
	PlanTasks  int              `json:"plan_tasks,omitempty"` // tasks in the plan waiting for the user
	Tasks      string           `json:"tasks"`                // "2 of 5 merged"
}

func goalSummary(g GoalView) goalRow {
	merged := 0
	for _, t := range g.Tasks {
		if t.Status == model.StatusMerged {
			merged++
		}
	}
	return goalRow{g.ID, g.Title, g.ProjectID, g.Machine, g.Mode, g.Status, g.WaitingFor, g.Note, g.Running, g.Budget, g.WeeklyUsed,
		len(g.Plan), fmt.Sprintf("%d of %d merged", merged, len(g.Tasks))}
}

// ---- the fleet's goals ----

// goalComputer is c's goals, or an error when it has none.
func goalComputer(c Computer) (GoalManager, error) {
	if gm, ok := c.(GoalManager); ok {
		return gm, nil
	}
	return nil, errors.New("this computer has no goals")
}

func (f Fleet) Goals(projectID string) ([]GoalView, error) {
	if projectID != "" {
		p, c, err := f.project(projectID)
		if err != nil {
			return nil, err
		}
		gm, err := goalComputer(c)
		if err != nil {
			return nil, err
		}
		gs, err := gm.Goals(p.ID)
		for i := range gs {
			gs[i].Machine = p.Machine
		}
		return gs, err
	}
	ms, err := f.machines()
	if err != nil {
		return nil, err
	}
	out := []GoalView{}
	for _, m := range ms {
		gm, ok := m.Board.(GoalManager)
		if !m.OK || !ok {
			continue
		}
		gs, err := gm.Goals("")
		if errors.Is(err, ErrUnauthorized) {
			return nil, err
		}
		if err != nil {
			continue // an older Dash without goals, or unreachable now
		}
		for _, g := range gs {
			g.Machine = m.Name
			out = append(out, g)
		}
	}
	return out, nil
}

// goal finds a goal on whichever computer has it.
func (f Fleet) goal(id string) (GoalView, GoalManager, error) {
	if id == "" {
		return GoalView{}, nil, errors.New("goal is required")
	}
	ms, err := f.machines()
	if err != nil {
		return GoalView{}, nil, err
	}
	for _, m := range ms {
		gm, ok := m.Board.(GoalManager)
		if !m.OK || !ok {
			continue
		}
		g, err := gm.GetGoal(id)
		if err == nil {
			g.Machine = m.Name
			return g, gm, nil
		}
		if errors.Is(err, ErrUnauthorized) {
			return GoalView{}, nil, err
		}
	}
	return GoalView{}, nil, fmt.Errorf("no goal %q (see list_goals)", id)
}

func (f Fleet) GetGoal(id string) (GoalView, error) {
	g, _, err := f.goal(id)
	return g, err
}

func (f Fleet) CreateGoal(ng NewGoal) (GoalView, error) {
	p, c, err := f.project(ng.Project)
	if err != nil {
		return GoalView{}, err
	}
	gm, err := goalComputer(c)
	if err != nil {
		return GoalView{}, err
	}
	ng.Project = p.ID
	g, err := gm.CreateGoal(ng)
	g.Machine = p.Machine
	return g, err
}

// onGoal runs fn on the computer that has goal id.
func (f Fleet) onGoal(id string, fn func(GoalManager) (GoalView, error)) (GoalView, error) {
	cur, gm, err := f.goal(id)
	if err != nil {
		return GoalView{}, err
	}
	g, err := fn(gm)
	g.Machine = cur.Machine
	return g, err
}

func (f Fleet) UpdateGoal(id string, c GoalChange) (GoalView, error) {
	return f.onGoal(id, func(gm GoalManager) (GoalView, error) { return gm.UpdateGoal(id, c) })
}

func (f Fleet) GoalAction(id, action string) (GoalView, error) {
	return f.onGoal(id, func(gm GoalManager) (GoalView, error) { return gm.GoalAction(id, action) })
}

func (f Fleet) AcceptGoalPlan(id string, plan []model.GoalPlanItem, queue bool) (GoalView, []model.Todo, error) {
	var made []model.Todo
	g, err := f.onGoal(id, func(gm GoalManager) (GoalView, error) {
		g, ts, err := gm.AcceptGoalPlan(id, plan, queue)
		made = ts
		return g, err
	})
	return g, made, err
}

func (f Fleet) RejectGoalPlan(id, feedback string) (GoalView, error) {
	return f.onGoal(id, func(gm GoalManager) (GoalView, error) { return gm.RejectGoalPlan(id, feedback) })
}

func (f Fleet) MessageGoal(id, text string) (GoalView, error) {
	return f.onGoal(id, func(gm GoalManager) (GoalView, error) { return gm.MessageGoal(id, text) })
}

// ---- over the REST API ----

func (c RESTComputer) goalPath(id string, rest ...string) string {
	return c.Prefix + "/api/goals/" + url.PathEscape(id) + strings.Join(rest, "")
}

func (c RESTComputer) Goals(projectID string) ([]GoalView, error) {
	path := c.Prefix + "/api/goals"
	if projectID != "" {
		path = c.Prefix + "/api/projects/" + url.PathEscape(projectID) + "/goals"
	}
	var out []GoalView
	return out, c.API.Call("GET", path, nil, &out)
}

func (c RESTComputer) GetGoal(id string) (GoalView, error) {
	var out GoalView
	return out, c.API.Call("GET", c.goalPath(id), nil, &out)
}

func (c RESTComputer) CreateGoal(g NewGoal) (GoalView, error) {
	var out GoalView
	return out, c.API.Call("POST", c.Prefix+"/api/goals", map[string]any{
		"project": g.Project, "title": g.Title, "description": g.Description, "mode": g.Mode, "budget": g.Budget,
	}, &out)
}

func (c RESTComputer) UpdateGoal(id string, ch GoalChange) (GoalView, error) {
	changes := map[string]any{}
	if ch.Title != nil {
		changes["title"] = *ch.Title
	}
	if ch.Description != nil {
		changes["description"] = *ch.Description
	}
	if ch.Mode != nil {
		changes["mode"] = *ch.Mode
	}
	if ch.Budget != nil {
		changes["budget"] = *ch.Budget
	}
	var out GoalView
	return out, c.API.Call("PATCH", c.goalPath(id), changes, &out)
}

func (c RESTComputer) GoalAction(id, action string) (GoalView, error) {
	switch action {
	case "pause", "resume", "stop":
	default:
		return GoalView{}, fmt.Errorf("unknown goal action %q", action)
	}
	var out GoalView
	return out, c.API.Call("POST", c.goalPath(id, "/", action), nil, &out)
}

func (c RESTComputer) AcceptGoalPlan(id string, plan []model.GoalPlanItem, queue bool) (GoalView, []model.Todo, error) {
	var out struct {
		GoalView
		Created []model.Todo `json:"created"`
	}
	body := map[string]any{"queue": queue}
	if plan != nil {
		body["plan"] = plan
	}
	err := c.API.Call("POST", c.goalPath(id, "/plan/accept"), body, &out)
	return out.GoalView, out.Created, err
}

func (c RESTComputer) RejectGoalPlan(id, feedback string) (GoalView, error) {
	var out GoalView
	return out, c.API.Call("POST", c.goalPath(id, "/plan/reject"), map[string]any{"feedback": feedback}, &out)
}

func (c RESTComputer) MessageGoal(id, text string) (GoalView, error) {
	var out GoalView
	return out, c.API.Call("POST", c.goalPath(id, "/message"), map[string]any{"text": text}, &out)
}
