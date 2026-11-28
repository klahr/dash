package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/textutil"
)

// decide answers one of a task run's permission requests: a tool of
// Dash's own MCP servers the run may call (AllowMCPTools) and a tool the
// run allows outright (AllowedTools, by name) are allowed; anything else
// is asked through the run's permission tool (PermissionTool), which
// queues it in **Needs you** and keeps "always" grants. A run without one
// (or unattended) rejects it.
func (a ACP) decide(ctx context.Context, req ExecRequest, r acpPermRequest, send func(model.RunEvent)) bool {
	tc := &acpToolCall{ID: r.ToolCall.ToolCallID}
	tc.merge(r.ToolCall)
	tool, input := tc.name(a.Profile), tc.input()
	if s, t, ok := tc.mcpTool(); ok && slices.Contains(req.AllowMCPTools[s], t) {
		return true
	}
	if allowedByName(req.AllowedTools, tool) {
		return true
	}
	if req.Unattended || req.PermissionTool == (MCPTool{}) {
		send(model.RunEvent{Kind: "status", Text: "⛔ refused " + toolLine(tool, input) + ": nobody can approve it in this run"})
		return false
	}
	allow, msg, err := askPermissionTool(ctx, req.MCPServers[req.PermissionTool.Server], req.PermissionTool.Tool, tool, input, tc.ID)
	if err != nil {
		send(model.RunEvent{Kind: "status", Text: "⚠ couldn't ask about " + tool + ": " + err.Error()})
		return false
	}
	if !allow && msg != "" {
		send(model.RunEvent{Kind: "status", Text: "⛔ " + tool + ": " + msg})
	}
	return allow
}

// allowedByName reports whether rules (Claude Code's syntax) allow tool
// outright: only a rule that is the bare tool name counts, since ACP gives
// no input Dash could match a pattern like "Bash(git status *)" against.
func allowedByName(rules []string, tool string) bool {
	return slices.Contains(rules, tool)
}

// askPermissionTool calls the permission prompt tool (perm.ToolName) on
// server (its config in --mcp-config shape: url and headers), as Claude
// Code would, and reads its answer: {"behavior":"allow"|"deny","message":…}.
// It waits until a person answers, or ctx ends.
func askPermissionTool(ctx context.Context, server any, toolName, tool string, input json.RawMessage, id string) (allow bool, message string, err error) {
	var s codexServer
	b, _ := json.Marshal(server)
	if json.Unmarshal(b, &s) != nil || s.URL == "" {
		return false, "", fmt.Errorf("no permission server")
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": toolName, "arguments": map[string]any{
			"tool_name": tool, "input": input, "tool_use_id": id,
		}},
	})
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return false, "", err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json") // one JSON answer, not an event stream
	for k, v := range s.Headers {
		hr.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, "", fmt.Errorf("permission server: %s", resp.Status)
	}
	var out struct {
		Result *struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *mcptools.Error `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, "", fmt.Errorf("permission server: %w", err)
	}
	if out.Error != nil {
		return false, "", fmt.Errorf("permission server: %s", out.Error.Message)
	}
	if out.Result == nil || len(out.Result.Content) == 0 {
		return false, "", fmt.Errorf("permission server: empty answer")
	}
	text := out.Result.Content[0].Text
	if out.Result.IsError {
		return false, "", fmt.Errorf("permission server: %s", textutil.Summary(text, 200))
	}
	var reply struct {
		Behavior string `json:"behavior"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal([]byte(text), &reply); err != nil {
		return false, "", fmt.Errorf("permission server: %q", textutil.Summary(text, 200))
	}
	return strings.EqualFold(reply.Behavior, "allow"), reply.Message, nil
}
