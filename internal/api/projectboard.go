package api

import (
	"fmt"
	"strings"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
)

// projectBoard is the board as a run in one project uses it: a task's run,
// a project chat, a chat a hub asks a runner for in one of its projects. It
// sees and changes that project only: its tasks, issues, checklist, goals
// and Try terminal. Other projects, even in the same org, look as if they
// didn't exist. Only the general chat (no project) gets the whole org's
// board.
//
// It wraps board without embedding it, so a method added to board later
// isn't reachable from a project's run until it is scoped here too.
type projectBoard struct {
	b       board
	project string // the project's id
}

// errOtherProject is what a project's run gets for another project.
func (pb projectBoard) errOtherProject(ref string) error {
	return fmt.Errorf("no project %q: this run works in its own project only", ref)
}

func (pb projectBoard) own() (model.Project, error) {
	p, err := pb.b.sv.data.GetProject(pb.project)
	if err != nil {
		return p, fmt.Errorf("the project is gone")
	}
	return p, nil
}

// check is nil when ref ("" for the run's own) names the run's project.
func (pb projectBoard) check(ref string) error {
	_, err := pb.Project(ref)
	return err
}

func (pb projectBoard) Projects() ([]model.Project, []string, error) {
	p, err := pb.own()
	if err != nil {
		return nil, nil, err
	}
	return []model.Project{p}, nil, nil
}

func (pb projectBoard) Project(ref string) (model.Project, error) {
	p, err := pb.own()
	if err != nil {
		return p, err
	}
	if ref != "" && ref != p.ID && !strings.EqualFold(ref, p.Name) {
		return model.Project{}, pb.errOtherProject(ref)
	}
	return p, nil
}

// todo is task id, if it is in the run's project.
func (pb projectBoard) todo(id string) (model.Todo, error) {
	t, err := pb.b.Todo(id)
	if err != nil {
		return t, err
	}
	if t.ProjectID != pb.project {
		return model.Todo{}, fmt.Errorf("no task %q", id)
	}
	return t, nil
}

// goal is goal id, if it is in the run's project.
func (pb projectBoard) goal(id string) error {
	g, err := pb.b.sv.data.GetGoal(id)
	if err != nil || g.ProjectID != pb.project {
		return fmt.Errorf("no goal %q (see list_goals)", id)
	}
	return nil
}

func (pb projectBoard) Todos(projectID string) ([]model.Todo, error) {
	if err := pb.check(projectID); err != nil {
		return nil, err
	}
	return pb.b.Todos(pb.project)
}

func (pb projectBoard) Todo(id string) (model.Todo, error) { return pb.todo(id) }

// WorkFiles lists the files task t, of this project, changes.
func (pb projectBoard) WorkFiles(t model.Todo) ([]string, error) {
	if t.ProjectID != pb.project {
		return nil, fmt.Errorf("no task %q", t.ID)
	}
	return pb.b.WorkFiles(t)
}

func (pb projectBoard) CreateTodo(projectID string, t mcptools.NewTodo) (model.Todo, error) {
	if err := pb.check(projectID); err != nil {
		return model.Todo{}, err
	}
	return pb.b.CreateTodo(pb.project, t)
}

func (pb projectBoard) QueueTodo(id string) (model.Todo, error) {
	if _, err := pb.todo(id); err != nil {
		return model.Todo{}, err
	}
	return pb.b.QueueTodo(id)
}

func (pb projectBoard) StopTodo(id string) (model.Todo, error) {
	if _, err := pb.todo(id); err != nil {
		return model.Todo{}, err
	}
	return pb.b.StopTodo(id)
}

func (pb projectBoard) RequeueTodo(id string, fresh bool, note string) (model.Todo, error) {
	if _, err := pb.todo(id); err != nil {
		return model.Todo{}, err
	}
	return pb.b.RequeueTodo(id, fresh, note)
}

func (pb projectBoard) StartNow(id string) (model.Todo, error) {
	if _, err := pb.todo(id); err != nil {
		return model.Todo{}, err
	}
	return pb.b.StartNow(id)
}

func (pb projectBoard) PaceWait(t model.Todo) string {
	if t.ProjectID != pb.project {
		return ""
	}
	return pb.b.PaceWait(t)
}

func (pb projectBoard) UpdateTodo(id string, u mcptools.TodoUpdate) (model.Todo, error) {
	if _, err := pb.todo(id); err != nil {
		return model.Todo{}, err
	}
	return pb.b.UpdateTodo(id, u)
}

func (pb projectBoard) DeleteTodo(id string) error {
	if _, err := pb.todo(id); err != nil {
		return err
	}
	return pb.b.DeleteTodo(id)
}

func (pb projectBoard) Issues(projectID string) ([]model.Issue, error) {
	if err := pb.check(projectID); err != nil {
		return nil, err
	}
	return pb.b.Issues(pb.project)
}

func (pb projectBoard) ReportIssue(projectID string, is model.Issue) (model.Issue, model.UpsertResult, error) {
	if err := pb.check(projectID); err != nil {
		return model.Issue{}, "", err
	}
	return pb.b.ReportIssue(pb.project, is)
}

