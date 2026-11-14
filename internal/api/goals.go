package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
)

// Goals: a long-lived aim in one project, worked towards by a coordinator
// (goalcoord.go) that plans and dispatches tasks and reviews what they did,
// but never approves or merges them. These are the REST endpoints, what the
// user does with a goal, and the board its coordinator's tools act on.

func (s *Server) goalRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/goals", s.listGoals)
	mux.HandleFunc("GET /api/projects/{pid}/goals", s.listGoals)
	mux.HandleFunc("POST /api/goals", s.createGoal)
	mux.HandleFunc("GET /api/goals/{gid}", s.getGoal)
	mux.HandleFunc("PATCH /api/goals/{gid}", s.updateGoal)
	mux.HandleFunc("POST /api/goals/{gid}/pause", s.goalAction("pause"))
	mux.HandleFunc("POST /api/goals/{gid}/resume", s.goalAction("resume"))
	mux.HandleFunc("POST /api/goals/{gid}/stop", s.goalAction("stop"))
	mux.HandleFunc("GET /api/goals/{gid}/log", s.goalLog)
	mux.HandleFunc("POST /api/goals/{gid}/plan/accept", s.acceptGoalPlan)
	mux.HandleFunc("POST /api/goals/{gid}/plan/reject", s.rejectGoalPlan)
	mux.HandleFunc("POST /api/goals/{gid}/message", s.messageGoal)
	mux.HandleFunc("GET /api/goals/{gid}/chat", s.goalChat)
}

// goalTask and goalView are a goal's tasks and the goal as the API answers
// it: without its log (GET …/log has it) unless asked, with its tasks and
// how much of its budget they used. The goal tools answer the same.
type (
	goalTask = mcptools.GoalTask
	goalView = mcptools.GoalView
)

func (sv *service) goalView(g model.Goal, withLog bool) goalView {
	v := goalView{Goal: g, Tasks: []goalTask{}}
	if !withLog {
		v.Log = nil
	}
	for _, t := range sv.data.GoalTodos(g) {
		v.Tasks = append(v.Tasks, goalTask{ID: t.ID, Title: t.Title, Status: t.Status})
	}
	if g.Budget.WeeklyShare > 0 {
		if used, ok := sv.s.goalWeekUsed(sv, g); ok {
			v.WeeklyUsed = &used
		}
	}
	return v
}

// listGoals handles GET /api/goals (all of the org's, or ?project=) and
// GET /api/projects/{pid}/goals.
func (s *Server) listGoals(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	pid := r.PathValue("pid")
	if pid == "" {
		pid = r.URL.Query().Get("project")
	}
	if pid != "" {
		if _, err := sv.data.GetProject(pid); err != nil {
			writeErr(w, err)
			return
		}
	}
	out := []goalView{}
	for _, g := range sv.data.ListGoals(pid) {
		out = append(out, sv.goalView(g, false))
	}
	writeJSON(w, http.StatusOK, out)
}

// goalRequest is what POST /api/goals and PATCH /api/goals/{gid} take.
type goalRequest struct {
	Project     string            `json:"project"`
	Title       *string           `json:"title"`
	Description *string           `json:"description"`
	Budget      *model.GoalBudget `json:"budget"`
	Mode        *string           `json:"mode"`
}

func (s *Server) createGoal(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req goalRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	g, err := sv.createGoal(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sv.goalView(g, true))
}

// createGoal makes a goal, planning, and wakes its coordinator to read the
// code and propose a plan. Its owner is the caller: the coordinator acts as
// them.
func (sv *service) createGoal(req goalRequest) (model.Goal, error) {
	if err := sv.writable(); err != nil {
		return model.Goal{}, err
	}
	if sv.c.User == 0 {
		return model.Goal{}, fmt.Errorf("%w: a goal is a person's: sign in to make one", errBadRequest)
	}
	p, err := sv.data.GetProject(req.Project)
	if err != nil {
		return model.Goal{}, err
	}
	g := model.Goal{ProjectID: p.ID, Owner: sv.c.User}
	set(&g.Title, req.Title)
	set(&g.Description, req.Description)
	set(&g.Budget, req.Budget)
	set(&g.Mode, req.Mode)
	if err := model.CleanGoal(&g); err != nil {
		return g, fmt.Errorf("%w: %v", errBadRequest, err)
	}
	g.Pending = []model.GoalWake{{Event: model.GoalWakeStart, At: now()}}
	g.Log = []model.GoalLogEntry{{At: now(), Kind: model.GoalLogUser, Text: "Goal created; the coordinator reads the code and proposes a plan."}}
	g, err = sv.data.CreateGoal(g)
	if err != nil {
		return g, err
	}
	sv.s.goals().kick(g.ID)
	return g, nil
}

