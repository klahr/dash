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
	return fx, &Scheduler{Store: fx.st, Runner: fx.runner, Now: c.now}, c
}

func TestSchedulerRunsWhenDue(t *testing.T) {
	fx, s, c := schedFixture(t, "---\nschedule: 0 6 * * *\n---\nGo.")
	// First seen at 12:00: nothing ran before, so wait for the next slot.
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
	s.Tick() // first sight
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
	// A repository template's schedule is on by default.
	repo := Template{Name: "r", Schedule: "0 6 * * *"}
	if sch := Effective(repo, model.ScanSetting{}, false); !sch.Enabled {
		t.Fatal("repository schedule off by default")
	}
}

type pausedUsage struct{ paused bool }

func (u *pausedUsage) CanStart() (bool, string) { return !u.paused, "usage" }

func TestSchedulerWaitsForUsage(t *testing.T) {
	fx, s, c := schedFixture(t, "---\nschedule: 0 6 * * *\n---\nGo.")
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
	tm, ok := Find(fx.repo, name)
	if !ok {
		t.Fatalf("no template %s", name)
	}
	return tm
}