func (pb projectBoard) ReadTry(projectID string, lines int) (mcptools.TryOutput, error) {
	if err := pb.check(projectID); err != nil {
		return mcptools.TryOutput{}, err
	}
	return pb.b.ReadTry(pb.project, lines)
}

func (pb projectBoard) CreateRelease(projectID string, r mcptools.NewRelease) (mcptools.Release, error) {
	if err := pb.check(projectID); err != nil {
		return mcptools.Release{}, err
	}
	return pb.b.CreateRelease(pb.project, r)
}

func (pb projectBoard) ReadChecklist(projectID string) (mcptools.Checklist, error) {
	if err := pb.check(projectID); err != nil {
		return mcptools.Checklist{}, err
	}
	return pb.b.ReadChecklist(pb.project)
}

func (pb projectBoard) ChangeChecklist(projectID string, c mcptools.ChecklistChange) (mcptools.ChecklistSaved, error) {
	if err := pb.check(projectID); err != nil {
		return mcptools.ChecklistSaved{}, err
	}
	return pb.b.ChangeChecklist(pb.project, c)
}

func (pb projectBoard) MoveChecklistItem(projectID string, m mcptools.ChecklistMove) (mcptools.ChecklistMoved, error) {
	if err := pb.check(projectID); err != nil {
		return mcptools.ChecklistMoved{}, err
	}
	return pb.b.MoveChecklistItem(pb.project, m)
}

// Goals lists the project's goals, also for projectID "" (every project's
// goals, on the org's board).
func (pb projectBoard) Goals(projectID string) ([]mcptools.GoalView, error) {
	if err := pb.check(projectID); err != nil {
		return nil, err
	}
	return pb.b.Goals(pb.project)
}

func (pb projectBoard) GetGoal(id string) (mcptools.GoalView, error) {
	if err := pb.goal(id); err != nil {
		return mcptools.GoalView{}, err
	}
	return pb.b.GetGoal(id)
}

func (pb projectBoard) CreateGoal(ng mcptools.NewGoal) (mcptools.GoalView, error) {
	if err := pb.check(ng.Project); err != nil {
		return mcptools.GoalView{}, err
	}
	ng.Project = pb.project
	return pb.b.CreateGoal(ng)
}

func (pb projectBoard) UpdateGoal(id string, c mcptools.GoalChange) (mcptools.GoalView, error) {
	if err := pb.goal(id); err != nil {
		return mcptools.GoalView{}, err
	}
	return pb.b.UpdateGoal(id, c)
}

func (pb projectBoard) GoalAction(id, action string) (mcptools.GoalView, error) {
	if err := pb.goal(id); err != nil {
		return mcptools.GoalView{}, err
	}
	return pb.b.GoalAction(id, action)
}

func (pb projectBoard) AcceptGoalPlan(id string, plan []model.GoalPlanItem, queue bool) (mcptools.GoalView, []model.Todo, error) {
	if err := pb.goal(id); err != nil {
		return mcptools.GoalView{}, nil, err
	}
	return pb.b.AcceptGoalPlan(id, plan, queue)
}

func (pb projectBoard) RejectGoalPlan(id, feedback string) (mcptools.GoalView, error) {
	if err := pb.goal(id); err != nil {
		return mcptools.GoalView{}, err
	}
	return pb.b.RejectGoalPlan(id, feedback)
}

func (pb projectBoard) MessageGoal(id, text string) (mcptools.GoalView, error) {
	if err := pb.goal(id); err != nil {
		return mcptools.GoalView{}, err
	}
	return pb.b.MessageGoal(id, text)
}

// The planning tools in a project's run: the cards of its project, and
// links from those (or cards of no project) to its own tasks and goals.

func (pb projectBoard) Cards(projectID string) ([]mcptools.CardRow, error) {
	if err := pb.check(projectID); err != nil {
		return nil, err
	}
	return pb.b.Cards(pb.project)
}

func (pb projectBoard) CreateCard(nc mcptools.NewCard) (mcptools.CardRow, error) {
	if err := pb.check(nc.Project); err != nil {
		return mcptools.CardRow{}, err
	}
	nc.Project = pb.project
	return pb.b.CreateCard(nc)
}

func (pb projectBoard) LinkCard(cardID, kind, workID string) (mcptools.CardRow, error) {
	c, err := pb.b.sv.data.GetCard(cardID)
	if err != nil || (c.ProjectID != "" && c.ProjectID != pb.project) {
		return mcptools.CardRow{}, fmt.Errorf("no card %q (see list_cards)", cardID)
	}
	switch kind {
	case model.LinkTask:
		if _, err := pb.todo(workID); err != nil {
			return mcptools.CardRow{}, err
		}
	case model.LinkGoal:
		if err := pb.goal(workID); err != nil {
			return mcptools.CardRow{}, err
		}
	}
	return pb.b.LinkCard(cardID, kind, workID)
}
