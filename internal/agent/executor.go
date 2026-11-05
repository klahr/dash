// Package agent runs Claude: headless Claude Code inside a worktree for Todo
// execution, and the Messages API for general chat.
package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/klahr/dash/internal/model"
)

// Executor carries out a Todo inside its worktree. emit receives
// human-readable progress lines as they happen.
type Executor interface {
	Run(ctx context.Context, todo model.Todo, dir string, emit func(string)) error
}

// ClaudeCode runs `claude -p` with stream-json output in the worktree. Claude
// Code supplies the agent loop and file/bash tools; Dash supplies isolation
// (the worktree), the commit, and review.
type ClaudeCode struct {
	Bin  string   // default "claude"
	Args []string // extra flags, e.g. permission mode / allowed tools
}

func (c ClaudeCode) Run(ctx context.Context, todo model.Todo, dir string, emit func(string)) error {
	bin := c.Bin
	if bin == "" {
		bin = "claude"
	}
	args := append([]string{"-p", "--output-format", "stream-json", "--verbose"}, c.Args...)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(BuildPrompt(todo))
	// Own process group so cancel kills Claude and everything it spawned.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", bin, err)
	}
	go pumpLines(stderr, func(l string) { emit("stderr: " + l) })

	var resultErr error
	pumpLines(stdout, func(l string) {
		line, isErr := summarizeStreamJSON(l)
		if isErr {
			resultErr = fmt.Errorf("claude reported an error: %s", line)
		}
		if line != "" {
			emit(line)
		}
	})
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("claude exited: %w", err)
	}
	return resultErr
}

// BuildPrompt turns a Todo (plus any reviewer feedback) into the agent prompt.
func BuildPrompt(t model.Todo) string {
	var b strings.Builder
	if t.Title != "" {
		fmt.Fprintf(&b, "# Task: %s\n\n", t.Title)
	}
	b.WriteString(t.Prompt)
	if len(t.Feedback) > 0 {
		b.WriteString("\n\n## Reviewer feedback on your previous attempt\n")
		b.WriteString("Your earlier changes are already in this working tree. Address the following:\n")
		for i, f := range t.Feedback {
			fmt.Fprintf(&b, "%d. %s\n", i+1, f)
		}
	}
	b.WriteString("\n\nWork only inside the current directory. Do not run git commit or create branches; " +
		"Dash commits your changes for review when you finish.")
	return b.String()
}

func pumpLines(r io.Reader, fn func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		fn(sc.Text())
	}
}

// summarizeStreamJSON converts one Claude Code stream-json line into a short
// progress line. Unknown shapes are passed through untouched.
func summarizeStreamJSON(raw string) (line string, isError bool) {
	var ev struct {
		Type    string  `json:"type"`
		Subtype string  `json:"subtype"`
		IsError bool    `json:"is_error"`
		Result  string  `json:"result"`
		Cost    float64 `json:"total_cost_usd"`
		Message struct {
			Content []struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return raw, false
	}
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			return "▶ Claude Code session started", false
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
		return strings.Join(parts, "\n"), false
	case "result":
		if ev.IsError || ev.Subtype != "success" {
			return fmt.Sprintf("✖ %s: %s", ev.Subtype, ev.Result), true
		}
		return fmt.Sprintf("✔ done ($%.4f)\n%s", ev.Cost, ev.Result), false
	}
	return "", false // user/tool_result echoes etc. are noise for the dashboard
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
