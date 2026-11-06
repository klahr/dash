// Package tui is a terminal frontend for Dash with vim-style keybindings. It
// is a pure client of the Dash server's REST and WebSocket API.
package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

// Client talks to a Dash server with a session cookie.
type Client struct {
	base *url.URL
	http *http.Client

	mu     sync.Mutex
	prefix string // "" or "/peer/<name>": the computer the selected project lives on
}

// Machine mirrors peer.Machine: a computer in this dashboard.
type Machine struct {
	Name   string `json:"name"`
	Local  bool   `json:"local"`
	Prefix string `json:"prefix"`
	OK     bool   `json:"ok"`
	Error  string `json:"error"`
}

// hubOnly paths always go to the Dash we logged in to.
var hubOnly = regexp.MustCompile(`^/api/(login|logout|me|machines|version)\b`)

// SetPrefix sends later project calls to another computer ("" = this one).
func (c *Client) SetPrefix(p string) {
	c.mu.Lock()
	c.prefix = p
	c.mu.Unlock()
}

func (c *Client) at(path string) string {
	if hubOnly.MatchString(path) {
		return path
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.prefix + path
}

// Machines lists the computers in this dashboard.
func (c *Client) Machines() (ms []Machine, err error) {
	return ms, c.do("GET", "/api/machines", nil, &ms)
}

// AllProjects lists the projects of every reachable computer, each tagged
// with its computer's name.
func (c *Client) AllProjects() ([]model.Project, []Machine, error) {
	ms, err := c.Machines()
	if err != nil {
		return nil, nil, err
	}
	var all []model.Project
	for i, m := range ms {
		if !m.OK {
			continue
		}
		var ps []model.Project
		if err := c.doRaw("GET", m.Prefix+"/api/projects", nil, &ps); err != nil {
			ms[i].OK, ms[i].Error = false, err.Error()
			continue
		}
		for _, p := range ps {
			p.Machine = m.Name
			all = append(all, p)
		}
	}
	return all, ms, nil
}

var ErrUnauthorized = errors.New("not logged in")

func NewClient(server string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(server, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid server URL %q", server)
	}
	jar, _ := cookiejar.New(nil)
	return &Client{base: u, http: &http.Client{Jar: jar, Timeout: 60 * time.Second}}, nil
}

func (c *Client) Login(username, password string) error {
	return c.do("POST", "/api/login", map[string]string{"username": username, "password": password}, nil)
}

// Call sends a request to path as given, ignoring SetPrefix.
func (c *Client) Call(method, path string, body, out any) error {
	return c.doRaw(method, path, body, out)
}

func (c *Client) do(method, path string, body, out any) error {
	return c.doRaw(method, c.at(path), body, out)
}

func (c *Client) doRaw(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base.String()+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode == http.StatusUnauthorized && path != "/api/login" {
		return ErrUnauthorized
	}
	if res.StatusCode >= 400 {
		var e struct{ Error string }
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("%s %s: %s", method, path, res.Status)
	}
	switch o := out.(type) {
	case nil:
	case *string:
		*o = string(data)
	default:
		return json.Unmarshal(data, out)
	}
	return nil
}

// ---- REST ----

func (c *Client) Projects() (ps []model.Project, err error) {
	return ps, c.do("GET", "/api/projects", nil, &ps)
}

func (c *Client) CreateProject(name, path string) (p model.Project, err error) {
	return p, c.do("POST", "/api/projects", map[string]string{"name": name, "path": path}, &p)
}

func (c *Client) PushProject(pid string) error {
	return c.do("POST", "/api/projects/"+pid+"/push", nil, nil)
}

func (c *Client) Todos(pid string) (ts []model.Todo, err error) {
	return ts, c.do("GET", "/api/projects/"+pid+"/todos", nil, &ts)
}

type NewTodo struct {
	Title    string `json:"title"`
	Prompt   string `json:"prompt"`
	Goal     bool   `json:"goal"`
	Priority int    `json:"priority"`
	Enqueue  bool   `json:"enqueue"`
}

func (c *Client) CreateTodo(pid string, t NewTodo) (out model.Todo, err error) {
	return out, c.do("POST", "/api/projects/"+pid+"/todos", t, &out)
}

func (c *Client) Todo(id string) (t model.Todo, err error) {
	return t, c.do("GET", "/api/todos/"+id, nil, &t)
}

func (c *Client) UpdateTodo(id string, fields map[string]any) error {
	return c.do("PATCH", "/api/todos/"+id, fields, nil)
}

func (c *Client) DeleteTodo(id string) error { return c.do("DELETE", "/api/todos/"+id, nil, nil) }

// TodoAction posts to /api/todos/{id}/{verb}: enqueue, cancel, approve, rework.
func (c *Client) TodoAction(id, verb string, body any) error {
	return c.do("POST", "/api/todos/"+id+"/"+verb, body, nil)
}

func (c *Client) TodoText(id, kind string) (s string, err error) { // kind: log | diff
	return s, c.do("GET", "/api/todos/"+id+"/"+kind, nil, &s)
}

func (c *Client) Processes(pid string) (ps []model.Process, err error) {
	return ps, c.do("GET", "/api/projects/"+pid+"/processes", nil, &ps)
}

type StartProc struct {
	Kind    model.ProcessKind `json:"kind"`
	Preset  string            `json:"preset,omitempty"`
	Command string            `json:"command,omitempty"`
	TodoID  string            `json:"todo_id,omitempty"`
	Cols    int               `json:"cols,omitempty"`
	Rows    int               `json:"rows,omitempty"`
}

func (c *Client) StartProcess(pid string, req StartProc) (p model.Process, err error) {
	return p, c.do("POST", "/api/projects/"+pid+"/processes", req, &p)
}

func (c *Client) StopProcess(id string) error { return c.do("DELETE", "/api/processes/"+id, nil, nil) }

// ---- WebSockets ----

func (c *Client) dial(ctx context.Context, path string) (*websocket.Conn, error) {
	return c.dialRaw(ctx, c.at(path))
}

func (c *Client) dialRaw(ctx context.Context, path string) (*websocket.Conn, error) {
	u := *c.base
	u.Scheme = map[string]string{"https": "wss"}[c.base.Scheme]
	if u.Scheme == "" {
		u.Scheme = "ws"
	}
	full, err := url.Parse(u.String() + path)
	if err != nil {
		return nil, err
	}
	d := websocket.Dialer{Jar: c.http.Jar, HandshakeTimeout: 10 * time.Second}
	conn, res, err := d.DialContext(ctx, full.String(), nil)
	if res != nil && res.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	return conn, err
}

// wireEvent mirrors model.Event but keeps Data raw so it can be decoded per type.
type wireEvent struct {
	Machine   string          `json:"-"` // which computer sent it
	Type      model.EventType `json:"type"`
	ProjectID string          `json:"project_id"`
	TodoID    string          `json:"todo_id"`
	Data      json.RawMessage `json:"data"`
	Time      time.Time       `json:"time"`
}

// StreamEvents connects to /ws/events and delivers events until ctx ends,
// reconnecting with backoff. connected reports link state changes.
func (c *Client) StreamEvents(ctx context.Context, m Machine, deliver func(wireEvent), connected func(bool, error)) {
	backoff := time.Second
	for ctx.Err() == nil {
		conn, err := c.dialRaw(ctx, m.Prefix+"/ws/events")
		if err != nil {
			connected(false, err)
			if errors.Is(err, ErrUnauthorized) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 15*time.Second)
			continue
		}
		backoff = time.Second
		connected(true, nil)
		go func() { <-ctx.Done(); conn.Close() }()
		for {
			var ev wireEvent
			if err := conn.ReadJSON(&ev); err != nil {
				break
			}
			ev.Machine = m.Name
			deliver(ev)
		}
		conn.Close()
		connected(false, nil)
	}
}

