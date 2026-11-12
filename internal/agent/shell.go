package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/model"
)

// Shell commands in the chat: a message starting with "!" isn't sent to
// Claude. Its command runs in the chat's working directory, its output
// streams into the chat ("shell_output" deltas, then "shell_done" with the
// result), and the command with its output is saved as a message, so Claude
// sees it on the next turn. There is no terminal: programs that need one
// (vim, top, …) fail or wait until the timeout.

// DefaultShellTimeout is how long a chat command may run when the Dash
// doesn't say.
const DefaultShellTimeout = 5 * time.Minute

// MaxShellOutput caps the output kept of a chat command (saved, and replayed
// to Claude): a longer one keeps its first and last halves.
const MaxShellOutput = 32_000

// maxShellLive caps the output streamed to the chat as it comes; past it,
// only the result (capped as above) is sent.
const maxShellLive = 1 << 20

// ShellNoTTY is the hint added when a command fails or times out: it ran
// without a terminal.
const ShellNoTTY = "Chat commands run without a terminal: interactive programs (vim, top, less, a password prompt, …) don't work here. Use the Shell view for those."

// ReadOnlyChat reports whether chatter is a read-only chat, which runs no
// commands, so no "!" commands either. Running them is opt-in: a chatter
// that doesn't say it isn't read-only (a ReadOnly method) is.
func ReadOnlyChat(chatter Chatter) bool {
	ro, ok := chatter.(interface{ ReadOnly() bool })
	return !ok || ro.ReadOnly()
}

// ReadOnly reports whether the chat only reads files (ChatReadOnly).
func (c ClaudeCodeChat) ReadOnly() bool { return !c.shell() }

// ReadOnly: the API chat runs no tools, so no commands.
func (*Chat) ReadOnly() bool { return true }

// ShellCommand is the command of a chat message typed with a leading "!"
// ("!ls -la" → "ls -la"), and whether it is one.
func ShellCommand(text string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(text), "!")
	rest = strings.TrimSpace(rest)
	return rest, ok && rest != ""
}

// RunShell runs command with the user's shell (sh if unset; Git for
// Windows' bash on Windows) in dir, without a
// terminal or input, and stops it (with everything it started) after
// timeout or when ctx ends. Its output goes to emit as "shell_output" deltas
// as it comes; the result has it capped (CapShellOutput).
func RunShell(ctx context.Context, dir, command string, timeout time.Duration, emit func(ChatDelta)) model.ChatShell {
	res := model.ChatShell{Command: command, Dir: dir, ExitCode: -1}
	if err := localrun.Check("a chat command"); err != nil {
		res.Error = err.Error()
		return res
	}
	if timeout <= 0 {
		timeout = DefaultShellTimeout
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		res.Error = fmt.Sprintf("the chat's directory %s isn't there", dir)
		return res
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sh := os.Getenv("SHELL")
	switch {
	case runtime.GOOS == "windows":
		sh = localrun.Bash() // Git for Windows' ($SHELL, if set, is an MSYS path)
	case sh == "" || !strings.HasPrefix(sh, "/"):
		sh = "/bin/sh"
	}
	cmd := exec.CommandContext(tctx, sh, "-c", command)
	cmd.Dir = dir
	cmd.Env = localrun.Environ("TERM=dumb", "PAGER=cat", "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0")
	// Its own process group, so stopping it stops what it started too.
	// Don't wait over 2s on a background process that keeps the output open.
	localrun.Group(cmd, syscall.SIGKILL, 2*time.Second)
	out := &shellOutput{emit: emit}
	cmd.Stdout, cmd.Stderr = out, out // one pipe: stdout and stderr in the order written

	started := time.Now()
	err := cmd.Run()
	res.DurationMS = time.Since(started).Milliseconds()
	res.Output, res.Truncated = out.result()
	var exit *exec.ExitError
	switch {
	case errors.Is(tctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
		res.Error = fmt.Sprintf("timed out after %s and was stopped", timeout)
	case ctx.Err() != nil:
		res.Error = "stopped"
	case errors.As(err, &exit) && exit.Exited():
		res.ExitCode = exit.ExitCode()
	case errors.As(err, &exit):
		res.Error = exit.String() // killed by a signal
	case errors.Is(err, exec.ErrWaitDelay):
		res.ExitCode = 0 // exited; something it left in the background kept the output open
	case err != nil:
		res.Error = err.Error()
	default:
		res.ExitCode = 0
	}
	return res
}

// shellOutput collects a command's output, keeping its first and last
// MaxShellOutput/2 bytes, and streams it to the chat.
type shellOutput struct {
	emit func(ChatDelta)

	mu      sync.Mutex
	head    []byte // the start
	tail    []byte // the last bytes after the start
	dropped int    // bytes left out between them
	lines   int    // newlines in those
	sent    int    // bytes streamed
}

func (o *shellOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	half := MaxShellOutput / 2
	rest := p
	if n := min(half-len(o.head), len(rest)); n > 0 {
		o.head = append(o.head, rest[:n]...)
		rest = rest[n:]
	}
	o.tail = append(o.tail, rest...)
	if over := len(o.tail) - half; over > 0 {
		o.dropped += over
		o.lines += strings.Count(string(o.tail[:over]), "\n")
		o.tail = append(o.tail[:0], o.tail[over:]...)
	}
	if o.emit != nil && o.sent < maxShellLive {
		chunk := p[:min(len(p), maxShellLive-o.sent)]
		o.sent += len(chunk)
		o.emit(ChatDelta{Kind: "shell_output", Text: string(chunk)})
		if o.sent >= maxShellLive {
			o.emit(ChatDelta{Kind: "shell_output", Text: "\n[too much output to show as it comes: its start and end are kept when it ends]\n"})
		}
	}
	return len(p), nil
}

// result is the output kept, and whether some was left out.
func (o *shellOutput) result() (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.dropped == 0 {
		return string(o.head) + string(o.tail), false
	}
	return joinCut(o.head, o.tail, o.dropped, o.lines), true
}

// joinCut is head and tail with a note of the bytes (and lines) left out
// between them, cut at character boundaries.
func joinCut(head, tail []byte, dropped, lines int) string {
	if i := lastRuneStart(head); i >= 0 && !utf8.FullRune(head[i:]) {
		dropped += len(head) - i
		head = head[:i]
	}
	for k := 0; k < utf8.UTFMax && len(tail) > 0 && !utf8.RuneStart(tail[0]); k++ {
		tail, dropped = tail[1:], dropped+1
	}
	return fmt.Sprintf("%s\n[… %d bytes (%d lines) of output left out …]\n%s", head, dropped, lines, tail)
}

func lastRuneStart(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			return i
		}
	}
	return -1
}

