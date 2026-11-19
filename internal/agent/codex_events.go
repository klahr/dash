package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
)

// The Codex CLI's `codex exec --json` output is JSON Lines, one event per
// line (checked against codex-rs/exec/src/exec_events.rs, Nov 2026):
//
//	{"type":"thread.started","thread_id":"…"}
//	{"type":"turn.started"}
//	{"type":"item.started"|"item.updated"|"item.completed","item":{"id":"item_0","type":"…",…}}
//	{"type":"turn.completed","usage":{"input_tokens":…,"cached_input_tokens":…,"output_tokens":…,"reasoning_output_tokens":…}}
//	{"type":"turn.failed","error":{"message":"…"}}
//	{"type":"error","message":"…"}
//
// Item types: agent_message and reasoning (text), command_execution
// (command, aggregated_output, exit_code, status), file_change (changes:
// path and kind), mcp_tool_call (server, tool, arguments, result, error),
// web_search (query), todo_list (items: text, completed), collab_tool_call
// and error (message). codexTranslator turns them into the deltas the chat
// streams (ChatDelta) and, through runEvent, a task log's model.RunEvent,
// shaped as Claude Code's are: a shell command is a "Bash" tool call with
// its "command", a file change an "Edit" (or "Write") with its
// "file_path", an MCP call mcp__server__tool, a plan a "TodoWrite".
type codexEvent struct {
	Type     string      `json:"type"`
	ThreadID string      `json:"thread_id"` // thread.started
	Usage    *codexUsage `json:"usage"`     // turn.completed
	Error    *codexError `json:"error"`     // turn.failed
	Message  string      `json:"message"`   // error
	Item     *codexItem  `json:"item"`      // item.*
}

type codexError struct {
	Message string `json:"message"`
}

type codexUsage struct {
	InputTokens       int64 `json:"input_tokens"` // cached ones included
	CachedInputTokens int64 `json:"cached_input_tokens"`
	CacheWriteTokens  int64 `json:"cache_write_input_tokens"`
	OutputTokens      int64 `json:"output_tokens"` // reasoning included
	ReasoningTokens   int64 `json:"reasoning_output_tokens"`
}

// chatUsage is u as Dash keeps it: input tokens without the cached ones,
// as Claude Code counts them.
func (u *codexUsage) chatUsage() model.ChatUsage {
	if u == nil {
		return model.ChatUsage{}
	}
	return model.ChatUsage{InputTokens: max(u.InputTokens-u.CachedInputTokens, 0), OutputTokens: u.OutputTokens,
		CacheReadTokens: u.CachedInputTokens, CacheCreationTokens: u.CacheWriteTokens}
}

type codexItem struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Status string `json:"status"` // in_progress, completed, failed, declined
	// agent_message, reasoning
	Text string `json:"text"`
	// command_execution
	Command          string `json:"command"`
	AggregatedOutput string `json:"aggregated_output"`
	ExitCode         *int   `json:"exit_code"`
	// file_change
	Changes []struct {
		Path string `json:"path"`
		Kind string `json:"kind"` // add, delete, update
	} `json:"changes"`
	// mcp_tool_call
	Server    string          `json:"server"`
	Tool      string          `json:"tool"` // also collab_tool_call
	Arguments json.RawMessage `json:"arguments"`
	Result    *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structured_content"`
	} `json:"result"`
	Error *codexError `json:"error"`
	// web_search
	Query string `json:"query"`
	// todo_list
	Items []struct {
		Text      string `json:"text"`
		Completed bool   `json:"completed"`
	} `json:"items"`
	// collab_tool_call
	Prompt string `json:"prompt"`
	// error
	Message string `json:"message"`
}

// parseCodexLine reads one line of `codex exec --json` output; ok is false
// for a line that isn't an event (no JSON object with a type).
func parseCodexLine(line string) (ev codexEvent, ok bool) {
	if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Type == "" {
		return ev, false
	}
	return ev, true
}

// codexStatus is the kind of a codexTranslator delta that is a note for the
// task log only (a warning, a session start); the chat leaves it out.
const codexStatus = "status"

