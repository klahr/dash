package model

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("no time zone data for %s: %v", name, err)
	}
	return loc
}

// The summary goes out at the same wall-clock time every day, in the
// user's time zone, on both sides of a daylight saving change.
func TestSummaryNextAcrossDST(t *testing.T) {
	sth := mustLoc(t, "Europe/Stockholm")
	s := DefaultNotifySettings()
	s.Summary, s.Time, s.TimeZone = SummaryDaily, "08:00", "Europe/Stockholm"

	// Clocks go forward on Sunday 29 March 2026 (CET → CEST).
	at := time.Date(2026, 3, 28, 8, 0, 0, 0, sth)
	var got []time.Time
	for range 3 {
		at = s.Next(at)
		got = append(got, at)
	}
	for i, g := range got {
		if l := g.In(sth); l.Hour() != 8 || l.Minute() != 0 || l.Day() != 29+i {
			t.Errorf("summary %d at %v (local %v)", i, g, l)
		}
	}
	if d := got[1].Sub(got[0]); d != 24*time.Hour {
		t.Errorf("29 → 30 March: %v apart", d)
	}
	if d := got[0].Sub(time.Date(2026, 3, 28, 8, 0, 0, 0, sth)); d != 23*time.Hour {
		t.Errorf("28 → 29 March (23-hour day): %v apart", d)
	}
	if u := got[0].UTC(); u.Hour() != 6 {
		t.Errorf("29 March 08:00 CEST is 06:00 UTC, got %v", u)
	}
	// Clocks go back on Sunday 25 October 2026: a 25-hour day.
	before := time.Date(2026, 10, 24, 8, 0, 0, 0, sth)
	next := s.Next(before)
	if next.Sub(before) != 25*time.Hour || next.In(sth).Hour() != 8 {
		t.Errorf("24 → 25 October: %v (%v)", next.Sub(before), next.In(sth))
	}

	// A time skipped by the change goes out right after the gap.
	ny := mustLoc(t, "America/New_York")
	s.Time, s.TimeZone = "02:30", "America/New_York"
	gap := s.Next(time.Date(2026, 3, 7, 12, 0, 0, 0, ny)) // 8 March 2026: 02:00 → 03:00
	if l := gap.In(ny); l.Day() != 8 || l.Hour() != 3 || l.Minute() != 30 {
		t.Errorf("in the gap: %v", l)
	}
}

func TestSummaryNextWeeklyAndOff(t *testing.T) {
	s := DefaultNotifySettings()
	if !s.Next(time.Now()).IsZero() {
		t.Fatal("off has a next time")
	}
	s.Summary, s.Time, s.Weekday, s.TimeZone = SummaryWeekly, "17:30", time.Friday, "UTC"
	from := time.Date(2026, 11, 17, 18, 0, 0, 0, time.UTC) // a Tuesday
	got := s.Next(from)
	if want := time.Date(2026, 11, 20, 17, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("next %v, want %v", got, want)
	}
	if again := s.Next(got); !again.Equal(got.Add(7 * 24 * time.Hour)) {
		t.Fatalf("after that %v", again)
	}
	// Exactly at the time: the next one, not this one again.
	s.Summary = SummaryDaily
	at := time.Date(2026, 11, 17, 17, 30, 0, 0, time.UTC)
	if got := s.Next(at); !got.Equal(at.Add(24 * time.Hour)) {
		t.Fatalf("at the time: %v", got)
	}
}

func TestNotifySettingsNormalize(t *testing.T) {
	s := NotifySettings{Summary: "daily", Time: "07:15", TimeZone: "Europe/Stockholm", AllProjects: true, Email: true,
		Projects: []string{" a ", "a", ""}, Push: []NotifyEvent{NotifyIssue, NotifyIssue}}
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	if len(s.Projects) != 1 || s.Projects[0] != "a" || len(s.Push) != 1 || s.Include.MinSeverity != SevMedium || s.Webhooks == nil || s.Tasks != TasksMine {
		t.Fatalf("normalized %+v", s)
	}
	for name, bad := range map[string]NotifySettings{
		"frequency":      {Summary: "hourly"},
		"time":           {Time: "25:00"},
		"time format":    {Time: "7:15"},
		"signed time":    {Time: "+7:15"},
		"zone":           {TimeZone: "Mars/Olympus"},
		"local zone":     {TimeZone: "Local"},
		"weekday":        {Weekday: 7},
		"push":           {Push: []NotifyEvent{NotifyBehind}},
		"tasks":          {Tasks: "theirs"},
		"severity":       {Include: SummaryInclude{MinSeverity: "dire"}},
		"no destination": {Summary: SummaryDaily, AllProjects: true},
		"no projects":    {Summary: SummaryDaily, Email: true},
	} {
		if err := bad.Normalize(); err == nil {
			t.Errorf("%s: accepted %+v", name, bad)
		}
	}
	in := SummaryInclude{Issues: true, MinSeverity: SevHigh, Review: true}
	if !in.Wants(SummaryIssue, SevCritical) || !in.Wants(SummaryIssue, SevHigh) || in.Wants(SummaryIssue, SevMedium) ||
		!in.Wants(SummaryReview, "") || in.Wants(SummaryFailed, "") {
		t.Fatal("include")
	}
	// Whose tasks: a creator of 0 (Dash) or below (a deleted account) is
	// nobody's, so everyone's.
	mine, all := NotifySettings{Tasks: TasksMine}, NotifySettings{Tasks: TasksAll}
	if !mine.WantsTask(5, 5) || mine.WantsTask(5, 6) || !mine.WantsTask(5, 0) || !mine.WantsTask(5, DeletedUser) || !all.WantsTask(5, 6) {
		t.Fatal("WantsTask")
	}
	if !TaskEvent(NotifyReview) || !TaskEvent(NotifyPermission) || TaskEvent(NotifyBuild) || TaskEvent(NotifyDisk) || TaskEvent(NotifyGoal) {
		t.Fatal("TaskEvent")
	}
}
