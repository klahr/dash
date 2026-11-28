package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/klahr/dash/internal/mcptools"
)

// An ACP agent streams a session's progress as session/update
// notifications (ACP v1, agentclientprotocol.com/protocol/prompt-turn):
//
//	{"sessionId":"…","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hel"}}}
//	{"sessionId":"…","update":{"sessionUpdate":"tool_call","toolCallId":"t1","title":"ls","kind":"execute","status":"pending","locations":[{"path":"/a.go"}],"rawInput":{…}}}
//	{"sessionId":"…","update":{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"completed","content":[{"type":"content","content":{"type":"text","text":"…"}}]}}
//	{"sessionId":"…","update":{"sessionUpdate":"plan","entries":[{"content":"…","status":"pending","priority":"high"}]}}
//	{"sessionId":"…","update":{"sessionUpdate":"usage_update","used":1200,"size":200000,"cost":{"amount":0.12,"currency":"USD"}}}
//
// acpTranslator turns them into the deltas the chat streams and, through
// runEvent, a task log's events, shaped as Claude Code's are: a tool call
// is named by its kind (ACPProfile.ToolName: "execute" → "Bash"), an MCP
// tool call mcp__server__tool.
type acpUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`
	// agent_message_chunk, agent_thought_chunk: a content block; tool_call
	// and tool_call_update: a list of tool call contents.
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"toolCallId"`
	Title      *string         `json:"title"`
	Kind       *string         `json:"kind"`
	Status     *string         `json:"status"`
	Locations  []acpLocation   `json:"locations"`
	RawInput   json.RawMessage `json:"rawInput"`
	RawOutput  json.RawMessage `json:"rawOutput"`
	Meta       json.RawMessage `json:"_meta"`
	// plan
	Entries []struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	} `json:"entries"`
	// usage_update
	Used int64    `json:"used"`
	Size int64    `json:"size"`
	Cost *acpCost `json:"cost"`
}

type acpLocation struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

type acpCost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// acpToolCall is what Dash knows of one tool call: its tool_call and the
// updates since (each update sets only the fields it has).
type acpToolCall struct {
	ID        string
	Title     string
	Kind      string
	Status    string
	Locations []acpLocation
	RawInput  json.RawMessage
	Meta      json.RawMessage
}

func (tc *acpToolCall) merge(u acpUpdate) {
	if u.Title != nil {
		tc.Title = *u.Title
	}
	if u.Kind != nil {
		tc.Kind = *u.Kind
	}
	if u.Status != nil {
		tc.Status = *u.Status
	}
	if u.Locations != nil {
		tc.Locations = u.Locations
	}
	if len(u.RawInput) > 0 && string(u.RawInput) != "null" {
		tc.RawInput = u.RawInput
	}
	if len(u.Meta) > 0 && string(u.Meta) != "null" {
		tc.Meta = u.Meta
	}
}

// mcpTool is the MCP server and tool a call is to, when the agent says so
// in the call's _meta under its own key, as Gemini CLI does
// ({"gemini-cli":{"mcp":{"server_name":…,"tool_name":…}}}).
func (tc *acpToolCall) mcpTool() (server, tool string, ok bool) {
	var meta map[string]struct {
		MCP *struct {
			Server string `json:"server_name"`
			Tool   string `json:"tool_name"`
		} `json:"mcp"`
	}
	if json.Unmarshal(tc.Meta, &meta) != nil {
		return "", "", false
	}
	for _, v := range meta {
		if v.MCP != nil && v.MCP.Server != "" && v.MCP.Tool != "" {
			return v.MCP.Server, v.MCP.Tool, true
		}
	}
	return "", "", false
}

// name is the call's tool as Claude Code would name it: mcp__server__tool
// for an MCP tool, else the profile's name for its kind.
func (tc *acpToolCall) name(p *ACPProfile) string {
	if s, t, ok := tc.mcpTool(); ok {
		return mcptools.ToolName(s, t)
	}
	return p.ToolName(tc.Kind)
}

