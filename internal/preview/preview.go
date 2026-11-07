// Package preview runs live previews of todos: the project's run_command in
// the todo's worktree, one per todo, served through a reverse proxy at a
// subdomain of its own (https://<todo>.<domain>/) so it opens from any device
// that reaches Dash, and so apps that assume they're served from / work.
//
// A preview stops when its todo leaves review (approved, sent back, merged,
// deleted), when it is stopped by hand, or after IdleAfter without traffic.
package preview

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/runner"
)

type Publisher interface{ Publish(model.Event) }

// Todos looks todos up, to stop previews of todos that left review.
type Todos interface {
	GetTodo(id string) (model.Todo, error)
}

var (
	ErrNoRunCommand = errors.New("this project has no start server command (run_command) to preview with")
	ErrNotInReview  = errors.New("only tasks ready for review can be previewed")
	ErrNotFound     = errors.New("no preview running for this task")
)

// DefaultIdle is how long a preview runs without traffic before it's stopped.
const DefaultIdle = 30 * time.Minute

type entry struct {
	mu   sync.Mutex
	info model.Preview
	addr string // host:port the server answered on (it may listen on ::1 only)

	seen atomic.Int64 // unix nanos of the last traffic
	done chan struct{}
	gone bool // stopped or replaced: the watcher must not touch the map
}

func (e *entry) snapshot() model.Preview {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.info
	p.LastSeen = time.Unix(0, e.seen.Load())
	return p
}

func (e *entry) touch() { e.seen.Store(time.Now().UnixNano()) }

type Manager struct {
	Procs *runner.Manager
	Pub   Publisher
	Todos Todos
	// Domain is the parent of the preview hosts, e.g. "preview.example.com"
	// for https://<todo>.preview.example.com. Empty: previews still run, but
	// are only reachable as http://localhost:<port> on this computer.
	Domain string
	// TrustProxy honours X-Forwarded-Host/-Proto from a TLS-terminating proxy.
	TrustProxy bool
	// Secure reports whether a request reached Dash over HTTPS.
	Secure    func(*http.Request) bool
	IdleAfter time.Duration
	// OriginFor, if set, is the Dash origin a todo's preview signs in at
	// and may be captured from (with org subdomains, the todo's org's);
	// "" falls back to Dash's origin as last seen.
	OriginFor func(todoID string) string
	// Remote reaches previews running on runners (the hub's side, remote.go).
	// Their hosts are <todo>-<runner>.<domain>. Optional.
	Remote Remote

	hub         atomic.Pointer[hubURL] // on a runner: where its hub serves its previews
	remotePorts sync.Map               // label → int, the port a runner's preview listens on
	remoteOnce  sync.Once
	remoteTr    *http.Transport

	key    []byte       // signs preview tokens and cookies; new on every start
	origin atomic.Value // string: Dash's own origin, as last seen by the UI

	mu      sync.Mutex
	entries map[string]*entry // todo id → its preview
}

func New(procs *runner.Manager, pub Publisher, todos Todos, domain string) *Manager {
	return &Manager{
		Procs: procs, Pub: pub, Todos: todos,
		Domain:    strings.Trim(strings.ToLower(domain), "."),
		IdleAfter: DefaultIdle,
		key:       newKey(),
		entries:   map[string]*entry{},
	}
}

// Start runs t's preview, or returns the one already starting or live.
// A crashed preview is started again.
func (m *Manager) Start(t model.Todo, p model.Project) (model.Preview, error) {
	if strings.TrimSpace(p.RunCommand) == "" {
		return model.Preview{}, ErrNoRunCommand
	}
	if t.Status != model.StatusReview || t.Worktree == nil {
		return model.Preview{}, ErrNotInReview
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.entries[t.ID]; e != nil {
		if s := m.info(e); s.State != model.PreviewCrashed {
			e.touch()
			return s, nil
		}
		e.mu.Lock()
		e.gone = true
		e.mu.Unlock()
		delete(m.entries, t.ID)
	}
	proc, err := m.Procs.StartWebFor(p.ID, t.ID, t.Worktree.Path, p.RunCommand)
	if err != nil {
		return model.Preview{}, err
	}
	e := &entry{done: make(chan struct{}), info: model.Preview{
		TodoID: t.ID, ProjectID: p.ID, ProcessID: proc.ID, Title: todoTitle(t), Port: proc.Port, State: model.PreviewStarting,
		LocalURL: fmt.Sprintf("http://localhost:%d/", proc.Port), StartedAt: proc.StartedAt,
	}}
	e.touch()
	m.entries[t.ID] = e
	go m.watch(e)
	m.publish(m.info(e))
	return m.info(e), nil
}

// info is e as the UI sees it, with the addresses it is served at now.
func (m *Manager) info(e *entry) model.Preview {
	p := e.snapshot()
	p.URL, p.OpenURL = m.URL(p.TodoID), ""
	if p.URL != "" {
		p.OpenURL = "/preview/" + p.TodoID
	}
	return p
}

// URL is where t's preview is served, or "" without a preview domain. The
// proxy is Dash's own listener, so it has Dash's scheme and port; on a
// runner connected to a hub with a preview domain, it's the hub's.
func (m *Manager) URL(todoID string) string {
	if h := m.hub.Load(); h != nil && h.template != "" {
		return strings.Replace(h.template, "*", todoID+"-"+h.runner, 1)
	}
	if m.Domain == "" {
		return ""
	}
	scheme, h := "https", todoID+"."+m.Domain
	if u, err := url.Parse(m.dashOrigin()); err == nil && u.Scheme != "" {
		scheme = u.Scheme
		if p := u.Port(); p != "" {
			h += ":" + p
		}
	}
	return scheme + "://" + h + "/"
}

// watch waits for the server's port to answer (starting → live) and for
// the process to exit (→ crashed, unless it was stopped).
func (m *Manager) watch(e *entry) {
	info := e.snapshot()
	sess, err := m.Procs.Attach(info.ProcessID)
	if err != nil {
		return
	}
	exited := make(chan struct{})
	go func() {
		for range sess.Output { // drain: only the end matters here
		}
		close(exited)
	}()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	probing := true
	for {
		select {
		case <-exited:
			m.exited(e)
			return
		case <-e.done:
			sess.Detach()
			return
		case <-tick.C:
			if !probing {
				continue
			}
			if addr := dial(info.Port); addr != "" {
				probing = false
				e.mu.Lock()
				live := !e.gone
				if live {
					e.addr, e.info.State = addr, model.PreviewLive
				}
				e.mu.Unlock()
				if live {
					m.publish(m.info(e))
				}
			}
		}
	}
}

// dial finds which loopback address answers on port: dev servers that
// listen on "localhost" are often IPv6-only.
func dial(port int) string {
	for _, host := range []string{"127.0.0.1", "::1"} {
		addr := net.JoinHostPort(host, fmt.Sprint(port))
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return addr
		}
	}
	return ""
}

