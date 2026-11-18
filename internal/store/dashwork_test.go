package store

import (
	"testing"

	"github.com/klahr/dash/internal/model"
)

// Dash's own tasks are for a member: who connected the tracker (its
// cards), who turned on build fixes (CI fixes), who added the project, or
// else the org's owner; never someone who left, nor a planner.
func TestDashWorkFor(t *testing.T) {
	t.Parallel()
	s, acc, owner, org, _ := tokenAccounts(t)
	carol := addMember(t, acc, owner, org, "carol", RoleMember)
	dave := addMember(t, acc, owner, org, "dave", RoleMember)
	erin := addMember(t, acc, owner, org, "erin", RoleAdmin)
	pat := addMember(t, acc, owner, org, "pat", RolePlanner)

	p, err := s.CreateProject(model.Project{Name: "web", OrgID: org})
	if err != nil {
		t.Fatal(err)
	}
	plain := model.Todo{ProjectID: p.ID}
	fix := model.Todo{ProjectID: p.ID, CIRun: 42}
	card := model.Todo{ProjectID: p.ID, ExternalRef: &model.ExternalRef{Provider: "github", ID: "7"}}
	for _, td := range []model.Todo{plain, fix, card} {
		if got := s.DashWorkFor(td); got != owner {
			t.Errorf("nothing recorded: %d, want the org's owner %d", got, owner)
		}
	}

	set := func(fn func(*model.Project)) {
		t.Helper()
		if _, err := s.UpdateProject(p.ID, func(x *model.Project) error { fn(x); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	set(func(x *model.Project) { x.AddedBy, x.CIBy = carol, dave })
	if got := s.DashWorkFor(plain); got != carol {
		t.Errorf("plain task: %d, want who added the project %d", got, carol)
	}
	if got := s.DashWorkFor(fix); got != dave {
		t.Errorf("CI fix: %d, want who turned on build fixes %d", got, dave)
	}
	// The board on Dash's own: a card's task goes by who added the project.
	if got := s.DashWorkFor(card); got != carol {
		t.Errorf("card on Dash's board: %d, want %d", got, carol)
	}
	if err := s.PutTrackerConfig(model.TrackerConfig{ProjectID: p.ID, Backend: "github"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddProjectEvent(model.ProjectEvent{ProjectID: p.ID, Kind: BoardSwitched, Text: "moved", UserID: erin}); err != nil {
		t.Fatal(err)
	}
	if got := s.DashWorkFor(card); got != erin {
		t.Errorf("tracker card: %d, want who connected the tracker %d", got, erin)
	}

	// dave left, and a planner never runs tasks: on to the next in line.
	if err := acc.RemoveMember(owner, org, dave); err != nil {
		t.Fatal(err)
	}
	set(func(x *model.Project) { x.AddedBy = pat })
	if got := s.DashWorkFor(fix); got != owner {
		t.Errorf("CI fix, dave gone, added by a planner: %d, want the owner %d", got, owner)
	}
}

// GiveDashWork makes the member its RunBy, keeps the task Dash's
// (CreatedBy 0), and leaves people's tasks and given ones alone.
func TestGiveDashWork(t *testing.T) {
	t.Parallel()
	s, acc, owner, org, _ := tokenAccounts(t)
	carol := addMember(t, acc, owner, org, "carol", RoleMember)
	p, err := s.CreateProject(model.Project{Name: "web", OrgID: org, AddedBy: carol})
	if err != nil {
		t.Fatal(err)
	}
	dash, _ := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: "Fix failing build: CI", CIRun: 1})
	got, err := s.GiveDashWork(dash.ID)
	if err != nil || got.RunBy != carol || got.CreatedBy != 0 {
		t.Fatalf("Dash's task: RunBy %d CreatedBy %d %v", got.RunBy, got.CreatedBy, err)
	}
	if w := got.WorkFor(); len(w) != 1 || w[0] != carol {
		t.Errorf("WorkFor: %v", w)
	}
	set, _ := s.UpdateProject(p.ID, func(x *model.Project) error { x.AddedBy = owner; return nil })
	if again, _ := s.GiveDashWork(dash.ID); again.RunBy != carol || set.AddedBy != owner {
		t.Errorf("given again: RunBy %d", again.RunBy)
	}
	mine, _ := s.CreateTodo(model.Todo{ProjectID: p.ID, CreatedBy: owner, Title: "mine"})
	if got, _ := s.GiveDashWork(mine.ID); got.RunBy != 0 {
		t.Errorf("a person's task got RunBy %d", got.RunBy)
	}
}
