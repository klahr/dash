package queue

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/klahr/dash/internal/usage"
)

// Following the weekly pace changes the engine's effective parallelism,
// never MaxParallel; off, it is MaxParallel.
func TestEffectiveParallelFollowsPace(t *testing.T) {
	tr := usage.New(nil, 0.9)
	e := NewTodoEngine(&TodoEngine{Usage: tr, MaxParallel: 2})
	// Half the week elapsed, a fifth used: far behind.
	raw, _ := json.Marshal(map[string]any{"status": "allowed", "unifiedWindows": map[string]any{
		"seven_day": map[string]any{"utilization": 0.2, "resetsAt": time.Now().Add(usage.WeekSpan / 2).Unix(), "status": "allowed"},
	}})
	tr.Observe(raw)
	if n := e.EffectiveParallel(); n != 2 {
		t.Fatalf("off: %d, want 2", n)
	}
	if err := tr.SetPace(usage.PaceSettings{Follow: true}); err != nil {
		t.Fatal(err)
	}
	if n := e.EffectiveParallel(); n != 4 || e.MaxParallel != 2 {
		t.Fatalf("behind: effective %d (want 4), MaxParallel %d", n, e.MaxParallel)
	}
	if err := tr.SetPace(usage.PaceSettings{Follow: true, MaxParallel: 3}); err != nil {
		t.Fatal(err)
	}
	if n := e.EffectiveParallel(); n != 3 {
		t.Fatalf("capped: %d, want 3", n)
	}
}
