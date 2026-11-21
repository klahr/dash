package store

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// An org's cards are on its boards: the default one, made when first asked
// for, and the boards added after it. Each board orders its columns on its
// own; a card moves between boards; boards are renamed and reordered; and
// deleting one moves its cards to the default board, which can't be
// deleted. Another org sees none of them.
func TestPlanningBoards(t *testing.T) {
	t.Parallel()

	evs := &recordedEvents{}
	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), evs)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
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

	bs, err := o.PlanningBoards()
	if err != nil || len(bs) != 1 || !bs[0].Default || bs[0].Name != DefaultBoardName {
		t.Fatalf("an org's first boards: %+v %v", bs, err)
	}
	main := bs[0]
	add := func(title, board string) model.Card {
		t.Helper()
		c := model.Card{Title: title, BoardID: board}
		if err := model.CleanCard(&c); err != nil {
			t.Fatal(err)
		}
		c, err := o.CreateCard(c)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	a := add("A", "")
	if a.BoardID != main.ID || a.Position != 0 {
		t.Fatalf("a card given no board: %+v", a)
	}

	web, err := o.CreatePlanningBoard("  Web   app ")
	if err != nil || web.Name != "Web app" || web.Position != 1 || web.Default {
		t.Fatalf("new board: %+v %v", web, err)
	}
	if _, err := o.CreatePlanningBoard("web APP"); !errors.Is(err, ErrBadPlanningBoard) {
		t.Fatalf("a board's name twice: %v", err)
	}
	if _, err := o.CreatePlanningBoard(" "); !errors.Is(err, ErrBadPlanningBoard) {
		t.Fatalf("a board without a name: %v", err)
	}
	if b, err := o.PlanningBoard("WEB APP"); err != nil || b.ID != web.ID {
		t.Fatalf("a board by its name: %+v %v", b, err)
	}
	if b, err := o.PlanningBoard(""); err != nil || b.ID != main.ID {
		t.Fatalf("the default board: %+v %v", b, err)
	}
	if _, err := s.ForOrg(other.ID).PlanningBoard(web.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org's board: %v", err)
	}
	if _, err := s.ForOrg(other.ID).AsUser(admin.ID).CreateCard(model.Card{Title: "X", Column: model.CardIdeas, BoardID: web.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a card on another org's board: %v", err)
	}
	if !slices.ContainsFunc(evs.evs, func(e model.Event) bool { return e.Type == model.EvPlanningBoards && e.OrgID == def.ID }) {
		t.Error("adding a board isn't announced")
	}

	// Each board numbers its columns on its own.
	w1, w2 := add("W1", web.ID), add("W2", web.ID)
	if w1.Position != 0 || w2.Position != 1 {
		t.Fatalf("positions on the new board: %d %d", w1.Position, w2.Position)
	}
	b := add("B", "")
	if b.Position != 1 {
		t.Fatalf("position on the default board: %d", b.Position)
	}
	titles := func(board string) []string {
		t.Helper()
		cs, err := o.BoardCards(board, "")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range cs {
			out = append(out, c.Title)
		}
		return out
	}
	if got := titles(web.ID); !slices.Equal(got, []string{"W1", "W2"}) {
		t.Fatalf("the new board's cards: %v", got)
	}
	if got := titles(main.ID); !slices.Equal(got, []string{"A", "B"}) {
		t.Fatalf("the default board's cards: %v", got)
	}
	if all, _ := o.ListCards(""); len(all) != 4 {
		t.Fatalf("every board's cards: %d", len(all))
	}
	// A move within a board leaves the other's alone.
	if _, err := o.MoveCard(w2.ID, model.CardIdeas, 0); err != nil {
		t.Fatal(err)
	}
	if got := titles(web.ID); !slices.Equal(got, []string{"W2", "W1"}) {
		t.Fatalf("after a move: %v", got)
	}
	if c, _ := o.GetCard(b.ID); c.Position != 1 {
		t.Fatalf("a move on another board moved B: %d", c.Position)
	}

	// A card moves to another board, to the end of its column there, and
	// may depend on a card of another board.
	moved, err := o.UpdateCard(a.ID, func(c *model.Card) error { c.BoardID = web.ID; c.DependsOn = []string{w1.ID}; return nil })
	if err != nil || moved.BoardID != web.ID || moved.Position != 2 {
		t.Fatalf("moved to another board: %+v %v", moved, err)
	}
	if got := titles(web.ID); !slices.Equal(got, []string{"W2", "W1", "A"}) {
		t.Fatalf("the new board after the move: %v", got)
	}
	if c, _ := o.GetCard(b.ID); c.Position != 0 {
		t.Fatalf("the gap A left: B at %d", c.Position)
	}
	if _, err := o.UpdateCard(a.ID, func(c *model.Card) error { c.BoardID = "nope"; return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("to a board that isn't: %v", err)
	}

	// Rename and reorder.
	if r, err := o.RenamePlanningBoard(main.ID, "Platform"); err != nil || r.Name != "Platform" || !r.Default {
		t.Fatalf("rename: %+v %v", r, err)
	}
	if _, err := o.RenamePlanningBoard(main.ID, "web app"); !errors.Is(err, ErrBadPlanningBoard) {
		t.Fatalf("rename to another's name: %v", err)
	}
	ops, err := o.CreatePlanningBoard("Ops")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.MovePlanningBoard(ops.ID, 0); err != nil {
		t.Fatal(err)
	}
	names := func() []string {
		bs, _ := o.PlanningBoards()
		var out []string
		for _, b := range bs {
			out = append(out, b.Name)
		}
		return out
	}
	if got := names(); !slices.Equal(got, []string{"Ops", "Platform", "Web app"}) {
		t.Fatalf("reordered: %v", got)
	}

	// Deleting a board moves its cards to the end of the default board's
	// columns; the default board stays.
	if err := o.DeletePlanningBoard(main.ID); !errors.Is(err, ErrBadPlanningBoard) {
		t.Fatalf("deleting the default board: %v", err)
	}
	if err := o.DeletePlanningBoard(web.ID); err != nil {
		t.Fatal(err)
	}
	if got := titles(main.ID); !slices.Equal(got, []string{"B", "W2", "W1", "A"}) {
		t.Fatalf("the default board after deleting the other: %v", got)
	}
	cs, _ := o.BoardCards(main.ID, "")
	for i, c := range cs {
		if c.Position != i || c.BoardID != main.ID {
			t.Fatalf("card %s: board %s position %d, want %d", c.Title, c.BoardID, c.Position, i)
		}
	}
	if got := names(); !slices.Equal(got, []string{"Ops", "Platform"}) {
		t.Fatalf("boards after the delete: %v", got)
	}
	if counts, err := o.PlanningBoardCounts(); err != nil || counts[main.ID] != 4 || counts[ops.ID] != 0 {
		t.Fatalf("counts: %v %v", counts, err)
	}
}

// Migration 0040 puts each org's cards of before on its new default board.
func TestMigratePlanningBoards(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	acc := s.Accounts()
	if err := acc.Bootstrap("admin", "s3cret-password"); err != nil {
		t.Fatal(err)
	}
	u, _ := acc.EnvUser()
	def, _ := acc.DefaultOrg()
	o := s.ForOrg(def.ID).AsUser(u.ID)
	var ids []string
	for _, title := range []string{"One", "Two"} {
		c, err := o.CreateCard(model.Card{Title: title, Column: model.CardIdeas})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID)
	}
	s.Close()

	// Back to before 0040: no boards.
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := migrations()
	if ms[39].name != "0040_planning_boards.sql" {
		t.Fatalf("migration 40 is %s", ms[39].name)
	}
	for _, q := range []string{"DROP TABLE planning_card_boards", "DROP TABLE planning_boards", "PRAGMA user_version = 39"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	db.Close()

	if s, err = Open(path, nil); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	o = s.ForOrg(def.ID).AsUser(u.ID)
	bs, err := o.PlanningBoards()
	if err != nil || len(bs) != 1 || !bs[0].Default || bs[0].Name != DefaultBoardName {
		t.Fatalf("boards after migrating: %+v %v", bs, err)
	}
	cs, err := o.BoardCards(bs[0].ID, "")
	if err != nil || len(cs) != 2 {
		t.Fatalf("the default board's cards after migrating: %d %v", len(cs), err)
	}
	for _, id := range ids {
		if c, err := o.GetCard(id); err != nil || c.BoardID != bs[0].ID {
			t.Errorf("card %s: board %q %v", id, c.BoardID, err)
		}
	}
}
