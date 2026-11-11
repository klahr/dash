package store

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// Migration 0018 moves the projects on the removed Favro backend back to
// Dash's own board: their settings, credentials, sync state and card links
// go, their tasks stay, and the move is in their history. Other backends'
// projects are left alone.
func TestMigrateRemoveFavro(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	gone, err := s.CreateProject(model.Project{Name: "on favro"})
	if err != nil {
		t.Fatal(err)
	}
	kept, err := s.CreateProject(model.Project{Name: "on another tracker"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		p       model.Project
		backend string
	}{{gone, "favro"}, {kept, "other"}} {
		if err := s.PutTrackerConfig(model.TrackerConfig{ProjectID: c.p.ID, Backend: c.backend, Config: json.RawMessage(`{"board":"b"}`),
			StatusMap: map[model.TodoStatus]string{model.StatusQueued: "Ready"}, Credentials: map[string]string{"token": "tok"}, WebhookSecret: "sec"}); err != nil {
			t.Fatal(err)
		}
	}
	todos := map[string]string{}
	for _, c := range []struct {
		p        model.Project
		provider string
	}{{gone, "favro"}, {kept, "other"}} {
		td, err := s.CreateTodo(model.Todo{ProjectID: c.p.ID, Title: "linked", Prompt: "x"})
		if err != nil {
			t.Fatal(err)
		}
		ref := model.ExternalRef{Provider: c.provider, ID: "card-1", URL: "https://tracker.test/card-1"}
		if err := s.PutTrackerLink(model.TrackerLink{TodoID: td.ID, ProjectID: c.p.ID, Ref: ref}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetExternalRef(td.ID, &ref); err != nil {
			t.Fatal(err)
		}
		todos[c.p.ID] = td.ID
	}
	s.Close()

	// Back to before 0018, which then runs again as Dash starts.
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := migrations()
	if ms[17].name != "0018_remove_favro.sql" {
		t.Fatalf("migration 18 is %s", ms[17].name)
	}
	if _, err := db.Exec("PRAGMA user_version = 17"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if s, err = Open(path, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	cfg, err := s.TrackerConfig(gone.ID)
	if err != nil || !cfg.Native() || cfg.HasCredentials || cfg.HasWebhookSecret || len(cfg.Config) > 0 || len(cfg.StatusMap) > 0 {
		t.Fatalf("the Favro project's board: %+v (%v)", cfg, err)
	}
	if links, err := s.TrackerLinks(gone.ID); err != nil || len(links) != 0 {
		t.Fatalf("the Favro project's links: %+v (%v)", links, err)
	}
	if td, err := s.GetTodo(todos[gone.ID]); err != nil || td.ExternalRef != nil || td.Title != "linked" {
		t.Fatalf("the Favro project's task: %+v (%v)", td, err)
	}
	evs, err := s.ProjectEvents(gone.ID, 10)
	if err != nil || len(evs) != 1 || evs[0].Kind != "board.switched" {
		t.Fatalf("the Favro project's history: %+v (%v)", evs, err)
	}

	cfg, err = s.TrackerConfig(kept.ID)
	if err != nil || cfg.Backend != "other" || !cfg.HasCredentials || cfg.StatusMap[model.StatusQueued] != "Ready" {
		t.Fatalf("the other project's board: %+v (%v)", cfg, err)
	}
	if links, err := s.TrackerLinks(kept.ID); err != nil || len(links) != 1 {
		t.Fatalf("the other project's links: %+v (%v)", links, err)
	}
	if td, err := s.GetTodo(todos[kept.ID]); err != nil || td.ExternalRef == nil {
		t.Fatalf("the other project's task: %+v (%v)", td, err)
	}
}
