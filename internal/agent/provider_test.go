package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/testbin"
)

// bareProvider is a provider with none of the optional abilities: it
// records the prompts it is asked and answers with reply.
type bareProvider struct {
	reply string
	asked *[]PromptRequest
}

func (bareProvider) Name() string { return "bare" }

func (bareProvider) Run(context.Context, ExecRequest, func(model.RunEvent)) (ExecResult, error) {
	return ExecResult{Stop: "success"}, nil
}

func (p bareProvider) Prompt(_ context.Context, req PromptRequest) (string, error) {
	*p.asked = append(*p.asked, req)
	return p.reply, nil
}

func (bareProvider) Chat(ChatConfig) Chatter { return &Chat{} }

func TestRegistryDefaultIsClaude(t *testing.T) {
	if !slices.Contains(ProviderNames(), "claude") || DefaultProvider != "claude" {
		t.Fatalf("providers %v, default %q", ProviderNames(), DefaultProvider)
	}
	cfg := Config{Bin: "/opt/claude", Args: []string{"--permission-mode", "acceptEdits"}, Sandbox: []string{"sb"}}
	for _, name := range []string{"", "claude"} {
		p, err := NewProvider(name, cfg)
		if err != nil {
			t.Fatalf("NewProvider(%q): %v", name, err)
		}
		c, ok := p.(*ClaudeCode)
		if !ok || p.Name() != "claude" {
			t.Fatalf("NewProvider(%q) = %T %q", name, p, p.Name())
		}
		if c.Bin != cfg.Bin || !slices.Equal(c.Args, cfg.Args) || !slices.Equal(c.Sandbox, cfg.Sandbox) {
			t.Errorf("NewProvider(%q) = %+v, want built from %+v", name, c, cfg)
		}
		if !CanResume(p) || !CanMCP(p) || !CanPromptPermissions(p) {
			t.Errorf("Claude Code lacks an ability")
		}
		if m := MonitorOf(p); m == nil || m.Bin != cfg.Bin {
			t.Errorf("MonitorOf = %+v, want one for %s", m, cfg.Bin)
		}
		var got json.RawMessage
		p.(RateLimitReporter).ReportRateLimits(func(r json.RawMessage) { got = r })
		c.OnRateLimit(json.RawMessage(`{}`))
		if string(got) != "{}" {
			t.Errorf("rate limits not reported: %s", got)
		}
	}
}

func TestRegistryRegisterAndUnknown(t *testing.T) {
	if _, err := NewProvider("nope", Config{}); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("unknown provider: %v", err)
	}
	var asked []PromptRequest
	RegisterProvider("bare-test", func(Config) Provider { return bareProvider{asked: &asked} })
	defer UnregisterProvider("bare-test")
	if !slices.Contains(ProviderNames(), "bare-test") || !slices.IsSorted(ProviderNames()) {
		t.Fatalf("providers %v", ProviderNames())
	}
	p, err := NewProvider("bare-test", Config{})
	if err != nil || p.Name() != "bare" {
		t.Fatalf("NewProvider: %v, %v", p, err)
	}
	UnregisterProvider("bare-test")
	if slices.Contains(ProviderNames(), "bare-test") {
		t.Fatal("still registered")
	}
	UnregisterProvider(DefaultProvider) // the default stays
	if _, err := NewProvider("", Config{}); err != nil {
		t.Fatalf("default gone: %v", err)
	}
}

// A provider without the optional abilities says so, and the helpers
// handle it: no resume, MCP servers, permission prompts or CLI monitor.
func TestProviderWithoutAbilities(t *testing.T) {
	var asked []PromptRequest
	p := bareProvider{asked: &asked}
	if CanResume(p) || CanMCP(p) || CanPromptPermissions(p) || MonitorOf(p) != nil {
		t.Fatal("bare provider claims an ability")
	}
	if _, ok := any(p).(RateLimitReporter); ok {
		t.Fatal("bare provider reports rate limits")
	}
	// A nil monitor (no StatusReporter) is safe to use.
	m := MonitorOf(p)
	m.OnChange(func() {})
	if s := m.Status(); s.Version != "" || len(m.Warnings()) != 0 {
		t.Fatalf("nil monitor status %+v", s)
	}
}

// Titler asks its Prompter: the small default model for titles, the larger
// one for splitting, and the models it was given when it has them.
func TestTitlerUsesItsPrompter(t *testing.T) {
	var asked []PromptRequest
	ctx := context.Background()
	tl := Titler{Prompter: bareProvider{reply: "Add a login page", asked: &asked}}
	if got, err := tl.Title(ctx, "make a login page"); err != nil || got != "Add a login page" {
		t.Fatalf("Title = %q, %v", got, err)
	}
	asked = nil
	tl.Prompter = bareProvider{reply: `[{"title": "Add a page", "prompt": "do it"}]`, asked: &asked}
	if parts, err := tl.Split(ctx, "make a login page"); err != nil || len(parts) != 1 {
		t.Fatalf("Split = %v, %v", parts, err)
	}
	tl.Model, tl.SplitModel = "small-x", "big-y"
	_, _ = tl.Summary(ctx, "did it")
	_, _ = tl.Split(ctx, "x")
	if len(asked) != 3 {
		t.Fatalf("asked %d prompts", len(asked))
	}
	split, sum, split2 := asked[0], asked[1], asked[2]
	if !split.Large || split.Model != "" || split.Timeout != 3*time.Minute || !strings.Contains(split.Input, "make a login page") {
		t.Errorf("split asked %+v", split)
	}
	if sum.Large || sum.Model != "small-x" || sum.System == "" {
		t.Errorf("summary asked %+v", sum)
	}
	if !split2.Large || split2.Model != "big-y" {
		t.Errorf("split with a model asked %+v", split2)
	}
}

