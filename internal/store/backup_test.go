package store

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

// A backup of a database Dash has open, mid-use, opens as a store with the
// data in it, and the live store carries on writing.
func TestBackupLiveDatabase(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "dash.db")
	s, err := Open(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.CreateProject(model.Project{Name: "app", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	td, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: "Fix login", Prompt: "fix it"})
	if err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(dir, "backups", "copy.db")
	if err := Backup(context.Background(), src, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest + ".partial"); !os.IsNotExist(err) {
		t.Errorf("temporary file left behind: %v", err)
	}
	if info, err := os.Stat(dest); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("backup mode: %v, %v", info, err)
	}
	if err := Backup(context.Background(), src, dest); err == nil {
		t.Error("a second backup overwrote the first")
	}

	// The live store still writes after the backup.
	if _, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: "later", Prompt: "x"}); err != nil {
		t.Fatal(err)
	}

	b, err := Open(dest, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	got, err := b.GetTodo(td.ID)
	if err != nil {
		t.Fatalf("todo missing from backup: %v", err)
	}
	if got.Title != "Fix login" || got.ProjectID != p.ID {
		t.Errorf("backup todo = %+v", got)
	}
	if n := len(b.ListTodos(p.ID)); n != 1 {
		t.Errorf("backup has %d todos, want 1 (the snapshot, not later writes)", n)
	}
}

func TestBackupMissingDatabase(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := Backup(context.Background(), filepath.Join(dir, "nope.db"), filepath.Join(dir, "b.db")); err == nil {
		t.Fatal("backed up a database that doesn't exist")
	}
	if _, err := os.Stat(filepath.Join(dir, "nope.db")); !os.IsNotExist(err) {
		t.Error("backup created the missing database")
	}
}

func TestBackupToPrunes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "dash.db")
	s, err := Open(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	out := filepath.Join(dir, "backups")
	start := time.Date(2026, 1, 31, 3, 0, 0, 0, time.UTC)
	var paths []string
	for i := range 5 {
		p, err := BackupTo(context.Background(), src, out, 3, start.Add(time.Duration(i)*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	if filepath.Base(paths[0]) != "dash-20260131T030000Z.db" {
		t.Errorf("name = %s", filepath.Base(paths[0]))
	}
	if last, ok := newestBackup(out); !ok || !last.Equal(start.Add(4*24*time.Hour)) {
		t.Errorf("newest backup = %v, %v", last, ok)
	}
	entries, _ := os.ReadDir(out)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{filepath.Base(paths[2]), filepath.Base(paths[3]), filepath.Base(paths[4])}
	if !slices.Equal(names, want) {
		t.Errorf("kept %v, want %v", names, want)
	}
}

func TestPruneBackupsLeavesOtherFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, n := range []string{
		"dash-20260101T000000Z.db", "dash-20260102T000000Z.db", "dash-20260103T000000Z.db",
		"dash.db", "notes.txt", "dash-20260104T000000Z.db.partial", "dash-manual.db",
	} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := PruneBackups(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "dash-20260101T000000Z.db"), filepath.Join(dir, "dash-20260102T000000Z.db")}
	if !slices.Equal(removed, want) {
		t.Errorf("removed %v, want %v", removed, want)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 5 {
		t.Errorf("%d files left, want 5", len(entries))
	}
	if removed, _ := PruneBackups(dir, 0); len(removed) != 1 {
		t.Errorf("keep 0 removed %v", removed)
	}
}
