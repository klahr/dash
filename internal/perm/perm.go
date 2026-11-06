// Package perm lets a human approve the tool calls headless Claude Code asks
// permission for. Dash serves a minimal MCP server (streamable HTTP, JSON
// responses) with one tool; each todo run passes it to Claude Code with
// --permission-prompt-tool. A call blocks until someone answers in the UI.
package perm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/model"
)

const (
	ServerName = "dash"
	ToolName   = "permission_prompt"
	// PromptTool is the value for claude --permission-prompt-tool.
	PromptTool = "mcp__" + ServerName + "__" + ToolName
)

type Publisher interface{ Publish(model.Event) }

var ErrNotFound = errors.New("permission request not found")

// Request is one pending or answered tool-use request.
type Request struct {
	ID        string          `json:"id"`
	TodoID    string          `json:"todo_id"`
	ProjectID string          `json:"project_id"`
	Tool      string          `json:"tool"`
	Input     json.RawMessage `json:"input"`
	Created   time.Time       `json:"created"`
	Decision  string          `json:"decision,omitempty"` // allow | deny | "" while pending
	Message   string          `json:"message,omitempty"`  // reason given to Claude on deny
}

type run struct{ todoID, projectID string }

type pending struct {
	req  Request
	done chan struct{}
}

// AllowList persists tools a project always allows (the store implements it).
type AllowList interface {
	ToolAllowed(projectID, tool string) bool
	AllowTool(projectID, tool string) error
}

type Broker struct {
	pub     Publisher
	Timeout time.Duration // unanswered requests are denied after this
	Saved   AllowList     // optional; "always" grants are saved per project here

	mu      sync.Mutex
	tokens  map[string]run
	pending map[string]*pending
	always  map[string]map[string]bool // todoID → tool → allowed
}

func NewBroker(pub Publisher) *Broker {
	return &Broker{
		pub: pub, Timeout: 50 * time.Minute,
		tokens: map[string]run{}, pending: map[string]*pending{}, always: map[string]map[string]bool{},
	}
}

// Register issues a bearer token for one Claude run of a todo. Call release
// when the run ends; it also denies anything still pending for that run.
func (b *Broker) Register(todoID, projectID string) (token string, release func()) {
	buf := make([]byte, 24)
	_, _ = rand.Read(buf)
	token = hex.EncodeToString(buf)
	b.mu.Lock()
	b.tokens[token] = run{todoID, projectID}
	b.mu.Unlock()
	return token, func() {
		b.mu.Lock()
		delete(b.tokens, token)
		var stale []string
		for id, p := range b.pending {
			if p.req.TodoID == todoID {
				stale = append(stale, id)
			}
		}
		b.mu.Unlock()
		for _, id := range stale {
			_ = b.Decide(id, "deny", "The task ended before anyone answered.")
		}
	}
}

// Forget drops "always allow" grants for a todo (call when it is merged or deleted).
func (b *Broker) Forget(todoID string) {
	b.mu.Lock()
	delete(b.always, todoID)
	b.mu.Unlock()
}

// Waiting reports whether a todo's run is blocked on a human's answer.
func (b *Broker) Waiting(todoID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range b.pending {
		if p.req.TodoID == todoID {
			return true
		}
	}
	return false
}

