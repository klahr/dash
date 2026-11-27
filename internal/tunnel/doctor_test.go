package tunnel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/klahr/dash/internal/doctor"
	"github.com/klahr/dash/internal/store/storetest"
)

// A runner's doctor report comes with its heartbeat: the hub keeps the
// latest (cleaned), only a newer one replaces it, and it survives a
// restart of the hub. A report without one (an older runner) leaves none.
func TestReportCarriesDoctor(t *testing.T) {
	t.Parallel()
	acc, owner := storetest.Accounts(t)
	path := filepath.Join(t.TempDir(), "runners.json")
	h := &Hub{Accounts: acc}
	if err := h.Keep(path); err != nil {
		t.Fatal(err)
	}
	code, _, _ := h.NewCode(owner)
	id, token, _, err := h.register(code, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(h.ServeReport))
	t.Cleanup(srv.Close)
	report := func(body map[string]any) {
		t.Helper()
		if err := Report(context.Background(), Credentials{Server: srv.URL, Token: token}, body); err != nil {
			t.Fatal(err)
		}
	}

	report(map[string]any{"parallel": 1}) // an older runner
	if r := runnerOf(t, h, id); r.Doctor != nil || r.HasChrome() {
		t.Fatalf("doctor from a runner without one: %+v", r.Doctor)
	}

	at := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	checks := []doctor.Check{
		{Group: doctor.GroupRequired, Name: doctor.CheckGit, Status: doctor.OK, Message: "/usr/bin/git"},
		{Group: doctor.GroupFeatures, Name: doctor.CheckChrome, Status: doctor.OK},
		{Group: doctor.GroupFeatures, Name: doctor.CheckGH, Status: doctor.Warning, Fix: "brew install gh"},
		{Group: doctor.GroupProject, Name: "go", Project: "hp1", ProjectName: "api", Status: doctor.Warning},
		{Group: "bogus", Name: "x", Status: doctor.OK},
	}
	report(map[string]any{"parallel": 1, "doctor": doctor.Report{At: at, System: "macOS", Checks: checks}})
	r := runnerOf(t, h, id)
	if r.Doctor == nil || !r.Doctor.At.Equal(at) || len(r.Doctor.Checks) != 4 || r.Doctor.System != "macOS" {
		t.Fatalf("doctor = %+v", r.Doctor)
	}
	if !r.HasChrome() || r.Has(doctor.CheckGH) || !AnyHas([]Runner{{}, r}, doctor.CheckChrome) || AnyHas([]Runner{r}, doctor.CheckGH) {
		t.Error("Has: chrome yes, gh no")
	}
	if got := r.MissingFor("hp1"); !slices.Equal(got, []string{"go"}) {
		t.Errorf("MissingFor = %q", got)
	}

	// An older report (the runner's clock, or a late heartbeat) doesn't win.
	report(map[string]any{"parallel": 1, "doctor": doctor.Report{At: at.Add(-time.Minute), Checks: checks[:1]}})
	if r := runnerOf(t, h, id); len(r.Doctor.Checks) != 4 {
		t.Errorf("older report replaced the newer: %+v", r.Doctor)
	}
	// Check again: a newer one, set directly.
	if err := h.SetDoctor(id, doctor.Report{At: at.Add(time.Hour), Checks: checks[:2]}); err != nil {
		t.Fatal(err)
	}
	if err := h.SetDoctor("nope", doctor.Report{At: at}); err != ErrUnknownName {
		t.Errorf("SetDoctor of no runner: %v", err)
	}

	h2 := &Hub{Accounts: acc}
	if err := h2.Keep(path); err != nil {
		t.Fatal(err)
	}
	if r := runnerOf(t, h2, id); r.Doctor == nil || !r.Doctor.At.Equal(at.Add(time.Hour)) || len(r.Doctor.Checks) != 2 {
		t.Errorf("after a restart: %+v", r.Doctor)
	}
}
