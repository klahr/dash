package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/klahr/dash/internal/claudecli"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/perm"
)

// Provider is an agent CLI Dash runs: tasks and scans (Executor), chat
// turns (Chat) and one-shot prompts such as titles and summaries (Prompter).
// Claude Code (ClaudeCode, registered as "claude") is the default.
//
// What only some agent CLIs can do is in the small interfaces below
// (Resumer, MCPConfigurer, PermissionHook, RateLimitReporter,
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

// RateLimitReporter is a provider that reports its account's rate limits
// as they change: f gets each report, as the CLI sent it.
type RateLimitReporter interface {
	ReportRateLimits(f func(json.RawMessage))
}

// StatusReporter is a provider that checks its installed CLI (version,
// accepted flags, login) and keeps what it found in a monitor.
type StatusReporter interface {
	CLIMonitor() *claudecli.Monitor
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

// MonitorOf is x's CLI monitor (StatusReporter), or nil: a nil monitor
// checks nothing and reports nothing.
func MonitorOf(x any) *claudecli.Monitor {
	if s, ok := x.(StatusReporter); ok {
		return s.CLIMonitor()
	}
	return nil
}

// Config is what every provider is built from (see NewProvider).
type Config struct {
	// Bin is the CLI's binary; "" is the provider's default (e.g. "claude").
	Bin string
	// Args are extra flags for task runs (-claude-args); scans and other
	// unattended runs go without them.
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
	out := make([]string, 0, len(providers))
	for n := range providers {
		out = append(out, n)
	}
	sort.Strings(out)
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