// input is the call's input for the log and a permission prompt: the
// agent's rawInput (an object), with the call's title, kind and locations
// added under keys it lacks, which toolLine falls back to.
func (tc *acpToolCall) input() json.RawMessage {
	in := map[string]any{}
	_ = json.Unmarshal(tc.RawInput, &in)
	if in == nil {
		in = map[string]any{}
	}
	add := func(k string, v any) {
		if _, ok := in[k]; !ok {
			in[k] = v
		}
	}
	if tc.Title != "" {
		add("title", tc.Title)
	}
	if tc.Kind != "" {
		add("kind", tc.Kind)
	}
	if len(tc.Locations) > 0 {
		add("locations", tc.Locations)
	}
	b, _ := json.Marshal(in)
	return b
}

// acpStatus is the kind of an acpTranslator delta that is a note for the
// task log only; the chat leaves it out.
const acpStatus = codexStatus

// acpTranslator turns one session's updates into deltas and keeps what the
// session reported: its last message and its cost.
type acpTranslator struct {
	Profile *ACPProfile
	// Stream sends message text as it comes (the chat); else each message
	// is one delta, sent when it ends (the task log).
	Stream bool
	// Last is the last agent message: the run's result.
	Last string
	// Cost is the cost so far the agent reported (usage_update), in USD.
	Cost     float64
	HasCost  bool
	OnCost   func(usd float64)
	text     strings.Builder // the message coming in
	thought  strings.Builder
	calls    map[string]*acpToolCall
	shown    map[string]bool // tool_use sent
	finished map[string]bool // tool_result sent
	plans    int
	unknown  map[string]bool
}

// call is the tool call called id, made if new.
func (t *acpTranslator) call(id string) *acpToolCall {
	if t.calls == nil {
		t.calls = map[string]*acpToolCall{}
	}
	tc := t.calls[id]
	if tc == nil {
		tc = &acpToolCall{ID: id}
		t.calls[id] = tc
	}
	return tc
}

// Translate is the deltas of one update.
func (t *acpTranslator) Translate(u acpUpdate) []ChatDelta {
	switch u.SessionUpdate {
	case "agent_message_chunk":
		s := blockText(u.Content)
		if t.Stream {
			if s == "" {
				return nil
			}
			t.text.WriteString(s)
			return []ChatDelta{{Kind: "text", Text: s}}
		}
		out := t.flushThought()
		t.text.WriteString(s)
		return out
	case "agent_thought_chunk":
		s := blockText(u.Content)
		if t.Stream {
			if s == "" {
				return nil
			}
			return []ChatDelta{{Kind: "thinking", Text: s}}
		}
		out := t.flushText()
		t.thought.WriteString(s)
		return out
	case "user_message_chunk", "available_commands_update", "current_mode_update",
		"config_option_update", "session_info_update":
		return nil
	}
	out := t.Flush()
	switch u.SessionUpdate {
	case "tool_call", "tool_call_update":
		if u.ToolCallID == "" {
			return out
		}
		tc := t.call(u.ToolCallID)
		tc.merge(u)
		out = append(out, t.use(tc)...)
		if tc.Status != "completed" && tc.Status != "failed" || t.finished[tc.ID] {
			return out
		}
		if t.finished == nil {
			t.finished = map[string]bool{}
		}
		t.finished[tc.ID] = true
		text := callText(u.Content)
		if text == "" && len(u.RawOutput) > 0 && string(u.RawOutput) != "null" {
			text = rawOutputText(u.RawOutput)
		}
		d := ChatDelta{Kind: "tool_result", ID: tc.ID, Text: clipToolOutput(text), IsError: tc.Status == "failed"}
		if s, tool, ok := tc.mcpTool(); ok && !d.IsError && s == mcptools.ServerName {
			switch tool {
			case mcptools.ShowImageTool:
				d.Image = ShownImage(tc.RawInput)
			case "create_todo":
				d.Task = CreatedTask(text)
			}
		}
		return append(out, d)
	case "plan":
		type todo struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		}
		todos := []todo{}
		for _, e := range u.Entries {
			todos = append(todos, todo{e.Content, e.Status})
		}
		input, _ := json.Marshal(map[string]any{"todos": todos})
		t.plans++
		id := fmt.Sprintf("plan#%d", t.plans)
		return append(out, ChatDelta{Kind: "tool_use", ID: id, Tool: "TodoWrite", Input: input}, ChatDelta{Kind: "tool_result", ID: id})
	case "usage_update":
		if u.Cost != nil && (u.Cost.Currency == "" || strings.EqualFold(u.Cost.Currency, "USD")) {
			t.Cost, t.HasCost = u.Cost.Amount, true
			if t.OnCost != nil {
				t.OnCost(u.Cost.Amount)
			}
		}
		return out
	}
	return append(out, t.warnUnknown(u.SessionUpdate)...)
}

