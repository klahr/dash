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

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/runtoken"
)

const (
	ServerName = "dash"
	ToolName   = "permission_prompt"
	// PromptTool is the value for claude --permission-prompt-tool.
	PromptTool = "mcp__" + ServerName + "__" + ToolName
	// AskTool is Claude Code's tool for asking the user questions. Its
	// request is answered with the user's answers, not a plain allow.
	AskTool = "AskUserQuestion"
)

type Publisher interface{ Publish(model.Event) }

var ErrNotFound = errors.New("permission request not found")

// Request is one pending or answered tool-use request.
type Request struct {
	ID        string `json:"id"`
	TodoID    string `json:"todo_id"`
	ProjectID string `json:"project_id"`
	// OrgID is the org of the run that asks: its task's org.
	OrgID    int64           `json:"org_id,omitempty"`
	Tool     string          `json:"tool"`
	Input    json.RawMessage `json:"input"`
	Created  time.Time       `json:"created"`
	Decision string          `json:"decision,omitempty"` // allow | deny | "" while pending
	Message  string          `json:"message,omitempty"`  // reason given to Claude on deny
	// Answers are the user's answers to an AskTool request, keyed by
	// question text (several choices joined with ", ").
	Answers map[string]string `json:"answers,omitempty"`
}

// Question is one question of an AskTool request's input.
type Question struct {
	Question    string `json:"question"`
	Header      string `json:"header,omitempty"`
	MultiSelect bool   `json:"multiSelect,omitempty"`
	Options     []struct {
		Label       string `json:"label"`
		Description string `json:"description,omitempty"`
	} `json:"options,omitempty"`
}

// Questions are the questions of an AskTool request's input.
func Questions(input json.RawMessage) []Question {
	var in struct {
		Questions []Question `json:"questions"`
	}
	_ = json.Unmarshal(input, &in)
	return in.Questions
}

// maxAnswer bounds the length of one answer, in bytes.
const maxAnswer = 4000

// checkAnswers checks that answers answer each of the questions in input,
// and returns them trimmed, with only the questions' keys.
func checkAnswers(input json.RawMessage, answers map[string]string) (map[string]string, error) {
	qs := Questions(input)
	if len(qs) == 0 {
		return nil, errors.New("the request has no questions")
	}
	out := map[string]string{}
	for _, q := range qs {
		a := strings.TrimSpace(answers[q.Question])
		if a == "" {
			return nil, errors.New("answer every question")
		}
		if len(a) > maxAnswer {
			return nil, errors.New("an answer is too long")
		}
		out[q.Question] = a
	}
	return out, nil
}

type run struct {
	owner             model.Owner // who the run acts as: its task's (or chat's) user and org
	todoID, projectID string
	ask               AskFunc // set for runs registered with RegisterFunc
}

// AskFunc decides a tool call itself instead of queueing it for the board,
// e.g. by asking in the chat that started the run.
type AskFunc func(ctx context.Context, tool string, input json.RawMessage) Answer

// Answer is how an AskFunc decided a tool call: Allow it or not, with
// Message saying why not. Answers are the user's answers to an AskTool
// request, keyed by question text; allowing one needs an answer to each.
type Answer struct {
	Allow   bool
	Message string
	Answers map[string]string
}

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
	// Member reports whether a run's owner still belongs to its org; a run
	// whose owner doesn't is refused (optional: without it, any valid owner).
	Member func(model.Owner) bool

	tokens runtoken.Map[run]

	mu      sync.Mutex
	pending map[string]*pending
	always  map[string]map[string]bool // todoID → tool → allowed
}

func NewBroker(pub Publisher) *Broker {
	return &Broker{
		pub: pub, Timeout: 50 * time.Minute,
		pending: map[string]*pending{}, always: map[string]map[string]bool{},
	}
}

