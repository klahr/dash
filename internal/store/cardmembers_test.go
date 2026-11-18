package store

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// Migration 0031 makes each card's assignee its member.
func TestMigrateCardMembers(t *testing.T) {
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
	assigned, err := o.CreateCard(model.Card{Title: "Assigned", Column: model.CardIdeas})
	if err != nil {
		t.Fatal(err)
	}
	nobody, err := o.CreateCard(model.Card{Title: "Nobody's", Column: model.CardIdeas})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Back to before 0031: the card has an assignee, and no members.
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := migrations()
	if ms[30].name != "0031_card_members.sql" {
		t.Fatalf("migration 31 is %s", ms[30].name)
	}
	for _, q := range []string{
		"DROP TABLE planning_card_members",
		"UPDATE planning_cards SET assignee = " + itoa(u.ID) + " WHERE id = '" + assigned.ID + "'",
		"PRAGMA user_version = 30",
	} {
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
	if c, err := o.GetCard(assigned.ID); err != nil || !slices.Equal(c.Members, []int64{u.ID}) {
		t.Errorf("the assignee's card: %v %v", c.Members, err)
	}
	if c, err := o.GetCard(nobody.ID); err != nil || len(c.Members) != 0 {
		t.Errorf("a card without an assignee: %v %v", c.Members, err)
	}
}
