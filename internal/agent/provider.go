package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/klahr/dash/internal/claudecli"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
)

// Provider is an agent CLI Dash runs: tasks and scans (Executor), chat
// turns (Chat) and one-shot prompts such as titles and summaries (Prompter).
// Claude Code (ClaudeCode, registered as "claude") is the default.
//
// What only some agent CLIs can do is in the small interfaces below
// (Resumer, MCPConfigurer, PermissionHook, ToolRuler, RateLimitReporter,
// StatusReporter). Check for them with a type assertion, or the Can…
// helpers, and work without them: a provider that lacks one is not an error.
type Provider interface {
	// Name is the provider's registered name, e.g. "claude".
	Name() string
	Executor
	Prompter
	// Chat is a chatter for the general chat set up as cfg says.
	Chat(cfg ChatConfig) Chatter
}

// Prompter answers one prompt with text, without tools or a session.
type Prompter interface {
	Prompt(ctx context.Context, req PromptRequest) (string, error)
}

// PromptRequest is one one-shot prompt.
type PromptRequest struct {
	// Model is the model to answer with; "" is the provider's default for
	// the size of the job (Large).
	Model string
	// Large marks a job that takes more judgement (planning work), which
	// gets the provider's larger default model; else its small, fast one.
	Large  bool
	System string
	Input  string
	// Timeout bounds the call (0: no limit beyond ctx).
	Timeout time.Duration
}

// ChatConfig is how a provider's chat is set up (see Provider.Chat).
type ChatConfig struct {
	// Dir is the working directory of the global chat (Stream gets no dir);
	// default os.TempDir().
	Dir   string
	Tools string // ChatReadOnly (default) or ChatShell
	// AllowedTools run without asking in shell mode, e.g. "Bash(git status *)".
	AllowedTools []string
	// PermissionMode is the default permission mode in shell mode, e.g.
	// "auto"; a turn may set its own (WithPermissionMode).
	PermissionMode string
	// Perms and PermURL answer permission prompts in shell mode; Board and
	// BoardURL serve the Dash task tools. A provider without MCP servers
	// (MCPConfigurer) or permission prompts (PermissionHook) leaves them out.
	Perms    *perm.Broker
	PermURL  string
	Board    *mcptools.Tokens
	BoardURL string
}

// Resumer is a provider that can continue an earlier session: it takes
// ExecRequest.Resume and ForkSession, and sets ExecResult.ResumeFailed when
// the session is gone. Without one, every run starts fresh with the full
// prompt.
type Resumer interface {
	CanResume() bool
}

// MCPConfigurer is a provider whose runs can use MCP servers
// (ExecRequest.MCPServers, AllowMCPTools). Without one, runs go without
// Dash's MCP servers (the task tools, a scan's git tools).
type MCPConfigurer interface {
	CanMCP() bool
}

// PermissionHook is a provider that can ask an MCP tool before a tool call
// it isn't allowed to make outright (ExecRequest.PermissionTool). Without
// one, nobody is asked: such calls follow the provider's own rules.
type PermissionHook interface {
	CanPromptPermissions() bool
}

// ToolRuler is a provider that follows per-tool permission rules
// (ExecRequest.AllowedTools, DeniedTools), so a run's file reads can be
// confined to its directory. Without one, a run reads what its CLI's own
// sandbox lets it: scans, which must not read the runner's credentials,
// don't run on it.
type ToolRuler interface {
	CanRuleTools() bool
}

// RateLimitReporter is a provider that reports its account's rate limits
// as they change: f gets each report, as the CLI sent it.
type RateLimitReporter interface {
	ReportRateLimits(f func(json.RawMessage))
}

// StatusReporter is a provider that checks its installed CLI (version,
// accepted flags, login) and keeps what it found (a nil CLIChecker: none).
type StatusReporter interface {
	CLIChecker() CLIChecker
}

// CLIChecker checks an agent's installed CLI now and then and keeps what
// it found.
type CLIChecker interface {
	// Run checks now and then every `every` until ctx ends.
	Run(ctx context.Context, every time.Duration)
	// OnChange sets f to be called after what it found changed.
	OnChange(f func())
	// CLIStatus is what it found last.
	CLIStatus() CLIStatus
}

// CLIStatus is what a CLIChecker found about an agent's CLI.
type CLIStatus struct {
	Version   string // "" when it isn't installed (or didn't say)
	Warnings  []string
	CheckedAt time.Time // zero: not checked yet
	// LoggedOut: the CLI said it isn't signed in. A sign-in state Dash
	// doesn't understand isn't LoggedOut, so it doesn't keep tasks away:
	// it is SignInUnknown.
	LoggedOut     bool
	SignInUnknown bool
	// Title is the agent's own name, when it says one (ACP's agentInfo).
	Title string
}