// codexTranslator turns a Codex run's events into deltas, one run at a
// time, and keeps what the run reported: its thread (session), the last
// agent message, its tokens and why it failed.
type codexTranslator struct {
	Thread  string          // thread.started
	Last    string          // the last agent message: the run's result
	Usage   model.ChatUsage // summed over its turns
	Turns   int             // turn.completed events
	Failure string          // turn.failed's message
	// Errors are the error events' messages, the last few: why a run that
	// exits without turn.failed stopped.
	Errors []string

	shown   map[string]bool // tool_use ids sent
	plans   int             // todo_list updates sent
	texts   int             // agent messages sent
	unknown map[string]bool // event and item types warned about
}

// codexWarnPrefix starts a codexStatus delta that warns about the CLI
// itself (its output changed): the CLI checker keeps those.
const codexWarnPrefix = "⚠ Codex CLI: "

// Translate is the deltas of one event.
func (t *codexTranslator) Translate(ev codexEvent) []ChatDelta {
	switch ev.Type {
	case "thread.started":
		if t.Thread == "" {
			t.Thread = ev.ThreadID
		}
		return []ChatDelta{{Kind: codexStatus, Text: "▶ Codex session started"}}
	case "turn.started":
		return nil
	case "turn.completed":
		t.Turns++
		u := ev.Usage.chatUsage()
		t.Usage.InputTokens += u.InputTokens
		t.Usage.OutputTokens += u.OutputTokens
		t.Usage.CacheReadTokens += u.CacheReadTokens
		t.Usage.CacheCreationTokens += u.CacheCreationTokens
		return nil
	case "turn.failed":
		msg := "the turn failed"
		if ev.Error != nil && strings.TrimSpace(ev.Error.Message) != "" {
			msg = strings.TrimSpace(ev.Error.Message)
		}
		t.Failure = msg
		return nil
	case "error":
		msg := strings.TrimSpace(ev.Message)
		if msg == "" {
			return nil
		}
		t.Errors = append(t.Errors, msg)
		if len(t.Errors) > 3 {
			t.Errors = t.Errors[1:]
		}
		return []ChatDelta{{Kind: codexStatus, Text: "⚠ Codex: " + msg}}
	case "item.started", "item.updated", "item.completed":
		if ev.Item == nil {
			return nil
		}
		return t.item(ev.Type, *ev.Item)
	}
	return t.warnUnknown("event", ev.Type)
}

func (t *codexTranslator) warnUnknown(what, typ string) []ChatDelta {
	if t.unknown == nil {
		t.unknown = map[string]bool{}
	}
	if t.unknown[what+typ] {
		return nil
	}
	t.unknown[what+typ] = true
	return []ChatDelta{{Kind: codexStatus, Text: fmt.Sprintf("%sDash doesn't know its %s type %q: its output format may have changed", codexWarnPrefix, what, typ)}}
}