// Register issues a bearer token for one Claude run of a todo, acting as
// owner: the todo's user and org. Call release when the run ends; it also
// denies anything still pending for that run.
func (b *Broker) Register(owner model.Owner, todoID, projectID string) (token string, release func()) {
	token, forget := b.tokens.Register(run{owner: owner, todoID: todoID, projectID: projectID})
	return token, func() {
		forget()
		b.mu.Lock()
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

// RegisterFunc issues a bearer token for one Claude run, acting as owner,
// whose permission requests are answered by ask. They don't show up in
// Pending. Call release when the run ends.
func (b *Broker) RegisterFunc(owner model.Owner, ask AskFunc) (token string, release func()) {
	return b.tokens.Register(run{owner: owner, ask: ask})
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

// Get returns the pending request id.
func (b *Broker) Get(id string) (Request, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.pending[id]
	if !ok {
		return Request{}, false
	}
	return p.req, true
}

// Decide answers a request: "allow", "always" (allow this tool from now on:
// for the whole project when Saved is set, otherwise for the rest of the todo)
// or "deny". An AskTool request can't be allowed this way: see DecideAnswers.
func (b *Broker) Decide(id, decision, message string) error {
	return b.DecideAnswers(id, decision, message, nil)
}

// DecideAnswers is Decide with the user's answers to an AskTool request,
// keyed by question text: "allow" with an answer to each question (they
// reach Claude in the tool's input), or "deny". Answers to any other
// tool's request are ignored.
func (b *Broker) DecideAnswers(id, decision, message string, given map[string]string) error {
	b.mu.Lock()
	p, ok := b.pending[id]
	if !ok {
		b.mu.Unlock()
		return ErrNotFound
	}
	var answers map[string]string
	if p.req.Tool == AskTool && decision != "deny" {
		if decision != "allow" {
			b.mu.Unlock()
			return errors.New("a question is answered or declined, not always allowed")
		}
		var err error
		if answers, err = checkAnswers(p.req.Input, given); err != nil {
			b.mu.Unlock()
			return err
		}
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
	p.req.Decision, p.req.Message, p.req.Answers = decision, message, answers
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
	// A question always waits for its answers.
	if tool != AskTool && b.Saved != nil && b.Saved.ToolAllowed(rn.projectID, tool) {
		return Request{Tool: tool, Input: input, Decision: "allow"}
	}
	b.mu.Lock()
	if tool != AskTool && b.always[rn.todoID][tool] {
		b.mu.Unlock()
		return Request{Tool: tool, Input: input, Decision: "allow"}
	}
	idb := make([]byte, 6)
	_, _ = rand.Read(idb)
	p := &pending{req: Request{
		ID: hex.EncodeToString(idb), TodoID: rn.todoID, ProjectID: rn.projectID, OrgID: rn.owner.Org,
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

// ServeMCP handles POST /mcp/permissions. Auth is the per-run bearer token.
func (b *Broker) ServeMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "this MCP server only accepts POST", http.StatusMethodNotAllowed)
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	var rn run
	if ok && token != "" {
		rn, ok = b.tokens.Lookup(token)
	}
	// Every run acts as a user in an org, and only while they belong to it.
	ok = ok && rn.owner.Valid() && (b.Member == nil || b.Member(rn.owner))
	if !ok {
		http.Error(w, "unknown or expired token", http.StatusUnauthorized)
		return
	}
	srv := &mcptools.Server{Name: ServerName, Tools: []mcptools.Tool{{
		Name: ToolName,
		Desc: "Ask the Dash user whether a tool call may run.",
		Params: []mcptools.Param{
			{Name: "tool_name", Type: "string", Desc: "The tool Claude wants to call.", Required: true},
			{Name: "input", Type: "object", Desc: "The tool call's input.", Required: true},
			{Name: "tool_use_id", Type: "string", Desc: "The tool call's id."},
		},
		Run: func(a mcptools.Args) (any, error) {
			input := json.RawMessage("{}")
			if a["input"] != nil {
				raw, err := json.Marshal(a["input"])
				if err != nil {
					return nil, err
				}
				input = raw
			}
			return b.answer(r.Context(), rn, a.Str("tool_name"), input), nil
		},
	}}}
	srv.ServeHTTP(w, r)
}

// answer decides one tool call for rn and returns the permission prompt
// tool's reply text for Claude Code.
func (b *Broker) answer(ctx context.Context, rn run, tool string, input json.RawMessage) string {
	var res Request
	if rn.ask != nil {
		a := rn.ask(ctx, tool, input)
		res.Decision, res.Message = "deny", a.Message
		if a.Allow {
			res.Decision = "allow"
		}
		if a.Allow && tool == AskTool {
			// A question is answered, not just allowed.
			var err error
			if res.Answers, err = checkAnswers(input, a.Answers); err != nil {
				res.Decision, res.Message = "deny", "The user didn't answer: "+err.Error()+"."
			}
		}
	} else {
		res = b.ask(ctx, rn, tool, input)
	}
	reply := map[string]any{"behavior": "deny", "message": res.Message}
	if res.Decision == "allow" {
		reply = map[string]any{"behavior": "allow", "updatedInput": withAnswers(input, res.Answers)}
	} else if res.Message == "" {
		reply["message"] = "The user denied this action."
	}
	text, _ := json.Marshal(reply)
	return string(text)
}

// withAnswers is a question's input with the user's answers merged in, as
// Claude Code reads them: {"questions": […], "answers": {question: answer}}.
// Without answers it is input as it is.
func withAnswers(input json.RawMessage, answers map[string]string) json.RawMessage {
	if len(answers) == 0 {
		return input
	}
	var in map[string]any
	if json.Unmarshal(input, &in) != nil || in == nil {
		in = map[string]any{}
	}
	in["answers"] = answers
	out, err := json.Marshal(in)
	if err != nil {
		return input
	}
	return out
}

// ServerConfig is the --mcp-config entry for the permission server at baseURL.
func ServerConfig(baseURL, token string) map[string]any {
	return map[string]any{
		"type":    "http",
		"url":     strings.TrimRight(baseURL, "/") + "/mcp/permissions",
		"headers": map[string]string{"Authorization": "Bearer " + token},
	}
}
