package store

import (
	"path/filepath"
	"testing"
)

// Migration 0009 writes the default tag format into existing projects and
// keeps one a project already set.
func TestMigrateTagFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dash.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := migrations()
	for _, m := range ms[:8] {
		if _, err := db.Exec(m.sql); err != nil {
			t.Fatal(m.name, err)
		}
	}
	for _, q := range []string{
		`INSERT INTO projects (id, data) VALUES ('p1', '{"id":"p1","name":"app"}')`,
		`INSERT INTO projects (id, data) VALUES ('p2', '{"id":"p2","name":"dash","tag_format":"vX.Y.Z"}')`,
		`PRAGMA user_version = 8`,
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
	for id, want := range map[string]string{"p1": "X.Y.Z", "p2": "vX.Y.Z"} {
		if p, err := s.GetProject(id); err != nil || p.TagFormat != want {
			t.Errorf("%s: tag format %q (%v), want %q", id, p.TagFormat, err, want)
		}
	}
}
