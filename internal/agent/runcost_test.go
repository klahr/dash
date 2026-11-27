package agent

import (
	"math"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// A message streams as several events with its usage so far: the latest
// counts, once; messages without an id count each.
func TestStreamCostCountsEachMessageOnce(t *testing.T) {
	var c streamCost
	m := "claude-sonnet-4-6" // $3 in, $15 out per million
	c.add("msg1", m, model.ChatUsage{InputTokens: 1_000_000, OutputTokens: 10})
	got := c.add("msg1", m, model.ChatUsage{InputTokens: 1_000_000, OutputTokens: 100_000})
	if math.Abs(got-(3+1.5)) > 1e-9 {
		t.Fatalf("one message, streamed twice: %v", got)
	}
	c.add("msg2", m, model.ChatUsage{OutputTokens: 100_000})
	got = c.add("", m, model.ChatUsage{OutputTokens: 100_000})
	if math.Abs(got-(3+1.5+1.5+1.5)) > 1e-9 {
		t.Fatalf("three messages: %v", got)
	}
	if u := c.usage(); u.InputTokens != 1_000_000 || u.OutputTokens != 300_000 {
		t.Errorf("usage %+v", u)
	}
}

func TestStreamEventReadsMessageUsage(t *testing.T) {
	ev, ok := parseStreamLine(`{"type":"assistant","message":{"id":"msg_1","model":"claude-opus-5-5","usage":{"input_tokens":5,"output_tokens":7,"cache_read_input_tokens":11},"content":[]}}`)
	if !ok || ev.Message.ID != "msg_1" || ev.Message.Model != "claude-opus-5-5" || ev.Message.Usage == nil || ev.Message.Usage.chatUsage().CacheReadTokens != 11 {
		t.Fatalf("parsed %+v", ev.Message)
	}
}
