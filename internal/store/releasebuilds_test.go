package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

// Release builds are saved per tag, survive reopening the store, list the
// most recently pushed first, keep only the newest KeepReleaseBuilds, and go
// with their project.
func TestReleaseBuildsSurviveReopening(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "dash.db")
	s := openTest(t, path)
	p, err := s.CreateProject(model.Project{Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutReleaseBuild(model.ReleaseBuild{ProjectID: "nope", Tag: "1.0.0"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a build of no project: %v", err)
	}
	at := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	building := model.ReleaseBuild{ProjectID: p.ID, Tag: "1.1.0", SHA: "bbb", State: "building", Pushed: at.Add(time.Hour),
		Runs: []model.CIRun{{ID: 7, Workflow: "Release", Status: "in_progress", SHA: "bbb", Branch: "1.1.0", URL: "https://github.com/o/r/actions/runs/7"}}}
	for _, b := range []model.ReleaseBuild{
		{ProjectID: p.ID, Tag: "1.0.0", SHA: "aaa", State: "published", Pushed: at, URL: "https://github.com/o/r/releases/tag/1.0.0"},
		building,
	} {
		if err := s.PutReleaseBuild(b); err != nil {
			t.Fatal(err)
		}
	}

	s = reopen(t, s, path)
	got, err := s.ReleaseBuilds(p.ID, 10)
	if err != nil || len(got) != 2 || got[0].Tag != "1.1.0" || got[1].Tag != "1.0.0" || got[1].State != "published" {
		t.Fatalf("after reopening: %v %+v", err, got)
	}
	if got[0].State != "building" || len(got[0].Runs) != 1 || got[0].Runs[0] != building.Runs[0] || !got[0].Pushed.Equal(building.Pushed) {
		t.Fatalf("the building one: %+v", got[0])
	}
	// Saving a tag again replaces it.
	building.State = "failed"
	if err := s.PutReleaseBuild(building); err != nil {
		t.Fatal(err)
	}
	if b, err := s.ReleaseBuild(p.ID, "1.1.0"); err != nil || b.State != "failed" {
		t.Fatalf("one tag: %v %+v", err, b)
	}
	if _, err := s.ReleaseBuild(p.ID, "9.9.9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown tag: %v", err)
	}

	for i := range KeepReleaseBuilds {
		b := model.ReleaseBuild{ProjectID: p.ID, Tag: fmt.Sprintf("2.0.%d", i), State: "published", Pushed: at.Add(time.Duration(i+2) * time.Hour)}
		if err := s.PutReleaseBuild(b); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := s.ReleaseBuilds(p.ID, 1000)
	if len(all) != KeepReleaseBuilds || all[len(all)-1].Tag != "2.0.0" {
		t.Fatalf("kept %d, oldest %s", len(all), all[len(all)-1].Tag)
	}

	if _, err := s.DeleteProject(p.ID); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.ReleaseBuilds(p.ID, 10); len(left) != 0 {
		t.Fatalf("left after deleting the project: %d", len(left))
	}
}
