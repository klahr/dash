package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/testbin"
)

// fakeGemini is the Gemini profile on a fake agent that plays
// testdata/acp/<script>.jsonl, and the file the agent logs what it got to.
// Each test gets a profile of its own name, so the abilities the agent
// reports (lastCaps) don't leak between tests.
func fakeGemini(t *testing.T, script string) (ACP, string) {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", "acp", script+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "agent.log")
	p := *Gemini
	p.Name = "gemini-" + t.Name()
	lastCaps.Delete(p.Name)
	p.Bin = os.Args[0]
	p.Args = []string{"-fake-acp", path, log}
	return ACP{Profile: &p, CLI: &acpChecker{Profile: &p}}, log
}

// agentGot is what the fake agent got, in order.
func agentGot(t *testing.T, log string) []acpMessage {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var out []acpMessage
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.HasPrefix(l, "UNEXPECTED") || strings.HasPrefix(l, "BAD") {
			t.Fatalf("fake agent: %s\n%s", l, b)
		}
		var m acpMessage
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("agent got %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func methods(ms []acpMessage) []string {
	var out []string
	for _, m := range ms {
		if m.Method != "" {
			out = append(out, m.Method)
		} else {
			out = append(out, "answer "+string(m.ID))
		}
	}
	return out
}

func got(ms []acpMessage, method string) acpMessage {
	for _, m := range ms {
		if m.Method == method {
			return m
		}
	}
	return acpMessage{}
}

func answerTo(ms []acpMessage, id string) string {
	for _, m := range ms {
		if m.Method == "" && string(m.ID) == id {
			return string(m.Result) + string(func() []byte { b, _ := json.Marshal(m.Error); return b }())
		}
	}
	return ""
}

// recorder keeps a run's events.
type recorder struct {
	mu     sync.Mutex
	events []model.RunEvent
	on     func(model.RunEvent)
}

func (r *recorder) emit(e model.RunEvent) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
	if r.on != nil {
		r.on(e)
	}
}

