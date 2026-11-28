package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/klahr/dash/internal/buildinfo"
	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/model"
)

// acpProtocolVersion is the ACP version Dash speaks.
const acpProtocolVersion = 1

// acpGrace is how long an agent gets to end a cancelled turn, and to exit
// once its input is closed, before its process group is killed.
var acpGrace = 5 * time.Second

// acpInit is the agent's initialize answer.
type acpInit struct {
	ProtocolVersion   int `json:"protocolVersion"`
	AgentCapabilities struct {
		LoadSession     bool `json:"loadSession"`
		MCPCapabilities struct {
			HTTP bool `json:"http"`
			SSE  bool `json:"sse"`
		} `json:"mcpCapabilities"`
		SessionCapabilities struct {
			Resume json.RawMessage `json:"resume"`
			Fork   json.RawMessage `json:"fork"`
		} `json:"sessionCapabilities"`
	} `json:"agentCapabilities"`
	AuthMethods []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"authMethods"`
	AgentInfo *struct {
		Name    string `json:"name"`
		Title   string `json:"title"`
		Version string `json:"version"`
	} `json:"agentInfo"`
}

// ACPCaps are the abilities an agent said it has in its initialize answer.
type ACPCaps struct {
	Known   bool // an initialize answer was read
	Version string
	Title   string // agentInfo's title, else its name
	Load    bool   // session/load
	Resume  bool   // session/resume
	Fork    bool   // session/fork (unstable)
	HTTP    bool   // MCP servers over HTTP
}

func present(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null" && string(raw) != "false"
}

func (i acpInit) caps() ACPCaps {
	c := ACPCaps{Known: true, Load: i.AgentCapabilities.LoadSession, HTTP: i.AgentCapabilities.MCPCapabilities.HTTP,
		Resume: present(i.AgentCapabilities.SessionCapabilities.Resume), Fork: present(i.AgentCapabilities.SessionCapabilities.Fork)}
	if i.AgentInfo != nil {
		c.Version = i.AgentInfo.Version
		c.Title = cmp.Or(strings.TrimSpace(i.AgentInfo.Title), strings.TrimSpace(i.AgentInfo.Name))
	}
	return c
}

// acpSessionInfo is the answer of session/new, load, resume and fork: the
// session's id (new and fork only), its modes and models (unstable) and
// its config options.
type acpSessionInfo struct {
	SessionID string `json:"sessionId"`
	Modes     *struct {
		CurrentModeID  string `json:"currentModeId"`
		AvailableModes []struct {
			ID string `json:"id"`
		} `json:"availableModes"`
	} `json:"modes"`
	Models *struct {
		CurrentModelID  string `json:"currentModelId"`
		AvailableModels []struct {
			ModelID string `json:"modelId"`
		} `json:"availableModels"`
	} `json:"models"`
	ConfigOptions []acpConfigOption `json:"configOptions"`
}

// acpConfigOption is one of a session's config options, e.g. its model
// (category "model") or reasoning effort ("thought_level").
type acpConfigOption struct {
	ID       string          `json:"id"`
	Category string          `json:"category"`
	Options  json.RawMessage `json:"options"`
}

// values are the values the option takes; its options may be grouped.
func (o acpConfigOption) values() []string {
	var opts []struct {
		Value   string `json:"value"`
		Options []struct {
			Value string `json:"value"`
		} `json:"options"`
	}
	_ = json.Unmarshal(o.Options, &opts)
	var out []string
	for _, x := range opts {
		if x.Value != "" {
			out = append(out, x.Value)
		}
		for _, y := range x.Options {
			out = append(out, y.Value)
		}
	}
	return out
}

func (s acpSessionInfo) option(category string) *acpConfigOption {
	for i, o := range s.ConfigOptions {
		if o.Category == category {
			return &s.ConfigOptions[i]
		}
	}
	return nil
}

// acpMCPServer is an MCP server as session/new takes it.
type acpMCPServer struct {
	Type    string      `json:"type,omitempty"` // "http"; none for stdio
	Name    string      `json:"name"`
	URL     string      `json:"url,omitempty"`
	Headers []acpEnvVar `json:"headers,omitempty"`
	Command string      `json:"command,omitempty"`
	Args    []string    `json:"args,omitempty"`
	Env     []acpEnvVar `json:"env,omitempty"`
}

type acpEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// acpServers are Dash's MCP servers (name → config in Claude Code's
// --mcp-config shape) as session/new takes them, sorted by name, without
// skip (the permission server, which Dash calls itself). An HTTP server
// is left out of an agent that can't take one; those are dropped.
func acpServers(servers map[string]any, skip string, http bool) (out []acpMCPServer, dropped []string) {
	out = []acpMCPServer{} // never null: the field is required
	for _, n := range slices.Sorted(maps.Keys(servers)) {
		if n == skip {
			continue
		}
		var s codexServer
		b, _ := json.Marshal(servers[n])
		if json.Unmarshal(b, &s) != nil || s.URL == "" {
			continue
		}
		if !http {
			dropped = append(dropped, n)
			continue
		}
		m := acpMCPServer{Type: "http", Name: n, URL: s.URL, Headers: []acpEnvVar{}}
		for _, h := range slices.Sorted(maps.Keys(s.Headers)) {
			m.Headers = append(m.Headers, acpEnvVar{h, s.Headers[h]})
		}
		out = append(out, m)
	}
	return out, dropped
}

// acpPermRequest is session/request_permission's params.
type acpPermRequest struct {
	SessionID string          `json:"sessionId"`
	ToolCall  acpUpdate       `json:"toolCall"`
	Options   []acpPermOption `json:"options"`
}

type acpPermOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // allow_once, allow_always, reject_once, reject_always
}

// acpOutcome is the answer to a permission request: the agent's option
// that allows the call once (or, without one, always), or that rejects it
// once. Dash keeps "always" in its own grants, not the agent's.
func acpOutcome(opts []acpPermOption, allow bool) map[string]any {
	order := []string{"reject_once", "reject_always"}
	if allow {
		order = []string{"allow_once", "allow_always"}
	}
	for _, k := range order {
		for _, o := range opts {
			if o.Kind == k {
				return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": o.OptionID}}
			}
		}
	}
	return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
}

// acpProc is a running ACP agent.
type acpProc struct {
	cmd    *exec.Cmd
	conn   *acpConn
	stdin  io.WriteCloser
	kill   context.CancelFunc
	init   acpInit
	stderr chan struct{} // closed once stderr is read to the end
}

// acpStart is how to start an agent and what to do with what it sends.
type acpStart struct {
	Name string // the program (the agent, or a sandbox command)
	Args []string
	Dir  string
	Env  []string // the whole environment
	// OnUpdate gets each session/update.
	OnUpdate func(u acpUpdate)
	// OnPermission decides each session/request_permission (nil: reject).
	OnPermission func(r acpPermRequest) bool
	// OnStderr gets each line the agent writes to stderr; OnOther each
	// line of its output that isn't JSON-RPC.
	OnStderr, OnOther func(string)
}

// startACP starts the agent in its own process group and initializes it,
// with the fs and terminal client capabilities off: the agent reads and
// writes files and runs commands itself. ctx bounds only the start; the
// process lives until close.
func startACP(ctx context.Context, s acpStart) (*acpProc, error) {
	if err := checkLocal(); err != nil {
		return nil, err
	}
	procCtx, kill := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, s.Name, s.Args...)
	cmd.Dir, cmd.Env = s.Dir, s.Env
	localrun.Group(cmd, syscall.SIGTERM, 10*time.Second)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		kill()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		kill()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		kill()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		kill()
		return nil, fmt.Errorf("start %s: %w", s.Name, err)
	}
	p := &acpProc{cmd: cmd, stdin: stdin, kill: kill, conn: newACPConn(stdin), stderr: make(chan struct{})}
	p.conn.onOther = s.OnOther
	p.conn.onNotify = func(method string, params json.RawMessage) {
		if method != "session/update" || s.OnUpdate == nil {
			return
		}
		var n struct {
			Update acpUpdate `json:"update"`
		}
		if json.Unmarshal(params, &n) == nil {
			s.OnUpdate(n.Update)
		}
	}
	p.conn.onRequest = func(method string, params json.RawMessage) (any, *acpError) {
		if method != "session/request_permission" {
			// fs/* and terminal/*: Dash said it serves neither.
			return nil, &acpError{Code: acpMethodNotFound, Message: "Dash doesn't serve " + method}
		}
		var r acpPermRequest
		if err := json.Unmarshal(params, &r); err != nil {
			return nil, &acpError{Code: -32602, Message: "invalid params"}
		}
		allow := s.OnPermission != nil && s.OnPermission(r)
		return acpOutcome(r.Options, allow), nil
	}
	go p.conn.serve(stdout)
	go func() {
		defer close(p.stderr)
		pumpLines(stderr, func(l string) {
			if s.OnStderr != nil && strings.TrimSpace(l) != "" {
				s.OnStderr(l)
			}
		})
	}()
	err = p.conn.call(ctx, "initialize", map[string]any{
		"protocolVersion": acpProtocolVersion,
		"clientCapabilities": map[string]any{
			"fs":       map[string]bool{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]string{"name": "dash", "title": "Dash", "version": buildinfo.Version},
	}, &p.init)
	if err == nil && p.init.ProtocolVersion != acpProtocolVersion {
		err = fmt.Errorf("the agent speaks ACP version %d; Dash speaks version %d", p.init.ProtocolVersion, acpProtocolVersion)
	}
	if err != nil {
		p.close()
		return p, fmt.Errorf("initialize: %w", err)
	}
	return p, nil
}

// close ends the agent: its input closes, and after acpGrace its process
// group is killed. It returns how the process exited.
func (p *acpProc) close() error {
	_ = p.stdin.Close()
	select {
	case <-p.conn.done:
	case <-time.After(acpGrace):
	}
	p.kill()
	select {
	case <-p.conn.done:
	case <-time.After(15 * time.Second):
	}
	<-p.stderr
	err := p.cmd.Wait()
	localrun.Reap(p.cmd)
	return err
}

// prompt sends one prompt and waits for the turn to end. When ctx ends
// first, it cancels the turn (session/cancel) and waits acpGrace for the
// agent to end it.
func (p *acpProc) prompt(ctx context.Context, session, text string) (stop string, usage *acpPromptUsage, err error) {
	var res struct {
		StopReason string          `json:"stopReason"`
		Usage      *acpPromptUsage `json:"usage"`
	}
	done := make(chan error, 1)
	go func() {
		done <- p.conn.call(context.Background(), "session/prompt", map[string]any{
			"sessionId": session,
			"prompt":    []map[string]string{{"type": "text", "text": text}},
		}, &res)
	}()
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = p.conn.notify("session/cancel", map[string]string{"sessionId": session})
		select {
		case <-done:
		case <-time.After(acpGrace):
		}
		return "cancelled", nil, ctx.Err()
	}
	return res.StopReason, res.Usage, err
}

// acpPromptUsage is a turn's tokens, where the agent reports them (an
// unstable addition to session/prompt's answer).
type acpPromptUsage struct {
	InputTokens       int64 `json:"inputTokens"`
	OutputTokens      int64 `json:"outputTokens"`
	CachedReadTokens  int64 `json:"cachedReadTokens"`
	CachedWriteTokens int64 `json:"cachedWriteTokens"`
}

// usageOf is u as Dash keeps it (none: no tokens).
func usageOf(u *acpPromptUsage) model.ChatUsage {
	if u == nil {
		return model.ChatUsage{}
	}
	return model.ChatUsage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		CacheReadTokens: u.CachedReadTokens, CacheCreationTokens: u.CachedWriteTokens}
}

// errACPLoggedOut wraps the agent's auth_required error.
var errACPLoggedOut = errors.New("not signed in")

// acpOpen are the parameters of opening a session.
type acpOpen struct {
	Cwd     string
	Servers []acpMCPServer
	Resume  string // continue this session
	Fork    bool   // in a new session (session/fork)
	// Key is an API key to sign in with (authenticate with the profile's
	// KeyAuthMethod) before the session, or after auth_required.
	Key    string
	Always bool // sign in with Key first, not only after auth_required
}

// open opens a session (session/new, or for o.Resume session/fork,
// resume or load, as the agent offers) and returns it. Updates the agent
// replays while it loads a session reach onUpdate: the caller ignores them
// until open returns (see acpRun.replaying). An auth_required answer is
// errACPLoggedOut, unless signing in with o.Key helped.
func (p *acpProc) open(ctx context.Context, prof *ACPProfile, o acpOpen) (acpSessionInfo, error) {
	caps := p.init.caps()
	auth := func() error {
		params := map[string]any{"methodId": prof.KeyAuthMethod}
		if o.Key != "" {
			params["_meta"] = map[string]any{"api-key": o.Key}
		}
		return p.conn.call(ctx, "authenticate", params, nil)
	}
	canAuth := o.Key != "" && prof.KeyAuthMethod != ""
	if canAuth && o.Always {
		if err := auth(); err != nil {
			return acpSessionInfo{}, fmt.Errorf("sign in with %s: %w", prof.KeyEnv, err)
		}
	}
	var info acpSessionInfo
	call := func() error {
		params := map[string]any{"cwd": o.Cwd, "mcpServers": o.Servers}
		method := "session/new"
		if o.Resume != "" {
			params["sessionId"] = o.Resume
			switch {
			case o.Fork:
				method = "session/fork"
			case caps.Resume:
				method = "session/resume"
			default:
				method = "session/load"
			}
		}
		info = acpSessionInfo{}
		if err := p.conn.call(ctx, method, params, &info); err != nil {
			return err
		}
		if info.SessionID == "" {
			info.SessionID = o.Resume
		}
		return nil
	}
	err := call()
	if isAuthRequired(err) && canAuth && !o.Always {
		if aerr := auth(); aerr == nil {
			err = call()
		}
	}
	if isAuthRequired(err) {
		return info, fmt.Errorf("%w: %v", errACPLoggedOut, err)
	}
	return info, err
}

// configure sets the session's mode, model and effort where the agent
// offers them, and returns notes on what it couldn't set.
func (p *acpProc) configure(ctx context.Context, info acpSessionInfo, mode, model, effort string) (notes []string) {
	setOption := func(o *acpConfigOption, value string) error {
		return p.conn.call(ctx, "session/set_config_option",
			map[string]any{"sessionId": info.SessionID, "configId": o.ID, "value": value}, nil)
	}
	if mode != "" {
		if o := info.option("mode"); o != nil && slices.Contains(o.values(), mode) {
			if err := setOption(o, mode); err != nil {
				notes = append(notes, "couldn't set mode "+mode+": "+err.Error())
			}
		} else if info.Modes != nil && slices.ContainsFunc(info.Modes.AvailableModes, func(m struct {
			ID string `json:"id"`
		}) bool {
			return m.ID == mode
		}) {
			if info.Modes.CurrentModeID != mode {
				if err := p.conn.call(ctx, "session/set_mode", map[string]any{"sessionId": info.SessionID, "modeId": mode}, nil); err != nil {
					notes = append(notes, "couldn't set mode "+mode+": "+err.Error())
				}
			}
		} else {
			notes = append(notes, "the agent has no mode "+mode)
		}
	}
	if model != "" {
		switch o := info.option("model"); {
		case o != nil:
			if err := setOption(o, model); err != nil {
				notes = append(notes, "couldn't set model "+model+": "+err.Error())
			}
		case info.Models != nil:
			if err := p.conn.call(ctx, "session/set_model", map[string]any{"sessionId": info.SessionID, "modelId": model}, nil); err != nil {
				notes = append(notes, "couldn't set model "+model+": "+err.Error())
			}
		default:
			notes = append(notes, "the agent doesn't let Dash pick model "+model)
		}
	}
	if effort != "" {
		if o := info.option("thought_level"); o != nil && slices.Contains(o.values(), effort) {
			if err := setOption(o, effort); err != nil {
				notes = append(notes, "couldn't set effort "+effort+": "+err.Error())
			}
		} else {
			notes = append(notes, "the agent doesn't let Dash set effort "+effort)
		}
	}
	return notes
}