// goal is the caller's service and the goal {gid} of r, or answers r.
func (s *Server) goal(w http.ResponseWriter, r *http.Request) (*service, model.Goal, bool) {
	sv, ok := s.service(w, r)
	if !ok {
		return nil, model.Goal{}, false
	}
	g, err := sv.data.GetGoal(r.PathValue("gid"))
	if err != nil {
		writeErr(w, err)
		return nil, model.Goal{}, false
	}
	return sv, g, true
}

func (s *Server) getGoal(w http.ResponseWriter, r *http.Request) {
	sv, g, ok := s.goal(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, sv.goalView(g, true))
}

func (s *Server) goalLog(w http.ResponseWriter, r *http.Request) {
	_, g, ok := s.goal(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, nonNil(g.Log))
}

func (s *Server) goalChat(w http.ResponseWriter, r *http.Request) {
	sv, g, ok := s.goal(w, r)
	if !ok {
		return
	}
	msgs, err := sv.data.GoalChat(g.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) updateGoal(w http.ResponseWriter, r *http.Request) {
	sv, g, ok := s.goal(w, r)
	if !ok {
		return
	}
	var req goalRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	g, err := sv.updateGoal(g.ID, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sv.goalView(g, true))
}

// updateGoal changes a goal's title, description, budget or mode (the
// fields of req that are set), noting it in the goal's log.
func (sv *service) updateGoal(id string, req goalRequest) (model.Goal, error) {
	if err := sv.writable(); err != nil {
		return model.Goal{}, err
	}
	g, err := sv.data.UpdateGoal(id, func(g *model.Goal) error {
		var changed []string
		if req.Title != nil && strings.TrimSpace(*req.Title) != g.Title {
			changed = append(changed, "title")
		}
		if req.Description != nil && strings.TrimSpace(*req.Description) != g.Description {
			changed = append(changed, "description")
		}
		if req.Budget != nil && *req.Budget != g.Budget {
			changed = append(changed, "budget")
		}
		if req.Mode != nil && *req.Mode != g.Mode {
			changed = append(changed, "mode")
		}
		set(&g.Title, req.Title)
		set(&g.Description, req.Description)
		set(&g.Budget, req.Budget)
		set(&g.Mode, req.Mode)
		if err := model.CleanGoal(g); err != nil {
			return fmt.Errorf("%w: %v", errBadRequest, err)
		}
		if len(changed) > 0 {
			g.AddLog(model.GoalLogEntry{Kind: model.GoalLogUser, Text: "You changed the goal's " + strings.Join(changed, ", ") + "."})
		}
		return nil
	})
	if err != nil {
		return g, err
	}
	sv.s.goals().refresh(g.ID) // a new mode or budget may change what it waits for
	return sv.data.GetGoal(g.ID)
}

// goalAction handles POST /api/goals/{gid}/pause, /resume and /stop.
func (s *Server) goalAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sv, g, ok := s.goal(w, r)
		if !ok {
			return
		}
		g, err := sv.goalAction(g.ID, action)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, sv.goalView(g, true))
	}
}

// goalAction pauses, resumes or stops goal id.
func (sv *service) goalAction(id, action string) (model.Goal, error) {
	if err := sv.writable(); err != nil {
		return model.Goal{}, err
	}
	switch action {
	case "pause":
		return sv.pauseGoal(id)
	case "resume":
		return sv.resumeGoal(id)
	case "stop":
		return sv.stopGoal(id)
	}
	return model.Goal{}, fmt.Errorf("%w: unknown goal action %q (pause, resume or stop)", errBadRequest, action)
}

// pauseGoal holds a goal: task events are kept for when it resumes, and a
// coordinator turn running now is stopped. Its tasks carry on.
func (sv *service) pauseGoal(id string) (model.Goal, error) {
	g, err := sv.data.UpdateGoal(id, func(g *model.Goal) error {
		if !g.Status.Active() {
			return fmt.Errorf("%w: only a planning, working or waiting goal can be paused (this one is %s)", errBadRequest, g.Status)
		}
		g.PausedFrom, g.Status = g.Status, model.GoalPaused
		g.AddLog(model.GoalLogEntry{Kind: model.GoalLogUser, Text: "You paused the goal."})
		return nil
	})
	if err == nil {
		sv.s.goals().cancel(id)
	}
	return g, err
}

