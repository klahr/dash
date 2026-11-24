package scan

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/usage"
)

// fakeUsage is a usage tracker: a 5-hour window and whether the queue is
// paused.
type fakeUsage struct {
	known  bool
	util   float64
	resets time.Time
	paused string // why the queue is paused ("": it isn't)
}

func (u *fakeUsage) CanStart() (bool, string) { return u.paused == "", u.paused }

func (u *fakeUsage) Snapshot() usage.Snapshot {
	s := usage.Snapshot{Known: u.known}
	if !u.resets.IsZero() {
		s.Windows = []usage.Window{{Name: "five_hour", Utilization: u.util, ResetsAt: u.resets, Status: "allowed"}}
	}
	return s
}

// spare is usage with the 5-hour window at util, resetting at resets.
func spare(util float64, resets string) *fakeUsage {
	return &fakeUsage{known: true, util: util, resets: at(resets)}
}

// earlyFixture is a scan scheduled daily at 06:00, turned on at
// 2026-11-07 12:00 with early runs set to when ("": off), on usage u.
func earlyFixture(t *testing.T, when string, u *fakeUsage) (*fixture, *Scheduler, *clock) {
	t.Helper()
	fx, s, c := schedFixture(t, "---\nschedule: 0 6 * * *\n---\nGo.")
	enable(t, fx, "nightly")
	if when != "" {
		setEarly(t, fx, model.EarlyRun{On: true, When: when})
	}
	s.Usage = u
	return fx, s, c
}

func setEarly(t *testing.T, fx *fixture, e model.EarlyRun) {
	t.Helper()
	if _, err := fx.st.SetScanSetting(fx.p.ID, "nightly", func(st *model.ScanSetting) { st.Early = &e }); err != nil {
		t.Fatal(err)
	}
}

// tickAt checks the schedules at when and returns the triggers of the runs
// started, after they finish.
func tickAt(t *testing.T, fx *fixture, s *Scheduler, c *clock, when string) []string {
	t.Helper()
	c.t = at(when)
	var out []string
	for _, r := range s.Tick() {
		out = append(out, r.Trigger)
	}
	fx.runner.Wait()
	return out
}

func wantRuns(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("started %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("started %v, want %v", got, want)
		}
	}
}

func TestEarlyRunWithinLeadTime(t *testing.T) {
	t.Parallel()

	fx, s, c := earlyFixture(t, model.EarlyAnyTime, spare(0.3, "2026-11-08 03:00"))
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 01:59")) // 61 minutes before the reset
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 02:00"), model.TriggerEarly)
	// Once per period: not again before the reset, nor at the cron time.
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 02:30"))
	if next := s.NextRun(fx.p.ID, mustFind(t, fx, "nightly")); !next.Equal(at("2026-11-09 06:00")) {
		t.Fatalf("next run %s, want the next period's cron time", next)
	}
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 06:00"))
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 06:01"))
	// The next period, no early chance: at its cron time.
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-09 06:00"), model.TriggerSchedule)
}

func TestEarlyRunNotAboveCeiling(t *testing.T) {
	t.Parallel()

	fx, s, c := earlyFixture(t, model.EarlyAnyTime, spare(0.7, "2026-11-08 03:00"))
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 02:00"))
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 02:59"))
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 06:00"), model.TriggerSchedule)
}

func TestEarlyRunOverrides(t *testing.T) {
	t.Parallel()

	fx, s, c := earlyFixture(t, "", spare(0.75, "2026-11-08 03:00"))
	setEarly(t, fx, model.EarlyRun{On: true, LeadMinutes: 15, MaxPercent: 80})
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 02:44"))
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 02:45"), model.TriggerEarly)
}

func TestEarlyRunAtNightOnly(t *testing.T) {
	t.Parallel()

	u := spare(0.2, "2026-11-07 15:00")
	fx, s, c := earlyFixture(t, model.EarlyNight, u)
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-07 14:30")) // resets in the day
	u.resets = at("2026-11-07 21:30")                    // night starts at 22:00
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-07 21:00"))
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 06:00"), model.TriggerSchedule)

	// A reset after midnight is at night.
	fx, s, c = earlyFixture(t, model.EarlyNight, spare(0.2, "2026-11-08 00:30"))
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-07 23:45"), model.TriggerEarly)
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 06:00"))

	// And one before it.
	fx, s, c = earlyFixture(t, model.EarlyNight, spare(0.2, "2026-11-07 22:30"))
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-07 22:00"), model.TriggerEarly)

	if !AtNight(at("2026-11-07 22:00"), time.Local) || !AtNight(at("2026-11-08 06:59"), time.Local) ||
		AtNight(at("2026-11-08 07:00"), time.Local) || AtNight(at("2026-11-07 21:59"), time.Local) {
		t.Fatal("night range")
	}
}

