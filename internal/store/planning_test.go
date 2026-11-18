package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
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

// A card's members are members of its org: someone else is refused, one
// who leaves doesn't stop other changes, one whose account is deleted is
// no longer on it, and new members are added after the others.
func TestCardMembers(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), &recordedEvents{})
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
	sam := addMember(t, acc, admin.ID, def.ID, "sam", RoleMember)
	kim := addMember(t, acc, admin.ID, def.ID, "kim", RoleMember)
	other, err := acc.CreateOrg(admin.ID, "Other")
	if err != nil {
		t.Fatal(err)
	}
	stranger := addMember(t, acc, admin.ID, other.ID, "lee", RoleMember)
	o := s.ForOrg(def.ID).AsUser(admin.ID)
	members := func(id string) []int64 {
		t.Helper()
		c, err := o.GetCard(id)
		if err != nil {
			t.Fatal(err)
		}
		return c.Members
	}

	c, err := o.CreateCard(model.Card{Title: "A", Column: model.CardIdeas, Members: []int64{sam}})
	if err != nil {
		t.Fatal(err)
	}
	if got := members(c.ID); !slices.Equal(got, []int64{sam}) {
		t.Fatalf("members %v, want [%d]", got, sam)
	}
	if _, err := o.CreateCard(model.Card{Title: "B", Column: model.CardIdeas, Members: []int64{stranger}}); !errors.Is(err, ErrBadMember) {
		t.Errorf("another org's member on a card: %v", err)
	}
	if _, err := o.UpdateCard(c.ID, func(c *model.Card) error { c.Members = append(c.Members, stranger); return nil }); !errors.Is(err, ErrBadMember) {
		t.Errorf("adding another org's member: %v", err)
	}
	if _, err := o.UpdateCard(c.ID, func(c *model.Card) error { c.Members = []int64{kim, sam}; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := members(c.ID); !slices.Equal(got, []int64{sam, kim}) {
		t.Errorf("members %v: sam was on it first, kim added after", got)
	}
	if cards, err := o.ListCards(""); err != nil || len(cards) != 1 || !slices.Equal(cards[0].Members, []int64{sam, kim}) {
		t.Errorf("listed: %+v %v", cards, err)
	}
	if err := acc.RemoveMember(admin.ID, def.ID, kim); err != nil {
		t.Fatal(err)
	}
	if _, err := o.UpdateCard(c.ID, func(c *model.Card) error { c.Priority = model.CardHigh; return nil }); err != nil {
		t.Errorf("a change to a card a member of has left: %v", err)
	}
	if _, err := s.db.Exec("DELETE FROM users WHERE id = ?", sam); err != nil {
		t.Fatal(err)
	}
	if got := members(c.ID); !slices.Equal(got, []int64{kim}) {
		t.Errorf("members %v after sam's account went, want [%d]", got, kim)
	}
	if _, err := o.UpdateCard(c.ID, func(c *model.Card) error { c.Members = nil; return nil }); err != nil {
		t.Errorf("taking everyone off: %v", err)
	}
	if got := members(c.ID); len(got) != 0 {
		t.Errorf("members %v, want none", got)
	}
}

// A card depends on other cards of its org: not on itself, another org's
// card or one that already depends on it (by way of others too). Deleting
// a card it depends on drops the dependency.
func TestCardDeps(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), &recordedEvents{})
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
	add := func(o *OrgStore, title string, deps ...string) (model.Card, error) {
		return o.CreateCard(model.Card{Title: title, Column: model.CardIdeas, DependsOn: deps})
	}
	setDeps := func(id string, deps ...string) error {
		_, err := o.UpdateCard(id, func(c *model.Card) error { c.DependsOn = deps; return nil })
		return err
	}
	deps := func(id string) []string {
		t.Helper()
		c, err := o.GetCard(id)
		if err != nil {
			t.Fatal(err)
		}
		return c.DependsOn
	}

	a, _ := add(o, "A")
	b, err := add(o, "B", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	c, err := add(o, "C", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := deps(b.ID); !slices.Equal(got, []string{a.ID}) {
		t.Fatalf("B depends on %v, want A", got)
	}
	if got := deps(a.ID); got == nil || len(got) != 0 {
		t.Errorf("A depends on %#v, want none", got)
	}
	theirs, err := add(s.ForOrg(other.ID).AsUser(admin.ID), "Theirs")
	if err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"itself":         setDeps(a.ID, a.ID),
		"a cycle":        setDeps(a.ID, b.ID),
		"a longer cycle": setDeps(a.ID, c.ID),
		"another org's":  setDeps(a.ID, theirs.ID),
		"no such card":   setDeps(a.ID, "nope"),
	} {
		if !errors.Is(err, ErrBadDependency) {
			t.Errorf("depending on %s: %v", name, err)
		}
	}
	if _, err := add(o, "D", theirs.ID); !errors.Is(err, ErrBadDependency) {
		t.Errorf("a new card on another org's: %v", err)
	}
	if err := setDeps(c.ID, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if cards, err := o.ListCards(""); err != nil || len(cards) != 3 || !slices.Equal(cards[2].DependsOn, []string{b.ID, a.ID}) {
		t.Errorf("listed: %+v %v", cards, err)
	}
	if err := o.DeleteCard(a.ID); err != nil {
		t.Fatal(err)
	}
	if got := deps(c.ID); !slices.Equal(got, []string{b.ID}) {
		t.Errorf("C depends on %v after A was deleted, want B", got)
	}
	if got := deps(b.ID); len(got) != 0 {
		t.Errorf("B depends on %v after A was deleted", got)
	}
	if err := setDeps(c.ID); err != nil || len(deps(c.ID)) != 0 {
		t.Errorf("no dependencies: %v %v", err, deps(c.ID))
	}
}

// A card's files keep their order, are its org's only, stop at
// model.MaxCardFiles, and go with the card; cleanup sees the uploads cards
// still have.
func TestCardFiles(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), &recordedEvents{})
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
	c := model.Card{Title: "Logo"}
	if err := model.CleanCard(&c); err != nil {
		t.Fatal(err)
	}
	if c, err = o.CreateCard(c); err != nil {
		t.Fatal(err)
	}
	file := func(id string) model.CardFile {
		return model.CardFile{ID: id, Name: id + ".png", Type: "image/png", Size: 10}
	}
	for _, id := range []string{"aaaaaaaaaaaa", "bbbbbbbbbbbb", "aaaaaaaaaaaa"} {
		if _, err := o.AddCardFile(c.ID, file(id)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := o.GetCard(c.ID)
	if err != nil || len(got.Files) != 2 || got.Files[0].ID != "aaaaaaaaaaaa" || got.Files[1].ID != "bbbbbbbbbbbb" || got.Files[0].AddedBy != admin.ID {
		t.Fatalf("files: %+v %v", got.Files, err)
	}
	if _, err := s.ForOrg(other.ID).AsUser(admin.ID).AddCardFile(c.ID, file("cccccccccccc")); !errors.Is(err, ErrNotFound) {
		t.Errorf("another org's card: %v", err)
	}
	if err := o.RemoveCardFile(c.ID, "aaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if err := o.RemoveCardFile(c.ID, "aaaaaaaaaaaa"); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove again: %v", err)
	}
	for i := 1; i < model.MaxCardFiles; i++ {
		if _, err := o.AddCardFile(c.ID, file(fmt.Sprintf("%012d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.AddCardFile(c.ID, file("dddddddddddd")); !errors.Is(err, ErrTooManyFiles) {
		t.Errorf("one too many: %v", err)
	}
	if ids, err := s.CardUploads(); err != nil || len(ids) != model.MaxCardFiles || !slices.Contains(ids, "bbbbbbbbbbbb") {
		t.Errorf("uploads in use: %d %v", len(ids), err)
	}
	if err := o.DeleteCard(c.ID); err != nil {
		t.Fatal(err)
	}
	if ids, err := s.CardUploads(); err != nil || len(ids) != 0 {
		t.Errorf("after the card is deleted: %v %v", ids, err)
	}
}