// resumeGoal goes on with a paused goal, or one out of budget whose budget
// now allows more, and wakes its coordinator for what came meanwhile.
func (sv *service) resumeGoal(id string) (model.Goal, error) {
	cur, err := sv.data.GetGoal(id)
	if err != nil {
		return cur, err
	}
	if cur.Status == model.GoalOutOfBudget {
		if why := sv.s.goalOverBudget(sv, cur); why != "" {
			return cur, fmt.Errorf("%w: %s; raise the budget first", errBadRequest, why)
		}
	}
	g, err := sv.data.UpdateGoal(id, func(g *model.Goal) error {
		switch g.Status {
		case model.GoalPaused:
			g.Status, g.PausedFrom = g.PausedFrom, ""
			if !g.Status.Active() {
				g.Status = model.GoalWorking
			}
		case model.GoalOutOfBudget:
			g.Status, g.Note = model.GoalWorking, ""
		default:
			return fmt.Errorf("%w: only a paused goal, or one out of budget, can be resumed (this one is %s)", errBadRequest, g.Status)
		}
		g.AddLog(model.GoalLogEntry{Kind: model.GoalLogUser, Text: "You resumed the goal."})
		return nil
	})
	if err != nil {
		return g, err
	}
	sv.s.goals().refresh(id)
	sv.s.goals().kick(id)
	return sv.data.GetGoal(id)
}

// stopGoal ends a goal for good: done, its coordinator never woken again.
// Its tasks stay on the board as they are.
func (sv *service) stopGoal(id string) (model.Goal, error) {
	g, err := sv.data.UpdateGoal(id, func(g *model.Goal) error {
		if g.Status == model.GoalDone {
			return fmt.Errorf("%w: the goal is done already", errBadRequest)
		}
		g.Status, g.PausedFrom, g.WaitingFor, g.Plan, g.Pending = model.GoalDone, "", "", nil, nil
		g.Note = "Stopped by you."
		g.AddLog(model.GoalLogEntry{Kind: model.GoalLogUser, Text: "You stopped the goal."})
		return nil
	})
	if err == nil {
		sv.s.goals().cancel(id)
	}
	return g, err
}

// acceptGoalPlan handles POST /api/goals/{gid}/plan/accept: the waiting
// plan (or plan, the user's edit of it) becomes the goal's tasks: drafts,
// or queued with queue.
func (s *Server) acceptGoalPlan(w http.ResponseWriter, r *http.Request) {
	sv, g, ok := s.goal(w, r)
	if !ok {
		return
	}
	var req struct {
		Plan  []model.GoalPlanItem `json:"plan"`
		Queue bool                 `json:"queue"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := sv.writable(); err != nil {
		writeErr(w, err)
		return
	}
	todos, err := sv.acceptPlan(g, req.Plan, req.Queue)
	if err != nil {
		writeErr(w, err)
		return
	}
	g, _ = sv.data.GetGoal(g.ID)
	writeJSON(w, http.StatusOK, struct {
		goalView
		Created []model.Todo `json:"created"`
	}{sv.goalView(g, true), todos})
}

// acceptPlan turns g's waiting plan, or edited (the user's version of
// it), into the goal's tasks, as the caller: drafts, or queued when queue.
func (sv *service) acceptPlan(g model.Goal, edited []model.GoalPlanItem, queue bool) ([]model.Todo, error) {
	if len(g.Plan) == 0 || g.WaitingFor != model.GoalWaitPlan {
		return nil, fmt.Errorf("%w: the goal has no plan waiting for you", errBadRequest)
	}
	plan := g.Plan
	if edited != nil {
		var err error
		if plan, err = model.CleanPlan(edited); err != nil {
			return nil, fmt.Errorf("%w: %v", errBadRequest, err)
		}
	}
	if why := sv.s.goalBudgetFor(sv, g, len(plan), queue); why != "" {
		return nil, fmt.Errorf("%w: %s", errBadRequest, why)
	}
	// Taken off the goal first, so accepting twice at once makes one set.
	if _, err := sv.data.UpdateGoal(g.ID, func(x *model.Goal) error {
		if len(x.Plan) == 0 || x.WaitingFor != model.GoalWaitPlan {
			return fmt.Errorf("%w: the goal has no plan waiting for you", errBadRequest)
		}
		x.Plan, x.WaitingFor, x.Status, x.Note = nil, "", model.GoalWorking, ""
		how := "as drafts"
		if queue {
			how = "and queued them"
		}
		x.AddLog(model.GoalLogEntry{Kind: model.GoalLogUser, Text: fmt.Sprintf("You accepted the plan of %d tasks %s.", len(plan), how)})
		return nil
	}); err != nil {
		return nil, err
	}
	todos, err := sv.s.createPlanTasks(sv, g, plan, queue)
	sv.s.goals().refresh(g.ID)
	return todos, err
}

// rejectGoalPlan handles POST /api/goals/{gid}/plan/reject: the coordinator
// is woken with the user's feedback to propose another plan.
func (s *Server) rejectGoalPlan(w http.ResponseWriter, r *http.Request) {
	sv, g, ok := s.goal(w, r)
	if !ok {
		return
	}
	var req struct {
		Feedback string `json:"feedback"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	g, err := sv.rejectPlan(g.ID, req.Feedback)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sv.goalView(g, true))
}

