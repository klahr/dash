package usage

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// weekly reports only the weekly window: utilization week, resetting at reset.
func weekly(week float64, reset time.Time) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"status": "allowed",
		"unifiedWindows": map[string]any{
			"seven_day": map[string]any{"utilization": week, "resetsAt": reset.Unix(), "status": "allowed"},
		},
	})
	return b
}

// pacedTracker follows the pace with 2 tasks at once; its weekly window
// started at start (so it resets at start + 7 days).
func pacedTracker(t *testing.T, now *time.Time) (*Tracker, time.Time) {
	t.Helper()
	tr := New(nil, 0.9)
	tr.now = func() time.Time { return *now }
	tr.SetParallel(2)
	if err := tr.SetPace(PaceSettings{Follow: true}); err != nil {
		t.Fatal(err)
	}
	return tr, now.Add(WeekSpan / 2) // half the window elapsed
}

func TestPaceGap(t *testing.T) {
	reset := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		util float64
		now  time.Time
		want time.Duration
	}{
		{0.5, reset.Add(-WeekSpan / 2), 0},                         // halfway, half used
		{0.6, reset.Add(-WeekSpan / 2), WeekSpan / 10},             // 10 points ahead: 16h48m
		{0.4, reset.Add(-WeekSpan / 2), -WeekSpan / 10},            // 10 points behind
		{0.1, reset.Add(-WeekSpan), WeekSpan / 10},                 // window just started
		{0.1, reset.Add(-2 * WeekSpan), WeekSpan / 10},             // before the window: elapsed clamps to 0
		{1, reset.Add(time.Hour), 0},                               // after the reset: elapsed clamps to 1
		{0.25, reset.Add(-WeekSpan + 42*time.Hour), 0},             // a quarter in, a quarter used
		{0.25, reset.Add(-WeekSpan + 40*time.Hour), 2 * time.Hour}, // two hours early
	} {
		got := PaceGap(c.util, reset, c.now, WeekSpan)
		if d := got - c.want; d < -time.Second || d > time.Second {
			t.Errorf("PaceGap(%v, now=%v) = %v, want %v", c.util, c.now, got, c.want)
		}
	}
}

func TestDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "<1m", 30 * time.Second: "<1m", 45 * time.Minute: "45m", 2*time.Hour + 15*time.Minute: "2h 15m",
		3 * time.Hour: "3h", 78*time.Hour + 59*time.Minute: "3d 6h", 48 * time.Hour: "2d", -time.Hour: "<1m",
	} {
		if got := Duration(d); got != want {
			t.Errorf("Duration(%v) = %q, want %q", d, got, want)
		}
	}
}

// Ahead of the line, no new task starts, the reason says how long until the
// line catches up, and the pause lifts then.
func TestPaceAheadWaits(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	tr, reset := pacedTracker(t, &now)
	// Half the window elapsed; 1h20m of window time ahead.
	tr.Observe(weekly(0.5+float64(80*time.Minute+30*time.Second)/float64(WeekSpan), reset))
	ok, why := tr.CanStart()
	if ok || why != "Waiting 1h 20m to stay on pace" {
		t.Fatalf("ahead should wait, got ok=%v why=%q", ok, why)
	}
	s := tr.Snapshot()
	if s.Pace.State != PaceWaiting || s.Pace.Slots != 2 || !s.Pacing {
		t.Fatalf("pace = %+v", s.Pace)
	}
	if want := now.Add(80 * time.Minute); s.Until.Sub(want).Abs() > time.Minute {
		t.Fatalf("until = %v, want about %v", s.Until, want)
	}
	now = now.Add(50 * time.Minute)
	if _, why := tr.CanStart(); why != "Waiting 30m to stay on pace" {
		t.Fatalf("the wait should shrink as the line moves, got %q", why)
	}
	now = now.Add(31 * time.Minute)
	if ok, why := tr.CanStart(); !ok {
		t.Fatalf("the line has caught up, got %q", why)
	}
}

// Within the dead band, the queue runs at its usual parallelism.
func TestPaceDeadBand(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	tr, reset := pacedTracker(t, &now)
	for _, gap := range []time.Duration{0, PaceDeadBand - time.Minute, -PaceDeadBand + time.Minute} {
		tr.Observe(weekly(0.5+float64(gap)/float64(WeekSpan), reset))
		s := tr.Snapshot()
		if s.Paused || s.Pace.State != PaceOnPace || s.Pace.Slots != 2 {
			t.Fatalf("gap %v: paused=%v pace=%+v", gap, s.Paused, s.Pace)
		}
	}
}

// Behind the line, more tasks run at once the further behind, up to the cap.
func TestPaceBehindScalesParallel(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	tr, reset := pacedTracker(t, &now)
	for _, c := range []struct {
		behind time.Duration
		slots  int
	}{
		{10 * time.Minute, 3},            // past the dead band: one more
		{PaceDeadBand + PaceSlotStep, 3}, // a full step: still one more
		{PaceDeadBand + PaceSlotStep + time.Minute, 4},
		{48 * time.Hour, 4}, // capped at twice the usual
	} {
		tr.Observe(weekly(0.5-float64(c.behind)/float64(WeekSpan), reset))
		s := tr.Snapshot()
		if s.Paused || s.Pace.Slots != c.slots || s.Pace.State != PaceCatching {
			t.Fatalf("%v behind: paused=%v pace=%+v, want %d slots", c.behind, s.Paused, s.Pace, c.slots)
		}
	}
	if err := tr.SetPace(PaceSettings{Follow: true, MaxParallel: 10}); err != nil {
		t.Fatal(err)
	}
	if s := tr.Snapshot(); s.Pace.Slots != 10 {
		t.Fatalf("48h behind with a cap of 10: %+v", s.Pace)
	}
}

