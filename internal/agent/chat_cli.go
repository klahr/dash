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

	"github.com/klahr/dash/internal/perm"
)

// Chatter streams one assistant reply to a conversation. dir is the working
// directory to answer from (a project's main checkout), or "" for the default.
type Chatter interface {
	Stream(ctx context.Context, dir string, history []ChatMessage, emit func(ChatDelta)) error
}

// Chat tool modes for ClaudeCodeChat.Tools.
const (
	ChatReadOnly = "readonly" // Read, Grep, Glob
	ChatShell    = "shell"    // also Bash, Edit and Write, each approved by the user
)

// ClaudeCodeChat answers chat through the Claude Code CLI, so it bills the
// account `claude` is logged into (e.g. a Pro/Max subscription) instead of an
// API key. Each turn is a fresh `claude -p` run with the conversation so far
// in the prompt.
type ClaudeCodeChat struct {
	Bin    string // default "claude"
	Model  string // optional --model
	System string
	Dir    string // working directory of the global chat (Stream gets no dir); default os.TempDir()
	Tools  string // ChatReadOnly (default) or ChatShell
	// AllowedTools run without asking in shell mode (--allowedTools), e.g. "Bash(git status *)".
	AllowedTools []string
	// Perms and PermURL serve the --permission-prompt-tool in shell mode. A
	// turn whose ctx carries an approver (WithApprover) asks it before every
	// tool call that isn't allowed already; without one, those calls are denied.
	Perms   *perm.Broker
	PermURL string
	// OnRateLimit receives the rate_limit_info of every rate_limit_event.
	OnRateLimit func(json.RawMessage)
}

type approverKey struct{}

// WithApprover makes chat turns run with ctx ask before each shell-mode tool call.
func WithApprover(ctx context.Context, ask perm.AskFunc) context.Context {
	return context.WithValue(ctx, approverKey{}, ask)
}

func approverFrom(ctx context.Context) perm.AskFunc {
	ask, _ := ctx.Value(approverKey{}).(perm.AskFunc)
	return ask
}

func (c ClaudeCodeChat) shell() bool { return c.Tools == ChatShell }

func (c ClaudeCodeChat) system(dir string) string {
	if c.System != "" {
		return c.System
	}
	where := " You run in the user's home directory"
	if dir != "" {
		where = " You run in the project's checkout"
	}
	s := (&Chat{}).defaultSystem() + where
	if c.shell() {
		s += " and can read files with Read, Grep and Glob, run shell commands with Bash, and edit files with Edit and Write. " +
			"The user approves each command and edit in the chat before it runs; if they deny one, don't retry it, ask what to do instead."
	} else {
		s += " and can read files with Read, Grep and Glob. You cannot edit files or run commands."
	}
	if dir != "" {
		s += " Look at the code before answering questions about it."
	}
	return s
}

// args builds the `claude -p` flags. mcpConfig is Dash's permission prompt
// server, or "" for none.
func (c ClaudeCodeChat) args(dir, mcpConfig string) []string {
	tools := "Read,Grep,Glob"
	if c.shell() {
		tools += ",Bash,Edit,Write"
	}
	args := []string{
		"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--tools", tools,
		"--no-session-persistence",
		"--strict-mcp-config",   // don't load the user's MCP servers
		"--setting-sources", "", // nor their settings/hooks
		"--system-prompt", c.system(dir),
	}
	if c.shell() {
		if mcpConfig != "" {
			args = append(args, "--mcp-config", mcpConfig, "--permission-prompt-tool", perm.PromptTool)
		}
		for _, t := range c.AllowedTools {
			args = append(args, "--allowedTools", t)
		}
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	return args
}

func (c ClaudeCodeChat) Stream(ctx context.Context, dir string, history []ChatMessage, emit func(ChatDelta)) error {
	if len(history) == 0 {
		return fmt.Errorf("empty conversation")
	}
	bin := c.Bin
	if bin == "" {
		bin = "claude"
	}
	var mcpConfig string
	if ask := approverFrom(ctx); c.shell() && ask != nil && c.Perms != nil && c.PermURL != "" {
		token, release := c.Perms.RegisterFunc(ask)
		defer release()
		mcpConfig = perm.MCPConfig(c.PermURL, token)
	}
	cmd := exec.CommandContext(ctx, bin, c.args(dir, mcpConfig)...)
	if mcpConfig != "" {
		// Claude waits on the MCP tool while the user decides; don't let it time out first.
		cmd.Env = append(os.Environ(), "MCP_TOOL_TIMEOUT=3600000")
	}
	cmd.Dir = dir
	if cmd.Dir == "" {
		cmd.Dir = c.Dir
	}
	if cmd.Dir == "" {
		cmd.Dir = os.TempDir()
	} else if err := os.MkdirAll(cmd.Dir, 0o755); err != nil {
		return err
	}
	emit(ChatDelta{Kind: "cwd", Text: cmd.Dir})
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
			Message struct {
				Content []contentBlock `json:"content"`
			} `json:"message"`
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
				if ev.Event.Delta.Thinking == "" {
					return
				}
				emit(ChatDelta{Kind: "thinking", Text: ev.Event.Delta.Thinking})
			}
		case "assistant", "user":
			for _, b := range ev.Message.Content {
				if d, ok := b.delta(); ok {
					emit(d)
				}
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

// maxToolOutput caps the tool output sent to the chat per call.
const maxToolOutput = 20_000

// contentBlock is a tool_use (assistant) or tool_result (user) block of a
// complete stream-json message; text arrives as deltas instead.
type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"` // a string or [{"type":"text","text":…}, …]
	IsError   bool            `json:"is_error"`
}

func (b contentBlock) delta() (ChatDelta, bool) {
	switch b.Type {
	case "tool_use":
		return ChatDelta{Kind: "tool_use", ID: b.ID, Tool: b.Name, Input: b.Input}, true
	case "tool_result":
		var text string
		if json.Unmarshal(b.Content, &text) != nil {
			var parts []struct{ Type, Text string }
			_ = json.Unmarshal(b.Content, &parts)
			var sb strings.Builder
			for _, p := range parts {
				if p.Type == "text" {
					sb.WriteString(p.Text)
				}
			}
			text = sb.String()
		}
		if len(text) > maxToolOutput {
			text = text[:maxToolOutput] + "\n[output truncated]"
		}
		return ChatDelta{Kind: "tool_result", ID: b.ToolUseID, Text: text, IsError: b.IsError}, true
	}
	return ChatDelta{}, false
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