// rejectPlan drops the plan goal id waits for the user on, and wakes its
// coordinator with feedback to propose another.
func (sv *service) rejectPlan(id, feedback string) (model.Goal, error) {
	if err := sv.writable(); err != nil {
		return model.Goal{}, err
	}
	g, err := sv.data.UpdateGoal(id, func(g *model.Goal) error {
		if len(g.Plan) == 0 || g.WaitingFor != model.GoalWaitPlan {
			return fmt.Errorf("%w: the goal has no plan waiting for you", errBadRequest)
		}
		fb := strings.TrimSpace(feedback)
		g.Plan, g.WaitingFor, g.Note, g.Status = nil, "", "", model.GoalPlanning
		g.Pending = append(g.Pending, model.GoalWake{Event: model.GoalWakeReplan, Note: fb, At: now()})
		text := "You rejected the plan."
		if fb != "" {
			text = "You rejected the plan: " + fb
		}
		g.AddLog(model.GoalLogEntry{Kind: model.GoalLogUser, Text: text})
		return nil
	})
	if err != nil {
		return g, err
	}
	sv.s.goals().kick(g.ID)
	return g, nil
}

// messageGoal handles POST /api/goals/{gid}/message: a message for the
// goal's coordinator from outside its chat (the MCP tools'
// message_goal). It answers a question the coordinator waits on.
func (s *Server) messageGoal(w http.ResponseWriter, r *http.Request) {
	sv, g, ok := s.goal(w, r)
	if !ok {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	g, err := sv.messageGoal(g.ID, req.Text)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sv.goalView(g, true))
}

// maxGoalMessage bounds a message to a coordinator.
const maxGoalMessage = 20_000

// messageGoal wakes goal id's coordinator with text from the caller. A
// question it waits on counts as answered; a paused goal (or one out of
// budget) keeps the message for when it resumes.
func (sv *service) messageGoal(id, text string) (model.Goal, error) {
	if err := sv.writable(); err != nil {
		return model.Goal{}, err
	}
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return model.Goal{}, fmt.Errorf("%w: the message is empty", errBadRequest)
	case len(text) > maxGoalMessage:
		return model.Goal{}, fmt.Errorf("%w: the message is longer than %d characters", errBadRequest, maxGoalMessage)
	}
	g, err := sv.data.UpdateGoal(id, func(g *model.Goal) error {
		if g.Status == model.GoalDone {
			return fmt.Errorf("%w: the goal is done", errBadRequest)
		}
		g.Pending = append(g.Pending, model.GoalWake{Event: model.GoalWakeMessage, Note: text, At: now()})
		if g.WaitingFor == model.GoalWaitQuestion { // answered
			g.WaitingFor, g.Note = "", ""
			switch {
			case g.Status == model.GoalWaiting:
				g.Status = model.GoalWorking
			case g.Status == model.GoalPaused && g.PausedFrom == model.GoalWaiting:
				g.PausedFrom = model.GoalWorking
			}
		}
		g.AddLog(model.GoalLogEntry{Kind: model.GoalLogUser, Text: "You wrote to the coordinator: " + oneLine(text, 300)})
		return nil
	})
	if err != nil {
		return g, err
	}
	sv.s.goals().refresh(g.ID)
	sv.s.goals().kick(g.ID)
	return sv.data.GetGoal(g.ID)
}

// createPlanTasks makes plan's tasks for g, as sv's caller: drafts, or
// queued when queue. An item's After become its depends_on. A task that
// can't be made stops the rest (they would depend on it).
func (s *Server) createPlanTasks(sv *service, g model.Goal, plan []model.GoalPlanItem, queue bool) ([]model.Todo, error) {
	var made []model.Todo
	for i, it := range plan {
		var deps []string
		for _, a := range it.After {
			deps = append(deps, made[a].ID)
		}
		t, err := sv.addTodo(model.Todo{ProjectID: g.ProjectID, Title: it.Title, Prompt: it.Prompt, DependsOn: deps, Model: it.Model, GoalID: g.ID}, queue)
		if err != nil {
			return made, fmt.Errorf("task %d of the plan: %w", i, err)
		}
		s.goalTimeline(t, g, "created this task from its plan")
		made = append(made, t)
	}
	return made, nil
}

// taskName is how a goal's log names a task: its title, else its
// instructions' first line.
func taskName(t model.Todo) string {
	if t.Title != "" {
		return t.Title
	}
	return plainTitle(t.Prompt)
}

// now is the clock of goals' log entries and wake-ups.
var now = time.Now

// goalReadTimeout bounds a coordinator's read of a task's diff.
const goalReadTimeout = 2 * time.Minute

