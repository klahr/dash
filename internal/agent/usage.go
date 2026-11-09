package agent

import (
	"time"

	"github.com/klahr/dash/internal/model"
)

// thinkClock adds up the wall-clock time spent inside thinking blocks.
type thinkClock struct {
	now   func() time.Time
	start time.Time
	total time.Duration
}

func newThinkClock() *thinkClock { return &thinkClock{now: time.Now} }

// begin marks a thinking block as started; a block already running keeps its start.
func (t *thinkClock) begin() {
	if t.start.IsZero() {
		t.start = t.now()
	}
}

// end closes the running thinking block, if any.
func (t *thinkClock) end() {
	if !t.start.IsZero() {
		t.total += t.now().Sub(t.start)
		t.start = time.Time{}
	}
}

func (t *thinkClock) ms() int64 {
	t.end()
	return t.total.Milliseconds()
}

// usageDelta is the "usage" delta sent just before "done".
func usageDelta(u model.ChatUsage) ChatDelta {
	return ChatDelta{Kind: "usage", Usage: &u}
}
