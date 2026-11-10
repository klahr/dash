package favro

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tracker"
	"github.com/klahr/dash/internal/tracker/favro/favrotest"
)

func ptr[T any](v T) *T { return &v }

func byRef(items []tracker.Item, id string) (tracker.Item, bool) {
	for _, it := range items {
		if it.Ref.ID == id {
			return it, true
		}
	}
	return tracker.Item{}, false
}

func TestReadMapping(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	prio := w.fv.AddNumberField(w.org, "Priority")
	w.fv.AddUser(w.org, "u-grace", "Grace")
	first := w.fv.AddCard(w.org, favrotest.Card{WidgetCommonID: w.widget, ColumnID: w.cols[0], Name: "Base"})
	id := w.fv.AddCard(w.org, favrotest.Card{
		WidgetCommonID: w.widget, ColumnID: w.cols[1], Name: "Add login",
		DetailedDescription: "Use **OAuth**.\n\n- Google\n",
		CustomFields:        []favrotest.Field{{CustomFieldID: prio, Total: ptr(2.0)}},
		Dependencies:        []favrotest.Dependency{{CardID: first}},
	})
	w.fv.AddComment(w.org, id, "u-grace", "Mind the redirect URL")
	b := w.backend(t, map[string]any{"priority_field": prio})
	items, err := b.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	it, ok := byRef(items, id)
	if !ok {
		t.Fatalf("card missing: %+v", items)
	}
	if it.Title != "Add login" || it.Description != "Use **OAuth**.\n\n- Google" || it.Column != "To do" ||
		it.Priority == nil || *it.Priority != 2 || !slices.Equal(it.DependsOn, []string{first}) || it.Ref.Provider != Name {
		t.Fatalf("read: %+v", it)
	}
	if b.pc.StatusFor(it.Column) != model.StatusQueued {
		t.Fatalf("column %q isn't queued", it.Column)
	}
	if len(it.Comments) != 1 || it.Comments[0].Author != "Grace" || it.Comments[0].Body != "Mind the redirect URL" || it.Comments[0].ID == "" {
		t.Fatalf("comments: %+v", it.Comments)
	}
	if base, _ := byRef(items, first); base.Priority != nil || len(base.DependsOn) != 0 {
		t.Fatalf("the dependency's card: %+v", base)
	}
	if b.Skips() != nil {
		t.Fatal("priority skipped with a priority field")
	}

	// After the cursor (as the engine keeps it): nothing unchanged, then
	// the changed card, dated at the read before.
	cursor := items[0].UpdatedAt.Add(-time.Second)
	if again, err := b.List(context.Background(), items[0].UpdatedAt); err != nil || len(again) != 0 {
		t.Fatalf("nothing changed: %+v %v", again, err)
	}
	w.fv.Edit(w.org, id, func(c *favrotest.Card) { c.Name = "Add login (v2)" })
	again, err := b.List(context.Background(), cursor)
	if err != nil {
		t.Fatal(err)
	}
	if ch, ok := byRef(again, id); !ok || ch.Title != "Add login (v2)" || ch.UpdatedAt.IsZero() {
		t.Fatalf("changes since: %+v", again)
	}

	// An archived card reads as deleted, once.
	w.fv.Edit(w.org, first, func(c *favrotest.Card) { c.Archived = true })
	items, _ = b.List(context.Background(), cursor)
	if gone, ok := byRef(items, first); !ok || !gone.Deleted {
		t.Fatalf("archived: %+v", items)
	}
	items, _ = b.List(context.Background(), cursor)
	if _, ok := byRef(items, first); ok {
		t.Fatal("deleted twice")
	}
}