// ChatTurn streams one assistant reply for history.
func (c *Client) ChatTurn(ctx context.Context, history []agent.ChatMessage, deliver func(agent.ChatDelta)) error {
	conn, err := c.dialRaw(ctx, "/ws/chat") // chat always runs on this computer
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() { <-ctx.Done(); conn.Close() }()
	if err := conn.WriteJSON(map[string]any{"type": "send", "messages": history}); err != nil {
		return err
	}
	for {
		var d agent.ChatDelta
		if err := conn.ReadJSON(&d); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if d.Kind == "permission" { // approving tool calls needs the web UI
			_ = conn.WriteJSON(map[string]any{"type": "permission", "id": d.ID, "decision": "deny"})
			deliver(agent.ChatDelta{Kind: "text", Text: "\n\n[" + d.Tool + " denied: approve tool calls in the web UI]\n\n"})
			continue
		}
		deliver(d)
		if d.Kind == "done" || d.Kind == "error" {
			return nil
		}
	}
}

// Chat returns the conversation saved on this computer.
func (c *Client) Chat() (ms []agent.ChatMessage, err error) {
	return ms, c.doRaw("GET", "/api/chat", nil, &ms)
}

func (c *Client) ClearChat() error { return c.doRaw("DELETE", "/api/chat", nil, nil) }

