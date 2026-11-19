package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/klahr/dash/internal/claudecli"
	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/mcptools"
)

// ClaudeCode is a Provider with every optional ability.
var (
	_ Provider          = (*ClaudeCode)(nil)
	_ Resumer           = ClaudeCode{}
	_ MCPConfigurer     = ClaudeCode{}
	_ PermissionHook    = ClaudeCode{}
	_ ToolRuler         = ClaudeCode{}
	_ RateLimitReporter = (*ClaudeCode)(nil)
	_ StatusReporter    = ClaudeCode{}
)

// Name is "claude".
func (ClaudeCode) Name() string { return DefaultProvider }

// DisplayName is "Claude Code".
func (ClaudeCode) DisplayName() string { return "Claude Code" }

// CanResume: Claude Code continues sessions with --resume.
func (ClaudeCode) CanResume() bool { return true }

// CanMCP: Claude Code takes MCP servers with --mcp-config.
func (ClaudeCode) CanMCP() bool { return true }

// CanPromptPermissions: Claude Code asks --permission-prompt-tool.
func (ClaudeCode) CanPromptPermissions() bool { return true }

// CanRuleTools: Claude Code takes --allowedTools and --disallowedTools.
func (ClaudeCode) CanRuleTools() bool { return true }

// ReportRateLimits sets f to get the rate_limit_info of every
// rate_limit_event of the runs and chats started after.
func (c *ClaudeCode) ReportRateLimits(f func(json.RawMessage)) { c.OnRateLimit = f }

// CLIMonitor is what Dash noticed about the installed Claude Code CLI.
func (c ClaudeCode) CLIMonitor() *claudecli.Monitor { return c.CLI }

// CLIChecker is CLI as a CLIChecker (nil without one).
func (c ClaudeCode) CLIChecker() CLIChecker {
	if c.CLI == nil {
		return nil
	}
	return claudeChecker{c.CLI}
}

// claudeChecker is a Claude Code CLI monitor as a CLIChecker.
type claudeChecker struct{ *claudecli.Monitor }

func (c claudeChecker) CLIStatus() CLIStatus { return claudeCLIStatus(c.Status()) }

// claudeCLIStatus is what a Claude Code CLI monitor found (c) as a
// CLIStatus: logged out only when `claude auth status` said so.
func claudeCLIStatus(c claudecli.Status) CLIStatus {
	return CLIStatus{Version: c.Version, Warnings: c.Warnings, CheckedAt: c.CheckedAt,
		LoggedOut: c.Auth != nil && c.Auth.Method == claudecli.AuthNone}
}

func (c ClaudeCode) bin() string {
	if c.Bin == "" {
		return localrun.Claude
	}
	return c.Bin
}

// RunArgs are the `claude` arguments of a task or scan run (after Sandbox).
func (c ClaudeCode) RunArgs(req ExecRequest) []string {
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if !req.Unattended {
		args = append(args, c.Args...)
	}
	if req.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(req.MaxTurns))
	}
	if req.Model != "" {
		// After Args, so it wins over a --model there.
		args = append(args, "--model", req.Model)
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	for _, d := range req.AddDirs {
		args = append(args, "--add-dir", d)
	}
	if req.Unattended {
		// Nothing may wait for a permission answer: a call outside the
		// allowed tools is denied at once, and the user's and project's
		// settings (and so their hooks) aren't loaded.
		args = append(args, "--setting-sources", "", "--permission-mode", "dontAsk", "--permission-prompts", "none")
	}
	if req.PermissionTool != (MCPTool{}) {
		args = append(args, "--permission-prompt-tool", mcptools.ToolName(req.PermissionTool.Server, req.PermissionTool.Tool))
	}
	if len(req.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(req.AllowedTools, ","))
	}
	servers := make([]string, 0, len(req.AllowMCPTools))
	for s := range req.AllowMCPTools {
		servers = append(servers, s)
	}
	slices.Sort(servers)
	for _, s := range servers {
		args = append(args, mcptools.AllowedToolArgs(s, req.AllowMCPTools[s]...)...)
	}
	if len(req.DeniedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(req.DeniedTools, ","))
	}
	if req.AppendSystem != "" {
		args = append(args, "--append-system-prompt", req.AppendSystem)
	}
	args = append(args, mcptools.ConfigArgs(req.MCPServers)...)
	if req.Resume != "" {
		// Claude Code finds the session wherever it was started (sessions
		// are kept per working directory, and the task building on another
		// runs in a worktree of its own); a fork is saved under this one.
		// (Checked with Claude Code 2.1.322; where it isn't found, Run
		// reports ResumeFailed and the caller starts fresh.)
		args = append(args, "--resume", req.Resume)
		if req.ForkSession {
			args = append(args, "--fork-session")
		}
	}
	return args
}

// runEnv is the environment a run adds to Dash's.
func runEnv(req ExecRequest) []string {
	env := slices.Clone(req.Env)
	if req.ToolTimeout > 0 {
		// Claude waits on an MCP tool (a person deciding, the tests
		// running); don't let it time out first.
		env = append(env, "MCP_TOOL_TIMEOUT="+strconv.FormatInt(req.ToolTimeout.Milliseconds(), 10))
	}
	return env
}

// Chat is the Claude Code chat set up as cfg says, on c's binary.
func (c ClaudeCode) Chat(cfg ChatConfig) Chatter {
	return ClaudeCodeChat{
		Bin: c.Bin, Dir: cfg.Dir, Tools: cfg.Tools, AllowedTools: cfg.AllowedTools, PermissionMode: cfg.PermissionMode,
		Perms: cfg.Perms, PermURL: cfg.PermURL, Board: cfg.Board, BoardURL: cfg.BoardURL,
		OnRateLimit: c.OnRateLimit, CLI: c.CLI,
	}
}

// Claude's default models for one-shot prompts.
const (
	claudeSmallModel = "haiku"
	claudeLargeModel = "sonnet"
)

// Prompt runs `claude -p` once on req.Input, without tools, MCP servers,
// the user's settings or a saved session, and returns its text. It runs c's
// binary directly: not in the Sandbox, and without Args.
func (c ClaudeCode) Prompt(ctx context.Context, req PromptRequest) (string, error) {
	if err := checkLocal(); err != nil {
		return "", err
	}
	model := req.Model
	if model == "" {
		model = claudeSmallModel
		if req.Large {
			model = claudeLargeModel
		}
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, c.bin(), "-p", "--output-format", "text", "--model", model,
		"--tools", "", "--no-session-persistence", "--strict-mcp-config", "--setting-sources", "",
		"--system-prompt", req.System)
	cmd.Dir = os.TempDir()
	cmd.Stdin = strings.NewReader(req.Input)
	// Don't wait on a grandchild that keeps stdout open past the timeout.
	localrun.Group(cmd, syscall.SIGKILL, 5*time.Second)
	out, err := cmd.Output()
	return string(out), err
}
