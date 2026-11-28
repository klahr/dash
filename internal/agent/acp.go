package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/model"
)

// ACPProfile is one agent CLI that speaks the Agent Client Protocol: how
// to start it and the strings that are its own. The ACP provider does the
// rest (see docs/acp.md). A new ACP agent is a new profile, registered
// with RegisterACPProfile.
type ACPProfile struct {
	// Name is the provider's registered name, e.g. "gemini"; Title its
	// name for people, e.g. "Gemini CLI".
	Name, Title string
	// Bin is the CLI's default binary and Args the flags that start it in
	// ACP mode, e.g. "gemini" and ["--acp"].
	Bin  string
	Args []string
	// Install and Login say how to install the CLI and sign it in, for
	// error messages.
	Install, Login string
	// KeyEnv is the API key a run for another member than the runner's
	// owner must use (ExecRequest.Shared). KeyAuthMethod is the agent's
	// authenticate method that signs in with it ("": the agent reads
	// KeyEnv itself). HomeEnv, if set, is the variable that moves the
	// CLI's config folder: such a run gets one of its own.
	KeyEnv, KeyAuthMethod, HomeEnv string
	// TaskMode is the session mode of a task run, UnattendedMode of a run
	// nobody watches, ChatMode of a chat turn ("": the agent's default).
	// Whatever mode, Dash answers each permission request the agent sends.
	TaskMode, UnattendedMode, ChatMode string
	// AddDirFlag, if set, is the flag that lets the agent read a directory
	// outside its working directory (ExecRequest.AddDirs).
	AddDirFlag string
	// Model maps a Dash model name to the agent's ("": its default); nil
	// passes every name but Claude's.
	Model func(string) string
	// Effort maps a Dash effort ("high") to the agent's thought_level
	// option value; one it lacks is left out.
	Effort map[string]string
	// Tools names a tool call by its ACP kind as Claude Code names its
	// tools, for saved grants and **Needs you** (acpToolNames by default).
	Tools map[string]string
}

// acpToolNames are Claude Code's names for ACP's tool kinds.
var acpToolNames = map[string]string{
	"read":        "Read",
	"edit":        "Edit",
	"delete":      "Edit",
	"move":        "Edit",
	"search":      "Grep",
	"execute":     "Bash",
	"fetch":       "WebFetch",
	"think":       "Task",
	"switch_mode": "ExitPlanMode",
}

// ToolName is a tool call's tool by its ACP kind: the profile's name, else
// Claude Code's, else "Other".
func (p *ACPProfile) ToolName(kind string) string {
	if n := p.Tools[kind]; n != "" {
		return n
	}
	if n := acpToolNames[kind]; n != "" {
		return n
	}
	return "Other"
}

func (p *ACPProfile) model(m string) string {
	if p.Model != nil {
		return p.Model(m)
	}
	return codexModel(m) // every name but Claude's
}

func (p *ACPProfile) warnPrefix() string { return "⚠ " + p.Title + ": " }

// RegisterACPProfile registers an ACP provider for p under p.Name.
func RegisterACPProfile(p *ACPProfile) {
	RegisterProvider(p.Name, func(cfg Config) Provider {
		return &ACP{Profile: p, Bin: cfg.Bin, Args: cfg.Args, Sandbox: cfg.Sandbox, CLI: &acpChecker{Profile: p, Bin: cfg.Bin}}
	})
}

// ACP runs an agent CLI over the Agent Client Protocol (v1): JSON-RPC on
// the agent's stdin and stdout. It starts the agent in the task's
// worktree, opens a session there with Dash's MCP servers, sends the
// prompt and turns the session's updates into Dash's events
// (acpTranslator). It answers the agent's permission requests through
// Dash's permission_prompt tool.
//
// What it can do beyond that it takes from the agent's initialize answer
// (ACPCaps), which its CLI checker keeps: resume (session/resume or
// session/load) and Dash's MCP servers (over HTTP). It has no per-tool
// rules (so scans don't run on it), no turn limit, no account rate limits
// and no tool-free one-shot prompt (titles and summaries stay on Claude
// Code or Codex).
type ACP struct {
	Profile *ACPProfile
	Bin     string   // default Profile.Bin
	Args    []string // extra flags for task runs, after Profile.Args
	// Sandbox, if set, is a command that runs the agent for Dash, as for
	// ClaudeCode.Sandbox.
	Sandbox []string
	CLI     *acpChecker
}