// goalTimeline notes on t's timeline what g's coordinator did to it, as
// "By coordinator · goal <title>: <what>".
func (s *Server) goalTimeline(t model.Todo, g model.Goal, what string) {
	if s.Todos == nil || s.Todos.Log == nil || s.Todos.Log.Pub == nil {
		return // no task log here (some tests)
	}
	s.Todos.Log.Line(t, model.GoalTimeline+g.Title+": "+what)
}

// goalBudgetFor says why g can't have n more tasks (queued ones, when
// queue), or "" when its budget allows them.
func (s *Server) goalBudgetFor(sv *service, g model.Goal, n int, queue bool) string {
	if max := g.Budget.MaxTasks; max > 0 {
		if have := len(sv.data.GoalTodos(g)); have+n > max {
			return fmt.Sprintf("the goal's budget is %d tasks and it has %d already", max, have)
		}
	}
	if queue && g.Budget.WeeklyShare > 0 {
		if used, ok := s.goalWeekUsed(sv, g); ok && used >= float64(g.Budget.WeeklyShare) {
			return fmt.Sprintf("the goal's tasks used %.1f%% of the weekly window, its share is %d%%", used, g.Budget.WeeklyShare)
		}
	}
	return ""
}

// goalOverBudget says why g is out of budget now, or "": its tasks are all
// made and finished, or its weekly share is used up.
func (s *Server) goalOverBudget(sv *service, g model.Goal) string {
	if max := g.Budget.MaxTasks; max > 0 {
		ts := sv.data.GoalTodos(g)
		finished := !slices.ContainsFunc(ts, func(t model.Todo) bool { return !t.Finished() })
		if len(ts) >= max && finished {
			return fmt.Sprintf("its %d tasks are used up and finished", max)
		}
	}
	if share := g.Budget.WeeklyShare; share > 0 {
		if used, ok := s.goalWeekUsed(sv, g); ok && used >= float64(share) {
			return fmt.Sprintf("its tasks used %.1f%% of the weekly window, its share is %d%%", used, share)
		}
	}
	return ""
}

// ---- the coordinator's board ----

// goalBoard is the board as a goal's coordinator may use it (an
// mcptools.GoalBoard): the goal's project only, changes to the goal's own
// tasks only, within its mode and budget. It has no way to approve or merge
// a task, publish a release or change the checklist. What it does to a task
// is noted on the task's timeline and in the goal's log.
type goalBoard struct {
	b    board
	goal string
}

func (gb goalBoard) sv() *service { return gb.b.sv }

func (gb goalBoard) Goal() (model.Goal, error) { return gb.sv().data.GetGoal(gb.goal) }

// active is the goal, when its coordinator may still change things.
func (gb goalBoard) active() (model.Goal, error) {
	g, err := gb.Goal()
	if err != nil {
		return g, err
	}
	if !g.Status.Active() {
		return g, fmt.Errorf("the goal is %s: nothing can be changed for it now", strings.ReplaceAll(string(g.Status), "_", " "))
	}
	if err := gb.sv().writable(); err != nil {
		return g, err
	}
	return g, nil
}

// own is the goal's active goal and its task id, or why the coordinator
// can't change that task.
func (gb goalBoard) own(id string) (model.Goal, model.Todo, error) {
	g, err := gb.active()
	if err != nil {
		return g, model.Todo{}, err
	}
	t, err := gb.b.Todo(id)
	if err != nil {
		return g, t, err
	}
	if t.GoalID != g.ID {
		return g, t, fmt.Errorf("task %q isn't one of this goal's tasks: you can only change those", id)
	}
	return g, t, nil
}

// did logs what the coordinator did to t, on its timeline and in the goal's log.
func (gb goalBoard) did(g model.Goal, t model.Todo, timeline, log string) {
	gb.sv().s.goalTimeline(t, g, timeline)
	_ = gb.log(model.GoalLogAction, log, "", t.ID)
}

func (gb goalBoard) GoalTodos() ([]model.Todo, error) {
	g, err := gb.Goal()
	if err != nil {
		return nil, err
	}
	return nonNil(gb.sv().data.GoalTodos(g)), nil
}