// Pending lists unanswered requests, optionally for one project.
func (b *Broker) Pending(projectID string) []Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []Request{}
	for _, p := range b.pending {
		if projectID == "" || p.req.ProjectID == projectID {
			out = append(out, p.req)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// Decide answers a request: "allow", "always" (allow this tool from now on:
// for the whole project when Saved is set, otherwise for the rest of the todo)
// or "deny".
func (b *Broker) Decide(id, decision, message string) error {
	b.mu.Lock()
	p, ok := b.pending[id]
	if !ok {
		b.mu.Unlock()
		return ErrNotFound
	}
	always := decision == "always"
	switch decision {
	case "always":
		if b.always[p.req.TodoID] == nil {
			b.always[p.req.TodoID] = map[string]bool{}
		}
		b.always[p.req.TodoID][p.req.Tool] = true
		decision = "allow"
	case "allow", "deny":
	default:
		b.mu.Unlock()
		return errors.New("decision must be allow, always or deny")
	}
	p.req.Decision, p.req.Message = decision, message
	delete(b.pending, id)
	close(p.done)
	req := p.req
	b.mu.Unlock()
	if always && b.Saved != nil {
		_ = b.Saved.AllowTool(req.ProjectID, req.Tool) // the todo grant above still holds if saving fails
	}
	b.publish(req)
	return nil
}

func (b *Broker) publish(r Request) {
	if b.pub != nil {
		b.pub.Publish(model.Event{Type: model.EvPermission, ProjectID: r.ProjectID, TodoID: r.TodoID, Data: r, Time: time.Now()})
	}
}

// ask blocks until the request is answered, times out, or ctx ends.
func (b *Broker) ask(ctx context.Context, rn run, tool string, input json.RawMessage) Request {
	if b.Saved != nil && b.Saved.ToolAllowed(rn.projectID, tool) {
		return Request{Tool: tool, Input: input, Decision: "allow"}
	}
	b.mu.Lock()
	if b.always[rn.todoID][tool] {
		b.mu.Unlock()
		return Request{Tool: tool, Input: input, Decision: "allow"}
	}
	idb := make([]byte, 6)
	_, _ = rand.Read(idb)
	p := &pending{req: Request{
		ID: hex.EncodeToString(idb), TodoID: rn.todoID, ProjectID: rn.projectID,
		Tool: tool, Input: input, Created: time.Now(),
	}, done: make(chan struct{})}
	b.pending[p.req.ID] = p
	snapshot := p.req // Decide may write p.req once the lock is released
	b.mu.Unlock()
	b.publish(snapshot)

	timer := time.NewTimer(b.Timeout)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
		_ = b.Decide(p.req.ID, "deny", "Nobody answered the permission request in time.")
	case <-ctx.Done():
		_ = b.Decide(p.req.ID, "deny", "The request was cancelled.")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return p.req
}

// ---- MCP over streamable HTTP (JSON responses only) ----

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ServeMCP handles POST /mcp/permissions. Auth is the per-run bearer token.
func (b *Broker) ServeMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "this MCP server only accepts POST", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	b.mu.Lock()
	rn, ok := b.tokens[token]
	b.mu.Unlock()
	if !ok {
		http.Error(w, "unknown or expired token", http.StatusUnauthorized)
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeRPC(w, nil, nil, &rpcError{-32700, "parse error"})
		return
	}
	if len(req.ID) == 0 { // notification (e.g. notifications/initialized)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		writeRPC(w, req.ID, map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": ServerName, "version": "1"},
		}, nil)
	case "ping":
		writeRPC(w, req.ID, map[string]any{}, nil)
	case "tools/list":
		writeRPC(w, req.ID, map[string]any{"tools": []any{map[string]any{
			"name":        ToolName,
			"description": "Ask the Dash user whether a tool call may run.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tool_name":   map[string]string{"type": "string"},
					"input":       map[string]string{"type": "object"},
					"tool_use_id": map[string]string{"type": "string"},
				},
				"required": []string{"tool_name", "input"},
			},
		}}}, nil)
	case "tools/call":
		var p struct {
			Name      string `json:"name"`
			Arguments struct {
				ToolName string          `json:"tool_name"`
				Input    json.RawMessage `json:"input"`
			} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name != ToolName {
			writeRPC(w, req.ID, nil, &rpcError{-32602, "unknown tool"})
			return
		}
		input := p.Arguments.Input
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		res := b.ask(r.Context(), rn, p.Arguments.ToolName, input)
		answer := map[string]any{"behavior": "deny", "message": res.Message}
		if res.Decision == "allow" {
			answer = map[string]any{"behavior": "allow", "updatedInput": input}
		} else if res.Message == "" {
			answer["message"] = "The user denied this action."
		}
		text, _ := json.Marshal(answer)
		writeRPC(w, req.ID, map[string]any{"content": []any{map[string]string{"type": "text", "text": string(text)}}}, nil)
	default:
		writeRPC(w, req.ID, nil, &rpcError{-32601, "method not found"})
	}
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, rerr *rpcError) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if rerr != nil {
		resp["error"] = rerr
	} else {
		resp["result"] = result
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// MCPConfig returns the --mcp-config JSON pointing Claude Code at baseURL.
func MCPConfig(baseURL, token string) string {
	cfg := map[string]any{"mcpServers": map[string]any{ServerName: map[string]any{
		"type":    "http",
		"url":     strings.TrimRight(baseURL, "/") + "/mcp/permissions",
		"headers": map[string]string{"Authorization": "Bearer " + token},
	}}}
	b, _ := json.Marshal(cfg)
	return string(b)
}
