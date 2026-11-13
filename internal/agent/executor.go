// Package agent runs Claude: headless Claude Code inside a worktree for Todo
// execution, and Claude Code or the Messages API for general chat.
package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/klahr/dash/internal/claudecli"
	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/textutil"
)

// ExecRequest is one Claude run inside a worktree.
type ExecRequest struct {
	Dir    string
	Prompt string
	Resume string // Claude Code session to continue, if any
	// ForkSession continues Resume in a new session (--fork-session),
	// leaving Resume's own untouched: a task building on another's.
	ForkSession bool
	ExtraArgs   []string // per-run flags, e.g. the permission prompt tool
	Env         []string
}

// ExecResult describes a finished run.
type ExecResult struct {
	SessionID string
	CostUSD   float64
	Turns     int
	// Model is the model Claude Code reported when the session started.
	Model string
	// Usage is the run's tokens (and duration) from the result event.
	Usage model.ChatUsage
	// ResumeFailed is set when Resume named a session Claude Code no longer
	// has; the caller should retry with a full prompt.
	ResumeFailed bool
	// Stop is the result subtype, e.g. "success" or "error_max_turns".
	Stop string
	// Result is Claude's final message.
	Result string
}

// Executor carries out a Todo inside its worktree. emit receives the run's
// events (text, tool calls and results, the result) as they happen; Summary
// turns one into a human-readable progress line.
type Executor interface {
	Run(ctx context.Context, req ExecRequest, emit func(model.RunEvent)) (ExecResult, error)
}

