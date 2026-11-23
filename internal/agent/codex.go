package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/textutil"
)

// CodexProvider is the registered name of the Codex CLI provider.
const CodexProvider = "codex"

// codexBin is the Codex CLI's default binary.
const codexBin = "codex"

// CodexKeyEnv is the API key a Codex run for another member than the
// runner's owner must use (see ExecRequest.Shared).
const CodexKeyEnv = "CODEX_API_KEY"

func init() {
	RegisterProvider(CodexProvider, func(cfg Config) Provider {
		return &Codex{Bin: cfg.Bin, Args: cfg.Args, Sandbox: cfg.Sandbox, CLI: &codexChecker{Bin: cfg.Bin}}
	})
}

// Codex is the OpenAI Codex CLI provider (registered as "codex"): it runs
// `codex exec --json` in the worktree, the prompt on stdin, and reads its
// JSON Lines events (codexTranslator). Codex supplies the agent loop, its
// tools and its own sandbox (workspace-write for tasks, read-only for chat
// and one-shot prompts); Dash supplies the worktree, the commit, tests and
// review.
//
// It continues sessions (`codex exec resume`, `codex exec fork`) and takes
// Dash's MCP servers (-c mcp_servers.…), but has no permission-prompt hook
// (a tool call its sandbox doesn't allow fails; nobody is asked), no
// per-tool rules (so scans don't run on it), no turn limit and no
// usage-limit reports.
type Codex struct {
	Bin string // default "codex"
	// Args are extra flags for task runs (-codex-args), e.g. "-c
	// sandbox_workspace_write.network_access=false" or "--model gpt-5.5";
	// unattended runs go without them.
	Args []string
	// Sandbox, if set, is a command that runs Codex for Dash, as for
	// ClaudeCode.Sandbox.
	Sandbox []string
	// CLI checks the installed CLI (optional).
	CLI *codexChecker
}

var (
	_ Provider       = (*Codex)(nil)
	_ Resumer        = Codex{}
	_ MCPConfigurer  = Codex{}
	_ StatusReporter = Codex{}
)

// Name is "codex".
func (Codex) Name() string { return CodexProvider }

// DisplayName is "Codex".
func (Codex) DisplayName() string { return "Codex" }

// CanResume: `codex exec resume <thread>` continues a session.
func (Codex) CanResume() bool { return true }

// CanMCP: Codex takes MCP servers as -c mcp_servers.<name>.… overrides.
func (Codex) CanMCP() bool { return true }

// CLIChecker is what Dash noticed about the installed Codex CLI.
func (c Codex) CLIChecker() CLIChecker {
	if c.CLI == nil {
		return nil
	}
	return c.CLI
}

func (c Codex) bin() string {
	if c.Bin == "" {
		return codexBin
	}
	return c.Bin
}

// codexOpts is one `codex exec` invocation, as RunArgs, the chat and
// Prompt use it.
type codexOpts struct {
	json      bool
	readOnly  bool // the read-only sandbox, else workspace-write with network
	ephemeral bool // no saved session
	bare      bool // without the user's config.toml and rules
	extra     []string
	model     string
	effort    string
	system    string
	servers   map[string]any
	allow     map[string][]string
	timeout   time.Duration
	resume    string
	fork      bool
}

// args are the `codex` arguments of o, and the environment they need (the
// MCP servers' tokens, which stay off the command line). The prompt comes
// on stdin ("-").
func (o codexOpts) args() (args, env []string) {
	args = []string{"exec"}
	if o.json {
		args = append(args, "--json")
	}
	// Dash's worktrees are git checkouts; the chat and short prompts run in
	// a scratch directory, which isn't.
	args = append(args, "--skip-git-repo-check")
	if o.bare {
		args = append(args, "--ignore-user-config", "--ignore-rules")
	}
	if o.ephemeral {
		args = append(args, "--ephemeral")
	}
	// Dash's sandbox defaults come before o.extra, so the operator's own
	// (-codex-args) win: Codex takes the last -c for a key. --sandbox can be
	// given once only, so Dash leaves its own out when extra has one.
	if !hasFlag(o.extra, "--sandbox", "-s") {
		if o.readOnly {
			args = append(args, "--sandbox", "read-only")
		} else {
			args = append(args, "--sandbox", "workspace-write")
		}
	}
	if !o.readOnly {
		args = append(args, "-c", "sandbox_workspace_write.network_access=true")
	}
	args = append(args, o.extra...)
	// Nobody answers an approval in `codex exec`: what the sandbox doesn't
	// allow fails at once.
	args = append(args, "-c", `approval_policy="never"`)
	if m := codexModel(o.model); m != "" {
		args = append(args, "--model", m)
	}
	if o.effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+tomlString(o.effort))
	}
	if o.system != "" {
		args = append(args, "-c", "developer_instructions="+tomlString(o.system))
	}
	mcp, env := codexMCPArgs(o.servers, o.allow, o.timeout)
	args = append(args, mcp...)
	if o.resume != "" {
		sub := "resume"
		if o.fork {
			sub = "fork" // a new thread from the old one, which stays as it was
		}
		args = append(args, sub, o.resume)
	}
	return append(args, "-"), env
}

