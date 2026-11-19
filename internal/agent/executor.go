// Package agent runs Claude: headless Claude Code inside a worktree for Todo
// execution, and Claude Code or the Messages API for general chat.
package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/textutil"
)

// ExecRequest is one agent run inside a worktree. Its fields say what the
// run needs, not how a CLI spells it: the provider turns them into its own
// flags (ClaudeCode's are in RunArgs).
type ExecRequest struct {
	Dir    string
	Prompt string
	// Resume is the session to continue, if any; only a Resumer takes it.
	Resume string
	// ForkSession continues Resume in a new session, leaving Resume's own
	// untouched: a task building on another's.
	ForkSession bool
	// Model is the model to run on ("": the provider's default, or one in
	// its Config.Args).
	Model string
	// Effort is the reasoning effort, e.g. "high" ("": the default).
	Effort string
	// MaxTurns stops the run after that many turns (0: no limit); the
	// result's Stop is then StopMaxTurns.
	MaxTurns int
	// AddDirs are directories outside Dir the run may read (attachments).
	AddDirs []string
	// AppendSystem is added to the provider's system prompt.
	AppendSystem string
	// MCPServers are MCP servers for the run, by name, each in the shape
	// of mcptools.ServerConfig; only an MCPConfigurer takes them.
	MCPServers map[string]any
	// AllowMCPTools are tools on MCPServers the run may call without
	// asking: server name → tool names.
	AllowMCPTools map[string][]string
	// PermissionTool is the tool, on a server in MCPServers, that is asked
	// before a tool call the run isn't allowed outright; only a
	// PermissionHook takes it.
	PermissionTool MCPTool
	// ToolTimeout is how long the run waits on one MCP tool call (0: the
	// provider's default), e.g. while a person answers a permission prompt.
	ToolTimeout time.Duration
	// Unattended marks a run nobody watches (a scan): it goes without the
	// provider's task flags (Config.Args) and the user's settings and hooks,
	// and any tool call outside AllowedTools is refused, never asked about.
	Unattended bool
	// AllowedTools and DeniedTools are tool permission rules for the run, in
	// Claude Code's syntax ("Read", "Bash(npm audit:*)", "Read(//abs/**)"),
	// which a provider maps to its own.
	AllowedTools, DeniedTools []string
	Env                       []string
	// Shared, when set, is the Claude config folder of work for another
	// member than this runner's owner: Claude runs with the API key in
	// claudecli.KeyEnv and without any subscription login
	// (claudecli.SharedEnv), and the run stops if Claude Code says it signed
	// in another way.
	Shared string
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
	// Stop is how the run ended, e.g. "success" or StopMaxTurns.
	Stop string
	// Result is Claude's final message.
	Result string
}

// StopMaxTurns is ExecResult.Stop of a run that used its MaxTurns.
const StopMaxTurns = "error_max_turns"

// MCPTool is a tool on an MCP server; the zero value is none.
type MCPTool struct{ Server, Tool string }

// Executor carries out a Todo inside its worktree. emit receives the run's
// events (text, tool calls and results, the result) as they happen; Summary
// turns one into a human-readable progress line.
type Executor interface {
	Run(ctx context.Context, req ExecRequest, emit func(model.RunEvent)) (ExecResult, error)
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
	case parent.Status == model.StatusMerged && parent.NoChanges && parent.MergedCommit == "":
		b.WriteString(" and closed: it changed no files, so nothing was merged.")
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

// maxLine is the longest output line pumpLines passes on.
const maxLine = 16 * 1024 * 1024

// skippedLines tells that pumpLines skipped n lines of Claude's stream.
func skippedLines(n int, stream string) string {
	return fmt.Sprintf("skipped %d %s line(s) over %d MB", n, stream, maxLine>>20)
}

// pumpLines calls fn with each line read from r, without its line ending,
// until r ends. A line longer than maxLine is skipped, not passed on, and
// reading goes on after it: r is always read to the end, so the process
// writing it never blocks on a full pipe. It returns how many lines it skipped.
func pumpLines(r io.Reader, fn func(string)) (skipped int) {
	return pumpLinesMax(r, maxLine, fn)
}

func pumpLinesMax(r io.Reader, max int, fn func(string)) (skipped int) {
	br := bufio.NewReaderSize(r, 64*1024)
	var line []byte
	tooLong := false
	for {
		chunk, err := br.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(chunk) > max+1 { // +1: the newline doesn't count
				tooLong, line = true, line[:0]
			} else {
				line = append(line, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue // the line goes on
		}
		switch {
		case tooLong:
			skipped++
		case err == nil || len(line) > 0: // a last line without a newline counts too
			line = bytes.TrimSuffix(line, []byte("\n"))
			line = bytes.TrimSuffix(line, []byte("\r"))
			fn(string(line))
		}
		line, tooLong = line[:0], false
		if err != nil {
			return skipped
		}
	}
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
