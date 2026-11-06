package usage

import (
	"encoding/json"
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
	tr := New(nil, 0.9)
	tr.now = func() time.Time { return now }

	if ok, _ := tr.CanStart(); !ok {
		t.Fatal("unknown usage should not block")
	}
	tr.Observe(info("allowed", 0.5, 0.2, now.Add(time.Hour)))
	if ok, _ := tr.CanStart(); !ok {
		t.Fatal("50% should not block")
	}
	tr.Observe(info("allowed", 0.93, 0.2, now.Add(time.Hour)))
	if ok, why := tr.CanStart(); ok || why != "5-hour usage at 93%" {
		t.Fatalf("93%% should block, got ok=%v why=%q", ok, why)
	}
	if s := tr.Snapshot(); !s.Until.Equal(now.Add(time.Hour).Truncate(time.Second)) {
		t.Fatalf("pause should lift at reset, got %v", s.Until)
	}
	now = now.Add(2 * time.Hour) // window reset: stale numbers are ignored
	if ok, _ := tr.CanStart(); !ok {
		t.Fatal("after reset the queue should run again")
	}
	tr.Observe(info("rejected", 1, 0.2, now.Add(time.Hour)))
	if ok, why := tr.CanStart(); ok || why != "5-hour limit reached" {
		t.Fatalf("limited should block, got %v %q", ok, why)
	}
}

func TestManualPause(t *testing.T) {
	tr := New(nil, 0.9)
	tr.SetManualPause(true)
	if ok, why := tr.CanStart(); ok || why != "Paused by you" {
		t.Fatalf("manual pause: %v %q", ok, why)
	}
	tr.SetManualPause(false)
	if ok, _ := tr.CanStart(); !ok {
		t.Fatal("resume should unblock")
	}
}
