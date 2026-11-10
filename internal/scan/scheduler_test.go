package scan

import (
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func schedFixture(t *testing.T, src string) (*fixture, *Scheduler, *clock) {
	fx := newFixture(t, "nightly", src)
	c := &clock{t: at("2026-11-07 12:00")}
	fx.runner.Now = c.now
	fx.st.Now = c.now
	return fx, &Scheduler{Store: fx.st, Runner: fx.runner, Now: c.now}, c
}

// enable turns a repository template's schedule on as the user does in
// Dash, approving the template's expression.
func enable(t *testing.T, fx *fixture, name string) {
	t.Helper()
	tm := mustFind(t, fx, name)
	on := true
	if _, err := fx.st.SetScanSetting(fx.p.ID, name, func(st *model.ScanSetting) {
		st.Enabled, st.ApprovedSchedule = &on, tm.Schedule
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerRunsWhenDue(t *testing.T) {
	fx, s, c := schedFixture(t, "---\nschedule: 0 6 * * *\n---\nGo.")
	enable(t, fx, "nightly")
	// Turned on at 12:00: nothing ran before, so wait for the next slot.
	if got := s.Tick(); len(got) != 0 {
		t.Fatalf("started at first sight: %+v", got)
	}
	if next := s.NextRun(fx.p.ID, mustFind(t, fx, "nightly")); !next.Equal(at("2026-11-08 06:00")) {
		t.Fatalf("next run %s", next)
	}
	c.t = at("2026-11-08 05:59")
	if got := s.Tick(); len(got) != 0 {
		t.Fatal("started early")
	}
	c.t = at("2026-11-08 06:00")
	got := s.Tick()
	if len(got) != 1 || got[0].Trigger != "schedule" {
		t.Fatalf("not started at 06:00: %+v", got)
	}
	fx.runner.Wait()
	c.t = at("2026-11-08 06:01")
	if got := s.Tick(); len(got) != 0 {
		t.Fatal("started twice for one slot")
	}
}

func TestSchedulerCatchesUpOnce(t *testing.T) {
	fx, s, c := schedFixture(t, "---\nschedule: 0 6 * * *\n---\nGo.")
	c.t = at("2026-11-01 12:00")
	enable(t, fx, "nightly")
	c.t = at("2026-11-07 12:00")
	// The last scheduled run was three days ago: Dash was down since.
	if _, err := fx.st.CreateScanRun(model.ScanRun{ProjectID: fx.p.ID, Scan: "nightly", Trigger: "schedule",
		Status: model.ScanSucceeded, StartedAt: at("2026-11-04 06:00")}); err != nil {
		t.Fatal(err)
	}
	if got := s.Tick(); len(got) != 1 {
		t.Fatalf("missed run: %d started", len(got))
	}
	fx.runner.Wait()
	for _, m := range []string{"12:01", "18:00", "23:59"} {
		c.t = at("2026-11-07 " + m)
		if got := s.Tick(); len(got) != 0 {
			t.Fatalf("missed slots run again at %s", m)
		}
	}
	c.t = at("2026-11-08 06:00")
	if got := s.Tick(); len(got) != 1 {
		t.Fatal("next slot not run")
	}
	fx.runner.Wait()
	if runs := fx.st.ListScanRuns(fx.p.ID, "nightly"); len(runs) != 3 {
		t.Fatalf("%d runs, want 3", len(runs))
	}
}

func TestSchedulerNoOverlap(t *testing.T) {
	fx, s, c := schedFixture(t, "---\nschedule: '*/5 * * * *'\n---\nGo.")
	fx.exec.release, fx.exec.started = make(chan struct{}), make(chan struct{}, 4)
	c.t = at("2026-11-07 12:05")
	enable(t, fx, "nightly")
	s.Tick() // just turned on
	c.t = at("2026-11-07 12:10")
	if got := s.Tick(); len(got) != 1 {
		t.Fatal("not started")
	}
	<-fx.exec.started
	// Still running at the next slots: no second copy.
	for _, m := range []string{"12:15", "12:20"} {
		c.t = at("2026-11-07 " + m)
		if got := s.Tick(); len(got) != 0 {
			t.Fatalf("second copy started at %s", m)
		}
	}
	// A manual run doesn't overlap either.
	if _, err := fx.runner.Start(fx.p.ID, "nightly", "manual"); err != ErrRunning {
		t.Fatalf("manual during scheduled: %v", err)
	}
	close(fx.exec.release)
	fx.runner.Wait()
	fx.exec.release = nil
	// Once it is done, the overdue slot runs (once).
	c.t = at("2026-11-07 12:21")
	if got := s.Tick(); len(got) != 1 {
		t.Fatal("overdue slot not run after the long run")
	}
	fx.runner.Wait()
}

func TestSchedulerSettings(t *testing.T) {
	fx, s, c := schedFixture(t, "")
	// Built-ins: the schedule is a suggestion, off by default.
	sec := mustFind(t, fx, "security")
	set, has := fx.st.ScanSetting(fx.p.ID, "security")
	if sch := Effective(sec, set, has); sch.Enabled || sch.Cron != "0 6 * * *" {
		t.Fatalf("built-in schedule: %+v", sch)
	}
	c.t = at("2026-11-09 07:00")
	if got := s.Tick(); len(got) != 0 {
		t.Fatal("built-in ran without being turned on")
	}
	// Turned on with its own expression; counts from when it was turned on.
	on := true
	if _, err := fx.st.SetScanSetting(fx.p.ID, "security", func(st *model.ScanSetting) { st.Enabled, st.Schedule = &on, "30 7 * * *" }); err != nil {
		t.Fatal(err)
	}
	set, has = fx.st.ScanSetting(fx.p.ID, "security")
	if sch := Effective(sec, set, has); !sch.Enabled || sch.Cron != "30 7 * * *" || !sch.Custom {
		t.Fatalf("custom schedule: %+v", sch)
	}
	c.t = set.UpdatedAt.Add(time.Minute)
	if got := s.Tick(); len(got) != 0 {
		t.Fatal("ran right after being turned on")
	}
	cr, _ := ParseCron("30 7 * * *")
	c.t = cr.Next(set.UpdatedAt)
	if got := s.Tick(); len(got) != 1 {
		t.Fatal("not run at its slot")
	}
	fx.runner.Wait()
	// Turned off: nothing runs.
	off := false
	fx.st.SetScanSetting(fx.p.ID, "security", func(st *model.ScanSetting) { st.Enabled = &off })
	c.t = c.t.AddDate(0, 0, 3)
	if got := s.Tick(); len(got) != 0 {
		t.Fatal("ran while off")
	}
	// A repository template's schedule is off until approved.
	repo := Template{Name: "r", Schedule: "0 6 * * *"}
	if sch := Effective(repo, model.ScanSetting{}, false); sch.Enabled || sch.Requested != "0 6 * * *" {
		t.Fatalf("repository schedule by default: %+v", sch)
	}
	// Turned on without an approved expression (a setting from before
	// approvals): still off.
	if sch := Effective(repo, model.ScanSetting{Enabled: &on}, true); sch.Enabled {
		t.Fatal("repository schedule on without approval")
	}
	// The user's own expression needs no approval.
	if sch := Effective(repo, model.ScanSetting{Enabled: &on, Schedule: "0 9 * * *"}, true); !sch.Enabled || sch.Cron != "0 9 * * *" || sch.Requested != "" {
		t.Fatalf("own expression: %+v", sch)
	}
}

func TestSchedulerRepoScheduleNeedsApproval(t *testing.T) {
	fx, s, c := schedFixture(t, "---\nschedule: 0 6 * * *\n---\nGo.")
	tm := mustFind(t, fx, "nightly")
	set, has := fx.st.ScanSetting(fx.p.ID, "nightly")
	if sch := Effective(tm, set, has); sch.Enabled || sch.Requested != "0 6 * * *" {
		t.Fatalf("before approval: %+v", sch)
	}
	for _, d := range []string{"2026-11-08 06:00", "2026-11-09 06:00", "2026-11-10 07:00"} {
		c.t = at(d)
		if got := s.Tick(); len(got) != 0 {
			t.Fatalf("ran at %s without being turned on", d)
		}
		if next := s.NextRun(fx.p.ID, tm); !next.IsZero() {
			t.Fatalf("next run %s without being turned on", next)
		}
	}
	// Turned on in Dash: runs from its next slot.
	enable(t, fx, "nightly")
	c.t = at("2026-11-11 06:00")
	if got := s.Tick(); len(got) != 1 {
		t.Fatal("not run once turned on")
	}
	fx.runner.Wait()
	// The template's schedule changes: the approved one keeps running, the
	// new one waits for approval.
	writeTemplate(t, fx, "nightly", "---\nschedule: '*/5 * * * *'\n---\nGo.")
	tm = mustFind(t, fx, "nightly")
	set, has = fx.st.ScanSetting(fx.p.ID, "nightly")
	if sch := Effective(tm, set, has); !sch.Enabled || sch.Cron != "0 6 * * *" || sch.Requested != "*/5 * * * *" {
		t.Fatalf("after the template changed: %+v", sch)
	}
	c.t = at("2026-11-11 12:00")
	if got := s.Tick(); len(got) != 0 {
		t.Fatal("ran on the template's new, unapproved schedule")
	}
	c.t = at("2026-11-12 06:00")
	if got := s.Tick(); len(got) != 1 {
		t.Fatal("approved schedule stopped running")
	}
	fx.runner.Wait()
}

type pausedUsage struct{ paused bool }

func (u *pausedUsage) CanStart() (bool, string) { return !u.paused, "usage" }

func TestSchedulerWaitsForUsage(t *testing.T) {
	fx, s, c := schedFixture(t, "---\nschedule: 0 6 * * *\n---\nGo.")
	enable(t, fx, "nightly")
	u := &pausedUsage{paused: true}
	s.Usage = u
	s.Tick()
	c.t = at("2026-11-08 06:00")
	if got := s.Tick(); len(got) != 0 {
		t.Fatal("ran while usage paused")
	}
	u.paused = false
	c.t = at("2026-11-08 09:00")
	if got := s.Tick(); len(got) != 1 {
		t.Fatal("didn't run once usage lifted")
	}
	fx.runner.Wait()
}

func mustFind(t *testing.T, fx *fixture, name string) Template {
	t.Helper()
	ts, _ := Load(fx.repo)
	tm, ok := Pick(ts, name)
	if !ok {
		t.Fatalf("no template %s", name)
	}
	return tm
}