// hasFlag reports whether args has one of names, alone or as name=value.
func hasFlag(args []string, names ...string) bool {
	for _, a := range args {
		for _, n := range names {
			if a == n || strings.HasPrefix(a, n+"=") {
				return true
			}
		}
	}
	return false
}

// codexNoTools are the -c overrides that leave Codex no tools: no shell
// (shell_tool, unified_exec's exec_command), no file edits, no image
// viewer, no web search. A one-shot prompt only answers in text, as
// ClaudeCode.Prompt's `claude -p` with no tools does: its input may be a
// task's text, which must not get to run commands or read files.
var codexNoTools = []string{
	"-c", "features.shell_tool=false",
	"-c", "features.unified_exec=false",
	"-c", "features.apply_patch_freeform=false",
	"-c", "include_apply_patch_tool=false",
	"-c", "tools.view_image=false",
	"-c", `web_search="disabled"`,
}

// codexModel is model as Codex takes it: "" (Codex's default) for a
// Claude model name, which a task or the project may have from before it
// switched agents.
func codexModel(m string) string {
	l := strings.ToLower(strings.TrimSpace(m))
	switch {
	case l == "", l == "default", strings.HasPrefix(l, "claude"), strings.Contains(l, "[1m]"):
		return ""
	case slices.Contains([]string{"opus", "sonnet", "haiku", "fable", "opusplan"}, l):
		return ""
	}
	return strings.TrimSpace(m)
}

// tomlString is s as a TOML basic string, for a -c override. A JSON string
// is one: TOML takes the same escapes, \uXXXX included.
func tomlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// codexServer is the part of an MCP server config (mcptools.ServerConfig,
// perm.ServerConfig) Codex needs.
type codexServer struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// codexMCPArgs are the -c overrides that give a run servers (name → config
// in Claude Code's --mcp-config shape: {"type":"http","url":…,"headers":…}).
// A header's value goes in env, named by env_http_headers, so tokens stay
// off the command line (and, as their names hold TOKEN, out of the
// commands Codex runs: its shell environment leaves such names out). The
// tools in allow run without asking, and are the only ones Codex gets of
// that server: nobody could approve the others.
func codexMCPArgs(servers map[string]any, allow map[string][]string, timeout time.Duration) (args, env []string) {
	names := slices.Sorted(maps.Keys(servers))
	for _, n := range names {
		var s codexServer
		b, _ := json.Marshal(servers[n])
		if json.Unmarshal(b, &s) != nil || s.URL == "" {
			continue
		}
		key := "mcp_servers." + n + "."
		args = append(args, "-c", key+"url="+tomlString(s.URL))
		if len(s.Headers) > 0 {
			hs := slices.Sorted(maps.Keys(s.Headers))
			var table []string
			for i, h := range hs {
				v := fmt.Sprintf("DASH_MCP_TOKEN_%s_%d", envName(n), i)
				env = append(env, v+"="+s.Headers[h])
				table = append(table, tomlString(h)+"="+tomlString(v))
			}
			args = append(args, "-c", key+"env_http_headers={"+strings.Join(table, ",")+"}")
		}
		if tools, ok := allow[n]; ok {
			list := make([]string, 0, len(tools))
			for _, t := range tools {
				list = append(list, tomlString(t))
			}
			args = append(args, "-c", key+"enabled_tools=["+strings.Join(list, ",")+"]",
				"-c", key+`default_tools_approval_mode="approve"`)
		}
		if timeout > 0 {
			args = append(args, "-c", key+"tool_timeout_sec="+strconv.FormatInt(int64(timeout/time.Second), 10))
		}
	}
	return args, env
}