// One sample just past a threshold doesn't flip the queue back and forth.
func TestPaceHysteresis(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	tr, reset := pacedTracker(t, &now)
	at := func(gap time.Duration) Snapshot {
		tr.Observe(weekly(0.5+float64(gap)/float64(WeekSpan), reset))
		return tr.Snapshot()
	}
	if s := at(PaceDeadBand + time.Minute); s.Pace.State != PaceWaiting {
		t.Fatalf("past the dead band should wait: %+v", s.Pace)
	}
	// Back inside the dead band but still ahead: keeps waiting until the line catches up.
	if s := at(PaceDeadBand / 2); s.Pace.State != PaceWaiting || !s.Paused {
		t.Fatalf("still ahead should keep waiting: %+v", s.Pace)
	}
	if s := at(0); s.Pace.State != PaceOnPace || s.Paused {
		t.Fatalf("on the line should run: %+v", s.Pace)
	}
	// Ahead again but within the dead band: runs.
	if s := at(PaceDeadBand / 2); s.Pace.State != PaceOnPace {
		t.Fatalf("within the dead band from on pace should run: %+v", s.Pace)
	}

	// Catching up: a slot earned at a threshold is kept until the lag is
	// PaceSlotHold below it.
	step2 := PaceDeadBand + PaceSlotStep // more than this behind earns a second slot
	if s := at(-(step2 + time.Minute)); s.Pace.Slots != 4 {
		t.Fatalf("two slots more: %+v", s.Pace)
	}
	if s := at(-(step2 - PaceSlotHold/2)); s.Pace.Slots != 4 {
		t.Fatalf("just under the threshold should keep the slot: %+v", s.Pace)
	}
	if s := at(-(step2 - PaceSlotHold - time.Minute)); s.Pace.Slots != 3 {
		t.Fatalf("well under the threshold should give it up: %+v", s.Pace)
	}
	if s := at(-(PaceDeadBand - time.Minute)); s.Pace.Slots != 3 {
		t.Fatalf("inside the dead band, still catching up until the line: %+v", s.Pace)
	}
	if s := at(0); s.Pace.Slots != 2 || s.Pace.State != PaceOnPace {
		t.Fatalf("back on pace: %+v", s.Pace)
	}
}

// With the setting off (the default), pacing changes nothing.
func TestPaceOffIsUnchanged(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	tr := New(nil, 0.9)
	tr.now = func() time.Time { return now }
	tr.SetParallel(2)
	reset := now.Add(WeekSpan / 2)
	for _, week := range []float64{0.1, 0.5, 0.8} { // far behind, on pace, far ahead
		tr.Observe(weekly(week, reset))
		s := tr.Snapshot()
		if s.Paused || s.Reason != "" || s.Pace.State != PaceOff || s.Pace.Slots != 2 {
			t.Fatalf("week %.0f%%: paused=%v reason=%q pace=%+v", week*100, s.Paused, s.Reason, s.Pace)
		}
	}
	// PaceAt still pauses as before.
	tr.Observe(weekly(0.95, reset))
	if ok, why := tr.CanStart(); ok || why != "Weekly usage at 95%" {
		t.Fatalf("pause-at: ok=%v why=%q", ok, why)
	}
}

// The hard stops and a manual pause win over pacing.
func TestPaceHardStopsWin(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	tr, _ := pacedTracker(t, &now)
	// 5-hour window over pause-at; weekly far behind.
	tr.Observe(info("allowed", 0.95, 0.1, now.Add(time.Hour))) // week resets in 97h: ~42% elapsed
	if ok, why := tr.CanStart(); ok || why != "5-hour usage at 95%" {
		t.Fatalf("the 5-hour window should still stop: ok=%v why=%q", ok, why)
	}
	tr.Observe(info("allowed", 0.1, 0.1, now.Add(time.Hour)))
	if s := tr.Snapshot(); s.Paused || s.Pace.State != PaceCatching {
		t.Fatalf("behind should run: %+v", s)
	}
	tr.SetManualPause(true)
	if ok, why := tr.CanStart(); ok || why != "Paused by you" {
		t.Fatalf("manual pause should win: %v %q", ok, why)
	}
}

// A runner's snapshot reevaluated later (on the hub) keeps its pace state.
func TestPaceReevaluateKeepsHysteresis(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	tr, reset := pacedTracker(t, &now)
	tr.Observe(weekly(0.5+float64(PaceDeadBand+time.Minute)/float64(WeekSpan), reset))
	s := tr.Snapshot()
	// 3 minutes later the gap is under the dead band but still > 0: keep waiting.
	if r := s.Reevaluate(now.Add(3 * time.Minute)); !r.Paused || r.Pace.State != PaceWaiting {
		t.Fatalf("reevaluated: %+v", r)
	}
	if r := s.Reevaluate(now.Add(10 * time.Minute)); r.Paused {
		t.Fatalf("after the line passes: %+v", r)
	}
}

func TestPaceSettingsAreKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	tr := New(nil, 0.9)
	if err := tr.Keep(path); err != nil {
		t.Fatal(err)
	}
	if err := tr.SetPace(PaceSettings{Follow: true, MaxParallel: 6}); err != nil {
		t.Fatal(err)
	}
	if err := tr.SetPace(PaceSettings{MaxParallel: -1}); err == nil {
		t.Fatal("a negative cap should be refused")
	}
	again := New(nil, 0.9)
	if err := again.Keep(path); err != nil {
		t.Fatal(err)
	}
	if p := again.Snapshot().Pace; !p.Follow || p.MaxParallel != 6 {
		t.Fatalf("restored %+v", p)
	}
}