// Claude Code's prompt picks haiku for small jobs and sonnet for large
// ones, unless asked for a model.
func TestClaudePromptModels(t *testing.T) {
	testbin.SkipOnWindows(t, "runs a #!/bin/sh stand-in for claude")
	dir := t.TempDir()
	bin := dir + "/claude"
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := ClaudeCode{Bin: bin, Args: []string{"--task-flag"}, Sandbox: []string{"/no/such/sandbox"}}
	for req, want := range map[PromptRequest]string{
		{}:                        "--model\nhaiku\n",
		{Large: true}:             "--model\nsonnet\n",
		{Model: "opus"}:           "--model\nopus\n",
		{Large: true, Model: "x"}: "--model\nx\n",
	} {
		req.System = "sys"
		out, err := c.Prompt(context.Background(), req)
		if err != nil || !strings.Contains(out, want) || !strings.Contains(out, "--system-prompt\nsys\n") {
			t.Errorf("Prompt(%+v) = %q, %v; want %q", req, out, err, want)
		}
		if strings.Contains(out, "--task-flag") {
			t.Errorf("a prompt got the task flags: %q", out)
		}
	}
}

// RunArgs spells each field of a request as Claude Code's flag.
func TestClaudeRunArgs(t *testing.T) {
	c := ClaudeCode{Args: []string{"--permission-mode", "acceptEdits", "--model", "opus"}}
	req := ExecRequest{
		Model: "sonnet", Effort: "high", MaxTurns: 7, AddDirs: []string{"/a", "/b"}, AppendSystem: "be brief",
		MCPServers:     map[string]any{"dash": map[string]any{"type": "http"}},
		AllowMCPTools:  map[string][]string{"dash": {"x", "y"}},
		PermissionTool: MCPTool{Server: "perm", Tool: "ask"},
		Resume:         "s1", ForkSession: true,
	}
	got := strings.Join(c.RunArgs(req), " ")
	for _, want := range []string{
		"-p --output-format stream-json --verbose --permission-mode acceptEdits --model opus",
		"--max-turns 7", "--model sonnet", "--effort high", "--add-dir /a --add-dir /b",
		"--permission-prompt-tool mcp__perm__ask",
		"--allowedTools " + mcptools.ToolName("dash", "x") + " --allowedTools " + mcptools.ToolName("dash", "y"),
		"--append-system-prompt be brief", `--mcp-config {"mcpServers":{"dash":{"type":"http"}}}`,
		"--resume s1 --fork-session",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in %q", want, got)
		}
	}
	// The request's model comes after Args', so it wins.
	if strings.Index(got, "--model sonnet") < strings.Index(got, "--model opus") {
		t.Errorf("request model before Args': %q", got)
	}
	if strings.Contains(got, "dontAsk") || strings.Contains(got, "--setting-sources") {
		t.Errorf("an attended run got the unattended flags: %q", got)
	}

	un := strings.Join(c.RunArgs(ExecRequest{Unattended: true, AllowedTools: []string{"Read", "Grep"}, DeniedTools: []string{"Edit", "Write"}}), " ")
	for _, want := range []string{"--setting-sources  --permission-mode dontAsk --permission-prompts none", "--allowedTools Read,Grep", "--disallowedTools Edit,Write"} {
		if !strings.Contains(un, want) {
			t.Errorf("no %q in %q", want, un)
		}
	}
	if strings.Contains(un, "acceptEdits") || strings.Contains(un, "--mcp-config") || strings.Contains(un, "--resume") {
		t.Errorf("unattended run got task flags, MCP servers or a resume: %q", un)
	}

	env := runEnv(ExecRequest{Env: []string{"A=1"}, ToolTimeout: time.Hour})
	if !slices.Equal(env, []string{"A=1", "MCP_TOOL_TIMEOUT=3600000"}) {
		t.Errorf("env %q", env)
	}
	if env := runEnv(ExecRequest{}); len(env) != 0 {
		t.Errorf("env without a timeout %q", env)
	}
}

// Chat builds Claude Code's chat on the provider's binary, rate limits and
// monitor, set up as the config says.
func TestClaudeChat(t *testing.T) {
	var got json.RawMessage
	c := &ClaudeCode{Bin: "/opt/claude"}
	c.ReportRateLimits(func(r json.RawMessage) { got = r })
	ch, ok := c.Chat(ChatConfig{Dir: "/home/u", Tools: ChatShell, AllowedTools: []string{"Read"}, PermissionMode: "auto"}).(ClaudeCodeChat)
	if !ok {
		t.Fatal("not a ClaudeCodeChat")
	}
	if ch.Bin != "/opt/claude" || ch.Dir != "/home/u" || ch.Tools != ChatShell || !slices.Equal(ch.AllowedTools, []string{"Read"}) || ch.PermissionMode != "auto" {
		t.Errorf("chat %+v", ch)
	}
	ch.OnRateLimit(json.RawMessage(`1`))
	if string(got) != "1" {
		t.Error("chat doesn't report rate limits")
	}
	if ReadOnlyChat(ch) {
		t.Error("a shell chat reads as read-only")
	}
}
