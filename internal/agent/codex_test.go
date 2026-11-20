package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/testbin"
)

// codexFixture is the events of testdata/codex/name.jsonl, translated.
func codexFixture(t *testing.T, name string) (*codexTranslator, []ChatDelta) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "codex", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := &codexTranslator{}
	var out []ChatDelta
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		ev, ok := parseCodexLine(sc.Text())
		if !ok {
			continue
		}
		out = append(out, tr.Translate(ev)...)
	}
	return tr, out
}

// short is a delta as "kind tool id text", for comparing.
func short(d ChatDelta) string {
	parts := []string{d.Kind}
	for _, p := range []string{d.Tool, d.ID, strings.TrimSpace(d.Text)} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " ")
}

func TestCodexTranslateTask(t *testing.T) {
	tr, out := codexFixture(t, "task.jsonl")
	var got []string
	for _, d := range out {
		got = append(got, short(d))
	}
	want := []string{
		"status ▶ Codex session started",
		"thinking **Looking at the tests**\n\nI'll run them first.",
		"tool_use TodoWrite item_1#plan1",
		"tool_result item_1#plan1",
		"tool_use Bash item_2",
		"tool_result item_2 --- FAIL: TestParse\nFAIL",
		"tool_use Edit item_3#0",
		"tool_result item_3#0 update",
		"tool_use Write item_3#1",
		"tool_result item_3#1 add",
		"tool_use TodoWrite item_1#plan2",
		"tool_result item_1#plan2",
		"tool_use mcp__dash_board__run_tests item_4",
		"tool_result item_4 tests passed",
		"tool_use WebSearch item_5",
		"tool_result item_5",
		"text Fixed the parser; the tests pass.",
	}
	if !slices.Equal(got, want) {
		t.Errorf("deltas:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Tool calls look like Claude Code's, so the log and chat show them alike.
	for _, d := range out {
		switch d.ID {
		case "item_2":
			if d.Kind == "tool_use" && string(d.Input) != `{"command":"bash -lc 'go test ./...'"}` {
				t.Errorf("Bash input %s", d.Input)
			}
			if d.Kind == "tool_result" && !d.IsError {
				t.Error("a command that exited 1 isn't an error")
			}
			if d.Kind == "tool_use" && toolLine(d.Tool, d.Input) != "$ bash -lc 'go test ./...'" {
				t.Errorf("log line %q", toolLine(d.Tool, d.Input))
			}
		case "item_3#0":
			if d.Kind == "tool_use" && string(d.Input) != `{"file_path":"/work/parse.go"}` {
				t.Errorf("Edit input %s", d.Input)
			}
		case "item_1#plan2":
			if d.Kind == "tool_use" && !strings.Contains(string(d.Input), `"status":"completed"`) {
				t.Errorf("plan %s", d.Input)
			}
		}
	}
	if tr.Thread != "0199a213-81c0-7800-8aa1-bbab2a035a53" || tr.Last != "Fixed the parser; the tests pass." || tr.Turns != 1 || tr.Failure != "" {
		t.Errorf("translator %+v", tr)
	}
	// Input without the cached tokens, as Claude Code counts it.
	if u := tr.Usage; u.InputTokens != 315 || u.CacheReadTokens != 24448 || u.OutputTokens != 122 {
		t.Errorf("usage %+v", u)
	}
	// A task log leaves the thinking out, as Claude's runs do.
	var kinds []string
	for _, d := range out {
		if e, ok := runEvent(d); ok {
			kinds = append(kinds, e.Kind)
		}
	}
	if slices.Contains(kinds, "thinking") || len(kinds) != len(out)-1 {
		t.Errorf("run events %v", kinds)
	}
}

func TestCodexTranslateFailed(t *testing.T) {
	tr, out := codexFixture(t, "failed.jsonl")
	if tr.Failure != "stream disconnected before completion" || !slices.Equal(tr.Errors, []string{"Reconnecting... 1/5"}) {
		t.Errorf("translator %+v", tr)
	}
	var res *ChatDelta
	for i := range out {
		if out[i].Kind == "tool_result" {
			res = &out[i]
		}
	}
	if res == nil || !res.IsError || res.Text != "tool call declined" {
		t.Errorf("declined MCP call: %+v", res)
	}
}

func TestCodexTranslateUnknown(t *testing.T) {
	tr, out := codexFixture(t, "unknown.jsonl")
	var warns []string
	for _, d := range out {
		if d.Kind == codexStatus && strings.HasPrefix(d.Text, codexWarnPrefix) {
			warns = append(warns, d.Text)
		}
	}
	// Once per type, not per event.
	if len(warns) != 2 || !strings.Contains(warns[0], `event type "session.configured"`) || !strings.Contains(warns[1], `item type "hologram"`) {
		t.Errorf("warnings %q", warns)
	}
	if tr.Last != "Done." {
		t.Errorf("last %q", tr.Last)
	}
	if _, ok := parseCodexLine("not json at all"); ok {
		t.Error("parsed a line that isn't JSON")
	}
}

// fakeCodex is a `codex` that saves its arguments, stdin and environment
// in dir and prints the fixture name (if any), then exits with code.
func fakeCodex(t *testing.T, fixture string, code int, stderr string) (bin, dir string) {
	t.Helper()
	testbin.SkipOnWindows(t, "runs a #!/bin/sh stand-in for codex")
	dir = t.TempDir()
	out := "/dev/null"
	if fixture != "" {
		var err error
		if out, err = filepath.Abs(filepath.Join("testdata", "codex", fixture)); err != nil {
			t.Fatal(err)
		}
	}
	bin = filepath.Join(dir, "codex")
	script := "#!/bin/sh\n" +
		`for a in "$@"; do printf '%s\n' "$a"; done > "` + dir + `/args"` + "\n" +
		`cat > "` + dir + `/stdin"` + "\n" +
		`env > "` + dir + `/env"` + "\n" +
		`cat "` + out + `"` + "\n"
	if stderr != "" {
		script += "echo '" + stderr + "' >&2\n"
	}
	script += "exit " + string(rune('0'+code)) + "\n"
	testbin.Write(t, bin, script)
	return bin, dir
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func TestCodexRun(t *testing.T) {
	bin, dir := fakeCodex(t, "task.jsonl", 0, "")
	c := Codex{Bin: bin}
	var evs []model.RunEvent
	req := ExecRequest{Dir: t.TempDir(), Prompt: "fix the parser", Model: "opus",
		MCPServers:    map[string]any{mcptools.ServerName: mcptools.ServerConfig("http://127.0.0.1:1", "s3cret")},
		AllowMCPTools: map[string][]string{mcptools.ServerName: {"run_tests"}}}
	res, err := c.Run(context.Background(), req, func(e model.RunEvent) { evs = append(evs, e) })
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionID != "0199a213-81c0-7800-8aa1-bbab2a035a53" || res.Result != "Fixed the parser; the tests pass." || res.Stop != "success" ||
		res.Usage.CacheReadTokens != 24448 || res.Model != "" || res.ResumeFailed {
		t.Errorf("result %+v", res)
	}
	last := evs[len(evs)-1]
	if last.Kind != "result" || last.IsError || last.Text != res.Result || Summary(last) != "✔ Finished\n"+res.Result {
		t.Errorf("last event %+v", last)
	}
	if evs[0].Kind != "status" || !strings.Contains(evs[0].Text, "Claude model") {
		t.Errorf("a Claude model isn't said to be left out: %+v", evs[0])
	}
	if got := strings.Join(readLines(t, dir+"/stdin"), "\n"); got != "fix the parser" {
		t.Errorf("stdin %q", got)
	}
	args := readLines(t, dir+"/args")
	if args[0] != "exec" || !slices.Contains(args, "--json") || args[len(args)-1] != "-" || slices.Contains(args, "opus") {
		t.Errorf("args %q", args)
	}
	// The token stays off the command line, in the environment.
	if strings.Contains(strings.Join(args, " "), "s3cret") {
		t.Errorf("token on the command line: %q", args)
	}
	if !slices.Contains(readLines(t, dir+"/env"), "DASH_MCP_TOKEN_DASH_BOARD_0=Bearer s3cret") {
		t.Error("token not in the environment")
	}
}

func TestCodexRunFailed(t *testing.T) {
	bin, _ := fakeCodex(t, "failed.jsonl", 1, "")
	var evs []model.RunEvent
	res, err := Codex{Bin: bin}.Run(context.Background(), ExecRequest{Dir: t.TempDir(), Prompt: "x"}, func(e model.RunEvent) { evs = append(evs, e) })
	if err == nil || !strings.Contains(err.Error(), "stream disconnected") || res.Stop != "error" {
		t.Fatalf("err %v, res %+v", err, res)
	}
	if last := evs[len(evs)-1]; last.Kind != "result" || !last.IsError {
		t.Errorf("last event %+v", last)
	}
}

func TestCodexResumeFailed(t *testing.T) {
	// Codex stops before it opens a thread it doesn't have.
	bin, dir := fakeCodex(t, "", 1, "Error: thread not found")
	res, err := Codex{Bin: bin}.Run(context.Background(), ExecRequest{Dir: t.TempDir(), Prompt: "more", Resume: "old-thread", ForkSession: true}, func(model.RunEvent) {})
	if err == nil || !res.ResumeFailed || !strings.Contains(err.Error(), "thread not found") {
		t.Fatalf("err %v, res %+v", err, res)
	}
	args := readLines(t, dir+"/args")
	if !slices.Equal(args[len(args)-3:], []string{"fork", "old-thread", "-"}) {
		t.Errorf("args %q", args)
	}
	// A run that failed in its thread isn't a lost session.
	bin, _ = fakeCodex(t, "failed.jsonl", 1, "")
	res, _ = Codex{Bin: bin}.Run(context.Background(), ExecRequest{Dir: t.TempDir(), Prompt: "more", Resume: "old-thread"}, func(model.RunEvent) {})
	if res.ResumeFailed {
		t.Error("ResumeFailed after the thread started")
	}
}

func TestCodexMissing(t *testing.T) {
	_, err := Codex{Bin: filepath.Join(t.TempDir(), "no-codex")}.Run(context.Background(), ExecRequest{Dir: t.TempDir()}, func(model.RunEvent) {})
	if err == nil || !strings.Contains(err.Error(), "isn't installed") || !strings.Contains(err.Error(), "npm install -g @openai/codex") {
		t.Errorf("err %v", err)
	}
	// Signed out (as its checker found): a clear message, nothing run.
	bin, dir := fakeCodex(t, "task.jsonl", 0, "")
	cli := &codexChecker{Bin: bin}
	cli.cli.Set("0.50.0", nil, func() bool { cli.checked, cli.loggedOut = time.Now(), true; return true })
	_, err = Codex{Bin: bin, CLI: cli}.Run(context.Background(), ExecRequest{Dir: t.TempDir()}, func(model.RunEvent) {})
	if !errors.Is(err, errCodexLoggedOut) {
		t.Errorf("err %v", err)
	}
	if _, err := os.Stat(dir + "/args"); err == nil {
		t.Error("ran a CLI that isn't signed in")
	}
}

func TestCodexRunArgs(t *testing.T) {
	c := Codex{Args: []string{"-c", "x=1"}}
	args, env := c.RunArgs(ExecRequest{Model: "gpt-5.5", Effort: "high", AppendSystem: "Say \"hi\".\nThen stop.",
		MCPServers: map[string]any{
			"dash_board": mcptools.ServerConfig("http://h", "tok"),
			"dash_git":   map[string]any{"type": "http", "url": "http://g/mcp"},
		},
		AllowMCPTools: map[string][]string{"dash_board": {"run_tests", "get_log"}},
		ToolTimeout:   20 * time.Minute,
		Resume:        "th"})
	line := strings.Join(args, " ")
	for _, want := range []string{
		"exec --json --skip-git-repo-check --sandbox workspace-write -c sandbox_workspace_write.network_access=true -c x=1",
		`-c approval_policy="never"`,
		"--model gpt-5.5",
		`-c model_reasoning_effort="high"`,
		`-c developer_instructions="Say \"hi\".\nThen stop."`,
		`-c mcp_servers.dash_board.url="http://h/mcp/dash"`,
		`-c mcp_servers.dash_board.env_http_headers={"Authorization"="DASH_MCP_TOKEN_DASH_BOARD_0"}`,
		`-c mcp_servers.dash_board.enabled_tools=["run_tests","get_log"] -c mcp_servers.dash_board.default_tools_approval_mode="approve"`,
		`-c mcp_servers.dash_board.tool_timeout_sec=1200`,
		`-c mcp_servers.dash_git.url="http://g/mcp"`,
		"resume th -",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("args lack %s:\n%s", want, line)
		}
	}
	if strings.Contains(line, "dash_git.enabled_tools") || strings.Contains(line, "tok") {
		t.Errorf("args %s", line)
	}
	if !slices.Equal(env, []string{"DASH_MCP_TOKEN_DASH_BOARD_0=Bearer tok"}) {
		t.Errorf("env %q", env)
	}
	// Unattended: read-only, without the user's config or Args, no session saved.
	args, _ = c.RunArgs(ExecRequest{Unattended: true, Model: "claude-opus-5-5", ForkSession: true})
	line = strings.Join(args, " ")
	if !strings.Contains(line, "--ignore-user-config --ignore-rules --ephemeral --sandbox read-only") ||
		strings.Contains(line, "x=1") || strings.Contains(line, "--model") || strings.Contains(line, "fork") {
		t.Errorf("unattended args %s", line)
	}
}

// The operator's -codex-args come after Dash's sandbox defaults, so they
// win: network_access=false holds, and their --sandbox replaces Dash's.
func TestCodexRunArgsOperatorWins(t *testing.T) {
	off := "sandbox_workspace_write.network_access=false"
	args, _ := Codex{Args: []string{"-c", off}}.RunArgs(ExecRequest{})
	if last := slices.Index(args, off); last < 0 || slices.Index(args, "sandbox_workspace_write.network_access=true") > last {
		t.Errorf("network_access=false doesn't win: %q", args)
	}
	for _, a := range [][]string{{"--sandbox", "read-only"}, {"-s", "read-only"}, {"--sandbox=read-only"}} {
		args, _ = Codex{Args: a}.RunArgs(ExecRequest{})
		if line := strings.Join(args, " "); strings.Contains(line, "workspace-write --") || strings.Count(line, "read-only") != 1 {
			t.Errorf("Args %q: %s", a, line)
		}
	}
}

func TestCodexModel(t *testing.T) {
	for in, want := range map[string]string{"": "", "opus": "", "Sonnet": "", "claude-opus-5-5": "", "opus[1m]": "", "default": "",
		"gpt-5.5": "gpt-5.5", "o4-mini": "o4-mini", " gpt-5.5-codex ": "gpt-5.5-codex"} {
		if got := codexModel(in); got != want {
			t.Errorf("codexModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCodexChat(t *testing.T) {
	bin, dir := fakeCodex(t, "task.jsonl", 0, "")
	work := t.TempDir()
	var got []string
	var usage *model.ChatUsage
	err := CodexChat{Bin: bin}.Stream(context.Background(), work, []ChatMessage{{Role: "user", Content: "why?"}}, func(d ChatDelta) {
		got = append(got, d.Kind)
		if d.Kind == "usage" {
			usage = d.Usage
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "cwd" || got[1] != "thinking" || got[len(got)-2] != "usage" || got[len(got)-1] != "done" || slices.Contains(got, codexStatus) {
		t.Errorf("deltas %v", got)
	}
	if usage == nil || usage.InputTokens != 315 || usage.OutputTokens != 122 {
		t.Errorf("usage %+v", usage)
	}
	// The chat only reads, and saves no session.
	line := strings.Join(readLines(t, dir+"/args"), " ")
	if !strings.Contains(line, "--ephemeral") || !strings.Contains(line, "--sandbox read-only") || strings.Contains(line, "workspace-write") {
		t.Errorf("chat args %s", line)
	}
	if !(CodexChat{}).ReadOnly() {
		t.Error("the Codex chat isn't read-only")
	}
	// A failed turn is the turn's error.
	bin, _ = fakeCodex(t, "failed.jsonl", 1, "")
	err = CodexChat{Bin: bin}.Stream(context.Background(), work, []ChatMessage{{Role: "user", Content: "x"}}, func(ChatDelta) {})
	if err == nil || !strings.Contains(err.Error(), "stream disconnected") {
		t.Errorf("err %v", err)
	}
}

func TestCodexPrompt(t *testing.T) {
	testbin.SkipOnWindows(t, "runs a #!/bin/sh stand-in for codex")
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	testbin.Write(t, bin, "#!/bin/sh\n"+`for a in "$@"; do printf '%s\n' "$a"; done > "`+dir+`/args"`+"\ncat > /dev/null\necho 'Fix the parser'\n")
	got, err := Codex{Bin: bin}.Prompt(context.Background(), PromptRequest{Model: "haiku", System: "Name the task.", Input: "the parser fails"})
	if err != nil || got != "Fix the parser" {
		t.Fatalf("%q, %v", got, err)
	}
	line := strings.Join(readLines(t, dir+"/args"), " ")
	// No tools: the input may be a task's text, which must not run commands
	// or read files.
	for _, want := range []string{"--ephemeral", "--ignore-user-config", "--sandbox read-only", `model_reasoning_effort="low"`, `developer_instructions="Name the task."`,
		"-c features.shell_tool=false", "-c features.unified_exec=false", "-c tools.view_image=false", `-c web_search="disabled"`} {
		if !strings.Contains(line, want) {
			t.Errorf("args lack %s: %s", want, line)
		}
	}
	if strings.Contains(line, "--json") || strings.Contains(line, "haiku") {
		t.Errorf("args %s", line)
	}
}

func TestCodexChecker(t *testing.T) {
	answers := map[string]string{
		"--version":    "codex-cli 0.50.0",
		"exec --help":  "Usage: codex exec [OPTIONS] [PROMPT] [COMMAND]\n  resume  …\n  --json\n  --skip-git-repo-check\n  -s, --sandbox\n  --ephemeral",
		"login status": "Not logged in",
	}
	c := &codexChecker{run: func(_ context.Context, _ string, args ...string) (string, error) {
		out := answers[strings.Join(args, " ")]
		if strings.HasPrefix(out, "Not") || out == "" {
			return out, errors.New("exit status 1")
		}
		return out, nil
	}}
	changes := 0
	c.OnChange(func() { changes++ })
	st := c.Check(context.Background())
	if st.Version != "0.50.0" || !st.LoggedOut || len(st.Warnings) != 0 || st.CheckedAt.IsZero() || changes != 1 {
		t.Errorf("status %+v (%d changes)", st, changes)
	}
	s := withCLIStatus(model.AgentStatus{Name: "codex"}, st)
	if !s.Installed || s.LoggedIn || s.Ready() {
		t.Errorf("agent status %+v", s)
	}
	answers["login status"] = "Logged in using ChatGPT"
	answers["exec --help"] = "Usage: codex exec --json"
	st = c.Check(context.Background())
	if st.LoggedOut || len(st.Warnings) != 4 || changes != 2 {
		t.Errorf("status %+v (%d changes)", st, changes)
	}
	// A run's warnings are kept until the version changes.
	c.observed([]string{"x"})
	if w := c.CLIStatus().Warnings; len(w) != 5 || w[4] != "x" {
		t.Errorf("warnings %q", w)
	}
	// Not installed.
	c = &codexChecker{run: func(context.Context, string, ...string) (string, error) {
		return "", errors.New(`exec: "codex": executable file not found in $PATH`)
	}}
	st = c.Check(context.Background())
	if st.Version != "" || len(st.Warnings) != 1 || !strings.Contains(st.Warnings[0], "npm install -g @openai/codex") {
		t.Errorf("not installed: %+v", st)
	}
	if s := withCLIStatus(model.AgentStatus{}, st); s.Installed || s.Ready() {
		t.Errorf("not installed: %+v", s)
	}
	var none *codexChecker
	none.observed([]string{"x"})
	if none.CLIStatus().Version != "" {
		t.Error("nil checker")
	}
}

func TestCodexLoggedOut(t *testing.T) {
	for _, x := range []struct {
		out  string
		err  error
		want bool
	}{
		{"Logged in using ChatGPT", nil, false},
		{"Logged in using an API key - sk-proj-***ABCD", nil, false},
		{"Not logged in", errors.New("exit status 1"), true},
		{"", errors.New("exit status 1"), true},
		{"something new", nil, false}, // not understood: not logged out
	} {
		if got := codexLoggedOut(x.out, x.err); got != x.want {
			t.Errorf("%q, %v: %v", x.out, x.err, got)
		}
	}
}

func TestCodexProvider(t *testing.T) {
	p, err := NewProvider(CodexProvider, Config{Bin: "/opt/codex"})
	if err != nil {
		t.Fatal(err)
	}
	x, ok := p.(*Codex)
	if !ok || x.Bin != "/opt/codex" || x.CLI == nil || x.CLI.Bin != "/opt/codex" || p.Name() != "codex" {
		t.Fatalf("provider %+v", p)
	}
	// Sessions and MCP servers; no permission prompts, tool rules or usage
	// reports, so Dash leaves those out for its projects.
	if !slices.Equal(Features(p), []string{model.FeatureResume, model.FeatureMCP}) || CanPromptPermissions(p) || CanRuleTools(p) || MonitorOf(p) != nil {
		t.Errorf("features %v", Features(p))
	}
	if _, ok := p.(RateLimitReporter); ok {
		t.Error("Codex reports rate limits")
	}
	if CheckerOf(p) == nil || CheckerOf(Codex{}) != nil {
		t.Error("checker")
	}
	if c, ok := p.Chat(ChatConfig{Dir: "/home", Tools: ChatShell}).(CodexChat); !ok || c.Bin != "/opt/codex" || !ReadOnlyChat(c) {
		t.Errorf("chat %+v", p.Chat(ChatConfig{}))
	}
}

// stubChecker is a CLIChecker that found st.
type stubChecker struct{ st CLIStatus }

func (stubChecker) Run(context.Context, time.Duration) {}
func (stubChecker) OnChange(func())                    {}
func (s stubChecker) CLIStatus() CLIStatus             { return s.st }

// checkedProvider is a provider called name whose CLI checker found st,
// and which answers prompts with its name.
type checkedProvider struct {
	bareProvider
	name string
	st   CLIStatus
}

func (p checkedProvider) Name() string                                          { return p.name }
func (p checkedProvider) CLIChecker() CLIChecker                                { return stubChecker{p.st} }
func (p checkedProvider) Prompt(context.Context, PromptRequest) (string, error) { return p.name, nil }

func TestSetPrompter(t *testing.T) {
	now := time.Now()
	ok := CLIStatus{Version: "1.0.0", CheckedAt: now}
	out := CLIStatus{Version: "1.0.0", CheckedAt: now, LoggedOut: true}
	missing := CLIStatus{CheckedAt: now}
	for _, x := range []struct {
		claude, codex CLIStatus
		want          string
	}{
		{ok, ok, "claude"},
		{CLIStatus{}, ok, "claude"}, // not checked yet: still the default
		{out, ok, "codex"},          // the default signed out: the one that isn't
		{missing, ok, "codex"},
		{missing, missing, "claude"}, // none ready: the default says what is wrong
	} {
		s := &Set{list: []Provider{checkedProvider{name: "claude", st: x.claude}, checkedProvider{name: "codex", st: x.codex}}}
		if got, _ := s.Prompter().Prompt(context.Background(), PromptRequest{}); got != x.want {
			t.Errorf("claude %+v, codex %+v: %s, want %s", x.claude, x.codex, got, x.want)
		}
	}
}

// The task log's events are JSON like Claude Code's: what the UI renders.
func TestCodexRunEventJSON(t *testing.T) {
	e, ok := runEvent(ChatDelta{Kind: "tool_use", ID: "item_2", Tool: "Bash", Input: json.RawMessage(`{"command":"ls"}`)})
	b, _ := json.Marshal(e)
	if !ok || string(b) != `{"kind":"tool_use","id":"item_2","tool":"Bash","input":{"command":"ls"},"at":"0001-01-01T00:00:00Z"}` {
		t.Errorf("%s", b)
	}
}