// ---- permissions & usage ----

// PermRequest mirrors perm.Request.
type PermRequest struct {
	ID        string          `json:"id"`
	TodoID    string          `json:"todo_id"`
	ProjectID string          `json:"project_id"`
	Tool      string          `json:"tool"`
	Input     json.RawMessage `json:"input"`
	Created   time.Time       `json:"created"`
	Decision  string          `json:"decision"`
}

func (c *Client) Permissions(pid string) (ps []PermRequest, err error) {
	return ps, c.do("GET", "/api/permissions?project="+url.QueryEscape(pid), nil, &ps)
}

// Decide answers a permission request: allow, always or deny.
func (c *Client) Decide(id, decision string) error {
	return c.do("POST", "/api/permissions/"+id, map[string]string{"decision": decision}, nil)
}

// Usage mirrors usage.Snapshot.
type Usage struct {
	Known       bool    `json:"known"`
	PauseAt     float64 `json:"pause_at"`
	ManualPause bool    `json:"manual_pause"`
	Paused      bool    `json:"paused"`
	Reason      string  `json:"reason"`
	Windows     []struct {
		Name        string    `json:"name"`
		Utilization float64   `json:"utilization"`
		ResetsAt    time.Time `json:"resets_at"`
	} `json:"windows"`
}

func (c *Client) Usage() (u Usage, err error) { return u, c.do("GET", "/api/usage", nil, &u) }

func (c *Client) SetQueuePaused(paused bool) (u Usage, err error) {
	path := "/api/queue/resume"
	if paused {
		path = "/api/queue/pause"
	}
	return u, c.do("POST", path, nil, &u)
}

// RunTests re-runs a todo's tests; Duplicate copies it as a draft;
// OpenEditor opens its worktree in the editor configured on the server.
func (c *Client) RunTests(id string) error { return c.do("POST", "/api/todos/"+id+"/test", nil, nil) }
func (c *Client) Duplicate(id string) error {
	return c.do("POST", "/api/todos/"+id+"/duplicate", nil, nil)
}
func (c *Client) OpenEditor(id string) error { return c.do("POST", "/api/todos/"+id+"/open", nil, nil) }

// Revert undoes a merged todo (a revert commit, or a revert pull request).
func (c *Client) Revert(id string) error { return c.do("POST", "/api/todos/"+id+"/revert", nil, nil) }

// Cleanup removes stale worktrees, branches, logs and uploads; it returns a summary.
func (c *Client) Cleanup(pid string) (string, error) {
	var res struct {
		Summary string `json:"summary"`
	}
	err := c.do("POST", "/api/projects/"+pid+"/cleanup", nil, &res)
	return res.Summary, err
}

// CIRun mirrors gitops.Run; CIStatus mirrors ci.Status.
type CIRun struct {
	ID         int64     `json:"id"`
	Workflow   string    `json:"workflow"`
	Title      string    `json:"title"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	SHA        string    `json:"sha"`
	URL        string    `json:"url"`
	CreatedAt  time.Time `json:"created_at"`
}

type CIStatus struct {
	ProjectID string    `json:"project_id"`
	Branch    string    `json:"branch"`
	State     string    `json:"state"` // passing | failing | running | none | unknown
	SHA       string    `json:"sha"`
	Runs      []CIRun   `json:"runs"`
	Error     string    `json:"error"`
	TodoID    string    `json:"todo_id"`
	Checked   time.Time `json:"checked"`
}

// CI returns the build status of a project's default branch; refresh checks GitHub first.
func (c *Client) CI(pid string, refresh bool) (st CIStatus, err error) {
	if refresh {
		return st, c.do("POST", "/api/projects/"+pid+"/ci/refresh", nil, &st)
	}
	return st, c.do("GET", "/api/projects/"+pid+"/ci", nil, &st)
}

// InvestigateCI queues a todo for Claude to fix the failing build.
func (c *Client) InvestigateCI(pid string) (t model.Todo, err error) {
	return t, c.do("POST", "/api/projects/"+pid+"/ci/investigate", nil, &t)
}
