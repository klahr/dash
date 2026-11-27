package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/klahr/dash/internal/claudecli"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/model"
)

// errNotAPIKey is why a shared run stopped: Claude Code didn't sign in
// with the API key.
type errNotAPIKey struct{ source string }

func (e errNotAPIKey) Error() string {
	return "stopped: this run is for another member than the runner's owner, so it must use the runner's API key (" + claudecli.KeyEnv +
		"), but Claude Code signed in with " + e.source
}

// ClaudeCode is the Claude Code provider (registered as "claude"): it runs
// `claude -p` with stream-json output in the worktree. Claude Code supplies
// the agent loop and file/bash tools; Dash supplies isolation (the
// worktree), the commit, tests and review. The stream-json format is read
// here and in ClaudeCodeChat only: the rest of Dash gets model.RunEvent and
// ChatDelta.
type ClaudeCode struct {
	Bin string // default "claude"
	// Args are extra flags for task runs (-claude-args), e.g. permission
	// mode / allowed tools; unattended runs (scans) go without them.
	Args []string
	// Sandbox, if set, is a command that runs Claude for Dash: Dash
	// starts Sandbox[0] with the rest of Sandbox, then Bin and its
	// arguments, in the worktree, e.g. a script that runs them as a user
	// with fewer rights or in a container (-claude-sandbox, see
	// docs/sandbox.md). The run's environment is passed to it, with
	// DASH_WORKTREE naming the worktree.
	Sandbox []string
	// OnRateLimit receives the rate_limit_info of every rate_limit_event.
	OnRateLimit func(json.RawMessage)
	// CLI collects what looks changed about the Claude Code CLI; the run's
	// log shows it too (optional).
	CLI *claudecli.Monitor
}

// cliWarning is how a Claude Code CLI warning shows in a task's log.
func cliWarning(w string) model.RunEvent {
	return model.RunEvent{Kind: "status", Text: "⚠ Claude Code CLI: " + w}
}

func (c ClaudeCode) Run(ctx context.Context, req ExecRequest, emit func(model.RunEvent)) (ExecResult, error) {
	if err := checkLocal(); err != nil {
		return ExecResult{}, err
	}
	bin := c.bin()
	args := c.RunArgs(req)
	name := bin
	if len(c.Sandbox) > 0 {
		name, args = c.Sandbox[0], append(append(c.Sandbox[1:len(c.Sandbox):len(c.Sandbox)], bin), args...)
	}
	if req.Shared != "" {
		if !claudecli.HasKey() {
			return ExecResult{}, errNotAPIKey{source: "no API key: " + claudecli.KeyEnv + " isn't set"}
		}
		if err := os.MkdirAll(req.Shared, 0o700); err != nil {
			return ExecResult{}, err
		}
	}
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = req.Dir
	cmd.Env = localrun.Environ(runEnv(req)...)
	if req.Shared != "" {
		cmd.Env = claudecli.SharedEnv(cmd.Env, req.Shared)
	}
	var gitState string
	if len(c.Sandbox) > 0 {
		cmd.Env = append(cmd.Env, "DASH_WORKTREE="+req.Dir)
		gitState = gitops.GitDirState(req.Dir)
	}
	cmd.Stdin = strings.NewReader(req.Prompt)
	// Own process group so cancel kills Claude and everything it spawned.
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

	var res ExecResult
	check := c.CLI.Stream()
	for _, w := range check.Start() {
		emit(cliWarning(w))
	}
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		n := pumpLines(stderr, func(l string) {
			if strings.Contains(l, "No conversation found with session ID") {
				res.ResumeFailed = true
			}
			emit(model.RunEvent{Kind: "status", Text: "stderr: " + l})
		})
		if n > 0 {
			emit(model.RunEvent{Kind: "status", Text: skippedLines(n, "stderr")})
		}
	}()

	var resultErr error
	var cost streamCost // the run's cost so far, estimated from its messages' tokens
	sawInit := false    // Claude Code's system init message, which says how it signed in
	skipped := pumpLines(stdout, func(l string) {
		for _, w := range check.Line(l) {
			emit(cliWarning(w))
		}
		ev, ok := parseStreamLine(l)
		if !ok {
			emit(model.RunEvent{Kind: "status", Text: l})
			return
		}
		if req.Shared != "" && !sawInit && ev.Type != "system" {
			stop(errNotAPIKey{source: "something Dash couldn't check (no init message)"})
			return
		}
		if ev.SessionID != "" && res.SessionID == "" {
			res.SessionID = ev.SessionID
		}
		switch ev.Type {
		case "rate_limit_event":
			if c.OnRateLimit != nil && len(ev.RateLimitInfo) > 0 {
				c.OnRateLimit(ev.RateLimitInfo)
			}
			return
		case "system":
			if ev.Subtype == "init" && ev.Model != "" {
				res.Model = ev.Model
			}
			if ev.Subtype == "init" {
				sawInit = true
			}
			if ev.Subtype == "init" && req.Shared != "" && ev.APIKeySource != claudecli.KeyEnv {
				// Before Claude does anything: not on the API key, not at all.
				src := ev.APIKeySource
				if src == "" || src == "none" {
					src = "a subscription login"
				}
				stop(errNotAPIKey{source: src})
			}
		case "assistant":
			if ev.Message.Usage != nil {
				m := ev.Message.Model
				if m == "" {
					m = res.Model
				}
				usd := cost.add(ev.Message.ID, m, ev.Message.Usage.chatUsage())
				if req.OnCost != nil {
					req.OnCost(usd)
				}
			}
		case "result":
			res.CostUSD, res.Turns, res.Stop, res.Result = ev.Cost, ev.NumTurns, ev.Subtype, ev.Result
			res.Usage = ev.Usage.chatUsage()
			res.Usage.DurationMS = ev.DurationMS
			if ev.IsError || ev.Subtype != "success" {
				resultErr = fmt.Errorf("claude reported %s: %s", ev.Subtype, strings.TrimSpace(ev.Result))
			}
		}
		for _, e := range events(ev) {
			emit(e)
		}
	})
	if skipped > 0 {
		emit(model.RunEvent{Kind: "status", Text: skippedLines(skipped, "output")})
	}
	<-stderrDone
	waitErr := cmd.Wait()
	localrun.Reap(cmd)
	if res.CostUSD == 0 && cost.total > 0 {
		// Stopped before its result event (a cap, a time limit, a crash):
		// what its messages' tokens cost, as an estimate.
		res.CostUSD, res.Estimated = cost.total, true
		if res.Usage == (model.ChatUsage{}) {
			res.Usage = cost.usage()
		}
	}
	// Dash runs git in the worktree next, outside the sandbox: a run that
	// pointed .git (or its commondir) elsewhere would choose the config,
	// and so the commands, that git runs then.
	if len(c.Sandbox) > 0 && gitops.GitDirState(req.Dir) != gitState {
		return res, fmt.Errorf("claude changed the worktree's git metadata (.git, commondir or config.worktree): Dash won't run git there")
	}
	if cause := context.Cause(ctx); errors.As(cause, new(errNotAPIKey)) {
		return res, cause
	}
	if err := waitErr; err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if resultErr != nil {
			return res, resultErr
		}
		return res, fmt.Errorf("claude exited: %w", err)
	}
	for _, w := range check.End() {
		emit(cliWarning(w))
	}
	return res, resultErr
}