// envName is s in upper case with only letters, digits and underscores.
func envName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '_'
	}, s)
}

// RunArgs are the `codex` arguments of a task run (after Sandbox), and the
// environment they add. Codex has no turn limit (MaxTurns) or per-tool
// rules (AllowedTools, DeniedTools: its sandbox decides), and reads
// anywhere, attachments (AddDirs) included. An unattended run is
// read-only, without the user's config, and saves no session.
func (c Codex) RunArgs(req ExecRequest) (args, env []string) {
	o := codexOpts{json: true, model: req.Model, effort: req.Effort, system: req.AppendSystem,
		servers: req.MCPServers, allow: req.AllowMCPTools, timeout: req.ToolTimeout,
		resume: req.Resume, fork: req.ForkSession && req.Resume != ""}
	if req.Unattended {
		o.readOnly, o.bare, o.ephemeral = true, true, true
	} else {
		o.extra = c.Args
	}
	return o.args()
}

// codexMissing is the error of a run on a computer without the Codex CLI.
func codexMissing(bin string, err error) error {
	return fmt.Errorf("the Codex CLI (%s) isn't installed on this computer: install it with `npm install -g @openai/codex`, "+
		"sign in with `codex login`, or pick another agent in Project settings → Agent (%w)", bin, err)
}

// errCodexLoggedOut is the error of a run with a Codex CLI that isn't signed in.
var errCodexLoggedOut = errors.New("the Codex CLI isn't signed in on this computer: run `codex login` (or set " + CodexKeyEnv + "), then try again")

// ready checks, before a run, that the CLI is there and signed in (as far
// as the checker knows), so a missing CLI fails with a clear message.
func (c Codex) ready() error {
	if _, err := exec.LookPath(c.bin()); err != nil {
		return codexMissing(c.bin(), err)
	}
	if st := c.CLI.CLIStatus(); st.Version != "" && st.LoggedOut {
		return errCodexLoggedOut
	}
	return nil
}