func (t *codexTranslator) item(phase string, it codexItem) []ChatDelta {
	done := phase == "item.completed"
	failed := it.Status == "failed" || it.Status == "declined"
	switch it.Type {
	case "agent_message":
		if !done || strings.TrimSpace(it.Text) == "" {
			return nil
		}
		t.Last = it.Text
		t.texts++
		return []ChatDelta{{Kind: "text", Text: it.Text}}
	case "reasoning":
		if !done || strings.TrimSpace(it.Text) == "" {
			return nil
		}
		return []ChatDelta{{Kind: "thinking", Text: it.Text}}
	case "command_execution":
		input, _ := json.Marshal(map[string]string{"command": it.Command})
		out := t.use(it.ID, "Bash", input)
		if !done {
			return out
		}
		text := it.AggregatedOutput
		if text == "" && it.Status == "declined" {
			text = "declined"
		}
		isErr := failed || it.ExitCode != nil && *it.ExitCode != 0
		return append(out, ChatDelta{Kind: "tool_result", ID: it.ID, Text: clipToolOutput(text), IsError: isErr})
	case "file_change":
		if !done {
			return nil
		}
		var out []ChatDelta
		for i, ch := range it.Changes {
			id := fmt.Sprintf("%s#%d", it.ID, i)
			tool := "Edit"
			switch ch.Kind {
			case "add":
				tool = "Write"
			case "delete":
				tool = "Delete"
			}
			input, _ := json.Marshal(map[string]string{"file_path": ch.Path})
			out = append(out, t.use(id, tool, input)...)
			res := ch.Kind
			if failed {
				res = "failed"
			}
			out = append(out, ChatDelta{Kind: "tool_result", ID: id, Text: res, IsError: failed})
		}
		return out
	case "mcp_tool_call":
		input := it.Arguments
		if len(input) == 0 || string(input) == "null" {
			input = json.RawMessage("{}")
		}
		out := t.use(it.ID, mcptools.ToolName(it.Server, it.Tool), input)
		if !done {
			return out
		}
		var text string
		switch {
		case it.Error != nil:
			text, failed = it.Error.Message, true
		case it.Result != nil:
			var sb strings.Builder
			for _, c := range it.Result.Content {
				if c.Type == "text" {
					sb.WriteString(c.Text)
				}
			}
			text = sb.String()
			if text == "" && len(it.Result.StructuredContent) > 0 {
				text = string(it.Result.StructuredContent)
			}
		}
		return append(out, ChatDelta{Kind: "tool_result", ID: it.ID, Text: clipToolOutput(text), IsError: failed})
	case "web_search":
		if !done {
			return nil
		}
		input, _ := json.Marshal(map[string]string{"query": it.Query})
		return append(t.use(it.ID, "WebSearch", input), ChatDelta{Kind: "tool_result", ID: it.ID})
	case "todo_list":
		// Each version of the plan is a TodoWrite call of its own, as
		// Claude Code's are; the completed item repeats the last one.
		if done && t.plans > 0 {
			return nil
		}
		type todo struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		}
		todos := []todo{}
		for _, x := range it.Items {
			st := "pending"
			if x.Completed {
				st = "completed"
			}
			todos = append(todos, todo{x.Text, st})
		}
		input, _ := json.Marshal(map[string]any{"todos": todos})
		t.plans++
		id := fmt.Sprintf("%s#plan%d", it.ID, t.plans)
		return append(t.use(id, "TodoWrite", input), ChatDelta{Kind: "tool_result", ID: id})
	case "collab_tool_call":
		input, _ := json.Marshal(map[string]string{"description": it.Tool, "prompt": it.Prompt})
		out := t.use(it.ID, "Task", input)
		if !done {
			return out
		}
		return append(out, ChatDelta{Kind: "tool_result", ID: it.ID, Text: it.Status, IsError: failed})
	case "error":
		if !done || strings.TrimSpace(it.Message) == "" {
			return nil
		}
		return []ChatDelta{{Kind: codexStatus, Text: "⚠ Codex: " + strings.TrimSpace(it.Message)}}
	}
	return t.warnUnknown("item", it.Type)
}

// use is the tool_use delta of a call, unless it was sent already (an
// item's started event sends it, its completed one then doesn't).
func (t *codexTranslator) use(id, tool string, input json.RawMessage) []ChatDelta {
	if t.shown == nil {
		t.shown = map[string]bool{}
	}
	if t.shown[id] {
		return nil
	}
	t.shown[id] = true
	return []ChatDelta{{Kind: "tool_use", ID: id, Tool: tool, Input: input}}
}

// clipToolOutput caps a tool's output as the chat's are (maxToolOutput).
func clipToolOutput(s string) string {
	if len(s) > maxToolOutput {
		return s[:maxToolOutput] + "\n[output truncated]"
	}
	return s
}

// runEvent is a delta as a task log's event; ok is false for one the log
// leaves out (thinking: Claude's runs don't log it either).
func runEvent(d ChatDelta) (model.RunEvent, bool) {
	switch d.Kind {
	case "text", codexStatus:
		return model.RunEvent{Kind: d.Kind, Text: d.Text}, true
	case "tool_use", "tool_result":
		return model.RunEvent{Kind: d.Kind, Text: d.Text, ID: d.ID, Tool: d.Tool, Input: d.Input, IsError: d.IsError}, true
	}
	return model.RunEvent{}, false
}
