package usage

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func info(status string, five, week float64, reset time.Time) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"status": status, "rateLimitType": "five_hour", "resetsAt": reset.Unix(),
		"unifiedWindows": map[string]any{
			"five_hour": map[string]any{"utilization": five, "resetsAt": reset.Unix(), "status": status},
			"seven_day": map[string]any{"utilization": week, "resetsAt": reset.Add(96 * time.Hour).Unix(), "status": "allowed"},
		},
	})
	return b
}

func TestPauseRules(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	tr := New(nil)
	tr.now = func() time.Time { return now }

	if ok, _ := tr.CanStart(); !ok {
		t.Fatal("unknown usage should not block")
	}
	tr.Observe(info("allowed", 0.5, 0.2, now.Add(time.Hour)))
	if ok, _ := tr.CanStart(); !ok {
		t.Fatal("50% should not block")
	}
	tr.Observe(info("allowed", 0.99, 0.2, now.Add(time.Hour)))
	if ok, why := tr.CanStart(); !ok {
		t.Fatalf("99%% should not block, only a limit does, got %q", why)
	}
	tr.Observe(info("rejected", 1, 0.2, now.Add(time.Hour)))
	if ok, why := tr.CanStart(); ok || why != "5-hour limit reached" {
		t.Fatalf("limited should block, got %v %q", ok, why)
	}
	if s := tr.Snapshot(); !s.Until.Equal(now.Add(time.Hour).Truncate(time.Second)) {
		t.Fatalf("pause should lift at reset, got %v", s.Until)
	}
	now = now.Add(2 * time.Hour) // window reset: stale numbers are ignored
	if ok, _ := tr.CanStart(); !ok {
		t.Fatal("after reset the queue should run again")
	}
}

// allowed_warning is Claude Code's "getting close", not a limit: it doesn't
// pause, whatever the utilization.
func TestWarningIsNotALimit(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	tr := New(nil)
	tr.now = func() time.Time { return now }
	b, _ := json.Marshal(map[string]any{
		"status": "allowed_warning", "rateLimitType": "seven_day", "resetsAt": now.Add(48 * time.Hour).Unix(),
		"unifiedWindows": map[string]any{
			"five_hour": map[string]any{"utilization": 0.1, "resetsAt": now.Add(time.Hour).Unix(), "status": "allowed"},
			"seven_day": map[string]any{"utilization": 0.76, "resetsAt": now.Add(48 * time.Hour).Unix(), "status": "allowed_warning"},
		},
	})
	tr.Observe(b)
	if ok, why := tr.CanStart(); !ok {
		t.Fatalf("a weekly warning at 76%% should not block, got %q", why)
	}
	// The older single-window form, with its utilization.
	tr = New(nil)
	tr.now = func() time.Time { return now }
	tr.Observe(json.RawMessage(`{"status":"allowed_warning","rateLimitType":"seven_day","utilization":0.95,"resetsAt":` +
		fmt.Sprint(now.Add(48*time.Hour).Unix()) + `}`))
	if ok, why := tr.CanStart(); !ok {
		t.Fatalf("a weekly warning at 95%% should not block, got %q", why)
	}
}

func TestManualPause(t *testing.T) {
	tr := New(nil)
	tr.SetManualPause(true)
	if ok, why := tr.CanStart(); ok || why != "Paused by you" {
		t.Fatalf("manual pause: %v %q", ok, why)
	}
	tr.SetManualPause(false)
	if ok, _ := tr.CanStart(); !ok {
		t.Fatal("resume should unblock")
	}
}

// A warning is not a limit: Claude Code still runs, the queue keeps going.
func TestWarningStatusDoesNotBlock(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	tr := New(nil)
	tr.now = func() time.Time { return now }
	tr.Observe(json.RawMessage(`{"status":"allowed_warning","rateLimitType":"seven_day","resetsAt":` +
		fmt.Sprint(now.Add(48*time.Hour).Unix()) + `}`))
	if ok, why := tr.CanStart(); !ok {
		t.Fatalf("allowed_warning should not block, got %q", why)
	}
}

// A snapshot reported earlier is worked out again once its window resets.
func TestReevaluateStaleSnapshot(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	tr := New(nil)
	tr.now = func() time.Time { return now }
	tr.Observe(info("allowed", 0.2, 0.2, now.Add(time.Hour)))
	tr.Observe(json.RawMessage(`{"status":"rejected","rateLimitType":"seven_day","resetsAt":` +
		fmt.Sprint(now.Add(time.Hour).Unix()) + `}`))
	s := tr.Snapshot()
	if !s.Paused || s.Reason != "Weekly limit reached" {
		t.Fatalf("want weekly limit, got %v %q", s.Paused, s.Reason)
	}
	if r := s.Reevaluate(now.Add(2 * time.Hour)); r.Paused || r.Reason != "" || !r.Until.IsZero() {
		t.Fatalf("after reset: %v %q %v", r.Paused, r.Reason, r.Until)
	}
	s.ManualPause, s.Reason = true, "Paused by you"
	if r := s.Reevaluate(now.Add(2 * time.Hour)); !r.Paused || r.Reason != "Paused by you" {
		t.Fatalf("manual pause kept: %v %q", r.Paused, r.Reason)
	}
}