// DisplayNamer is a provider with a name for people, e.g. "Claude Code".
type DisplayNamer interface {
	DisplayName() string
}

// SetupHinter is a provider that says how to set its CLI up, for people:
// how to install it ("npm install -g @openai/codex") and the command that
// signs it in ("codex login").
type SetupHinter interface {
	InstallHint() string
	LoginHint() string
}

// CanResume reports whether x can continue an earlier session (Resumer).
func CanResume(x any) bool {
	r, ok := x.(Resumer)
	return ok && r.CanResume()
}

// CanMCP reports whether x's runs can use MCP servers (MCPConfigurer).
func CanMCP(x any) bool {
	m, ok := x.(MCPConfigurer)
	return ok && m.CanMCP()
}

// CanPromptPermissions reports whether x can ask an MCP tool before a tool
// call (PermissionHook); that takes MCP servers too.
func CanPromptPermissions(x any) bool {
	p, ok := x.(PermissionHook)
	return ok && p.CanPromptPermissions() && CanMCP(x)
}

// CanRuleTools reports whether x follows per-tool permission rules
// (ToolRuler).
func CanRuleTools(x any) bool {
	r, ok := x.(ToolRuler)
	return ok && r.CanRuleTools()
}

// CheckerOf is x's CLI checker (StatusReporter), or nil.
func CheckerOf(x any) CLIChecker {
	if s, ok := x.(StatusReporter); ok {
		return s.CLIChecker()
	}
	return nil
}

// MonitorOf is x's Claude Code CLI monitor, or nil when x isn't Claude
// Code: a nil monitor checks nothing and reports nothing. What only Claude
// Code says (its account, how it signs in) is read from it.
func MonitorOf(x any) *claudecli.Monitor {
	if c, ok := x.(interface{ CLIMonitor() *claudecli.Monitor }); ok {
		return c.CLIMonitor()
	}
	return nil
}

// Config is what a provider is built from (see NewProvider). Each provider
// has its own: the flags of one CLI mean nothing to another.
type Config struct {
	// Bin is the CLI's binary; "" is the provider's default (e.g. "claude").
	Bin string
	// Args are extra flags for task runs (-claude-args, -codex-args); scans
	// and other unattended runs go without them.
	Args []string
	// Sandbox, if set, is a command that runs the CLI for Dash (see
	// ClaudeCode.Sandbox and docs/sandbox.md).
	Sandbox []string
}

// Factory builds a provider from cfg.
type Factory func(cfg Config) Provider

// DefaultProvider is the provider Dash uses when none is named.
const DefaultProvider = "claude"

// ErrUnknownProvider is returned for a provider name nobody registered.
var ErrUnknownProvider = errors.New("unknown agent provider")

var (
	provMu    sync.RWMutex
	providers = map[string]Factory{}
)

func init() {
	RegisterProvider(DefaultProvider, func(cfg Config) Provider {
		return &ClaudeCode{Bin: cfg.Bin, Args: cfg.Args, Sandbox: cfg.Sandbox, CLI: &claudecli.Monitor{Bin: cfg.Bin}}
	})
}

// RegisterProvider adds a provider by name; registering a name again
// replaces it.
func RegisterProvider(name string, f Factory) {
	if name == "" {
		panic("agent: RegisterProvider without a name")
	}
	if f == nil {
		panic("agent: RegisterProvider " + name + " without a factory")
	}
	provMu.Lock()
	defer provMu.Unlock()
	providers[name] = f
}

// UnregisterProvider removes a provider (tests). The default stays.
func UnregisterProvider(name string) {
	provMu.Lock()
	defer provMu.Unlock()
	if name != DefaultProvider {
		delete(providers, name)
	}
}

// ProviderNames are the registered providers' names, sorted.
func ProviderNames() []string {
	provMu.RLock()
	defer provMu.RUnlock()
	out := slices.Sorted(maps.Keys(providers))
	return out
}

// CLIBins are the default binaries of the registered providers' CLIs, e.g.
// "claude", "codex" and "gemini", each once.
func CLIBins() []string {
	var out []string
	for _, n := range ProviderNames() {
		p, err := NewProvider(n, Config{})
		if err != nil {
			continue
		}
		if b, ok := p.(interface{ bin() string }); ok && b.bin() != "" && !slices.Contains(out, b.bin()) {
			out = append(out, b.bin())
		}
	}
	return out
}

// NewProvider builds the provider registered as name ("": DefaultProvider).
func NewProvider(name string, cfg Config) (Provider, error) {
	if name == "" {
		name = DefaultProvider
	}
	provMu.RLock()
	f, ok := providers[name]
	provMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownProvider, name)
	}
	return f(cfg), nil
}