func (gb goalBoard) Projects() ([]model.Project, []string, error) {
	g, err := gb.Goal()
	if err != nil {
		return nil, nil, err
	}
	p, err := gb.sv().data.GetProject(g.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	return []model.Project{p}, nil, nil
}

func (gb goalBoard) Project(ref string) (model.Project, error) {
	g, err := gb.Goal()
	if err != nil {
		return model.Project{}, err
	}
	p, err := gb.sv().data.GetProject(g.ProjectID)
	if err != nil {
		return p, err
	}
	if ref != "" && ref != p.ID && !strings.EqualFold(ref, p.Name) {
		return model.Project{}, fmt.Errorf("this goal is in %s: its coordinator works there only", p.Name)
	}
	return p, nil
}

func (gb goalBoard) Todos(projectID string) ([]model.Todo, error) {
	if _, err := gb.Project(projectID); err != nil {
		return nil, err
	}
	return gb.b.Todos(projectID)
}

func (gb goalBoard) Todo(id string) (model.Todo, error) {
	t, err := gb.b.Todo(id)
	if err != nil {
		return t, err
	}
	if _, err := gb.Project(t.ProjectID); err != nil {
		return model.Todo{}, fmt.Errorf("no task %q", id)
	}
	return t, nil
}

func (gb goalBoard) CreateTodo(projectID string, nt mcptools.NewTodo) (model.Todo, error) {
	g, err := gb.active()
	if err != nil {
		return model.Todo{}, err
	}
	if _, err := gb.Project(projectID); err != nil {
		return model.Todo{}, err
	}
	enqueue := nt.Enqueue && g.Mode == model.GoalQueue // in ask mode the user queues it
	sv := gb.sv()
	if why := sv.s.goalBudgetFor(sv, g, 1, enqueue); why != "" {
		return model.Todo{}, errors.New(why)
	}
	t, err := sv.addTodo(model.Todo{
		ProjectID: g.ProjectID, Title: nt.Title, Prompt: nt.Prompt, Priority: nt.Priority,
		DependsOn: nt.DependsOn, BuildsOn: nt.BuildsOn, Model: nt.Model, GoalID: g.ID,
	}, enqueue)
	if err != nil {
		return t, err
	}
	how := "a draft"
	if enqueue {
		how = "queued"
	}
	gb.did(g, t, "created this task", fmt.Sprintf("Created task %s (%s): %s", t.ID, how, taskName(t)))
	return t, nil
}

func (gb goalBoard) QueueTodo(id string) (model.Todo, error) {
	g, t, err := gb.own(id)
	if err != nil {
		return t, err
	}
	if g.Mode != model.GoalQueue {
		return t, errors.New("this goal asks before queuing: the user queues its tasks")
	}
	sv := gb.sv()
	if why := sv.s.goalBudgetFor(sv, g, 0, true); why != "" {
		return t, errors.New(why)
	}
	if t, err = gb.b.QueueTodo(id); err != nil {
		return t, err
	}
	gb.did(g, t, "queued this task", "Queued task "+t.ID+": "+taskName(t))
	return t, nil
}

func (gb goalBoard) UpdateTodo(id string, u mcptools.TodoUpdate) (model.Todo, error) {
	g, t, err := gb.own(id)
	if err != nil {
		return t, err
	}
	if t, err = gb.b.UpdateTodo(id, u); err != nil {
		return t, err
	}
	gb.did(g, t, "edited this task", "Edited task "+t.ID+": "+taskName(t))
	return t, nil
}

func (gb goalBoard) DeleteTodo(id string) error {
	_, t, err := gb.own(id)
	if err != nil {
		return err
	}
	if err := gb.b.DeleteTodo(id); err != nil {
		return err
	}
	return gb.log(model.GoalLogAction, "Dropped task "+t.ID+": "+taskName(t), "", "")
}

func (gb goalBoard) StopTodo(id string) (model.Todo, error) {
	g, t, err := gb.own(id)
	if err != nil {
		return t, err
	}
	if t, err = gb.b.StopTodo(id); err != nil {
		return t, err
	}
	gb.did(g, t, "stopped this task", "Stopped task "+t.ID+": "+taskName(t))
	return t, nil
}

// RequeueTodo queues one of the goal's tasks again (restart_todo, queue_todo
// with fresh). In ask mode only a task the user queued before: one that
// failed, or was stopped running.
func (gb goalBoard) RequeueTodo(id string, fresh bool, note string) (model.Todo, error) {
	g, t, err := gb.own(id)
	if err != nil {
		return t, err
	}
	if g.Mode != model.GoalQueue && t.Status == model.StatusDraft && t.StartedAt == nil {
		return t, errors.New("this goal asks before queuing: the user queues its tasks")
	}
	sv := gb.sv()
	if why := sv.s.goalBudgetFor(sv, g, 0, true); why != "" {
		return t, errors.New(why)
	}
	if t, err = gb.b.RequeueTodo(id, fresh, note); err != nil {
		return t, err
	}
	gb.did(g, t, "re-queued this task", "Re-queued task "+t.ID+": "+taskName(t))
	return t, nil
}

func (gb goalBoard) Issues(projectID string) ([]model.Issue, error) {
	if _, err := gb.Project(projectID); err != nil {
		return nil, err
	}
	return gb.b.Issues(projectID)
}

func (gb goalBoard) ReportIssue(projectID string, is model.Issue) (model.Issue, model.UpsertResult, error) {
	if _, err := gb.Project(projectID); err != nil {
		return is, "", err
	}
	return gb.b.ReportIssue(projectID, is)
}

func (gb goalBoard) ReadChecklist(projectID string) (mcptools.Checklist, error) {
	if _, err := gb.Project(projectID); err != nil {
		return mcptools.Checklist{}, err
	}
	return gb.b.ReadChecklist(projectID)
}

// errCoordinator is what a coordinator gets for what only the user does.
var errCoordinator = errors.New("a goal's coordinator can't do that: ask the user")

func (gb goalBoard) CreateRelease(string, mcptools.NewRelease) (mcptools.Release, error) {
	return mcptools.Release{}, errCoordinator
}

func (gb goalBoard) ChangeChecklist(string, mcptools.ChecklistChange) (mcptools.ChecklistSaved, error) {
	return mcptools.ChecklistSaved{}, errCoordinator
}

func (gb goalBoard) MoveChecklistItem(string, mcptools.ChecklistMove) (mcptools.ChecklistMoved, error) {
	return mcptools.ChecklistMoved{}, errCoordinator
}

func (gb goalBoard) ProposePlan(items []model.GoalPlanItem, why string) (string, error) {
	g, err := gb.active()
	if err != nil {
		return "", err
	}
	sv := gb.sv()
	queue := g.Mode == model.GoalQueue
	if why := sv.s.goalBudgetFor(sv, g, len(items), queue); why != "" {
		return "", errors.New(why)
	}
	logText := fmt.Sprintf("Proposed a plan of %d tasks.", len(items))
	if queue {
		logText = fmt.Sprintf("Made a plan of %d tasks and queued them.", len(items))
	}
	if _, err := sv.data.UpdateGoal(g.ID, func(x *model.Goal) error {
		if queue {
			x.Plan, x.WaitingFor, x.Status, x.Note = nil, "", model.GoalWorking, ""
		} else {
			x.Plan, x.WaitingFor, x.Status = items, model.GoalWaitPlan, model.GoalWaiting
			x.Note = fmt.Sprintf("A plan of %d tasks waits for you to accept it.", len(items))
		}
		x.AddLog(model.GoalLogEntry{Kind: model.GoalLogPlan, Text: logText, Why: strings.TrimSpace(why)})
		return nil
	}); err != nil {
		return "", err
	}
	if !queue {
		return "The plan waits for the user to accept it; you are woken again once its tasks run.", nil
	}
	made, err := sv.s.createPlanTasks(sv, g, items, true)
	var ids []string
	for _, t := range made {
		ids = append(ids, t.ID)
	}
	if err != nil {
		return "", fmt.Errorf("made tasks %s, then: %w", strings.Join(ids, ", "), err)
	}
	return "Queued tasks " + strings.Join(ids, ", ") + ", in the plan's order.", nil
}

func (gb goalBoard) SendBack(id, notes string) (model.Todo, error) {
	g, t, err := gb.own(id)
	if err != nil {
		return t, err
	}
	if t.Status != model.StatusReview && t.Status != model.StatusFailed {
		return t, fmt.Errorf("task %q is %s; only a task in review or failed can be sent back", id, t.Status)
	}
	sv := gb.sv()
	if why := sv.s.goalBudgetFor(sv, g, 0, true); why != "" {
		return t, errors.New(why)
	}
	if t, err = sv.rework(id, notes, nil); err != nil {
		return t, err
	}
	gb.did(g, t, "sent this task back with notes", "Sent task "+t.ID+" back: "+oneLine(notes, 300))
	return t, nil
}

func (gb goalBoard) ReviewNote(id, note string) (model.Todo, error) {
	g, t, err := gb.own(id)
	if err != nil {
		return t, err
	}
	t, err = gb.sv().data.UpdateTodo(id, func(t *model.Todo) error {
		t.GoalNote = &model.GoalNote{Goal: g.ID, Text: strings.TrimSpace(note), At: now()}
		return nil
	})
	if err != nil {
		return t, err
	}
	gb.did(g, t, "posted a review note", "Posted a review note on task "+t.ID+": "+oneLine(note, 300))
	return t, nil
}

func (gb goalBoard) AddLog(text, why, todo string) error {
	return gb.log(model.GoalLogNote, text, why, todo)
}

// log adds an entry of kind to the goal's log.
func (gb goalBoard) log(kind, text, why, todo string) error {
	_, err := gb.sv().data.UpdateGoal(gb.goal, func(g *model.Goal) error {
		g.AddLog(model.GoalLogEntry{Kind: kind, Text: strings.TrimSpace(text), Why: strings.TrimSpace(why), Todo: strings.TrimSpace(todo)})
		return nil
	})
	return err
}

func (gb goalBoard) AskUser(question string) error {
	if _, err := gb.active(); err != nil {
		return err
	}
	_, err := gb.sv().data.UpdateGoal(gb.goal, func(g *model.Goal) error {
		g.Status, g.WaitingFor, g.Note = model.GoalWaiting, model.GoalWaitQuestion, strings.TrimSpace(question)
		g.AddLog(model.GoalLogEntry{Kind: model.GoalLogNote, Text: "Asked you: " + strings.TrimSpace(question)})
		return nil
	})
	return err
}

func (gb goalBoard) FinishGoal(summary string) error {
	if _, err := gb.active(); err != nil {
		return err
	}
	_, err := gb.sv().data.UpdateGoal(gb.goal, func(g *model.Goal) error {
		g.Status, g.WaitingFor, g.Plan, g.Pending = model.GoalDone, "", nil, nil
		g.Note = strings.TrimSpace(summary)
		g.AddLog(model.GoalLogEntry{Kind: model.GoalLogStatus, Text: "Finished the goal: " + strings.TrimSpace(summary)})
		return nil
	})
	return err
}

func (gb goalBoard) Diff(id string) (string, error) {
	if _, err := gb.Todo(id); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), goalReadTimeout)
	defer cancel()
	return gb.sv().diff(ctx, id)
}

