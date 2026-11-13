package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/claudecli"
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
		{model.RunEvent{Kind: "tool_use", Tool: "Bash", Input: []byte(`{"command":"ls"}`)}, "$ ls"},
		{model.RunEvent{Kind: "tool_use", Tool: "Bash", Input: []byte(`{"command":"sed -n 1,5p a.go\nls","description":"x"}`)}, "$ sed -n 1,5p a.go …"},
		{model.RunEvent{Kind: "tool_use", Tool: "Read", Input: []byte(`{"file_path":"/x/a.go","limit":5}`)}, "🔧 Read /x/a.go"},
		{model.RunEvent{Kind: "tool_use", Tool: "TodoWrite", Input: []byte(`{}`)}, "🔧 TodoWrite"},
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

// fakeClaude is a `claude` that prints out and exits 0.
func fakeClaude(t *testing.T, out string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "out.jsonl"), []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat >/dev/null\ncat '"+dir+"/out.jsonl'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func runFake(t *testing.T, cli *claudecli.Monitor, out string) []string {
	t.Helper()
	var warns []string
	c := ClaudeCode{Bin: fakeClaude(t, out), CLI: cli}
	_, err := c.Run(context.Background(), ExecRequest{Dir: t.TempDir(), Prompt: "hi"}, func(e model.RunEvent) {
		if strings.HasPrefix(e.Text, "⚠ Claude Code CLI: ") {
			warns = append(warns, e.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return warns
}

func TestRunLogsClaudeCodeOutputChanges(t *testing.T) {
	sample, err := os.ReadFile("../claudecli/testdata/2.1.319-tool.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	cli := &claudecli.Monitor{}
	if w := runFake(t, cli, string(sample)); w != nil {
		t.Fatalf("recorded sample: %v", w)
	}

	// A newer Claude Code that renamed the cost and dropped the init event.
	changed := `{"type":"assistant","message":{"content":[{"type":"text","text":"Done."}]}}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"session_id":"s1","num_turns":1,"cost":{"usd":0.1},"result":"Done."}` + "\n"
	w := runFake(t, cli, changed)
	if len(w) != 2 || !strings.Contains(w[0], "total_cost_usd is missing") || !strings.Contains(w[1], "no system event") {
		t.Fatalf("task log warnings: %v", w)
	}
	if got := cli.Warnings(); len(got) != 2 {
		t.Fatalf("the Runners page should hear of them: %v", got)
	}
	// The next run starts its log with them, once.
	if w := runFake(t, cli, string(sample)); len(w) != 2 {
		t.Fatalf("next run: %v", w)
	}
}

func TestRunForksAResumedSession(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\ncat >/dev/null\necho \"$@\" >'" + dir + "/args'\n" +
		`echo '{"type":"result","subtype":"success","is_error":false,"session_id":"new","num_turns":1,"total_cost_usd":0,"result":"ok"}'` + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	args := func(req ExecRequest) string {
		t.Helper()
		req.Dir, req.Prompt = t.TempDir(), "hi"
		res, err := ClaudeCode{Bin: bin}.Run(context.Background(), req, func(model.RunEvent) {})
		if err != nil || res.SessionID != "new" {
			t.Fatalf("run: %v, session %q", err, res.SessionID)
		}
		b, err := os.ReadFile(filepath.Join(dir, "args"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if a := args(ExecRequest{Resume: "old", ForkSession: true}); !strings.Contains(a, "--resume old --fork-session") {
		t.Fatalf("a fork resumes the old session as a new one: %s", a)
	}
	if a := args(ExecRequest{Resume: "old"}); !strings.Contains(a, "--resume old") || strings.Contains(a, "--fork-session") {
		t.Fatalf("a plain resume continues the session itself: %s", a)
	}
	if a := args(ExecRequest{ForkSession: true}); strings.Contains(a, "--fork-session") || strings.Contains(a, "--resume") {
		t.Fatalf("nothing to fork without a session: %s", a)
	}
}

func TestFollowUpPromptClosesTheEarlierTask(t *testing.T) {
	child := model.Todo{Title: "Next", Prompt: "do the next thing"}
	merged := FollowUpPrompt(child, model.Todo{Title: "First", Status: model.StatusMerged})
	if !strings.Contains(merged, `"First"`) || !strings.Contains(merged, "and merged") || !strings.Contains(merged, "new, separate follow-up task") ||
		!strings.HasSuffix(merged, BuildPrompt(child)) {
		t.Fatalf("merged parent:\n%s", merged)
	}
	failed := FollowUpPrompt(child, model.Todo{Prompt: "look into it", Status: model.StatusFailed})
	if !strings.Contains(failed, `"look into it"`) || !strings.Contains(failed, "not merged") {
		t.Fatalf("unmerged parent:\n%s", failed)
	}
}

// TestRunReadsUsage: the run's tokens come from the result event's usage
// block and its model from the init event.
func TestRunReadsUsage(t *testing.T) {
	sample, err := os.ReadFile("../claudecli/testdata/2.1.319-tool.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	c := ClaudeCode{Bin: fakeClaude(t, string(sample))}
	res, err := c.Run(context.Background(), ExecRequest{Dir: t.TempDir(), Prompt: "hi"}, func(model.RunEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	want := model.ChatUsage{InputTokens: 4, OutputTokens: 82, CacheReadTokens: 6148, CacheCreationTokens: 2905}
	got := res.Usage
	got.DurationMS = 0
	if got != want {
		t.Errorf("usage = %+v, want %+v", got, want)
	}
	if res.Usage.DurationMS == 0 {
		t.Error("want the run's duration from the result event")
	}
	if res.Model != "claude-haiku-5-5" {
		t.Errorf("model = %q", res.Model)
	}
}
