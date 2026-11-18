package store

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// A card's tags are stored in the order given, each once whatever its
// case; a change replaces them, and they go with the card.
func TestCardTags(t *testing.T) {
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
	o := s.ForOrg(def.ID).AsUser(admin.ID)
	tags := func(id string) []string {
		t.Helper()
		c, err := o.GetCard(id)
		if err != nil {
			t.Fatal(err)
		}
		return c.Tags
	}

	c := model.Card{Title: "A", Tags: []string{" backend ", "Q3", "Backend", ""}}
	if err := model.CleanCard(&c); err != nil {
		t.Fatal(err)
	}
	c, err = o.CreateCard(c)
	if err != nil {
		t.Fatal(err)
	}
	if got := tags(c.ID); !slices.Equal(got, []string{"backend", "Q3"}) {
		t.Fatalf("tags %q, want [backend Q3]", got)
	}
	plain, err := o.CreateCard(model.Card{Title: "B", Column: model.CardIdeas})
	if err != nil {
		t.Fatal(err)
	}
	if got := tags(plain.ID); got == nil || len(got) != 0 {
		t.Errorf("a card without tags has %#v, want []", got)
	}
	if _, err := o.UpdateCard(c.ID, func(c *model.Card) error { c.Tags = []string{"Q3", "ux"}; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := tags(c.ID); !slices.Equal(got, []string{"Q3", "ux"}) {
		t.Errorf("tags %q after a change, want [Q3 ux]", got)
	}
	if _, err := o.UpdateCard(c.ID, func(c *model.Card) error { c.Priority = model.CardLow; return nil }); err != nil {
		t.Fatal(err)
	}
	cards, err := o.ListCards("")
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range cards {
		if want := map[string][]string{c.ID: {"Q3", "ux"}, plain.ID: {}}[x.ID]; !slices.Equal(x.Tags, want) {
			t.Errorf("listed %s with tags %q, want %q", x.Title, x.Tags, want)
		}
	}
	if err := o.DeleteCard(c.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM planning_card_tags").Scan(&n); err != nil || n != 0 {
		t.Errorf("%d tags left of a deleted card (%v)", n, err)
	}
}
