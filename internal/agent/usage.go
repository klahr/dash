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

// cliUsage is the usage block of a Claude Code result event.
type cliUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// chatUsage is u as Dash keeps it; nil u is no tokens.
func (u *cliUsage) chatUsage() model.ChatUsage {
	if u == nil {
		return model.ChatUsage{}
	}
	return model.ChatUsage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		CacheReadTokens: u.CacheReadInputTokens, CacheCreationTokens: u.CacheCreationInputTokens}
}
