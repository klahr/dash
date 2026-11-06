// Package agent runs Claude: headless Claude Code inside a worktree for Todo
// execution, and Claude Code or the Messages API for general chat.
package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/klahr/dash/internal/model"
)

// ExecRequest is one Claude run inside a worktree.
type ExecRequest struct {
	Dir       string
	Prompt    string
	Resume    string   // Claude Code session to continue, if any
	ExtraArgs []string // per-run flags, e.g. the permission prompt tool
	Env       []string
}

// ExecResult describes a finished run.
type ExecResult struct {
	SessionID string
	CostUSD   float64
	Turns     int
	// ResumeFailed is set when Resume named a session Claude Code no longer
	// has; the caller should retry with a full prompt.
	ResumeFailed bool
	// Stop is the result subtype, e.g. "success" or "error_max_turns".
	Stop string
}

// Executor carries out a Todo inside its worktree. emit receives
// human-readable progress lines as they happen.
type Executor interface {
	Run(ctx context.Context, req ExecRequest, emit func(string)) (ExecResult, error)
}

// ClaudeCode runs `claude -p` with stream-json output in the worktree. Claude
// Code supplies the agent loop and file/bash tools; Dash supplies isolation
// (the worktree), the commit, tests and review.
type ClaudeCode struct {
	Bin  string   // default "claude"
	Args []string // extra flags, e.g. permission mode / allowed tools
	// OnRateLimit receives the rate_limit_info of every rate_limit_event.
	OnRateLimit func(json.RawMessage)
}

func (c ClaudeCode) Run(ctx context.Context, req ExecRequest, emit func(string)) (ExecResult, error) {
	bin := c.Bin
	if bin == "" {
		bin = "claude"
	}
	args := append([]string{"-p", "--output-format", "stream-json", "--verbose"}, c.Args...)
	args = append(args, req.ExtraArgs...)
	if req.Resume != "" {
		args = append(args, "--resume", req.Resume)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = req.Dir
	cmd.Env = append(os.Environ(), req.Env...)
	cmd.Stdin = strings.NewReader(req.Prompt)
	// Own process group so cancel kills Claude and everything it spawned.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second

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
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		pumpLines(stderr, func(l string) {
			if strings.Contains(l, "No conversation found with session ID") {
				res.ResumeFailed = true
			}
			emit("stderr: " + l)
		})
	}()

	var resultErr error
	pumpLines(stdout, func(l string) {
		ev, ok := parseStreamLine(l)
		if !ok {
			emit(l)
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
		case "result":
			res.CostUSD, res.Turns, res.Stop = ev.Cost, ev.NumTurns, ev.Subtype
			if ev.IsError || ev.Subtype != "success" {
				resultErr = fmt.Errorf("claude reported %s: %s", ev.Subtype, strings.TrimSpace(ev.Result))
			}
		}
		if line := summarize(ev); line != "" {
			emit(line)
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
	return res, resultErr
}

// BuildPrompt is the full prompt for a todo's first run (or when its
// session can't be resumed): the task plus every note so far.
func BuildPrompt(t model.Todo) string {
	var b strings.Builder
	if t.Title != "" {
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
	RateLimitInfo json.RawMessage `json:"rate_limit_info"`
	Message       struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

func parseStreamLine(raw string) (streamEvent, bool) {
	var ev streamEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil || ev.Type == "" {
		return ev, false
	}
	return ev, true
}

// summarize converts one stream-json event into a short progress line.
func summarize(ev streamEvent) string {
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			return "▶ Claude Code session started"
		}
	case "assistant":
		var parts []string
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				parts = append(parts, c.Text)
			case "tool_use":
				parts = append(parts, fmt.Sprintf("🔧 %s %s", c.Name, truncate(string(c.Input), 200)))
			}
		}
		return strings.Join(parts, "\n")
	case "result":
		if ev.IsError || ev.Subtype != "success" {
			return fmt.Sprintf("✖ %s: %s", ev.Subtype, ev.Result)
		}
		return fmt.Sprintf("✔ Claude finished (%d turns, ≈$%.2f)\n%s", ev.NumTurns, ev.Cost, ev.Result)
	}
	return "" // tool results etc. are noise for the dashboard
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
