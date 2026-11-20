package model

import (
	"testing"
	"time"
)

// Todos created at the same instant (a coarse clock, as on Windows) must
// still come in one order, or listings flip between calls.
func TestQueueBeforeBreaksTiesByID(t *testing.T) {
	at := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	one := 1
	a := Todo{ID: "a", CreatedAt: at}
	b := Todo{ID: "b", CreatedAt: at}
	if !QueueBefore(a, b) || QueueBefore(b, a) {
		t.Error("equal created_at: want a before b by id")
	}
	pa, pb := a, b
	pa.Priority, pb.Priority = &one, &one
	if !QueueBefore(pa, pb) || QueueBefore(pb, pa) {
		t.Error("equal priority and created_at: want a before b by id")
	}
	// The id only breaks ties: an older todo still goes first.
	old := Todo{ID: "z", CreatedAt: at.Add(-time.Second)}
	if !QueueBefore(old, a) {
		t.Error("older todo should go first whatever its id")
	}
}