var (
	_ Provider       = (*ACP)(nil)
	_ Resumer        = ACP{}
	_ MCPConfigurer  = ACP{}
	_ PermissionHook = ACP{}
	_ StatusReporter = ACP{}
	_ DisplayNamer   = ACP{}
)

func (a ACP) Name() string        { return a.Profile.Name }
func (a ACP) DisplayName() string { return a.Profile.Title }

// InstallHint and LoginHint are the profile's (SetupHinter).
func (a ACP) InstallHint() string { return a.Profile.Install }
func (a ACP) LoginHint() string   { return a.Profile.Login }

// lastCaps are the abilities each profile's agent last said it has, by
// profile name: a provider built afresh (Providers) knows them too.
var lastCaps sync.Map

// Caps are what the agent said it can do when it was last checked.
func (a ACP) Caps() ACPCaps {
	if c := a.CLI.caps(); c.Known {
		return c
	}
	if c, ok := lastCaps.Load(a.Profile.Name); ok {
		return c.(ACPCaps)
	}
	return ACPCaps{}
}

// CanResume: the agent offers session/resume or session/load.
func (a ACP) CanResume() bool { c := a.Caps(); return c.Resume || c.Load }

// CanMCP: the agent takes MCP servers over HTTP, as Dash serves them.
func (a ACP) CanMCP() bool { return a.Caps().HTTP }

// CanPromptPermissions: every ACP agent asks with session/request_permission.
func (ACP) CanPromptPermissions() bool { return true }

// NoPrompts: an ACP agent can't answer a prompt without its tools.
func (ACP) NoPrompts() bool { return true }

func (a ACP) CLIChecker() CLIChecker {
	if a.CLI == nil {
		return nil
	}
	return a.CLI
}

func (a ACP) bin() string {
	if a.Bin != "" {
		return a.Bin
	}
	return a.Profile.Bin
}

// errACPPrompt is Prompt's error: titles and summaries use Claude Code or
// Codex (Set.Prompter skips ACP agents).
var errACPPrompt = errors.New("an ACP agent can't answer one-shot prompts without its tools: use Claude Code or Codex for titles and summaries")

func (a ACP) Prompt(context.Context, PromptRequest) (string, error) { return "", errACPPrompt }

// missing is the error of a run on a computer without the CLI.
func (a ACP) missing(err error) error {
	return fmt.Errorf("the %s (%s) isn't installed on this computer: %s, or pick another agent in Project settings → Agent & runners (%w)",
		a.Profile.Title, a.bin(), a.Profile.Install, err)
}

func (a ACP) loggedOut() error {
	msg := fmt.Sprintf("the %s isn't signed in on this computer: run `%s` and sign in", a.Profile.Title, a.Profile.Login)
	if a.Profile.KeyEnv != "" {
		msg += " (or set " + a.Profile.KeyEnv + ")"
	}
	return errors.New(msg + ", then try again")
}

// ready checks, before a run, that the CLI is there and signed in (as far
// as the checker knows).
func (a ACP) ready() error {
	if _, err := exec.LookPath(a.bin()); err != nil {
		return a.missing(err)
	}
	if st := a.CLI.CLIStatus(); st.Version != "" && st.LoggedOut {
		return a.loggedOut()
	}
	return nil
}

// command is the program and arguments that start the agent: the
// profile's ACP flags, then extra (Args, AddDirs), wrapped in the sandbox
// command if there is one.
func (a ACP) command(extra []string, sandbox bool) (string, []string) {
	args := append(slices.Clone(a.Profile.Args), extra...)
	if sandbox && len(a.Sandbox) > 0 {
		return a.Sandbox[0], append(append(slices.Clone(a.Sandbox[1:]), a.bin()), args...)
	}
	return a.bin(), args
}

// RunArgs are the agent's arguments for a task run (after Sandbox).
func (a ACP) RunArgs(req ExecRequest) []string {
	_, args := a.command(a.runExtra(req), false)
	return args
}

// runExtra are a task run's flags after the profile's: Args (unless
// unattended) and AddDirs.
func (a ACP) runExtra(req ExecRequest) []string {
	var extra []string
	if !req.Unattended {
		extra = append(extra, a.Args...)
	}
	if a.Profile.AddDirFlag != "" {
		for _, d := range req.AddDirs {
			extra = append(extra, a.Profile.AddDirFlag, d)
		}
	}
	return extra
}

