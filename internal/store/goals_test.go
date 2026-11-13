package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/klahr/dash/internal/model"
)

func TestGoalsPersistAndGoWithTheirProject(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p, err := s.CreateProject(model.Project{Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateProject(model.Project{Name: "api"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateGoal(model.Goal{ProjectID: "nope", Title: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("goal in a missing project: %v", err)
	}
	g, err := s.CreateGoal(model.Goal{ProjectID: p.ID, Owner: 1, Title: "Faster search", Mode: model.GoalAsk,
		Budget: model.GoalBudget{MaxTasks: 4}})
	if err != nil {
		t.Fatal(err)
	}
	if g.Status != model.GoalPlanning || g.ID == "" {
		t.Fatalf("new goal %+v", g)
	}
	keep, err := s.CreateGoal(model.Goal{ProjectID: other.ID, Owner: 1, Title: "Keep me"})
	if err != nil {
		t.Fatal(err)
	}
	g, err = s.UpdateGoal(g.ID, func(g *model.Goal) error {
		g.Running = true
		g.Plan = []model.GoalPlanItem{{Title: "Index", Prompt: "Add an index"}, {Title: "Use it", Prompt: "Query it", After: []int{0}}}
		g.AddLog(model.GoalLogEntry{Kind: model.GoalLogPlan, Text: "Proposed a plan", Why: "two steps"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A copy handed out can't change the stored goal.
	g.Plan[1].After[0] = 9
	if got, _ := s.GetGoal(g.ID); got.Plan[1].After[0] != 0 {
		t.Fatal("a goal handed out shares its plan with the store")
	}
	if err := s.AppendGoalChat(g.ID, model.ChatMessage{Role: "user", Content: "start"}, model.ChatMessage{Role: "assistant", Content: "planned"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendGoalChat(g.ID, model.ChatMessage{Role: "user", Content: "more?"}); err != nil {
		t.Fatal(err)
	}
	td, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "Add an index", GoalID: g.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "not the goal's"}); err != nil {
		t.Fatal(err)
	}
	if ts := s.GoalTodos(g); len(ts) != 1 || ts[0].ID != td.ID {
		t.Fatalf("goal todos %+v", ts)
	}

	s = reopen(t, s, path)
	got, err := s.GetGoal(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The coordinator's turn died with the old process.
	if got.Running || got.Title != "Faster search" || len(got.Plan) != 2 || len(got.Log) != 1 || got.Log[0].Why != "two steps" {
		t.Fatalf("after reopen %+v", got)
	}
	if chat := s.GoalChat(g.ID); len(chat) != 3 || chat[2].Content != "more?" {
		t.Fatalf("goal chat %+v", chat)
	}
	if gs := s.ListGoals(p.ID); len(gs) != 1 || gs[0].ID != g.ID {
		t.Fatalf("project's goals %+v", gs)
	}
	if gs := s.ListGoals(""); len(gs) != 2 {
		t.Fatalf("all goals %+v", gs)
	}

	if _, err := s.DeleteProject(p.ID); err != nil {
		t.Fatal(err)
	}
	s = reopen(t, s, path)
	if _, err := s.GetGoal(g.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("goal of a deleted project: %v", err)
	}
	if len(s.GoalChat(g.ID)) != 0 {
		t.Fatal("the deleted goal's chat stayed")
	}
	if _, err := s.GetGoal(keep.ID); err != nil {
		t.Fatalf("another project's goal: %v", err)
	}
}

func TestGoalsAreScopedToTheirOrg(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p, err := s.CreateProject(model.Project{Name: "web", OrgID: 1})
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateGoal(model.Goal{ProjectID: p.ID, Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	mine, theirs := s.ForOrg(1), s.ForOrg(2)
	if _, err := mine.GetGoal(g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := theirs.GetGoal(g.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org read the goal: %v", err)
	}
	if _, err := theirs.UpdateGoal(g.ID, func(*model.Goal) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org changed the goal: %v", err)
	}
	if _, err := theirs.GoalChat(g.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org read the goal's chat: %v", err)
	}
	if _, err := theirs.CreateGoal(model.Goal{ProjectID: p.ID, Title: "y"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org made a goal in the project: %v", err)
	}
	if len(theirs.ListGoals("")) != 0 || len(mine.ListGoals("")) != 1 {
		t.Fatal("goal lists cross orgs")
	}
}