// CapShellOutput caps s as a command's output is kept: its first and last
// MaxShellOutput/2 bytes with a note of what was left out between them, and
// whether it cut.
func CapShellOutput(s string) (string, bool) {
	if len(s) <= MaxShellOutput {
		return s, false
	}
	half := MaxShellOutput / 2
	mid := s[half : len(s)-half]
	return joinCut([]byte(s[:half]), []byte(s[len(s)-half:]), len(mid), strings.Count(mid, "\n")), true
}

// ShellText is how Claude reads a chat message: a "!" command (m.Shell set)
// as a command the user ran themselves, with its output (capped) and how it
// ended; any other message as it is.
func ShellText(m ChatMessage) string {
	sh := m.Shell
	if sh == nil {
		return m.Content
	}
	var b strings.Builder
	b.WriteString("I ran this shell command myself from the chat (you didn't run it; this is for your information)")
	if sh.Dir != "" {
		fmt.Fprintf(&b, ", in %s", sh.Dir)
	}
	b.WriteString(":\n<user_shell_command>\n")
	fmt.Fprintf(&b, "<command>%s</command>\n", sh.Command)
	out, cut := CapShellOutput(sh.Output)
	if strings.TrimSpace(out) == "" {
		b.WriteString("<output>(no output)</output>\n")
	} else {
		fmt.Fprintf(&b, "<output truncated=\"%t\">\n%s\n</output>\n", cut || sh.Truncated, strings.TrimRight(out, "\n"))
	}
	if sh.Error != "" {
		fmt.Fprintf(&b, "<status>%s</status>\n", sh.Error)
	} else {
		fmt.Fprintf(&b, "<exit_code>%d</exit_code>\n", sh.ExitCode)
	}
	b.WriteString("</user_shell_command>")
	return b.String()
}

// WithShellText turns the "!" commands of a conversation into the text
// Claude reads (ShellText), so what adds to the messages' text (a task's
// context, attachments) reaches Claude whatever kind of message it is.
func WithShellText(history []ChatMessage) []ChatMessage {
	out := append([]ChatMessage(nil), history...)
	for i, m := range out {
		if m.Shell != nil {
			out[i].Content, out[i].Shell = ShellText(m), nil
		}
	}
	return out
}