// ClaudeCode runs `claude -p` with stream-json output in the worktree. Claude
// Code supplies the agent loop and file/bash tools; Dash supplies isolation
// (the worktree), the commit, tests and review.
type ClaudeCode struct {
	Bin  string   // default "claude"
	Args []string // extra flags, e.g. permission mode / allowed tools
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
	bin := c.Bin
	if bin == "" {
		bin = localrun.Claude
	}
	args := append([]string{"-p", "--output-format", "stream-json", "--verbose"}, c.Args...)
	args = append(args, req.ExtraArgs...)
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
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = req.Dir
	cmd.Env = localrun.Environ(req.Env...)
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
		return ExecResult{}, fmt.Errorf("start %s: %w", bin, err)
	}

	var res ExecResult
	check := c.CLI.Stream()
	for _, w := range check.Start() {
		emit(cliWarning(w))
	}
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		pumpLines(stderr, func(l string) {
			if strings.Contains(l, "No conversation found with session ID") {
				res.ResumeFailed = true
			}
			emit(model.RunEvent{Kind: "status", Text: "stderr: " + l})
		})
	}()

	var resultErr error
	pumpLines(stdout, func(l string) {
		for _, w := range check.Line(l) {
			emit(cliWarning(w))
		}
		ev, ok := parseStreamLine(l)
		if !ok {
			emit(model.RunEvent{Kind: "status", Text: l})
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
	<-stderrDone
	if err := cmd.Wait(); err != nil {
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

// BuildPrompt is the full prompt for a todo's first run (or when its
// session can't be resumed): the task plus every note so far.
func BuildPrompt(t model.Todo) string {
	var b strings.Builder
	// A prompt that starts with a slash command (e.g. "/init") must reach
	// Claude first to run as one, so it goes without the title.
	if t.Title != "" && !slashCommand.MatchString(t.Prompt) {
		fmt.Fprintf(&b, "# Task: %s\n\n", t.Title)
	}
	b.WriteString(t.Prompt)
	if len(t.Attachments) > 0 {
		b.WriteString("\n\n## Attached files\n")
		b.WriteString(AttachmentList(t.Attachments))
	}
	if len(t.Feedback) > 0 {
		b.WriteString("\n\n## Follow-up notes\n")
		b.WriteString("Earlier work on this task may already be in this working tree. Address the following:\n")
		for i, f := range t.Feedback {
			fmt.Fprintf(&b, "\n### Note %d\n%s\n", i+1, f)
		}
	}
	b.WriteString(rules)
	return b.String()
}

// FollowUpPrompt is the first prompt of task t when it builds on an earlier
// task, parent, whose Claude session it continues as a fork: it closes the
// earlier task and gives the new one in full (BuildPrompt).
func FollowUpPrompt(t, parent model.Todo) string {
	var b strings.Builder
	name := parent.Title
	if name == "" {
		name = textutil.FirstLine(parent.Prompt)
	}
	fmt.Fprintf(&b, "The task above (%q) is finished", name)
	switch {
	case parent.Status == model.StatusMerged && parent.Revert != nil:
		b.WriteString(": its changes were merged and later reverted, so they are not in the default branch.")
	case parent.Status == model.StatusMerged:
		b.WriteString(" and merged: its changes are in the default branch.")
	default:
		b.WriteString(", but it was not merged: its changes are not in the default branch.")
	}
	b.WriteString(" What follows is a new, separate follow-up task. It runs in a new working tree " +
		"(the current directory, not the one you worked in before), branched from the default branch. " +
		"Use what you learned about the code, but read files again before you change them: " +
		"paths and contents from the earlier task may differ here.\n\n")
	b.WriteString(BuildPrompt(t))
	return b.String()
}

// AttachmentList tells Claude where attached files are.
func AttachmentList(as []model.Attachment) string {
	var b strings.Builder
	b.WriteString("Look at these files; the Read tool shows images:\n")
	for _, a := range as {
		fmt.Fprintf(&b, "- %s (%s)\n", a.Path, a.Name)
	}
	return b.String()
}

// ContinuePrompt is sent to a resumed session: only the notes Claude hasn't seen.
func ContinuePrompt(notes []string) string {
	var b strings.Builder
	if len(notes) == 1 {
		b.WriteString(notes[0])
	} else {
		b.WriteString("Please address the following:\n")
		for i, n := range notes {
			fmt.Fprintf(&b, "\n### %d\n%s\n", i+1, n)
		}
	}
	b.WriteString(rules)
	return b.String()
}

// slashCommand matches a prompt that starts with a slash command like
// "/init" or "/plugin:skill", not a path like "/api/x".
var slashCommand = regexp.MustCompile(`^/[A-Za-z][\w:-]*(\s|$)`)

const rules = "\n\nWork only inside the current directory. Do not run git commit, create branches or " +
	"change git history; Dash commits your changes for review when you finish."

func pumpLines(r io.Reader, fn func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		fn(sc.Text())
	}
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
	Model         string          `json:"model"` // system init
	Usage         *cliUsage       `json:"usage"` // result
	RateLimitInfo json.RawMessage `json:"rate_limit_info"`
	Message       struct {
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

// Summary converts one run event into a short progress line for the text
// log; "" leaves it out.
func Summary(e model.RunEvent) string {
	switch e.Kind {
	case "status", "text":
		return e.Text
	case "tool_use":
		return toolLine(e.Tool, e.Input)
	case "result":
		if e.IsError {
			return "✖ " + e.Text
		}
		return fmt.Sprintf("✔ Claude finished (%d turns, ≈$%.2f)\n%s", e.Turns, e.CostUSD, e.Text)
	}
	return "" // tool results and run boundaries are noise for the text log
}

// toolLine shows a tool call the way a person reads it: "$ ls" for a shell
// command, "🔧 Read a.go" for the rest; the raw JSON only when the input has
// none of the usual fields.
func toolLine(tool string, input json.RawMessage) string {
	var in struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
		Pattern  string `json:"pattern"`
		Path     string `json:"path"`
		URL      string `json:"url"`
		Query    string `json:"query"`
	}
	_ = json.Unmarshal(input, &in)
	if tool == "Bash" && in.Command != "" {
		return "$ " + textutil.Summary(in.Command, 200)
	}
	for _, s := range []string{in.Command, in.FilePath, in.Pattern, in.Path, in.URL, in.Query} {
		if s != "" {
			return fmt.Sprintf("🔧 %s %s", tool, textutil.Summary(s, 200))
		}
	}
	if s := strings.TrimSpace(string(input)); s == "" || s == "{}" || s == "null" {
		return "🔧 " + tool
	}
	return fmt.Sprintf("🔧 %s %s", tool, textutil.Clip(string(input), 200))
}