// defaultPrompter is the default provider with no settings: what a Titler
// without a Prompter asks.
func defaultPrompter() Prompter {
	p, err := NewProvider(DefaultProvider, Config{})
	if err != nil {
		panic(err) // registered in init
	}
	return p
}

// Features are the optional abilities x has, by name (model.FeatureResume,
// model.FeatureMCP, model.FeaturePermissionHook, model.FeatureToolRules,
// model.FeatureUsage), as
// the interfaces above say: what a runner reports of each agent it has.
func Features(x any) []string {
	out := []string{}
	if CanResume(x) {
		out = append(out, model.FeatureResume)
	}
	if CanMCP(x) {
		out = append(out, model.FeatureMCP)
	}
	if CanPromptPermissions(x) {
		out = append(out, model.FeaturePermissionHook)
	}
	if CanRuleTools(x) {
		out = append(out, model.FeatureToolRules)
	}
	if _, ok := x.(RateLimitReporter); ok {
		out = append(out, model.FeatureUsage)
	}
	return out
}

// ProviderFeatures are the features of the provider registered as name,
// built with no settings (nil for a name nobody registered).
func ProviderFeatures(name string) []string {
	p, err := NewProvider(name, Config{})
	if err != nil {
		return nil
	}
	return Features(p)
}

// StatusOf is what a runner reports of provider p: its name and features,
// and what its CLI checker (StatusReporter) last found. A provider without
// a checker can't say whether its CLI is installed: it is reported as not
// installed, with a warning, so no project's tasks are sent to it.
func StatusOf(p Provider) model.AgentStatus {
	s := model.AgentStatus{Name: p.Name(), Features: Features(p)}
	c := CheckerOf(p)
	if c == nil {
		s.Warnings = []string{"Dash can't check this agent's CLI"}
		s.CheckedAt = time.Now()
		return s
	}
	return withCLIStatus(s, c.CLIStatus())
}

// ClaudeStatus is the agent status of Claude Code as a runner too old to
// report agents described its CLI (c), for the hub to show and match.
func ClaudeStatus(c claudecli.Status) model.AgentStatus {
	return withCLIStatus(model.AgentStatus{Name: DefaultProvider, Features: ProviderFeatures(DefaultProvider)}, claudeCLIStatus(c))
}

// withCLIStatus fills s from what a CLI checker found (c). It counts as
// logged in unless the CLI said it isn't: an auth status Dash doesn't
// understand doesn't keep tasks from a runner.
func withCLIStatus(s model.AgentStatus, c CLIStatus) model.AgentStatus {
	s.Version, s.CheckedAt, s.Title = c.Version, c.CheckedAt, c.Title
	s.Warnings = slices.Clone(c.Warnings)
	s.Installed = c.Version != ""
	s.LoggedIn = s.Installed && !c.LoggedOut
	switch {
	case !s.Installed:
	case c.LoggedOut:
		s.SignIn = model.SignedOut
	case c.SignInUnknown:
		s.SignIn = model.SignInUnknown
	default:
		s.SignIn = model.SignedIn
	}
	return s
}

// Ready reports whether p can run work as far as Dash knows (StatusOf's
// model.AgentStatus.Ready): installed and signed in, or not checked yet.
func Ready(p Provider) bool { return p != nil && StatusOf(p).Ready() }

// ProviderInfo is a registered provider as the dashboard shows it.
type ProviderInfo struct {
	Name     string   `json:"name"`
	Title    string   `json:"title"` // for people (DisplayNamer), else Name
	Features []string `json:"features"`
	Default  bool     `json:"default,omitempty"` // DefaultProvider
	// Install and Login say how to install its CLI and sign it in
	// (SetupHinter; "" when it doesn't say).
	Install string `json:"install,omitempty"`
	Login   string `json:"login,omitempty"`
	// Status is what this Dash found of its CLI, when it runs tasks itself
	// (nil on a hub: its runners report theirs).
	Status *model.AgentStatus `json:"status,omitempty"`
}

// Providers are the registered providers, by name.
func Providers() []ProviderInfo {
	out := []ProviderInfo{}
	for _, n := range ProviderNames() {
		p, err := NewProvider(n, Config{})
		if err != nil {
			continue
		}
		i := ProviderInfo{Name: n, Title: n, Features: Features(p), Default: n == DefaultProvider}
		if d, ok := p.(DisplayNamer); ok && d.DisplayName() != "" {
			i.Title = d.DisplayName()
		}
		if h, ok := p.(SetupHinter); ok {
			i.Install, i.Login = h.InstallHint(), h.LoginHint()
		}
		out = append(out, i)
	}
	return out
}
