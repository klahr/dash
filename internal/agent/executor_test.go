package agent

import (
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

func streamEvents(t *testing.T, line string) []model.RunEvent {
	t.Helper()
	ev, ok := parseStreamLine(line)
	if !ok {
		t.Fatalf("not a stream event: %s", line)
	}
	return events(ev)
}

func TestEventsKeepToolCallsAndResults(t *testing.T) {
	long := strings.Repeat("x", 500)
	evs := streamEvents(t, `{"type":"assistant","message":{"content":[`+
		`{"type":"text","text":"Let me look."},`+
		`{"type":"tool_use","id":"tu1","name":"Write","input":{"file_path":"a.go","content":"`+long+`"}}]}}`)
	if len(evs) != 2 {
		t.Fatalf("events: %+v", evs)
	}
	if evs[0].Kind != "text" || evs[0].Text != "Let me look." {
		t.Errorf("text: %+v", evs[0])
	}
	if e := evs[1]; e.Kind != "tool_use" || e.ID != "tu1" || e.Tool != "Write" || !strings.Contains(string(e.Input), long) {
		t.Errorf("tool_use should keep the full input: %+v", e)
	}

	evs = streamEvents(t, `{"type":"user","message":{"content":[`+
		`{"type":"tool_result","tool_use_id":"tu1","content":[{"type":"text","text":"ok"}],"is_error":true}]}}`)
	if len(evs) != 1 || evs[0].Kind != "tool_result" || evs[0].ID != "tu1" || evs[0].Text != "ok" || !evs[0].IsError {
		t.Errorf("tool_result: %+v", evs)
	}

	evs = streamEvents(t, `{"type":"result","subtype":"success","result":"Done.","num_turns":3,"total_cost_usd":0.5}`)
	if len(evs) != 1 || evs[0].Kind != "result" || evs[0].Text != "Done." || evs[0].Turns != 3 || evs[0].CostUSD != 0.5 || evs[0].IsError {
		t.Errorf("result: %+v", evs)
	}
	evs = streamEvents(t, `{"type":"result","subtype":"error_max_turns","result":"out of turns"}`)
	if len(evs) != 1 || !evs[0].IsError || evs[0].Text != "error_max_turns: out of turns" {
		t.Errorf("error result: %+v", evs)
	}

	if evs := streamEvents(t, `{"type":"stream_event"}`); len(evs) != 0 {
		t.Errorf("stream_event: %+v", evs)
	}
}

func TestSummary(t *testing.T) {
	for _, c := range []struct {
		ev   model.RunEvent
		want string
	}{
		{model.RunEvent{Kind: "status", Text: "▶ reusing worktree"}, "▶ reusing worktree"},
		{model.RunEvent{Kind: "text", Text: "Hello"}, "Hello"},
		{model.RunEvent{Kind: "tool_use", Tool: "Bash", Input: []byte(`{"command":"ls"}`)}, `🔧 Bash {"command":"ls"}`},
		{model.RunEvent{Kind: "tool_use", Tool: "Write", Input: []byte(strings.Repeat("y", 300))}, "🔧 Write " + strings.Repeat("y", 200) + "…"},
		{model.RunEvent{Kind: "tool_result", Text: "lots of output"}, ""},
		{model.RunEvent{Kind: "result", Text: "Done.", Turns: 3, CostUSD: 0.5}, "✔ Claude finished (3 turns, ≈$0.50)\nDone."},
		{model.RunEvent{Kind: "result", Text: "error_max_turns: out", IsError: true}, "✖ error_max_turns: out"},
		{model.RunEvent{Kind: "run_start", Text: "Rework"}, ""},
		{model.RunEvent{Kind: "prompt", Text: "Fix it"}, ""},
	} {
		if got := Summary(c.ev); got != c.want {
			t.Errorf("Summary(%+v) = %q, want %q", c.ev, got, c.want)
		}
	}
}