func TestTagFilter(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	claude := w.fv.AddTag(w.org, "Claude")
	mine := w.fv.AddCard(w.org, favrotest.Card{WidgetCommonID: w.widget, ColumnID: w.cols[1], Name: "for Claude", Tags: []string{claude}})
	other := w.fv.AddCard(w.org, favrotest.Card{WidgetCommonID: w.widget, ColumnID: w.cols[1], Name: "for people"})
	b := w.backend(t, map[string]any{"tag": "claude"})
	ctx := context.Background()
	items, err := b.List(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Ref.ID != mine {
		t.Fatalf("tagged: %+v", items)
	}
	// Dash's cards get the tag.
	ref, err := b.Create(ctx, tracker.Item{Title: "new", Column: "To do"})
	if err != nil {
		t.Fatal(err)
	}
	if c := w.fv.Card(w.org, ref.ID); !slices.Contains(c.Tags, claude) {
		t.Fatalf("created card's tags: %v", c.Tags)
	}
	// A card losing the tag isn't read as deleted.
	w.fv.Edit(w.org, mine, func(c *favrotest.Card) { c.Tags = nil })
	items, _ = b.List(ctx, time.Time{})
	if _, ok := byRef(items, mine); ok {
		t.Fatalf("untagged card read: %+v", items)
	}
	if _, ok := byRef(items, other); ok {
		t.Fatal("other card read")
	}
}

func TestWriteMapping(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	prio := w.fv.AddNumberField(w.org, "Priority")
	b := w.backend(t, map[string]any{"priority_field": prio})
	ctx := context.Background()
	dep, err := b.Create(ctx, tracker.Item{Title: "first", Column: "Backlog"})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := b.Create(ctx, tracker.Item{Title: "Add login", Description: "Use **OAuth**.", Priority: ptr(3), DependsOn: []string{dep.ID}, Column: "To do"})
	if err != nil {
		t.Fatal(err)
	}
	c := w.fv.Card(w.org, ref.ID)
	if c.Name != "Add login" || c.DetailedDescription != "Use **OAuth**." || c.ColumnID != w.cols[1] ||
		len(c.CustomFields) != 1 || *c.CustomFields[0].Total != 3 || len(c.Dependencies) != 1 || c.Dependencies[0].CardID != dep.ID {
		t.Fatalf("created: %+v", c)
	}
	var created bool
	for _, r := range w.fv.Requests() {
		if r.Method == "POST" && r.Path == "/api/v1/cards" && strings.Contains(r.Body, `"descriptionFormat":"markdown"`) {
			created = true
		}
	}
	if !created {
		t.Fatal("no create request")
	}

	// Update: new name and description, priority cleared, dependency dropped.
	if err := b.Update(ctx, ref, tracker.Item{Title: "Add SSO", Description: "Now SAML."}); err != nil {
		t.Fatal(err)
	}
	c = w.fv.Card(w.org, ref.ID)
	if c.Name != "Add SSO" || c.DetailedDescription != "Now SAML." || len(c.CustomFields) != 0 || len(c.Dependencies) != 0 {
		t.Fatalf("updated: %+v", c)
	}
	if err := b.Update(ctx, ref, tracker.Item{Title: "Add SSO", Description: "Now SAML.", Priority: ptr(1), DependsOn: []string{dep.ID}}); err != nil {
		t.Fatal(err)
	}
	if c = w.fv.Card(w.org, ref.ID); len(c.Dependencies) != 1 || *c.CustomFields[0].Total != 1 {
		t.Fatalf("dependency and priority back: %+v", c)
	}

	// Status → column, through the project's mapping; unmapped stays.
	if err := b.SetStatus(ctx, ref, model.StatusReview); err != nil {
		t.Fatal(err)
	}
	if c = w.fv.Card(w.org, ref.ID); c.ColumnID != w.cols[3] {
		t.Fatalf("moved to %s, want Review %s", c.ColumnID, w.cols[3])
	}
	if err := b.SetStatus(ctx, ref, model.StatusFailed); err != nil {
		t.Fatal(err)
	}
	if c = w.fv.Card(w.org, ref.ID); c.ColumnID != w.cols[3] {
		t.Fatal("an unmapped status moved the card")
	}

	// Comments go to the card's common id.
	if err := b.Comment(ctx, ref, "Ready for review in Dash.\n\nPull request: https://github.com/x/y/pull/1"); err != nil {
		t.Fatal(err)
	}
	if ks := w.fv.Comments(w.org, ref.ID); len(ks) != 1 || ks[0].Comment == "" {
		t.Fatalf("comments: %+v", ks)
	}

	// Delete archives.
	if err := b.Delete(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if c = w.fv.Card(w.org, ref.ID); !c.Archived {
		t.Fatal("not archived")
	}
}

func TestPriorityTags(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p2 := w.fv.AddTag(w.org, "P2")
	w.fv.AddTag(w.org, "frontend")
	id := w.fv.AddCard(w.org, favrotest.Card{WidgetCommonID: w.widget, ColumnID: w.cols[1], Name: "x", Tags: []string{p2}})
	b := w.backend(t, map[string]any{"priority_tag_prefix": "P"})
	ctx := context.Background()
	items, err := b.List(ctx, time.Time{})
	if err != nil || len(items) != 1 || items[0].Priority == nil || *items[0].Priority != 2 {
		t.Fatalf("read: %+v %v", items, err)
	}
	if err := b.Update(ctx, tracker.Ref{ID: id}, tracker.Item{Title: "x", Priority: ptr(1)}); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tg := range w.fv.Card(w.org, id).Tags {
		names = append(names, w.fv.TagName(w.org, tg))
	}
	if !slices.Equal(names, []string{"P1"}) {
		t.Fatalf("tags after setting priority 1: %v", names)
	}
}

func TestNoPriorityLeavesDashAlone(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	b := w.backend(t, nil)
	if !slices.Contains(b.Skips(), tracker.FieldPriority) {
		t.Fatal("priority isn't left to Dash")
	}
	ref, err := b.Create(context.Background(), tracker.Item{Title: "x", Priority: ptr(1), Column: "To do"})
	if err != nil {
		t.Fatal(err)
	}
	if c := w.fv.Card(w.org, ref.ID); len(c.CustomFields) != 0 || len(c.Tags) != 0 {
		t.Fatalf("priority written: %+v", c)
	}
}

func TestRegisterWebhook(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	b := w.backend(t, nil)
	ctx := context.Background()
	hook := "http://127.0.0.1:1/api/projects/p1/tracker/webhook" // nothing answers: the ping just fails
	for range 2 {
		if err := b.RegisterWebhook(ctx, hook, "s3cret"); err != nil {
			t.Fatal(err)
		}
	}
	w.fv.WaitWebhooks()
	hs := w.fv.Webhooks(w.org)
	if len(hs) != 1 || hs[0].PostToURL != hook || hs[0].Secret != "s3cret" || hs[0].WidgetCommonID != w.widget || hs[0].Options["notifications"] == nil {
		t.Fatalf("webhooks: %+v", hs)
	}
	// A new address replaces the project's old webhook (found by name).
	if err := b.RegisterWebhook(ctx, "http://127.0.0.1:1/new", "s3cret"); err != nil {
		t.Fatal(err)
	}
	w.fv.WaitWebhooks()
	if hs = w.fv.Webhooks(w.org); len(hs) != 1 || hs[0].PostToURL != "http://127.0.0.1:1/new" {
		t.Fatalf("after a new address: %+v", hs)
	}
	if err := b.UnregisterWebhook(ctx, "http://127.0.0.1:1/new"); err != nil {
		t.Fatal(err)
	}
	if hs = w.fv.Webhooks(w.org); len(hs) != 0 {
		t.Fatalf("after unregister: %+v", hs)
	}
}