// use is the tool_use delta of a call, unless it was sent already.
func (t *acpTranslator) use(tc *acpToolCall) []ChatDelta {
	if t.shown == nil {
		t.shown = map[string]bool{}
	}
	if t.shown[tc.ID] {
		return nil
	}
	t.shown[tc.ID] = true
	return []ChatDelta{{Kind: "tool_use", ID: tc.ID, Tool: tc.name(t.Profile), Input: tc.input()}}
}

// Flush ends the message and the thought coming in: their deltas, unless
// they were streamed.
func (t *acpTranslator) Flush() []ChatDelta {
	return append(t.flushThought(), t.flushText()...)
}

func (t *acpTranslator) flushText() []ChatDelta {
	s := t.text.String()
	t.text.Reset()
	if strings.TrimSpace(s) == "" {
		return nil
	}
	t.Last = s
	if t.Stream {
		return nil // sent as it came
	}
	return []ChatDelta{{Kind: "text", Text: s}}
}

func (t *acpTranslator) flushThought() []ChatDelta {
	s := t.thought.String()
	t.thought.Reset()
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return []ChatDelta{{Kind: "thinking", Text: s}}
}

func (t *acpTranslator) warnUnknown(typ string) []ChatDelta {
	if t.unknown == nil {
		t.unknown = map[string]bool{}
	}
	if t.unknown[typ] {
		return nil
	}
	t.unknown[typ] = true
	return []ChatDelta{{Kind: acpStatus, Text: fmt.Sprintf("%sDash doesn't know the session update %q", t.Profile.warnPrefix(), typ)}}
}

// blockText is the text of an ACP content block ("" for an image or other
// non-text block).
func blockText(raw json.RawMessage) string {
	var b struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &b) != nil || b.Type != "text" {
		return ""
	}
	return b.Text
}

// callText is the text of a tool call's content list: its text blocks,
// and a line per diff.
func callText(raw json.RawMessage) string {
	var cs []struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
		Path    string          `json:"path"`
	}
	if json.Unmarshal(raw, &cs) != nil {
		return ""
	}
	var parts []string
	for _, c := range cs {
		switch c.Type {
		case "content":
			if s := blockText(c.Content); s != "" {
				parts = append(parts, s)
			}
		case "diff":
			parts = append(parts, "edited "+c.Path)
		}
	}
	return strings.Join(parts, "\n")
}

// rawOutputText is a tool's raw output as text: a string as it is, an
// object's usual output fields, else its JSON.
func rawOutputText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Output string `json:"output"`
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	}
	if json.Unmarshal(raw, &o) == nil && o.Output+o.Stdout+o.Stderr != "" {
		return strings.TrimSpace(o.Output + o.Stdout + "\n" + o.Stderr)
	}
	return string(raw)
}
