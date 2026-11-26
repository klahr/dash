package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// A project with a ClearMergedZone loses its merged tasks once a new day
// starts there; its other tasks, today's merged ones, other projects'
// merged tasks and those of a hub's project on this runner stay.
func TestClearMergedOnNewDay(t *testing.T) {
	s, _, p := boardServer(t)
	other, err := raw(s).CreateProject(model.Project{Name: "keeps"})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = raw(s).UpdateProject(p.ID, func(x *model.Project) error { x.ClearMergedZone = "Europe/Stockholm"; return nil }); err != nil {
		t.Fatal(err)
	}
	// The hub's project, with a zone set: its tasks are the hub's to delete.
	hub, err := raw(s).CreateProject(model.Project{Name: "hub's", HubProjectID: "hp1", ClearMergedZone: "Europe/Stockholm"})
	if err != nil {
		t.Fatal(err)
	}
	if hub.HubProjectID == "" || hub.ClearMergedZone == "" {
		t.Fatalf("the hub's project lost its settings: %+v", hub)
	}
	merged := func(p model.Project) model.Todo {
		td := reviewTask(t, s, p, nil)
		td, err := raw(s).UpdateTodo(td.ID, func(x *model.Todo) error { x.Worktree = nil; return nil })
		if err != nil {
			t.Fatal(err)
		}
		if td, err = raw(s).SetStatus(td.ID, model.StatusMerged, ""); err != nil {
			t.Fatal(err)
		}
		return td
	}
	done, inReview, elsewhere, hubs := merged(p), reviewTask(t, s, p, nil), merged(other), merged(hub)

	ctx := context.Background()
	if n := s.clearMerged(ctx, time.Now()); n != 0 {
		t.Fatalf("the same day: deleted %d, want none", n)
	}
	if n := s.clearMerged(ctx, time.Now().Add(48*time.Hour)); n != 1 {
		t.Fatalf("a new day: deleted %d, want 1", n)
	}
	if _, err := raw(s).GetTodo(done.ID); err == nil {
		t.Fatal("want the merged task deleted")
	}
	for _, id := range []string{inReview.ID, elsewhere.ID, hubs.ID} {
		if _, err := raw(s).GetTodo(id); err != nil {
			t.Fatalf("want task %s kept: %v", id, err)
		}
	}
}

func TestStartOfDay(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no time zone data")
	}
	// 03:00 UTC on the 2nd is still the 1st in New York.
	got := startOfDay(time.Date(2026, 3, 2, 3, 0, 0, 0, time.UTC), loc)
	if want := time.Date(2026, 3, 1, 0, 0, 0, 0, loc); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// The setting takes an IANA time zone or "", and refuses others.
func TestClearMergedZoneSetting(t *testing.T) {
	s, _, p := boardServer(t)
	s.Git = gitops.New(nil)
	w := httptest.NewRecorder()
	s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest("PATCH", "/api/projects/"+p.ID, strings.NewReader(`{"clear_merged_zone":"Mars/Olympus"}`))))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "time zone") {
		t.Fatalf("unknown zone: %d %s", w.Code, w.Body)
	}
	patch := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest("PATCH", "/api/projects/"+p.ID, strings.NewReader(body))))
		return w
	}
	// "Local" loads, but is the server's zone, not one the user chose.
	for _, z := range []string{"Local", "local"} {
		if w := patch(`{"clear_merged_zone":"` + z + `"}`); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", z, w.Code, w.Body)
		}
	}
	if w := patch(`{"clear_merged_zone":" Europe/Stockholm "}`); w.Code != http.StatusOK {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	if x, err := raw(s).GetProject(p.ID); err != nil || x.ClearMergedZone != "Europe/Stockholm" {
		t.Fatalf("set: zone %q, %v", x.ClearMergedZone, err)
	}
	// "" turns it off.
	if w := patch(`{"clear_merged_zone":""}`); w.Code != http.StatusOK {
		t.Fatalf("off: %d %s", w.Code, w.Body)
	}
	if x, err := raw(s).GetProject(p.ID); err != nil || x.ClearMergedZone != "" {
		t.Fatalf("off: zone %q, %v", x.ClearMergedZone, err)
	}
}
