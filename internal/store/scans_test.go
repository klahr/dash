package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/klahr/dash/internal/model"
)

func TestScanTemplates(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.CreateProject(model.Project{Name: "p", Path: t.TempDir()})
	q, _ := s.CreateProject(model.Project{Name: "q", Path: t.TempDir()})

	if _, err := s.SaveScanTemplate(model.ScanTemplate{ProjectID: "nope", Name: "x", Prompt: "Go."}, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
	if _, err := s.SaveScanTemplate(model.ScanTemplate{ProjectID: p.ID, Name: "x", Prompt: "Go."}, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("edit of a missing scan: %v", err)
	}
	if _, err := s.SaveScanTemplate(model.ScanTemplate{ProjectID: p.ID, Name: "a/b", Prompt: "Go."}, true); err == nil {
		t.Fatal("name with a slash accepted")
	}
	a, err := s.SaveScanTemplate(model.ScanTemplate{ProjectID: p.ID, Name: "lint", Tools: []string{"Read"}, Prompt: "Lint."}, true)
	if err != nil || a.CreatedAt.IsZero() {
		t.Fatalf("create: %+v %v", a, err)
	}
	if _, err := s.SaveScanTemplate(model.ScanTemplate{ProjectID: p.ID, Name: "lint", Prompt: "Again."}, true); !errors.Is(err, ErrScanExists) {
		t.Fatalf("duplicate: %v", err)
	}
	// Another project may use the same name.
	if _, err := s.SaveScanTemplate(model.ScanTemplate{ProjectID: q.ID, Name: "lint", Prompt: "Q."}, true); err != nil {
		t.Fatal(err)
	}
	b, err := s.SaveScanTemplate(model.ScanTemplate{ProjectID: p.ID, Name: "lint", Prompt: "Lint better."}, false)
	if err != nil || !b.CreatedAt.Equal(a.CreatedAt) {
		t.Fatalf("edit: %+v %v", b, err)
	}

	// It survives a restart.
	if s, err = Open(path, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.ScanTemplates(p.ID); len(got) != 1 || got[0].Prompt != "Lint better." {
		t.Fatalf("after reopen: %+v", got)
	}

	// Deleting keeps runs; settings go unless kept.
	run, _ := s.CreateScanRun(model.ScanRun{ProjectID: p.ID, Scan: "lint", Status: model.ScanSucceeded})
	_, _ = s.SetScanSetting(p.ID, "lint", func(st *model.ScanSetting) { st.Schedule = "0 1 * * *" })
	if err := s.DeleteScanTemplate(p.ID, "lint", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ScanTemplate(p.ID, "lint"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("still there: %v", err)
	}
	if _, err := s.GetScanRun(run.ID); err != nil {
		t.Fatalf("run gone: %v", err)
	}
	if _, has := s.ScanSetting(p.ID, "lint"); has {
		t.Fatal("setting kept")
	}
	if err := s.DeleteScanTemplate(p.ID, "lint", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	_, _ = s.SetScanSetting(q.ID, "lint", func(st *model.ScanSetting) { st.Schedule = "0 1 * * *" })
	if err := s.DeleteScanTemplate(q.ID, "lint", true); err != nil {
		t.Fatal(err)
	}
	if _, has := s.ScanSetting(q.ID, "lint"); !has {
		t.Fatal("kept setting deleted")
	}

	// They go with their project.
	_, _ = s.SaveScanTemplate(model.ScanTemplate{ProjectID: q.ID, Name: "x", Prompt: "X."}, true)
	if _, err := s.DeleteProject(q.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.ScanTemplates(q.ID); len(got) != 0 {
		t.Fatalf("after project delete: %+v", got)
	}
}
