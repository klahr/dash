package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Chatter streams one assistant reply to a conversation. dir is the working
// directory to answer from (a project's main checkout), or "" for the default.
type Chatter interface {
	Stream(ctx context.Context, dir string, history []ChatMessage, emit func(ChatDelta)) error
}

// ClaudeCodeChat answers chat through the Claude Code CLI, so it bills the
// account `claude` is logged into (e.g. a Pro/Max subscription) instead of an
// API key. Each turn is a fresh, tool-less `claude -p` run with the
// conversation so far in the prompt.
type ClaudeCodeChat struct {
	Bin    string // default "claude"
	Model  string // optional --model
	System string
	Dir    string // working directory when Stream gets none; default os.TempDir()
	// OnRateLimit receives the rate_limit_info of every rate_limit_event.
	OnRateLimit func(json.RawMessage)
}

func (c ClaudeCodeChat) Stream(ctx context.Context, dir string, history []ChatMessage, emit func(ChatDelta)) error {
	if len(history) == 0 {
		return fmt.Errorf("empty conversation")
	}
	bin := c.Bin
	if bin == "" {
		bin = "claude"
	}
	system := c.System
	if system == "" {
		system = (&Chat{}).defaultSystem() +
			" You run in the project's checkout and can read its files with Read, Grep and Glob; " +
			"look at the code before answering questions about it. You cannot edit files or run commands."
	}
	args := []string{
		"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--tools", "Read,Grep,Glob", // read-only: no edits, no shell
		"--no-session-persistence",
		"--strict-mcp-config",   // don't load the user's MCP servers
		"--setting-sources", "", // nor their settings/hooks
		"--system-prompt", system,
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	if cmd.Dir == "" {
		cmd.Dir = c.Dir
	}
	if cmd.Dir == "" {
		cmd.Dir = os.TempDir()
	} else if err := os.MkdirAll(cmd.Dir, 0o755); err != nil {
		return err
	}
	cmd.Stdin = strings.NewReader(transcript(history))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", bin, err)
	}

	var resultErr error
	var wroteText, newMessage bool
	pumpLines(stdout, func(line string) {
		var ev struct {
			Type          string          `json:"type"`
			IsError       bool            `json:"is_error"`
			Result        string          `json:"result"`
			RateLimitInfo json.RawMessage `json:"rate_limit_info"`
			Event         struct {
				Type  string `json:"type"`
				Delta struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					Thinking string `json:"thinking"`
				} `json:"delta"`
			} `json:"event"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			return
		}
		switch ev.Type {
		case "rate_limit_event":
			if c.OnRateLimit != nil && len(ev.RateLimitInfo) > 0 {
				c.OnRateLimit(ev.RateLimitInfo)
			}
		case "stream_event":
			if ev.Event.Type == "message_start" {
				newMessage = true
			}
			if ev.Event.Type != "content_block_delta" {
				return
			}
			switch ev.Event.Delta.Type {
			case "text_delta":
				text := ev.Event.Delta.Text
				// Tool use splits a reply across messages; keep their text apart.
				if newMessage && wroteText {
					text = "\n\n" + text
				}
				newMessage, wroteText = false, true
				emit(ChatDelta{Kind: "text", Text: text})
			case "thinking_delta":
				emit(ChatDelta{Kind: "thinking", Text: ev.Event.Delta.Thinking})
			}
		case "result":
			if ev.IsError {
				resultErr = fmt.Errorf("claude: %s", ev.Result)
			}
		}
	})
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if resultErr != nil {
			return resultErr
		}
		return fmt.Errorf("claude exited: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if resultErr != nil {
		return resultErr
	}
	emit(ChatDelta{Kind: "done"})
	return nil
}

// transcript renders the conversation as one prompt. A single question is
// passed through as-is.
func transcript(history []ChatMessage) string {
	if len(history) == 1 {
		return history[0].Content
	}
	var b strings.Builder
	b.WriteString("Here is our conversation so far:\n\n<conversation>\n")
	for _, m := range history[:len(history)-1] {
		fmt.Fprintf(&b, "<%s>\n%s\n</%s>\n", m.Role, m.Content, m.Role)
	}
	b.WriteString("</conversation>\n\nReply to my latest message:\n\n")
	b.WriteString(history[len(history)-1].Content)
	return b.String()
}