func (c Codex) Run(ctx context.Context, req ExecRequest, emit func(model.RunEvent)) (ExecResult, error) {
	if err := checkLocal(); err != nil {
		return ExecResult{}, err
	}
	if err := c.ready(); err != nil {
		return ExecResult{}, err
	}
	bin := c.bin()
	args, env := c.RunArgs(req)
	name := bin
	if len(c.Sandbox) > 0 {
		name, args = c.Sandbox[0], append(append(c.Sandbox[1:len(c.Sandbox):len(c.Sandbox)], bin), args...)
	}
	env = append(slices.Clone(req.Env), env...)
	if req.Shared != "" {
		// Work for another member: Codex signs in with the runner's API key
		// only, from a config folder of its own, never the owner's login.
		if strings.TrimSpace(os.Getenv(CodexKeyEnv)) == "" {
			return ExecResult{}, fmt.Errorf("stopped: this run is for another member than the runner's owner, so it must use the runner's API key (%s), which isn't set", CodexKeyEnv)
		}
		if err := os.MkdirAll(req.Shared, 0o700); err != nil {
			return ExecResult{}, err
		}
		env = append(env, "CODEX_HOME="+req.Shared)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = req.Dir
	cmd.Env = localrun.Environ(env...)
	var gitState string
	if len(c.Sandbox) > 0 {
		cmd.Env = append(cmd.Env, "DASH_WORKTREE="+req.Dir)
		gitState = gitops.GitDirState(req.Dir)
	}
	cmd.Stdin = strings.NewReader(req.Prompt)
	// Own process group so cancel kills Codex and everything it spawned.
	localrun.Group(cmd, syscall.SIGTERM, 10*time.Second)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ExecResult{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return ExecResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return ExecResult{}, fmt.Errorf("start %s: %w", name, err)
	}
	for _, w := range c.CLI.CLIStatus().Warnings {
		emit(model.RunEvent{Kind: "status", Text: codexWarnPrefix + w})
	}
	if m := codexModel(req.Model); m == "" && req.Model != "" {
		emit(model.RunEvent{Kind: "status", Text: "model " + req.Model + " is a Claude model: Codex runs on its default model"})
	}
	stderrDone := make(chan struct{})
	var stderrTail []string
	go func() {
		defer close(stderrDone)
		n := pumpLines(stderr, func(l string) {
			if strings.TrimSpace(l) == "" {
				return
			}
			stderrTail = append(stderrTail, l)
			if len(stderrTail) > 5 {
				stderrTail = stderrTail[1:]
			}
			emit(model.RunEvent{Kind: "status", Text: "stderr: " + l})
		})
		if n > 0 {
			emit(model.RunEvent{Kind: "status", Text: skippedLines(n, "stderr")})
		}
	}()
	var tr codexTranslator
	started := time.Now()
	skipped := pumpLines(stdout, func(l string) {
		ev, ok := parseCodexLine(l)
		if !ok {
			if strings.TrimSpace(l) != "" {
				emit(model.RunEvent{Kind: "status", Text: l})
			}
			return
		}
		for _, d := range tr.Translate(ev) {
			if d.Kind == codexStatus && strings.HasPrefix(d.Text, codexWarnPrefix) {
				c.CLI.observed([]string{strings.TrimPrefix(d.Text, codexWarnPrefix)})
			}
			if e, ok := runEvent(d); ok {
				emit(e)
			}
		}
	})
	if skipped > 0 {
		emit(model.RunEvent{Kind: "status", Text: skippedLines(skipped, "output")})
	}
	<-stderrDone
	waitErr := cmd.Wait()
	res := ExecResult{SessionID: tr.Thread, Turns: 0, Model: codexModel(req.Model), Usage: tr.Usage, Result: tr.Last}
	res.Usage.DurationMS = time.Since(started).Milliseconds()
	if len(c.Sandbox) > 0 && gitops.GitDirState(req.Dir) != gitState {
		return res, fmt.Errorf("codex changed the worktree's git metadata (.git, commondir or config.worktree): Dash won't run git there")
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if waitErr != nil && req.Resume != "" && tr.Thread == "" && tr.Turns == 0 {
		// Codex stopped before it opened the session: it doesn't have it
		// (it ran on another computer, or was deleted). The caller starts
		// over with the full prompt.
		res.ResumeFailed = true
	}
	why := tr.Failure
	if why == "" && waitErr != nil {
		why = strings.Join(tr.Errors, "; ")
		if why == "" {
			why = strings.Join(stderrTail, "; ")
		}
		if why == "" {
			why = waitErr.Error()
		}
	}
	if why != "" {
		res.Stop = "error"
		emit(model.RunEvent{Kind: "result", Text: "error: " + why, IsError: true})
		return res, fmt.Errorf("codex reported an error: %s", why)
	}
	res.Stop = "success"
	emit(model.RunEvent{Kind: "result", Text: tr.Last})
	return res, nil
}

// Chat is the Codex chat set up as cfg says, on c's binary. Codex's chat
// only reads (its read-only sandbox) whatever cfg.Tools says: it can't ask
// the user before a call (no PermissionHook), so it runs nothing that
// changes files. It has Dash's task tools (cfg.Board).
func (c Codex) Chat(cfg ChatConfig) Chatter {
	return CodexChat{Bin: c.Bin, Dir: cfg.Dir, Board: cfg.Board, BoardURL: cfg.BoardURL, CLI: c.CLI}
}

// Prompt runs `codex exec` once on req.Input, read-only, with no tools
// (codexNoTools), without the user's config or a saved session, and returns its final message (stdout:
// `codex exec` prints only that there). req.System goes in as developer
// instructions, next to Codex's own. A Claude model name is left out
// (codexModel); a small job asks for low reasoning effort.
func (c Codex) Prompt(ctx context.Context, req PromptRequest) (string, error) {
	if err := checkLocal(); err != nil {
		return "", err
	}
	if err := c.ready(); err != nil {
		return "", err
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	o := codexOpts{readOnly: true, ephemeral: true, bare: true, model: req.Model, system: req.System,
		extra: codexNoTools}
	if !req.Large {
		o.effort = "low"
	}
	args, _ := o.args()
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Dir = os.TempDir()
	cmd.Stdin = strings.NewReader(req.Input)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	localrun.Group(cmd, syscall.SIGKILL, 5*time.Second)
	out, err := cmd.Output()
	if err != nil && ctx.Err() == nil {
		return string(out), fmt.Errorf("codex exec: %w: %s", err, textutil.LastLine(stderr.String()))
	}
	return strings.TrimSpace(string(out)), err
}