func (m *Manager) exited(e *entry) {
	e.mu.Lock()
	if e.gone {
		e.mu.Unlock()
		return
	}
	e.info.State = model.PreviewCrashed
	if p, err := m.Procs.Get(e.info.ProcessID); err == nil {
		e.info.ExitCode = p.ExitCode
	}
	e.mu.Unlock()
	m.publish(m.info(e))
}

// Get returns todoID's preview.
func (m *Manager) Get(todoID string) (model.Preview, bool) {
	e := m.entry(todoID)
	if e == nil {
		return model.Preview{}, false
	}
	return m.info(e), true
}

func (m *Manager) entry(todoID string) *entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entries[todoID]
}

// List returns every preview, newest first.
func (m *Manager) List() []model.Preview {
	m.mu.Lock()
	out := make([]model.Preview, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, m.info(e))
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// Stop stops todoID's preview; reason says why (shown in the UI).
func (m *Manager) Stop(todoID, reason string) error {
	m.mu.Lock()
	e := m.entries[todoID]
	delete(m.entries, todoID)
	m.mu.Unlock()
	if e == nil {
		return ErrNotFound
	}
	e.mu.Lock()
	e.gone = true
	close(e.done)
	e.mu.Unlock()
	if err := m.Procs.Stop(e.info.ProcessID); err != nil && !errors.Is(err, runner.ErrNotFound) {
		slog.Warn("stop preview", "todo", todoID, "err", err)
	}
	info := m.info(e)
	info.State, info.Reason = model.PreviewStopped, reason
	m.publish(info)
	return nil
}

// StopAll stops every preview and reports how many there were.
func (m *Manager) StopAll(reason string) int {
	n := 0
	for _, p := range m.List() {
		if m.Stop(p.TodoID, reason) == nil {
			n++
		}
	}
	return n
}

// Observe sees every store event: a todo that leaves review (approved, sent
// back, merged) or is deleted has its preview stopped. It never blocks, as
// the store calls it holding its lock.
func (m *Manager) Observe(ev model.Event) {
	var reason string
	switch ev.Type {
	case model.EvTodoDeleted:
		reason = "the task was deleted"
	case model.EvTodoUpdated:
		t, ok := ev.Data.(model.Todo)
		if !ok || t.Status == model.StatusReview {
			return
		}
		reason = leftReview(t.Status)
	default:
		return
	}
	if m.entry(ev.TodoID) != nil {
		go func() { _ = m.Stop(ev.TodoID, reason) }()
	}
}

func leftReview(s model.TodoStatus) string {
	switch s {
	case model.StatusMerging:
		return "the task was approved"
	case model.StatusMerged:
		return "the task was merged"
	case model.StatusQueued, model.StatusInProgress:
		return "the task was sent back"
	}
	return "the task left review"
}

// Run stops idle previews, and those of todos no longer in review, until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.StopAll("Dash is shutting down")
			return
		case <-t.C:
			m.sweep(time.Now())
		}
	}
}

func (m *Manager) sweep(now time.Time) {
	idle := m.IdleAfter
	if idle <= 0 {
		idle = DefaultIdle
	}
	for _, p := range m.List() {
		if now.Sub(p.LastSeen) > idle {
			_ = m.Stop(p.TodoID, fmt.Sprintf("no traffic for %d minutes", int(idle.Minutes())))
			continue
		}
		if m.Todos == nil {
			continue
		}
		if t, err := m.Todos.GetTodo(p.TodoID); err != nil {
			_ = m.Stop(p.TodoID, "the task was deleted")
		} else if t.Status != model.StatusReview {
			_ = m.Stop(p.TodoID, leftReview(t.Status))
		}
	}
}

func (m *Manager) publish(p model.Preview) {
	if m.Pub != nil {
		m.Pub.Publish(model.Event{Type: model.EvPreview, ProjectID: p.ProjectID, TodoID: p.TodoID, Data: p, Time: time.Now()})
	}
}

func todoTitle(t model.Todo) string {
	if t.Title != "" {
		return t.Title
	}
	s, _, _ := strings.Cut(strings.TrimSpace(t.Prompt), "\n")
	return s
}