func TestEarlyRunFallsBackToCron(t *testing.T) {
	t.Parallel()

	for name, u := range map[string]*fakeUsage{
		"no reset before the cron time": spare(0.1, "2026-11-08 10:00"),
		"no usage data":                 {},
		"stale usage":                   spare(0.1, "2026-11-07 20:00"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fx, s, c := earlyFixture(t, model.EarlyAnyTime, u)
			for _, m := range []string{"2026-11-07 21:00", "2026-11-08 03:00", "2026-11-08 05:59"} {
				wantRuns(t, tickAt(t, fx, s, c, m))
			}
			wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 06:00"), model.TriggerSchedule)
		})
	}
}

// A cron run at 06:00 isn't followed by an early run for the next period
// when usage resets at 07:00: not until half a period has gone by.
func TestEarlyRunWaitsHalfAPeriod(t *testing.T) {
	t.Parallel()

	u := &fakeUsage{}
	fx, s, c := earlyFixture(t, model.EarlyAnyTime, u)
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 06:00"), model.TriggerSchedule)
	*u = *spare(0.1, "2026-11-08 07:00")
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 06:30"))
	*u = *spare(0.1, "2026-11-08 18:30")
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 17:59")) // 11:59 after it
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 18:00"), model.TriggerEarly)
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-09 06:00"))
}

func TestEarlyRunHolds(t *testing.T) {
	t.Parallel()

	for _, why := range []string{"5-hour limit reached", "Paused by you", "Ahead of the weekly pace"} {
		t.Run(why, func(t *testing.T) {
			t.Parallel()
			u := spare(0.1, "2026-11-08 03:00")
			u.paused = why
			fx, s, c := earlyFixture(t, model.EarlyAnyTime, u)
			wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 02:30"))
			u.paused = ""
			wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 02:31"), model.TriggerEarly)
		})
	}
	t.Run("billing", func(t *testing.T) {
		t.Parallel()
		fx, s, c := earlyFixture(t, model.EarlyAnyTime, spare(0.1, "2026-11-08 03:00"))
		s.Hold = func(int64) string { return "unpaid" }
		wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 02:30"))
	})
}

// The early run and the cron time landing together start one run.
func TestEarlyRunNoDoubleRun(t *testing.T) {
	t.Parallel()

	fx, s, c := earlyFixture(t, model.EarlyAnyTime, spare(0.1, "2026-11-08 06:30"))
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 06:00"), model.TriggerSchedule)
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 06:01"))
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 06:29"))

	// Still running when the cron time comes after an early start: no
	// second copy, and none once it's done.
	fx, s, c = earlyFixture(t, model.EarlyAnyTime, spare(0.1, "2026-11-08 06:30"))
	fx.exec.release, fx.exec.started = make(chan struct{}), make(chan struct{}, 4)
	c.t = at("2026-11-08 05:30")
	if got := s.Tick(); len(got) != 1 || got[0].Trigger != model.TriggerEarly {
		t.Fatalf("early run: %+v", got)
	}
	<-fx.exec.started
	c.t = at("2026-11-08 06:00")
	if got := s.Tick(); len(got) != 0 {
		t.Fatalf("second copy: %+v", got)
	}
	close(fx.exec.release)
	fx.runner.Wait()
	fx.exec.release = nil
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 06:01"))
}

// A run by hand counts for its period too.
func TestEarlyRunAfterManualRun(t *testing.T) {
	t.Parallel()

	fx, s, c := earlyFixture(t, model.EarlyAnyTime, spare(0.1, "2026-11-08 03:00"))
	c.t = at("2026-11-07 20:00")
	if _, err := fx.runner.Start(fx.p.ID, "nightly", model.TriggerManual); err != nil {
		t.Fatal(err)
	}
	fx.runner.Wait()
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 02:30"))
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 06:00"))
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-09 06:00"), model.TriggerSchedule)
}

