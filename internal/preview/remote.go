package preview

// Previews of todos on runners (`dash runner`). A runner starts the preview
// server on its own computer, which the hub can't reach; the hub serves it at
// https://<todo>-<runner>.<domain>/ with the same sign-in and rewriting as a
// local preview, and connects to the server through the runner's preview
// connection (package tunnel), which forwards only to previews the runner
// started itself.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tunnel"
)

// Remote reaches previews running on runners (a *tunnel.Hub).
type Remote interface {
	// Has reports whether runner is a registered runner.
	Has(runner string) bool
	// DialPreview connects to todoID's preview server on runner and returns
	// the port it listens on there.
	DialPreview(ctx context.Context, runner, todoID string) (net.Conn, int, error)
}

// remoteHost is the made-up host the hub's transport addresses a runner's
// preview by: <label>.preview.invalid.
const remoteHost = ".preview.invalid"

// hubURL is where a runner's hub serves its previews.
type hubURL struct {
	template string // https://*.preview.example.com/
	runner   string // this runner's name, as the hub knows it
}

// SetHub tells a runner's previews where its hub serves them: template is a
// URL with "*" for the host's first label (see tunnel.PreviewHeader), or ""
// if the hub has no preview domain; runner is this runner's name there.
func (m *Manager) SetHub(template, runner string) {
	m.hub.Store(&hubURL{template: template, runner: runner})
	for _, p := range m.List() { // their URLs changed
		m.publish(p)
	}
}

// URLTemplate is where this hub serves runners' previews, for a runner that
// connects with r: a URL with "*" in place of <todo>-<runner>, or "" without a
// preview domain. Its scheme and port are Dash's own, as the browser last
// reached it, else as the runner did.
func (m *Manager) URLTemplate(r *http.Request) string {
	if m == nil || m.Domain == "" || m.Remote == nil {
		return ""
	}
	scheme, port := "http", ""
	if o, _ := m.origin.Load().(string); o != "" {
		if u, err := url.Parse(o); err == nil {
			scheme, port = u.Scheme, u.Port()
		}
	} else {
		if m.secure(r) {
			scheme = "https"
		}
		if _, p, err := net.SplitHostPort(m.reqHost(r)); err == nil {
			port = p
		}
	}
	h := "*." + m.Domain
	if port != "" {
		h += ":" + port
	}
	return scheme + "://" + h + "/"
}

// remoteLabel splits a preview host's label into the todo and the runner it
// runs on, if it names a registered runner. Todo ids have no dashes.
func (m *Manager) remoteLabel(label string) (runner, todoID string, ok bool) {
	if m.Remote == nil {
		return "", "", false
	}
	todoID, runner, ok = strings.Cut(label, "-")
	if !ok || todoID == "" || runner == "" || !m.Remote.Has(runner) {
		return "", "", false
	}
	return runner, todoID, true
}

// IsRemote reports whether label is a preview on a runner.
func (m *Manager) IsRemote(label string) bool {
	_, _, ok := m.remoteLabel(label)
	return ok
}

// serveRemote proxies r to todoID's preview on runner.
func (m *Manager) serveRemote(w http.ResponseWriter, r *http.Request, label, runner, todoID, origin string) {
	port, ok := m.remotePorts.Load(label)
	if !ok {
		// The rewriting needs the server's port before the first request;
		// this also shows a starting or crashed preview as such.
		c, p, err := m.Remote.DialPreview(r.Context(), runner, todoID)
		if err != nil {
			m.remoteError(w, err)
			return
		}
		c.Close()
		m.remotePorts.Store(label, p)
		port = p
	}
	m.proxy(label+remoteHost, port.(int), origin, m.remoteTransport()).ServeHTTP(w, r)
}

// remoteTransport carries proxied requests to runners' previews. Its
// connections are streams on the runners' preview connections, pooled per
// preview like any keep-alive connection.
func (m *Manager) remoteTransport() *http.Transport {
	m.remoteOnce.Do(func() {
		m.remoteTr = &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				host, _, _ := net.SplitHostPort(addr)
				label := strings.TrimSuffix(host, remoteHost)
				runner, todoID, ok := m.remoteLabel(label)
				if !ok {
					return nil, tunnel.ErrPreviewNotRunning
				}
				c, port, err := m.Remote.DialPreview(ctx, runner, todoID)
				if err != nil {
					m.remotePorts.Delete(label)
					return nil, err
				}
				m.remotePorts.Store(label, port) // it may have been restarted on another port
				return c, nil
			},
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       30 * time.Second,
			ResponseHeaderTimeout: 0, // slow first compiles, server-sent events
		}
	})
	return m.remoteTr
}

// remoteError shows why a runner's preview can't be reached.
func (m *Manager) remoteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tunnel.ErrPreviewStarting):
		page(w, http.StatusServiceUnavailable, "Starting…", "The preview server is starting, waiting for its port to answer. This page reloads by itself.", "", true)
	case errors.Is(err, tunnel.ErrPreviewCrashed):
		page(w, http.StatusBadGateway, "Preview crashed", "The preview server stopped. Its log is in Dash, on the task.", m.dashOrigin(), false)
	case errors.Is(err, tunnel.ErrPreviewNotRunning):
		page(w, http.StatusNotFound, "Preview not running", "No preview is running for this task. Start one with Preview on the task in Dash.", m.dashOrigin(), false)
	default:
		page(w, http.StatusBadGateway, "Preview not answering", "The preview server didn't answer: "+err.Error(), "", true)
	}
}

// DialPreview connects to todoID's preview server, on a runner for its hub
// (it implements tunnel.PreviewDialer). Only previews this Dash started can be
// reached, at the loopback address their server answered on. Traffic on the
// connection keeps the preview from being stopped as idle.
func (m *Manager) DialPreview(todoID string) (net.Conn, int, error) {
	e := m.entry(todoID)
	if e == nil {
		return nil, 0, tunnel.ErrPreviewNotRunning
	}
	e.mu.Lock()
	state, addr, port, kind := e.info.State, e.addr, e.info.Port, e.info.Kind
	e.mu.Unlock()
	switch {
	case kind == model.PreviewTerminal:
		return nil, 0, tunnel.ErrPreviewNotRunning // no server: it's attached to over /ws/todos/{tid}/preview
	case state == model.PreviewStarting:
		return nil, 0, tunnel.ErrPreviewStarting
	case state != model.PreviewLive || addr == "":
		return nil, 0, tunnel.ErrPreviewCrashed
	}
	e.touch()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, 0, err
	}
	return touchConn{c, e}, port, nil
}

var _ tunnel.PreviewDialer = (*Manager)(nil)
