package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// Cards are their org's: they keep their column order through moves, take
// comments and links to the org's tasks, and another org finds none of it.
// A linked task's change is announced to Planning; a planner can be
// invited and survives a restart.
func TestPlanningCards(t *testing.T) {
	t.Parallel()

	evs := &recordedEvents{}
	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, evs)
	if err != nil {
		t.Fatal(err)
	}
	acc := s.Accounts()
	if err := acc.Bootstrap("admin", "s3cret-pass"); err != nil {
		t.Fatal(err)
	}
	admin, _ := acc.EnvUser()
	def, _ := acc.DefaultOrg()
	pat := addMember(t, acc, admin.ID, def.ID, "pat", RolePlanner)
	if m, err := acc.Membership(pat, def.ID); err != nil || m.Role != RolePlanner {
		t.Fatalf("planner membership: %+v %v", m, err)
	}
	other, err := acc.CreateOrg(admin.ID, "Other")
	if err != nil {
		t.Fatal(err)
	}
	o := s.ForOrg(def.ID).AsUser(pat)
	p, err := s.CreateProject(model.Project{Name: "web", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	add := func(title string, col model.CardColumn) model.Card {
		c := model.Card{Title: title, Column: col, ProjectID: p.ID}
		if err := model.CleanCard(&c); err != nil {
			t.Fatal(err)
		}
		c, err := o.CreateCard(c)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	a, b, c := add("A", model.CardIdeas), add("B", model.CardIdeas), add("C", model.CardIdeas)
	if a.Position != 0 || b.Position != 1 || c.Position != 2 || a.CreatedBy != pat {
		t.Fatalf("positions %d %d %d, by %d", a.Position, b.Position, c.Position, a.CreatedBy)
	}
	order := func(col model.CardColumn) (out []string) {
		cs, err := o.ListCards("")
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range cs {
			if x.Column == col {
				out = append(out, x.Title)
			}
		}
		return out
	}
	if _, err := o.MoveCard(c.ID, model.CardIdeas, 0); err != nil {
		t.Fatal(err)
	}
	if got := order(model.CardIdeas); len(got) != 3 || got[0] != "C" || got[1] != "A" || got[2] != "B" {
		t.Errorf("after reorder: %v", got)
	}
	if _, err := o.MoveCard(a.ID, model.CardPlanned, 99); err != nil {
		t.Fatal(err)
	}
	if got := order(model.CardIdeas); len(got) != 2 || got[0] != "C" || got[1] != "B" {
		t.Errorf("ideas after move: %v", got)
	}
	if got, _ := o.GetCard(a.ID); got.Column != model.CardPlanned || got.Position != 0 {
		t.Errorf("moved card: %+v", got)
	}
	if _, err := o.UpdateCard(b.ID, func(x *model.Card) error { x.Target, x.Column = "1.4", model.CardDone; return nil }); err != nil {
		t.Fatal(err)
	}
	if got, _ := o.GetCard(b.ID); got.Target != "1.4" || got.Column != model.CardIdeas {
		t.Errorf("update changed the column or not the target: %+v", got)
	}

	// Comments and links.
	m, err := o.AddCardComment(a.ID, "Customers ask for this")
	if err != nil || m.AuthorName != "pat" {
		t.Fatalf("comment: %+v %v", m, err)
	}
	td, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: "do A", Prompt: "do A"})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.LinkCard(a.ID, model.LinkTask, td.ID); err != nil {
		t.Fatal(err)
	}
	if err := o.LinkCard(a.ID, model.LinkTask, td.ID); err != nil {
		t.Errorf("linking twice: %v", err)
	}
	if err := o.LinkCard(a.ID, model.LinkGoal, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("link to a missing goal: %v", err)
	}
	links, _ := o.CardLinks()
	if len(links[a.ID]) != 1 || links[a.ID][0].WorkID != td.ID {
		t.Errorf("links %+v", links)
	}
	if cs, _ := o.CardsLinkedTo(model.LinkTask, td.ID); len(cs) != 1 || cs[0].ID != a.ID {
		t.Errorf("cards linked to the task: %+v", cs)
	}
	evs.evs = nil
	if _, err := s.SetStatus(td.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs.evs {
		if ev.Type == model.EvPlanningWork && ev.OrgID == def.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("no planning.work event for the linked task: %+v", evs.evs)
	}

	// Another org sees nothing of it.
	x := s.ForOrg(other.ID).AsUser(admin.ID)
	if l, _ := x.ListCards(""); len(l) != 0 {
		t.Errorf("other org lists %+v", l)
	}
	if _, err := x.GetCard(a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("other org reads it: %v", err)
	}
	if _, err := x.MoveCard(a.ID, model.CardDone, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("other org moves it: %v", err)
	}
	if _, err := x.AddCardComment(a.ID, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("other org comments: %v", err)
	}
	if err := x.LinkCard(a.ID, model.LinkTask, td.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("other org links: %v", err)
	}
	if err := x.DeleteCard(a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("other org deletes it: %v", err)
	}

	// A restart keeps it all, and the link index.
	s.Close()
	evs2 := &recordedEvents{}
	s, err = Open(path, evs2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	o = s.ForOrg(def.ID).AsUser(pat)
	if ms, _ := o.CardComments(a.ID); len(ms) != 1 || ms[0].Text != "Customers ask for this" {
		t.Errorf("comments after restart: %+v", ms)
	}
	if m, err := s.Accounts().Membership(pat, def.ID); err != nil || m.Role != RolePlanner {
		t.Errorf("planner after restart: %+v %v", m, err)
	}
	if _, err := s.SetStatus(td.ID, model.StatusInProgress, ""); err != nil {
		t.Fatal(err)
	}
	if len(evs2.evs) == 0 || evs2.evs[len(evs2.evs)-1].Type != model.EvPlanningWork {
		t.Errorf("link index not rebuilt: %+v", evs2.evs)
	}

	// Deleting the card takes its comments and links, not the task.
	if err := o.DeleteCard(a.ID); err != nil {
		t.Fatal(err)
	}
	if l, _ := o.CardLinks(); len(l) != 0 {
		t.Errorf("links left: %+v", l)
	}
	if _, err := s.GetTodo(td.ID); err != nil {
		t.Errorf("the task went too: %v", err)
	}
	// Deleting the project keeps its cards, as the org's.
	if _, err := s.DeleteProject(p.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := o.GetCard(b.ID); err != nil || got.ProjectID != "" {
		t.Errorf("card of a deleted project: %+v %v", got, err)
	}
}
