package claudecli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The testdata samples were recorded from real Claude Code runs (version in
// the file name) the way Dash runs it: a task (-p --output-format
// stream-json --verbose), a task with a tool call, and a chat turn
// (--include-partial-messages). Paths, ids and the init event's long lists
// are trimmed. Record new ones when Claude Code changes its output and keep
// the old: Dash should read every version that's still about.
func samples(t *testing.T) map[string][]string {
	t.Helper()
	files, _ := filepath.Glob("testdata/*.jsonl")
	if len(files) == 0 {
		t.Fatal("no samples in testdata")
	}
	out := map[string][]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(f)] = strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	return out
}

func TestRecordedSamplesLookAsExpected(t *testing.T) {
	for name, lines := range samples(t) {
		var m Monitor
		s := m.Stream()
		for _, l := range lines {
			if ws := s.Line(l); ws != nil {
				t.Errorf("%s: %v", name, ws)
			}
		}
		if ws := s.End(); ws != nil {
			t.Errorf("%s end: %v", name, ws)
		}
		if ws := m.Warnings(); ws != nil {
			t.Errorf("%s: monitor kept %v", name, ws)
		}
	}
}

// eventOf is the first line of a sample with the given type.
func eventOf(t *testing.T, typ string) string {
	t.Helper()
	for _, l := range samples(t)["2.1.319-task.jsonl"] {
		var ev struct{ Type string }
		if json.Unmarshal([]byte(l), &ev) == nil && ev.Type == typ {
			return l
		}
	}
	t.Fatalf("no %s event in the samples", typ)
	return ""
}

func TestChangedEventsWarn(t *testing.T) {
	rate := eventOf(t, "rate_limit_event")
	result := eventOf(t, "result")
	initEv := eventOf(t, "system")
	cases := []struct {
		name, line, want string
	}{
		{"not json", "Claude Code 3 says hello", "isn't a JSON object"},
		{"no type", `{"kind":"result"}`, `without a "type"`},
		{"rate limit info renamed", strings.Replace(rate, `"rate_limit_info"`, `"rateLimit"`, 1), "rate_limit_info is missing"},
		{"windows renamed", strings.Replace(strings.Replace(rate, `"unifiedWindows"`, `"windows"`, 1), `"rateLimitType"`, `"type2"`, 1), "neither unifiedWindows nor rateLimitType"},
		{"utilization as text", strings.Replace(rate, `"utilization":0.5`, `"utilization":"50%"`, 1), "unifiedWindows.seven_day.utilization is a string, not a number"},
		{"cost renamed", strings.Replace(result, `"total_cost_usd"`, `"cost_usd"`, 1), "result: total_cost_usd is missing"},
		{"turns as text", strings.Replace(result, `"num_turns":1`, `"num_turns":"1"`, 1), "num_turns is a string, not a number"},
		{"init without session", strings.Replace(initEv, `"session_id"`, `"sessionId"`, 1), "system init: session_id is missing"},
		{"content not a list", `{"type":"assistant","message":{"content":"hi"}}`, "assistant: content is a string, not an array"},
		{"tool use without input", `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read"}]}}`, "input is missing"},
		{"delta text renamed", `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","content":"hi"}}}`, "text is missing"},
	}
	for _, c := range cases {
		ws := (&Monitor{}).Stream().Line(c.line)
		if len(ws) != 1 || !strings.Contains(ws[0], c.want) {
			t.Errorf("%s: got %v, want one containing %q", c.name, ws, c.want)
		}
	}
}

func TestIgnoresWhatDashDoesntRead(t *testing.T) {
	for _, l := range []string{
		`{"type":"tool_progress","whatever":1}`,
		`{"type":"system","subtype":"status","status":"thinking"}`,
		`{"type":"user","message":{"role":"user","content":"a plain prompt"}}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{}}}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","resetsAt":1}}`,
		"",
	} {
		if ws := (&Monitor{}).Stream().Line(l); ws != nil {
			t.Errorf("%s: %v", l, ws)
		}
	}
}

func TestEndWithoutResultOrInit(t *testing.T) {
	var m Monitor
	s := m.Stream()
	s.Line(`{"type":"assistant","message":{"content":[]}}`)
	ws := s.End()
	if len(ws) != 2 || !strings.Contains(ws[0], "no system event") || !strings.Contains(ws[1], "without a result") {
		t.Fatalf("got %v", ws)
	}
	if len((&Monitor{}).Stream().End()) != 0 {
		t.Error("a run that printed nothing should not warn here")
	}
}

func TestWarningsReportedOncePerRunAndKept(t *testing.T) {
	changes := 0
	m := &Monitor{}
	m.OnChange(func() { changes++ })
	bad := `{"type":"result","subtype":"success"}`
	for range 2 {
		s := m.Stream()
		if ws := s.Line(bad); len(ws) != 1 {
			t.Fatalf("each run reports it once: %v", ws)
		}
		if ws := s.Line(bad); ws != nil {
			t.Fatalf("repeated in the same run: %v", ws)
		}
	}
	if got := m.Warnings(); len(got) != 1 {
		t.Fatalf("monitor keeps it once: %v", got)
	}
	if changes != 1 {
		t.Fatalf("OnChange %d times", changes)
	}
	var nilM *Monitor
	if ws := nilM.Stream().Line(bad); len(ws) != 1 {
		t.Fatal("a nil monitor still checks")
	}
}