func (r *recorder) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		if s := Summary(e); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func TestACPRunGemini(t *testing.T) {
	a, log := fakeGemini(t, "gemini-task")
	dir := t.TempDir()

	// Dash's real permission server: the run asks it, a person answers.
	b := perm.NewBroker(nil)
	srv := httptest.NewServer(http.HandlerFunc(b.ServeMCP))
	defer srv.Close()
	token, release := b.Register(model.Owner{User: 1, Org: 1}, "t1", "p1")
	defer release()
	asked := make(chan perm.Request, 4)
	go func() {
		seen := map[string]bool{}
		for i := 0; i < 400; i++ {
			for _, r := range b.Pending("p1") {
				if seen[r.ID] {
					continue
				}
				seen[r.ID] = true
				asked <- r
				d := "deny"
				if strings.Contains(string(r.Input), "ls -la") {
					d = "allow"
				}
				_ = b.Decide(r.ID, d, "Not that.")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	var costs []float64
	var rec recorder
	res, err := a.Run(context.Background(), ExecRequest{
		Dir: dir, Prompt: "List the files", Model: "gemini-3-pro", AppendSystem: "Use run_tests.",
		MCPServers: map[string]any{
			perm.ServerName:     perm.ServerConfig(srv.URL, token),
			mcptools.ServerName: mcptools.ServerConfig("http://127.0.0.1:1", "board-token"),
		},
		AllowMCPTools:  map[string][]string{mcptools.ServerName: {"run_tests"}},
		PermissionTool: MCPTool{Server: perm.ServerName, Tool: perm.ToolName},
		OnCost:         func(usd float64) { costs = append(costs, usd) },
	}, rec.emit)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, strings.Join(rec.lines(), "\n"))
	}
	if res.SessionID != "sess-1" || res.Result != "Done." || res.CostUSD != 0.42 || res.Estimated || res.Stop != "success" || res.ResumeFailed {
		t.Errorf("result = %+v", res)
	}
	if !slices.Equal(costs, []float64{0.42}) {
		t.Errorf("OnCost got %v", costs)
	}

	want := []string{
		"▶ Gemini CLI session started",
		"I'll list the files.", // the chunks joined into one message
		"🔧 Bash ls -la",
		"🔧 mcp__dash_board__run_tests {}",
		"🔧 Edit Writing to a.go",
		"🔧 Bash rm -rf build",
		"⛔ Bash: Not that.",
		`🔧 TodoWrite {"todos":[{"content":"List files","status":"completed"},{"content":"Write a.go","status":"in_progress"}]}`,
		"Done.",
		"✔ Finished\nDone.",
	}
	if lines := rec.lines(); !slices.Equal(lines, want) {
		t.Errorf("log =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	var results []model.RunEvent
	for _, e := range rec.events {
		if e.Kind == "tool_result" {
			results = append(results, e)
		}
	}
	if len(results) != 5 || results[0].Text != "a.go\nb.go" || results[2].Text != "edited /w/a.go" || !results[3].IsError {
		t.Errorf("tool results = %+v", results)
	}

	// Only the shell commands were asked about: run_tests is allowed.
	close(asked)
	var tools []string
	for r := range asked {
		tools = append(tools, r.Tool)
	}
	if !slices.Equal(tools, []string{"Bash", "Bash"}) {
		t.Errorf("asked about %v", tools)
	}

	ms := agentGot(t, log)
	if m := methods(ms); !slices.Equal(m, []string{"initialize", "session/new", "session/set_mode", "session/set_model", "session/prompt",
		"answer 100", "answer 101", "answer 102", "answer 103"}) {
		t.Errorf("agent got %v", m)
	}
	var init struct {
		ProtocolVersion    int `json:"protocolVersion"`
		ClientCapabilities struct {
			FS       map[string]bool `json:"fs"`
			Terminal bool            `json:"terminal"`
		} `json:"clientCapabilities"`
	}
	_ = json.Unmarshal(got(ms, "initialize").Params, &init)
	if init.ProtocolVersion != 1 || init.ClientCapabilities.FS["readTextFile"] || init.ClientCapabilities.FS["writeTextFile"] || init.ClientCapabilities.Terminal {
		t.Errorf("initialize = %s", got(ms, "initialize").Params)
	}
	var sess struct {
		Cwd        string         `json:"cwd"`
		MCPServers []acpMCPServer `json:"mcpServers"`
	}
	_ = json.Unmarshal(got(ms, "session/new").Params, &sess)
	if sess.Cwd != dir || len(sess.MCPServers) != 1 {
		t.Fatalf("session/new = %s", got(ms, "session/new").Params)
	}
	if s := sess.MCPServers[0]; s.Name != mcptools.ServerName || s.Type != "http" ||
		!slices.Equal(s.Headers, []acpEnvVar{{"Authorization", "Bearer board-token"}}) {
		t.Errorf("MCP server = %+v (the permission server stays with Dash)", s)
	}
	if p := string(got(ms, "session/set_mode").Params); !strings.Contains(p, `"modeId":"autoEdit"`) {
		t.Errorf("set_mode = %s", p)
	}
	if p := string(got(ms, "session/set_model").Params); !strings.Contains(p, `"modelId":"gemini-3-pro"`) {
		t.Errorf("set_model = %s", p)
	}
	var prompt struct {
		Prompt []struct{ Text string } `json:"prompt"`
	}
	_ = json.Unmarshal(got(ms, "session/prompt").Params, &prompt)
	if len(prompt.Prompt) != 1 || !strings.HasPrefix(prompt.Prompt[0].Text, "<system-instructions>\nUse run_tests.\n</system-instructions>") ||
		!strings.HasSuffix(prompt.Prompt[0].Text, "List the files") {
		t.Errorf("prompt = %+v", prompt)
	}
	for id, want := range map[string]string{"100": `"optionId":"proceed_once"`, "101": `"optionId":"proceed_once"`,
		"102": `"optionId":"cancel"`, "103": `"code":-32601`} {
		if a := answerTo(ms, id); !strings.Contains(a, want) {
			t.Errorf("answer to %s = %s, want %s", id, a, want)
		}
	}
	if st := a.CLI.CLIStatus(); st.LoggedOut {
		t.Errorf("status = %+v", st)
	}
	if c := a.Caps(); !c.Known || c.Version != "0.71.1" || !c.Load || c.Resume || c.Fork || !c.HTTP {
		t.Errorf("caps = %+v", c)
	}
}

func TestACPRunWithoutPermissionTool(t *testing.T) {
	// No one to ask (an unattended run): every request is refused but for
	// a tool the run allows by name.
	a, log := fakeGemini(t, "gemini-task")
	var rec recorder
	_, err := a.Run(context.Background(), ExecRequest{Dir: t.TempDir(), Prompt: "x", Model: "gemini-3-pro",
		AllowedTools: []string{"Bash"}, MCPServers: map[string]any{}}, rec.emit)
	if err != nil {
		t.Fatal(err)
	}
	ms := agentGot(t, log)
	for _, id := range []string{"100", "102"} {
		if a := answerTo(ms, id); !strings.Contains(a, "proceed_once") {
			t.Errorf("answer to %s = %s", id, a)
		}
	}
	if a := answerTo(ms, "101"); !strings.Contains(a, "cancel") {
		t.Errorf("run_tests without AllowMCPTools: answer = %s", a)
	}
}

func TestACPResumeLoad(t *testing.T) {
	a, log := fakeGemini(t, "gemini-load")
	var rec recorder
	res, err := a.Run(context.Background(), ExecRequest{Dir: t.TempDir(), Prompt: "Go on", Resume: "sess-old", AppendSystem: "sys"}, rec.emit)
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionID != "sess-old" || res.Result != "New answer" {
		t.Errorf("result = %+v", res)
	}
	if lines := strings.Join(rec.lines(), "\n"); strings.Contains(lines, "Old") || !strings.Contains(lines, "▶ Gemini CLI session continued") {
		t.Errorf("the loaded history is logged again:\n%s", lines)
	}
	ms := agentGot(t, log)
	if m := methods(ms); !slices.Equal(m, []string{"initialize", "session/load", "session/prompt"}) {
		t.Errorf("agent got %v (autoEdit is set already)", m)
	}
	if p := string(got(ms, "session/load").Params); !strings.Contains(p, `"sessionId":"sess-old"`) {
		t.Errorf("load = %s", p)
	}
	if p := string(got(ms, "session/prompt").Params); strings.Contains(p, "sys") {
		t.Errorf("a resumed session got the system text again: %s", p)
	}
}

func TestACPResumeFailed(t *testing.T) {
	a, _ := fakeGemini(t, "gemini-load-missing")
	res, err := a.Run(context.Background(), ExecRequest{Dir: t.TempDir(), Prompt: "x", Resume: "gone"}, func(model.RunEvent) {})
	if err == nil || !res.ResumeFailed {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if !strings.Contains(err.Error(), "No previous sessions found") {
		t.Errorf("err = %v", err)
	}
}

func TestACPForkWithoutFork(t *testing.T) {
	a, log := fakeGemini(t, "gemini-load")
	var init acpInit
	init.AgentCapabilities.LoadSession = true
	a.CLI.initialized(init)
	res, err := a.Run(context.Background(), ExecRequest{Dir: t.TempDir(), Prompt: "x", Resume: "parent", ForkSession: true}, func(model.RunEvent) {})
	if err == nil || !res.ResumeFailed {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if _, err := os.Stat(log); err == nil {
		t.Error("the agent was started: loading the parent's session would change it")
	}
}

func TestACPSignedOut(t *testing.T) {
	a, _ := fakeGemini(t, "gemini-signed-out")
	a.Profile.KeyEnv = "DASH_TEST_NO_SUCH_KEY"
	_, err := a.Run(context.Background(), ExecRequest{Dir: t.TempDir(), Prompt: "x"}, func(model.RunEvent) {})
	if err == nil || !strings.Contains(err.Error(), "isn't signed in") {
		t.Fatalf("err = %v", err)
	}
	if !a.CLI.CLIStatus().LoggedOut {
		t.Error("the checker doesn't know it is signed out")
	}
}

func TestACPSignInWithKey(t *testing.T) {
	a, log := fakeGemini(t, "gemini-api-key")
	a.Profile.KeyEnv = "DASH_TEST_ACP_KEY"
	t.Setenv("DASH_TEST_ACP_KEY", "k-123")
	res, err := a.Run(context.Background(), ExecRequest{Dir: t.TempDir(), Prompt: "x"}, func(model.RunEvent) {})
	if err != nil || res.Result != "Hi" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	ms := agentGot(t, log)
	if p := string(got(ms, "authenticate").Params); !strings.Contains(p, `"methodId":"gemini-api-key"`) || !strings.Contains(p, `"api-key":"k-123"`) {
		t.Errorf("authenticate = %s", p)
	}
}

func TestACPShared(t *testing.T) {
	a, log := fakeGemini(t, "gemini-check")
	a.Profile.KeyEnv = "DASH_TEST_ACP_SHARED_KEY"
	_, err := a.Run(context.Background(), ExecRequest{Dir: t.TempDir(), Prompt: "x", Shared: t.TempDir()}, func(model.RunEvent) {})
	if err == nil || !strings.Contains(err.Error(), "DASH_TEST_ACP_SHARED_KEY") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(log); err == nil {
		t.Error("the agent was started without the runner's key")
	}
}

func TestACPCancel(t *testing.T) {
	old := acpGrace
	acpGrace = 2 * time.Second
	defer func() { acpGrace = old }()
	a, log := fakeGemini(t, "gemini-cancel")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := recorder{on: func(e model.RunEvent) {
		if e.Kind == "tool_use" {
			cancel()
		}
	}}
	_, err := a.Run(ctx, ExecRequest{Dir: t.TempDir(), Prompt: "x"}, rec.emit)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if m := methods(agentGot(t, log)); !slices.Contains(m, "session/cancel") {
		t.Errorf("agent got %v", m)
	}
}

func TestACPRunInSandbox(t *testing.T) {
	a, log := fakeGemini(t, "gemini-hello")
	dir := t.TempDir()
	wrap := filepath.Join(dir, "wrap")
	testbin.Write(t, wrap, "#!/bin/sh\necho \"$DASH_WORKTREE\" > \""+dir+"/wrapped\"\nexec \"$@\"\n")
	a.Sandbox = []string{wrap}
	res, err := a.Run(context.Background(), ExecRequest{Dir: dir, Prompt: "x"}, func(model.RunEvent) {})
	if err != nil || res.Result != "Hi" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "wrapped"))
	if strings.TrimSpace(string(b)) != dir {
		t.Errorf("sandbox got DASH_WORKTREE %q", b)
	}
	if m := methods(agentGot(t, log)); !slices.Contains(m, "session/prompt") {
		t.Errorf("agent got %v", m)
	}
}

func TestACPChecker(t *testing.T) {
	a, _ := fakeGemini(t, "gemini-check")
	if f := Features(a); slices.Contains(f, model.FeatureResume) || slices.Contains(f, model.FeatureMCP) {
		t.Errorf("features before a check = %v", f)
	}
	st := a.CLI.Check(context.Background())
	if st.Version != "0.71.1" || st.LoggedOut || len(st.Warnings) > 0 || st.CheckedAt.IsZero() {
		t.Errorf("status = %+v", st)
	}
	if f := Features(a); !slices.Equal(f, []string{model.FeatureResume, model.FeatureMCP, model.FeaturePermissionHook}) {
		t.Errorf("features = %v", f)
	}
	// A provider built afresh knows them too.
	fresh := ACP{Profile: a.Profile}
	if !fresh.CanResume() || !fresh.CanMCP() {
		t.Error("a fresh provider doesn't know the agent's abilities")
	}

	out, _ := fakeGemini(t, "gemini-signed-out")
	out.Profile.KeyEnv = "DASH_TEST_NO_SUCH_KEY"
	if st := out.CLI.Check(context.Background()); st.Version != "0.71.1" || !st.LoggedOut {
		t.Errorf("signed out: status = %+v", st)
	}
	if s := StatusOf(out); !s.Installed || s.LoggedIn {
		t.Errorf("signed out: agent status = %+v", s)
	}
	// With the API key set, a run signs in with it.
	withKey, _ := fakeGemini(t, "gemini-signed-out")
	withKey.Profile.KeyEnv = "DASH_TEST_ACP_CHECK_KEY"
	t.Setenv("DASH_TEST_ACP_CHECK_KEY", "k")
	if st := withKey.CLI.Check(context.Background()); st.LoggedOut {
		t.Errorf("with a key: status = %+v", st)
	}

	// A session that fails for another reason: its sign-in is unknown,
	// which keeps its label, and the agent's own name is kept.
	odd := &ACP{Profile: &ACPProfile{Name: "odd", Title: "Odd"}}
	odd.CLI = &acpChecker{Profile: odd.Profile, check: func(context.Context) (acpInit, error, error) {
		var i acpInit
		i.AgentInfo = &struct {
			Name    string `json:"name"`
			Title   string `json:"title"`
			Version string `json:"version"`
		}{Name: "odd-agent", Title: "Odd Agent", Version: "1.2.3"}
		return i, nil, errors.New("internal error")
	}}
	if st := odd.CLI.Check(context.Background()); st.LoggedOut || !st.SignInUnknown || st.Title != "Odd Agent" {
		t.Errorf("unknown: status = %+v", st)
	}
	if s := StatusOf(odd); !s.Installed || !s.LoggedIn || !s.Ready() || s.SignIn != model.SignInUnknown || s.Title != "Odd Agent" || s.Version != "1.2.3" {
		t.Errorf("unknown: agent status = %+v", s)
	}
	// Then a run finds it signed out.
	odd.CLI.signedOut()
	if s := StatusOf(odd); s.LoggedIn || s.SignIn != model.SignedOut {
		t.Errorf("signed out after unknown: agent status = %+v", s)
	}

	missing := &acpChecker{Profile: &ACPProfile{Name: "x", Title: "X", Bin: filepath.Join(t.TempDir(), "nope"), Install: "install it"}}
	if st := missing.Check(context.Background()); st.Version != "" || len(st.Warnings) != 1 {
		t.Errorf("missing: status = %+v", st)
	}
}

func TestACPChat(t *testing.T) {
	a, log := fakeGemini(t, "gemini-chat")
	var mu sync.Mutex
	var ds []ChatDelta
	err := a.Chat(ChatConfig{}).Stream(context.Background(), t.TempDir(), []ChatMessage{{Role: "user", Content: "Can you edit?"}}, func(d ChatDelta) {
		mu.Lock()
		ds = append(ds, d)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var kinds []string
	for _, d := range ds {
		kinds = append(kinds, d.Kind)
		if d.Kind == "text" {
			text.WriteString(d.Text)
		}
	}
	if text.String() != "It only reads." || kinds[0] != "cwd" || kinds[len(kinds)-1] != "done" || !slices.Contains(kinds, "tool_use") {
		t.Errorf("deltas = %v %q", kinds, text.String())
	}
	if u := ds[len(ds)-2]; u.Kind != "usage" || u.Usage.InputTokens != 100 || u.Usage.OutputTokens != 20 {
		t.Errorf("usage = %+v", u)
	}
	ms := agentGot(t, log)
	if a := answerTo(ms, "200"); !strings.Contains(a, "cancel") {
		t.Errorf("the chat allowed an edit: %s", a)
	}
	if p := string(got(ms, "session/prompt").Params); !strings.Contains(p, "Can you edit?") || !strings.Contains(p, "system-instructions") {
		t.Errorf("prompt = %s", p)
	}
	if !a.Chat(ChatConfig{}).(ACPChat).ReadOnly() {
		t.Error("not read-only")
	}
}

func TestACPCaps(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "acp", "opencode-init.json"))
	if err != nil {
		t.Fatal(err)
	}
	var i acpInit
	if err := json.Unmarshal(b, &i); err != nil {
		t.Fatal(err)
	}
	if c := i.caps(); !c.Known || c.Version != "1.2.20" || !c.Load || !c.Resume || !c.Fork || !c.HTTP {
		t.Errorf("caps = %+v", c)
	}
}

func TestACPTranslate(t *testing.T) {
	str := func(s string) *string { return &s }
	tr := acpTranslator{Profile: Gemini}
	var out []ChatDelta
	for _, u := range []acpUpdate{
		{SessionUpdate: "agent_message_chunk", Content: json.RawMessage(`{"type":"text","text":"Hel"}`)},
		{SessionUpdate: "agent_message_chunk", Content: json.RawMessage(`{"type":"text","text":"lo"}`)},
		// An update before its call: the call is made from it.
		{SessionUpdate: "tool_call_update", ToolCallID: "c1", Kind: str("read"), Status: str("completed"),
			Locations: []acpLocation{{Path: "/w/x.go"}}, RawOutput: json.RawMessage(`{"output":"package x"}`)},
		{SessionUpdate: "tool_call_update", ToolCallID: "c1", Status: str("completed")}, // no second result
		{SessionUpdate: "tool_call", ToolCallID: "c2", Title: str("go test"), Kind: str("execute"), RawInput: json.RawMessage(`{"command":"go test ./..."}`)},
		{SessionUpdate: "something_new"},
		{SessionUpdate: "something_new"},
		{SessionUpdate: "usage_update", Cost: &acpCost{Amount: 1, Currency: "EUR"}},
		{SessionUpdate: "agent_message_chunk", Content: json.RawMessage(`{"type":"text","text":"Bye"}`)},
	} {
		out = append(out, tr.Translate(u)...)
	}
	out = append(out, tr.Flush()...)
	var lines []string
	for _, d := range out {
		e, _ := runEvent(d)
		lines = append(lines, d.Kind+": "+Summary(e))
	}
	want := []string{
		"text: Hello",
		"tool_use: 🔧 Read /w/x.go",
		"tool_result: ",
		"tool_use: $ go test ./...",
		`status: ⚠ Gemini CLI: Dash doesn't know the session update "something_new"`,
		"text: Bye",
	}
	if !slices.Equal(lines, want) {
		t.Errorf("deltas =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if out[2].Text != "package x" || tr.Last != "Bye" || tr.HasCost {
		t.Errorf("result %q, last %q, cost %v (only USD counts)", out[2].Text, tr.Last, tr.HasCost)
	}
}

func TestACPToolLine(t *testing.T) {
	for _, c := range []struct{ tool, input, want string }{
		{"Bash", `{"title":"ls","kind":"execute"}`, "🔧 Bash ls"},
		{"Edit", `{"kind":"edit","locations":[{"path":"/w/a.go"}]}`, "🔧 Edit /w/a.go"},
		{"Other", `{"kind":"other"}`, "🔧 Other other"},
		{"Bash", `{"command":"go vet","title":"Vet"}`, "$ go vet"},
	} {
		if got := toolLine(c.tool, json.RawMessage(c.input)); got != c.want {
			t.Errorf("toolLine(%s, %s) = %q, want %q", c.tool, c.input, got, c.want)
		}
	}
}

func TestACPOutcome(t *testing.T) {
	opts := []acpPermOption{{"a", "Always", "allow_always"}, {"r", "Never", "reject_always"}}
	if b, _ := json.Marshal(acpOutcome(opts, true)); string(b) != `{"outcome":{"optionId":"a","outcome":"selected"}}` {
		t.Errorf("allow = %s", b)
	}
	if b, _ := json.Marshal(acpOutcome(opts, false)); string(b) != `{"outcome":{"optionId":"r","outcome":"selected"}}` {
		t.Errorf("reject = %s", b)
	}
	if b, _ := json.Marshal(acpOutcome(nil, true)); string(b) != `{"outcome":{"outcome":"cancelled"}}` {
		t.Errorf("no options = %s", b)
	}
}

func TestACPToolName(t *testing.T) {
	p := &ACPProfile{Tools: map[string]string{"execute": "Shell"}}
	for kind, want := range map[string]string{"execute": "Shell", "read": "Read", "edit": "Edit", "delete": "Edit",
		"fetch": "WebFetch", "search": "Grep", "other": "Other", "": "Other"} {
		if got := p.ToolName(kind); got != want {
			t.Errorf("ToolName(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestGeminiProvider(t *testing.T) {
	p, err := NewProvider(GeminiProvider, Config{Bin: "/opt/gemini", Args: []string{"--debug"}})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := p.(*ACP)
	if !ok || a.DisplayName() != "Gemini CLI" || a.bin() != "/opt/gemini" {
		t.Fatalf("provider = %#v", p)
	}
	if CanRuleTools(p) {
		t.Error("an ACP agent can't follow tool rules")
	}
	if _, ok := p.(RateLimitReporter); ok {
		t.Error("an ACP agent doesn't report rate limits")
	}
	if _, err := p.Prompt(context.Background(), PromptRequest{Input: "x"}); err == nil {
		t.Error("Prompt answered")
	}
	args := a.RunArgs(ExecRequest{AddDirs: []string{"/att"}})
	if !slices.Equal(args, []string{"--acp", "--debug", "--include-directories", "/att"}) {
		t.Errorf("args = %v", args)
	}
	if args := a.RunArgs(ExecRequest{Unattended: true}); !slices.Equal(args, []string{"--acp"}) {
		t.Errorf("unattended args = %v", args)
	}
}

func TestSetPrompterSkipsACP(t *testing.T) {
	gem := &ACP{Profile: Gemini, CLI: &acpChecker{Profile: Gemini}}
	gem.CLI.cli.Set("0.71.1", nil, func() bool { gem.CLI.checked = time.Now(); return true })
	s := &Set{list: []Provider{gem}}
	if p := (setPrompter{s}).pick(); p.Name() != DefaultProvider {
		t.Errorf("titles go to %s", p.Name())
	}
}