// sharedEnv is the environment of work for another member: the runner's
// API key, and a config folder of the agent's own when the profile can
// move it.
func (a ACP) sharedEnv(dir string) (env []string, key string, err error) {
	p := a.Profile
	if p.KeyEnv == "" {
		return nil, "", fmt.Errorf("stopped: this run is for another member than the runner's owner, and the %s has no API key setting for that", p.Title)
	}
	key = strings.TrimSpace(os.Getenv(p.KeyEnv))
	if key == "" {
		return nil, "", fmt.Errorf("stopped: this run is for another member than the runner's owner, so it must use the runner's API key (%s), which isn't set", p.KeyEnv)
	}
	if p.HomeEnv != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, "", err
		}
		env = append(env, p.HomeEnv+"="+dir)
	}
	return env, key, nil
}

// Run runs one task turn: a session (new, or continued from req.Resume),
// one prompt, the updates as events.
func (a ACP) Run(ctx context.Context, req ExecRequest, emit func(model.RunEvent)) (ExecResult, error) {
	if err := checkLocal(); err != nil {
		return ExecResult{}, err
	}
	if err := a.ready(); err != nil {
		return ExecResult{}, err
	}
	p := a.Profile
	res := ExecResult{Model: p.model(req.Model)}
	caps := a.Caps()
	if req.Resume != "" && (req.ForkSession && !caps.Fork || !req.ForkSession && caps.Known && !caps.Resume && !caps.Load) {
		// It can't continue that session without changing it, or at all:
		// the caller starts over with the full prompt.
		res.ResumeFailed = true
		return res, fmt.Errorf("the %s can't %s a session", p.Title, map[bool]string{true: "fork", false: "resume"}[req.ForkSession])
	}
	env := slices.Clone(req.Env)
	key := ""
	if p.KeyEnv != "" {
		key = strings.TrimSpace(os.Getenv(p.KeyEnv))
	}
	if req.Shared != "" {
		senv, k, err := a.sharedEnv(req.Shared)
		if err != nil {
			return res, err
		}
		env, key = append(env, senv...), k
	}
	name, args := a.command(a.runExtra(req), true)
	env = localrun.Environ(env...)
	var gitState string
	if len(a.Sandbox) > 0 {
		env = append(env, "DASH_WORKTREE="+req.Dir)
		gitState = gitops.GitDirState(req.Dir)
	}

	// Updates arrive on the connection's reader, in order; a permission
	// request on a goroutine of its own. emit is called from both.
	var mu sync.Mutex
	send := func(e model.RunEvent) {
		mu.Lock()
		defer mu.Unlock()
		emit(e)
	}
	tr := acpTranslator{Profile: p, OnCost: req.OnCost}
	replaying := true // updates before the session is open: a loaded session's history
	var stderrTail []string
	started := time.Now()
	proc, err := startACP(ctx, acpStart{
		Name: name, Args: args, Dir: req.Dir, Env: env,
		OnUpdate: func(u acpUpdate) {
			mu.Lock()
			defer mu.Unlock()
			if replaying {
				return
			}
			for _, d := range tr.Translate(u) {
				if d.Kind == acpStatus && strings.HasPrefix(d.Text, p.warnPrefix()) {
					a.CLI.observed([]string{strings.TrimPrefix(d.Text, p.warnPrefix())})
				}
				if e, ok := runEvent(d); ok {
					emit(e)
				}
			}
		},
		OnPermission: func(r acpPermRequest) bool { return a.decide(ctx, req, r, send) },
		OnStderr: func(l string) {
			mu.Lock()
			stderrTail = append(stderrTail, l)
			if len(stderrTail) > 5 {
				stderrTail = stderrTail[1:]
			}
			mu.Unlock()
			send(model.RunEvent{Kind: "status", Text: "stderr: " + l})
		},
		OnOther: func(l string) { send(model.RunEvent{Kind: "status", Text: l}) },
	})
	if err != nil {
		if proc == nil {
			return res, err
		}
		return res, fmt.Errorf("%s: %w%s", p.Title, err, tail(stderrTail))
	}
	a.CLI.initialized(proc.init)
	caps = proc.init.caps()
	defer func() {
		_ = proc.close()
		if n := proc.conn.skipped(); n > 0 {
			send(model.RunEvent{Kind: "status", Text: skippedLines(n, "output")})
		}
	}()
	for _, w := range a.CLI.CLIStatus().Warnings {
		send(model.RunEvent{Kind: "status", Text: p.warnPrefix() + w})
	}
	servers, dropped := acpServers(req.MCPServers, req.PermissionTool.Server, caps.HTTP)
	if len(dropped) > 0 {
		send(model.RunEvent{Kind: "status", Text: fmt.Sprintf("the %s can't use MCP servers over HTTP: the run goes without %s", p.Title, strings.Join(dropped, ", "))})
	}
	open := acpOpen{Cwd: req.Dir, Servers: servers, Key: key, Always: req.Shared != ""}
	if req.Resume != "" {
		open.Resume, open.Fork = req.Resume, req.ForkSession
	}
	info, err := proc.open(ctx, p, open)
	mu.Lock()
	replaying = false
	mu.Unlock()
	if errors.Is(err, errACPLoggedOut) {
		a.CLI.signedOut()
		return res, a.loggedOut()
	}
	if err != nil {
		if req.Resume != "" && ctx.Err() == nil {
			// The agent doesn't have the session (it ran on another
			// computer, in another directory, or was deleted).
			res.ResumeFailed = true
		}
		return res, fmt.Errorf("%s: open the session: %w", p.Title, err)
	}
	res.SessionID = info.SessionID
	verb := "started"
	if req.Resume != "" {
		verb = "continued"
	}
	send(model.RunEvent{Kind: "status", Text: fmt.Sprintf("▶ %s session %s", p.Title, verb)})
	mode := p.TaskMode
	if req.Unattended {
		mode = p.UnattendedMode
	}
	if req.Model != "" && res.Model == "" {
		send(model.RunEvent{Kind: "status", Text: "model " + req.Model + " isn't one of the " + p.Title + "'s: it runs on its default model"})
	}
	for _, n := range proc.configure(ctx, info, mode, res.Model, p.Effort[req.Effort]) {
		send(model.RunEvent{Kind: "status", Text: "⚠ " + n})
	}
	if req.Effort != "" && p.Effort[req.Effort] == "" {
		send(model.RunEvent{Kind: "status", Text: "effort " + req.Effort + " doesn't apply to the " + p.Title})
	}
	if req.MaxTurns > 0 {
		send(model.RunEvent{Kind: "status", Text: "the turn limit doesn't apply to the " + p.Title + ": the time limit does"})
	}
	prompt := req.Prompt
	if req.AppendSystem != "" && req.Resume == "" {
		// ACP has no system prompt: it leads the first prompt.
		prompt = "<system-instructions>\n" + req.AppendSystem + "\n</system-instructions>\n\n" + prompt
	}
	stop, usage, perr := proc.prompt(ctx, info.SessionID, prompt)
	mu.Lock()
	for _, d := range tr.Flush() {
		if e, ok := runEvent(d); ok {
			emit(e)
		}
	}
	res.Result = tr.Last
	res.CostUSD = tr.Cost
	mu.Unlock()
	res.Usage = usageOf(usage)
	res.Usage.DurationMS = time.Since(started).Milliseconds()
	if len(a.Sandbox) > 0 && gitops.GitDirState(req.Dir) != gitState {
		return res, fmt.Errorf("%s changed the worktree's git metadata (.git, commondir or config.worktree): Dash won't run git there", p.Title)
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	why := ""
	switch {
	case perr != nil:
		why = perr.Error() + tail(stderrTail)
	case stop == "end_turn" || stop == "":
	case stop == "max_tokens":
		why = "the agent reached its output token limit"
	case stop == "max_turn_requests":
		why = "the agent reached its own limit of model requests in a turn"
	case stop == "refusal":
		why = "the agent refused to go on"
	case stop == "cancelled":
		why = "the agent cancelled the turn"
	default:
		why = "the turn stopped: " + stop
	}
	if why != "" {
		res.Stop = "error"
		send(model.RunEvent{Kind: "result", Text: "error: " + why, IsError: true})
		return res, fmt.Errorf("%s reported an error: %s", p.Title, why)
	}
	res.Stop = "success"
	send(model.RunEvent{Kind: "result", Text: tr.Last})
	return res, nil
}

// tail is the agent's last stderr lines, to add to an error.
func tail(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return " (stderr: " + strings.Join(lines, "; ") + ")"
}