type fakeCLI struct {
	version, probe string
	versionErr     error
	probeErr       error
}

func (f fakeCLI) run(_ context.Context, _ string, args ...string) (string, error) {
	if args[0] == "--version" {
		return f.version, f.versionErr
	}
	return f.probe, f.probeErr
}

var errExit1 = errors.New("exit status 1")

func TestCheck(t *testing.T) {
	const noInput = "Error: Input must be provided either through stdin or as a prompt argument when using --print"
	cases := []struct {
		name    string
		cli     fakeCLI
		version string
		want    string // in the only warning; "" for none
	}{
		{"as expected", fakeCLI{version: "2.1.319 (Claude Code)", probe: noInput, probeErr: errExit1}, "2.1.319", ""},
		{"prompt tool gone", fakeCLI{version: "3.0.0 (Claude Code)", probe: "error: unknown option '--permission-prompt-tool'", probeErr: errExit1}, "3.0.0",
			"no longer accepts --permission-prompt-tool"},
		{"stream-json gone", fakeCLI{version: "3.0.0 (Claude Code)", probe: "error: option '--output-format <format>' argument 'stream-json' is invalid. Allowed choices are text, json.", probeErr: errExit1}, "3.0.0",
			"rejected a flag"},
		{"runs without input", fakeCLI{version: "3.0.0 (Claude Code)"}, "3.0.0", "without a prompt succeeded"},
		{"not installed", fakeCLI{versionErr: errors.New(`exec: "claude": executable file not found in $PATH`), probeErr: errExit1}, "", "Couldn't run"},
		{"odd version", fakeCLI{version: "Claude Code, the next generation", probe: noInput, probeErr: errExit1}, "", "not a version"},
	}
	for _, c := range cases {
		m := &Monitor{run: c.cli.run}
		st := m.Check(context.Background())
		if st.Version != c.version {
			t.Errorf("%s: version %q", c.name, st.Version)
		}
		switch {
		case c.want == "" && st.Warnings != nil:
			t.Errorf("%s: %v", c.name, st.Warnings)
		case c.want != "" && (len(st.Warnings) != 1 || !strings.Contains(st.Warnings[0], c.want)):
			t.Errorf("%s: got %v, want %q", c.name, st.Warnings, c.want)
		}
		if st.CheckedAt.IsZero() {
			t.Errorf("%s: CheckedAt not set", c.name)
		}
	}
}

func TestNewVersionForgetsOldRunWarnings(t *testing.T) {
	cli := fakeCLI{version: "2.1.0 (Claude Code)", probe: "no input", probeErr: errExit1}
	m := &Monitor{run: cli.run}
	m.Check(context.Background())
	m.Stream().Line(`{"type":"result"}`)
	if len(m.Warnings()) != 1 {
		t.Fatal("run warning not kept")
	}
	m.Check(context.Background()) // same version: kept
	if len(m.Warnings()) != 1 {
		t.Fatal("run warning dropped on a recheck")
	}
	cli.version = "2.2.0 (Claude Code)"
	m.run = cli.run
	if st := m.Check(context.Background()); st.Version != "2.2.0" || st.Warnings != nil {
		t.Fatalf("after update: %+v", st)
	}
}

func TestParseAuthStatus(t *testing.T) {
	a := ParseAuthStatus([]byte(`{"loggedIn":true,"authMethod":"claude.ai","email":"ann@example.com","orgId":"o-1","orgName":"Ann's","subscriptionType":"max"}`))
	if a == nil || *a != (Account{Email: "ann@example.com", OrgID: "o-1", OrgName: "Ann's", Subscription: "max"}) {
		t.Fatalf("logged in: %+v", a)
	}
	for _, out := range []string{`{"loggedIn":false}`, `{"loggedIn":true,"authMethod":"api_key"}`, `not json`, ``} {
		if a := ParseAuthStatus([]byte(out)); a != nil {
			t.Errorf("%q: %+v", out, a)
		}
	}
}

// The account is checked with the CLI, and again between checks, so a
// log-out or another account is noticed and reported.
func TestCheckReportsTheAccount(t *testing.T) {
	email := "ann@example.com"
	run := func(_ context.Context, _ string, args ...string) (string, error) {
		switch args[0] {
		case "--version":
			return "2.1.0 (Claude Code)", nil
		case "auth":
			if email == "" {
				return `{"loggedIn":false}`, errExit1
			}
			return `{"loggedIn":true,"email":"` + email + `","subscriptionType":"pro"}`, nil
		}
		return "Error: Input must be provided", errExit1
	}
	m := &Monitor{run: run}
	changes := 0
	m.OnChange(func() { changes++ })
	if st := m.Check(context.Background()); st.Account == nil || st.Account.Email != email || st.Account.Subscription != "pro" {
		t.Fatalf("check: %+v", st.Account)
	}
	email = "bo@example.com"
	if a := m.CheckAccount(context.Background()); a == nil || a.Email != email || m.Status().Account.Email != email {
		t.Fatalf("switched: %+v", a)
	}
	email = ""
	if a := m.CheckAccount(context.Background()); a != nil || m.Status().Account != nil {
		t.Fatalf("logged out: %+v", a)
	}
	m.CheckAccount(context.Background()) // no change
	if changes != 3 {
		t.Errorf("OnChange %d times, want 3", changes)
	}
}