func (gb goalBoard) Log(id string) (string, error) {
	if _, err := gb.Todo(id); err != nil {
		return "", err
	}
	return gb.sv().log(id)
}

// ---- the user's goal tools (mcptools.GoalManager) on the board ----

func (b board) Goals(projectID string) ([]mcptools.GoalView, error) {
	if projectID != "" {
		if _, err := b.sv.data.GetProject(projectID); err != nil {
			return nil, fmt.Errorf("no project %q (see list_projects)", projectID)
		}
	}
	out := []mcptools.GoalView{}
	for _, g := range b.sv.data.ListGoals(projectID) {
		out = append(out, b.sv.goalView(g, false))
	}
	return out, nil
}

func (b board) GetGoal(id string) (mcptools.GoalView, error) {
	g, err := b.sv.data.GetGoal(id)
	if err != nil {
		return mcptools.GoalView{}, fmt.Errorf("no goal %q (see list_goals)", id)
	}
	return b.sv.goalView(g, true), nil
}

// view is g as the goal tools answer it, or err.
func (b board) view(g model.Goal, err error) (mcptools.GoalView, error) {
	if err != nil {
		return mcptools.GoalView{}, err
	}
	return b.sv.goalView(g, true), nil
}

func (b board) CreateGoal(ng mcptools.NewGoal) (mcptools.GoalView, error) {
	return b.view(b.sv.createGoal(goalRequest{Project: ng.Project, Title: &ng.Title, Description: &ng.Description,
		Mode: &ng.Mode, Budget: &ng.Budget}))
}