// An early run that Dash restarts after a shutdown still stands for its
// period: the restart doesn't let the cron time run the scan again.
func TestEarlyRunResumedCoversPeriod(t *testing.T) {
	t.Parallel()

	fx, s, c := earlyFixture(t, model.EarlyAnyTime, &fakeUsage{})
	c.t = at("2026-11-08 05:10")
	fx.interrupt(t, "nightly", model.TriggerEarly)
	s.Runner = fx.runner
	c.t = at("2026-11-08 05:21")
	if got := fx.runner.ResumeInterrupted(); len(got) != 1 {
		t.Fatalf("resumed: %+v", got)
	}
	fx.runner.Wait()
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 06:00"))
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 06:01"))
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-09 06:00"), model.TriggerSchedule)
}

// With the option off, spare usage changes nothing.
func TestEarlyRunOff(t *testing.T) {
	t.Parallel()

	fx, s, c := earlyFixture(t, "", spare(0.1, "2026-11-08 03:00"))
	setEarly(t, fx, model.EarlyRun{On: false, When: model.EarlyNight})
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 02:30"))
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 06:00"), model.TriggerSchedule)
	// A manual run doesn't skip the cron time either, as before.
	c.t = at("2026-11-08 20:00")
	if _, err := fx.runner.Start(fx.p.ID, "nightly", model.TriggerManual); err != nil {
		t.Fatal(err)
	}
	fx.runner.Wait()
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-09 06:00"), model.TriggerSchedule)
}

// Irregular schedules: the periods come from the expression.
func TestEarlyRunWeekdays(t *testing.T) {
	t.Parallel()

	fx, s, c := schedFixture(t, "---\nschedule: 0 6 * * 1-5\n---\nGo.")
	enable(t, fx, "nightly") // Saturday 2026-11-07 12:00
	setEarly(t, fx, model.EarlyRun{On: true})
	u := spare(0.1, "2026-11-08 12:30")
	s.Usage = u
	// Sunday: in the period ending Monday 06:00.
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 12:00"), model.TriggerEarly)
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-09 06:00"))
	cr, _ := ParseCron("0 6 * * 1-5")
	if p := cr.Prev(at("2026-11-09 05:59")); !p.Equal(at("2026-11-06 06:00")) {
		t.Fatalf("prev %s", p)
	}
	if e := cr.PeriodEnd(at("2026-11-09 06:00")); !e.Equal(at("2026-11-09 06:00")) {
		t.Fatalf("period end %s", e)
	}
}

// fakeRemote is a hub's runners: it notes the account of each job.
type fakeRemote struct {
	mu       sync.Mutex
	accounts []string
}

func (f *fakeRemote) Scan(_ context.Context, _ model.Project, job Job, _ Hooks) (Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accounts = append(f.accounts, job.Account)
	return Outcome{Skip: "nothing to scan"}, nil
}

// On a hub, an early run weighs the usage of the accounts of the runners
// that could take it, and runs on one with room.
func TestEarlyRunOnHub(t *testing.T) {
	t.Parallel()

	fx, s, c := earlyFixture(t, model.EarlyAnyTime, &fakeUsage{}) // the hub's own: none
	remote := &fakeRemote{}
	fx.runner.Remote = remote
	full := spare(0.9, "2026-11-08 03:00").Snapshot()
	room := spare(0.2, "2026-11-08 03:00").Snapshot()
	var gotBy int64 = -1
	accounts := []Spare{
		{Account: "held@example.com", Usage: room, Hold: "5-hour limit reached"},
		{Account: "full@example.com", Usage: full},
	}
	s.Spare = func(_ model.Project, by int64) []Spare { gotBy = by; return accounts }
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 02:30"))
	if gotBy != 0 {
		t.Fatalf("asked for user %d", gotBy)
	}
	accounts = append(accounts, Spare{Account: "room@example.com", Usage: room})
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-08 02:31"), model.TriggerEarly)
	if len(remote.accounts) != 1 || remote.accounts[0] != "room@example.com" {
		t.Fatalf("dispatched to %q", remote.accounts)
	}
	// The cron run goes to any account.
	wantRuns(t, tickAt(t, fx, s, c, "2026-11-09 06:00"), model.TriggerSchedule)
	if len(remote.accounts) != 2 || remote.accounts[1] != "" {
		t.Fatalf("dispatched to %q", remote.accounts)
	}
}