type streamEvent struct {
	Type          string          `json:"type"`
	Subtype       string          `json:"subtype"`
	SessionID     string          `json:"session_id"`
	IsError       bool            `json:"is_error"`
	Result        string          `json:"result"`
	Cost          float64         `json:"total_cost_usd"`
	NumTurns      int             `json:"num_turns"`
	DurationMS    int64           `json:"duration_ms"`
	Model         string          `json:"model"`        // system init
	APIKeySource  string          `json:"apiKeySource"` // system init: "ANTHROPIC_API_KEY", "none", …
	Usage         *cliUsage       `json:"usage"`        // result
	RateLimitInfo json.RawMessage `json:"rate_limit_info"`
	Message       struct {
		ID      string         `json:"id"`
		Model   string         `json:"model"`
		Usage   *cliUsage      `json:"usage"`
		Content []contentBlock `json:"content"`
	} `json:"message"`
}

func parseStreamLine(raw string) (streamEvent, bool) {
	var ev streamEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil || ev.Type == "" {
		return ev, false
	}
	return ev, true
}

// events converts one stream-json event into the run's events: Claude's text,
// its tool calls with their full input, the tools' results and the result.
func events(ev streamEvent) []model.RunEvent {
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			return []model.RunEvent{{Kind: "status", Text: "▶ Claude Code session started"}}
		}
	case "assistant", "user":
		var out []model.RunEvent
		for _, b := range ev.Message.Content {
			if b.Type == "text" {
				if ev.Type == "assistant" && strings.TrimSpace(b.Text) != "" {
					out = append(out, model.RunEvent{Kind: "text", Text: b.Text})
				}
				continue
			}
			if d, ok := b.delta(); ok {
				out = append(out, model.RunEvent{Kind: d.Kind, Text: d.Text, ID: d.ID, Tool: d.Tool, Input: d.Input, IsError: d.IsError})
			}
		}
		return out
	case "result":
		e := model.RunEvent{Kind: "result", Text: ev.Result, Turns: ev.NumTurns, CostUSD: ev.Cost}
		if ev.IsError || ev.Subtype != "success" {
			e.IsError, e.Text = true, fmt.Sprintf("%s: %s", ev.Subtype, ev.Result)
		}
		return []model.RunEvent{e}
	}
	return nil
}