func (b board) UpdateGoal(id string, c mcptools.GoalChange) (mcptools.GoalView, error) {
	if _, err := b.GetGoal(id); err != nil {
		return mcptools.GoalView{}, err
	}
	return b.view(b.sv.updateGoal(id, goalRequest{Title: c.Title, Description: c.Description, Mode: c.Mode, Budget: c.Budget}))
}

func (b board) GoalAction(id, action string) (mcptools.GoalView, error) {
	if _, err := b.GetGoal(id); err != nil {
		return mcptools.GoalView{}, err
	}
	return b.view(b.sv.goalAction(id, action))
}

func (b board) AcceptGoalPlan(id string, plan []model.GoalPlanItem, queue bool) (mcptools.GoalView, []model.Todo, error) {
	if err := b.sv.writable(); err != nil {
		return mcptools.GoalView{}, nil, err
	}
	g, err := b.sv.data.GetGoal(id)
	if err != nil {
		return mcptools.GoalView{}, nil, fmt.Errorf("no goal %q (see list_goals)", id)
	}
	made, err := b.sv.acceptPlan(g, plan, queue)
	if err != nil {
		return mcptools.GoalView{}, made, err
	}
	v, err := b.view(b.sv.data.GetGoal(id))
	return v, made, err
}

func (b board) RejectGoalPlan(id, feedback string) (mcptools.GoalView, error) {
	if _, err := b.GetGoal(id); err != nil {
		return mcptools.GoalView{}, err
	}
	return b.view(b.sv.rejectPlan(id, feedback))
}

func (b board) MessageGoal(id, text string) (mcptools.GoalView, error) {
	if _, err := b.GetGoal(id); err != nil {
		return mcptools.GoalView{}, err
	}
	return b.view(b.sv.messageGoal(id, text))
}
