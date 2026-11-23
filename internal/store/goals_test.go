package store

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

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

// Deleting a goal takes it and its conversation, in one go; what pointed to
// it stays and forgets it: its tasks keep their place on the board, and the
// plan that launched it no longer names it. Another org can't delete it.
func TestDeleteGoalKeepsItsTasks(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	evs := &recordedEvents{}
	s, err := Open(path, evs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	acc := s.Accounts()
	if err := acc.Bootstrap("admin", "s3cret-pass"); err != nil {
		t.Fatal(err)
	}
	admin, _ := acc.EnvUser()
	def, _ := acc.DefaultOrg()
	other, err := acc.CreateOrg(admin.ID, "Other")
	if err != nil {
		t.Fatal(err)
	}
	o := s.ForOrg(def.ID).AsUser(admin.ID)
	p, err := o.CreateProject(model.Project{Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := o.CreateGoal(model.Goal{ProjectID: p.ID, Owner: admin.ID, Title: "Faster search"})
	if err != nil {
		t.Fatal(err)
	}
	keep, err := o.CreateGoal(model.Goal{ProjectID: p.ID, Owner: admin.ID, Title: "Keep me"})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.AppendGoalChat(g.ID, model.ChatMessage{Role: "user", Content: "start"}); err != nil {
		t.Fatal(err)
	}
	if err := o.AppendGoalChat(keep.ID, model.ChatMessage{Role: "user", Content: "mine"}); err != nil {
		t.Fatal(err)
	}
	first, err := o.CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "Add an index", GoalID: g.ID})
	if err != nil {
		t.Fatal(err)
	}
	second, err := o.CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "Use it", GoalID: g.ID, DependsOn: []string{first.ID},
		GoalNote: &model.GoalNote{Goal: g.ID, Text: "looks right"}})
	if err != nil {
		t.Fatal(err)
	}
	kept, err := o.CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "Other goal's", GoalID: keep.ID})
	if err != nil {
		t.Fatal(err)
	}
	pl, err := o.CreatePlan(model.Plan{Idea: "Search", Step: "launch"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := o.UpdatePlan(pl.ID, func(x *model.Plan) error { x.GoalID, x.ProjectID, x.LaunchedAt = g.ID, p.ID, &now; return nil }); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.ForOrg(other.ID).DeleteGoal(g.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org deleted the goal: %v", err)
	}
	evs.evs = nil
	gone, freed, err := o.DeleteGoal(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gone.ID != g.ID || len(freed) != 2 {
		t.Fatalf("deleted %+v, freed %+v", gone, freed)
	}
	var deleted, updated int
	for _, ev := range evs.evs {
		switch ev.Type {
		case model.EvGoalDeleted:
			if eg, ok := ev.Data.(model.Goal); !ok || eg.ID != g.ID || ev.ProjectID != p.ID || ev.OrgID != def.ID {
				t.Errorf("goal.deleted %+v", ev)
			}
			deleted++
		case model.EvTodoUpdated:
			if et, ok := ev.Data.(model.Todo); !ok || et.GoalID != "" {
				t.Errorf("todo.updated %+v", ev.Data)
			}
			updated++
		}
	}
	if deleted != 1 || updated != 2 {
		t.Errorf("events: %d goal.deleted, %d todo.updated", deleted, updated)
	}
	if _, _, err := o.DeleteGoal(g.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}

	s = reopen(t, s, path)
	o = s.ForOrg(def.ID).AsUser(admin.ID)
	if _, err := s.GetGoal(g.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted goal: %v", err)
	}
	if len(s.GoalChat(g.ID)) != 0 {
		t.Fatal("the deleted goal's chat stayed")
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM chat_messages WHERE kind = ? AND owner_id = ?", chatGoal, g.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("chat rows left: %d %v", n, err)
	}
	for _, want := range []model.Todo{first, second} {
		got, err := s.GetTodo(want.ID)
		if err != nil {
			t.Fatalf("the goal's task went: %v", err)
		}
		if got.GoalID != "" || got.Status != want.Status || got.ProjectID != p.ID || !slices.Equal(got.DependsOn, want.DependsOn) {
			t.Errorf("task after the goal went: %+v", got)
		}
	}
	if got, _ := s.GetTodo(second.ID); got.GoalNote == nil || got.GoalNote.Text != "looks right" || got.GoalNote.Goal != "" {
		t.Errorf("the coordinator's review note went: %+v", got.GoalNote)
	}
	if got, _ := s.GetTodo(kept.ID); got.GoalID != keep.ID {
		t.Errorf("another goal's task lost its goal: %+v", got)
	}
	if got, err := s.GetGoal(keep.ID); err != nil || got.Title != "Keep me" {
		t.Fatalf("another goal: %+v %v", got, err)
	}
	if chat := s.GoalChat(keep.ID); len(chat) != 1 {
		t.Fatalf("another goal's chat: %+v", chat)
	}
	got, err := o.GetPlan(pl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GoalID != "" || got.ProjectID != p.ID || got.LaunchedAt == nil || got.Idea != "Search" {
		t.Errorf("plan after its goal went: %+v", got)
	}
}
