package store

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

// ListTodos (the queue order) runs prioritised tasks first, by priority then
// age, and unprioritised ones after all of them, oldest first; 0 is a real
// priority.
func TestListTodosQueueOrder(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p, err := s.CreateProject(model.Project{Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, c := range []struct {
		title string
		prio  *int
	}{
		// In creation order, oldest first.
		{"unset-old", nil},
		{"p2-old", new(2)},
		{"p1", new(1)},
		{"unset-new", nil},
		{"p0", new(0)},
		{"p2-new", new(2)},
	} {
		td, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: c.title, Prompt: "x", Priority: c.prio})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdateTodo(td.ID, func(td *model.Todo) error {
			td.CreatedAt = base.Add(time.Duration(i) * time.Hour)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, td := range s.ListTodos(p.ID) {
		got = append(got, td.Title)
	}
	want := []string{"p0", "p1", "p2-old", "p2-new", "unset-old", "unset-new"}
	if !slices.Equal(got, want) {
		t.Errorf("queue order %v, want %v", got, want)
	}
}

// Migration 0013 turns priority 0 (which meant unset) into no priority and
// keeps every other value.
func TestMigrateOptionalPriority(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := migrations()
	for _, m := range ms[:12] {
		if _, err := db.Exec(m.sql); err != nil {
			t.Fatal(m.name, err)
		}
	}
	todo := func(id, prio string) string {
		return `INSERT INTO todos (id, project_id, org_id, data) VALUES ('` + id + `', 'p1', 0, '{"id":"` + id +
			`","project_id":"p1","title":"` + id + `","status":"queued"` + prio + `}')`
	}
	for _, q := range []string{
		`INSERT INTO projects (id, data) VALUES ('p1', '{"id":"p1","name":"app"}')`,
		todo("zero", `,"priority":0`),
		todo("one", `,"priority":1`),
		todo("neg", `,"priority":-2`),
		todo("absent", ``),
		`PRAGMA user_version = 12`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	db.Close()

	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for id, want := range map[string]*int{"zero": nil, "one": new(1), "neg": new(-2), "absent": nil} {
		td, err := s.GetTodo(id)
		if err != nil {
			t.Fatal(id, err)
		}
		if (td.Priority == nil) != (want == nil) || (want != nil && *td.Priority != *want) {
			t.Errorf("%s: priority %v, want %v", id, td.Priority, want)
		}
	}
}
