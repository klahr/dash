package store

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// oldDatabase writes a database at path as the Dash before the latest
// migration left it, with the given todos (of one project) in it.
func oldDatabase(t *testing.T, path string, todos ...model.Todo) int {
	t.Helper()
	ms, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	old := len(ms) - 1
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, m := range ms[:old] {
		if _, err := db.Exec(m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec("PRAGMA user_version = " + strconv.Itoa(old))
	p, _ := json.Marshal(model.Project{ID: "p1", Name: "web", DefaultBranch: "main", RemoteURL: "https://github.com/x/web"})
	exec("INSERT INTO projects (id, data) VALUES ('p1', ?)", string(p))
	for _, td := range todos {
		td.ProjectID = "p1"
		b, _ := json.Marshal(td)
		exec("INSERT INTO todos (id, project_id, data) VALUES (?, 'p1', ?)", td.ID, string(b))
	}
	return old
}

func userVersion(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// An upgrade that migrates the database keeps the work that was going on:
// tasks running on runners stay running (the runners report on them when
// they reconnect), a task running on this computer is queued again to
// continue, and an approved one stays merging for the merge queue to pick
// up. The database as it was is copied beside it first, so going back to
// the older version is restoring that copy.
func TestUpgradeMigrationKeepsRunningWorkAndBacksUpFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dash.db")
	old := oldDatabase(t, path,
		model.Todo{ID: "remote", Title: "on a runner", Status: model.StatusInProgress, Runner: "laptop", RemoteID: "r1"},
		model.Todo{ID: "local", Title: "here", Status: model.StatusInProgress},
		model.Todo{ID: "merging", Title: "approved", Status: model.StatusMerging, Runner: "laptop", RemoteID: "r2"},
		model.Todo{ID: "review", Title: "done", Status: model.StatusReview},
	)

	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ms, _ := migrations()
	if v := userVersion(t, path); v != len(ms) {
		t.Fatalf("schema %d after the upgrade, want %d", v, len(ms))
	}
	want := map[string]model.TodoStatus{"remote": model.StatusInProgress, "local": model.StatusQueued,
		"merging": model.StatusMerging, "review": model.StatusReview}
	for id, st := range want {
		td, err := s.GetTodo(id)
		if err != nil || td.Status != st {
			t.Errorf("%s: %s (%v), want %s", id, td.Status, err, st)
		}
	}
	if td, _ := s.GetTodo("remote"); td.Runner != "laptop" || td.RemoteID != "r1" {
		t.Errorf("the task on a runner lost its runner: %+v", td)
	}

	// The copy taken before migrating is the old database, as it was.
	backups, _ := filepath.Glob(path + ".pre-migration-*")
	if len(backups) != 1 || !strings.Contains(backups[0], ".pre-migration-"+strconv.Itoa(old)+"-to-"+strconv.Itoa(len(ms))+"-") {
		t.Fatalf("backups before migrating: %q", backups)
	}
	if v := userVersion(t, backups[0]); v != old {
		t.Errorf("the backup has schema %d, want the old %d", v, old)
	}
	if fi, err := os.Stat(backups[0]); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("backup mode: %v %v", fi, err)
	}

	// Opening it again migrates nothing and copies nothing.
	s = reopen(t, s, path)
	if again, _ := filepath.Glob(path + ".pre-migration-*"); len(again) != 1 {
		t.Errorf("backups after a start without migrations: %q", again)
	}

	// Rolling back: the older Dash refuses the migrated database (and
	// leaves it alone) rather than misreading it; the backup opens.
	s.Close()
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = " + strconv.Itoa(len(ms)+1)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(path, nil); err == nil || !strings.Contains(err.Error(), "newer than this Dash knows") {
		t.Fatalf("a database from a newer Dash: %v", err)
	}
	if v := userVersion(t, path); v != len(ms)+1 {
		t.Errorf("the refused database was changed: schema %d", v)
	}
	restored := filepath.Join(dir, "restored", "dash.db")
	if err := os.MkdirAll(filepath.Dir(restored), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(backups[0])
	if err := os.WriteFile(restored, b, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Open(restored, nil)
	if err != nil {
		t.Fatalf("open the backup: %v", err)
	}
	defer r.Close()
	if td, err := r.GetTodo("remote"); err != nil || td.Status != model.StatusInProgress {
		t.Errorf("the backup's running task: %s %v", td.Status, err)
	}
}

// A new database has nothing to back up.
func TestNewDatabaseIsNotBackedUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if got, _ := filepath.Glob(path + ".pre-migration-*"); len(got) != 0 {
		t.Errorf("a new database was backed up: %q", got)
	}
}
