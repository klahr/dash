// Package hub fans out model.Events to every connected dashboard WebSocket.
package hub

import (
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/model"
)

// Upgrader is shared by all Dash WebSocket endpoints. Browsers may only open
// sockets from Dash's own pages (same host); clients that send no Origin
// header (CLI tools) are allowed and still need a session cookie.
var Upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		if TrustProxy {
			if fh := r.Header.Get("X-Forwarded-Host"); fh != "" && u.Host == fh && (ForwardedHostOK == nil || ForwardedHostOK(fh)) {
				return true
			}
		}
		return u.Host == r.Host
	},
}

// TrustProxy accepts X-Forwarded-Host from a reverse proxy in front of Dash
// when checking WebSocket origins. Set it before serving.
var TrustProxy bool

// ForwardedHostOK, if set, limits the X-Forwarded-Host values TrustProxy
// accepts (with org subdomains: the base domain and the names under it).
var ForwardedHostOK func(host string) bool

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 50 * time.Second
	clientBuf  = 256
)

type client struct {
	send    chan model.Event
	project string // optional filter: only events for this project (plus global ones)
	sub     Subscriber
}

// Subscriber is who a dashboard connection is for: the org whose events it
// receives, or every org's (All: another Dash, e.g. the hub of this runner,
// which filters for its own users).
type Subscriber struct {
	Org int64
	All bool
}

type Hub struct {
	mu      sync.RWMutex
	clients map[*client]struct{}

	// ProjectOrg finds the org of an event that names a project but not its
	// org. Events of projects it doesn't know (deleted, say) go to nobody
	// but All subscribers. Without it, only events with OrgID set are
	// delivered per org.
	ProjectOrg func(projectID string) (int64, bool)

	// Observe, if set, sees every published event as well, e.g. outgoing
	// notifications (internal/notify). Publishers may hold their own locks
	// (the store does), so it must not block or call back into them. Set it
	// before serving.
	Observe func(model.Event)

	actMu    sync.Mutex
	activity map[string]model.RunEvent // todo id → its latest tool call (see Activity)
}

func New() *Hub { return &Hub{clients: map[*client]struct{}{}} }

// Publish never blocks: a client whose buffer is full is disconnected and
// is expected to reconnect and re-fetch state over REST.
func (h *Hub) Publish(ev model.Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	org, scoped := h.orgOf(ev)
	h.mu.RLock()
	var slow []*client
	for c := range h.clients {
		if c.project != "" && ev.ProjectID != "" && ev.ProjectID != c.project {
			continue
		}
		if scoped && !c.sub.All && c.sub.Org != org {
			continue // another org's event (org 0: nobody's that we know)
		}
		select {
		case c.send <- ev:
		default:
			slow = append(slow, c)
		}
	}
	h.mu.RUnlock()
	for _, c := range slow {
		h.remove(c)
	}
	h.noteActivity(ev)
	if h.Observe != nil {
		h.Observe(ev)
	}
}

// maxActivity bounds the activity map: past it, the oldest entries go.
const maxActivity = 512

// noteActivity keeps each task's latest tool call (the file being edited,
// the command running) for the wall view, and forgets deleted tasks.
func (h *Hub) noteActivity(ev model.Event) {
	if ev.TodoID == "" {
		return
	}
	switch ev.Type {
	case model.EvTodoEvent:
		re, ok := ev.Data.(model.RunEvent)
		if !ok || re.Kind != "tool_use" {
			return
		}
		if re.At.IsZero() {
			re.At = ev.Time
		}
		h.actMu.Lock()
		defer h.actMu.Unlock()
		if h.activity == nil {
			h.activity = map[string]model.RunEvent{}
		}
		h.activity[ev.TodoID] = re
		for len(h.activity) > maxActivity {
			oldest, at := "", time.Time{}
			for id, x := range h.activity {
				if oldest == "" || x.At.Before(at) {
					oldest, at = id, x.At
				}
			}
			delete(h.activity, oldest)
		}
	case model.EvTodoDeleted:
		h.actMu.Lock()
		delete(h.activity, ev.TodoID)
		h.actMu.Unlock()
	}
}

// Activity is task todoID's latest tool call seen since this Dash started.
func (h *Hub) Activity(todoID string) (model.RunEvent, bool) {
	h.actMu.Lock()
	defer h.actMu.Unlock()
	re, ok := h.activity[todoID]
	return re, ok
}

// orgOf is the org ev belongs to; scoped is false for events about Dash as
// a whole, which every dashboard receives.
func (h *Hub) orgOf(ev model.Event) (org int64, scoped bool) {
	if ev.OrgID != 0 {
		return ev.OrgID, true
	}
	if ev.ProjectID == "" {
		return 0, false
	}
	if h.ProjectOrg != nil {
		if org, ok := h.ProjectOrg(ev.ProjectID); ok {
			return org, true
		}
	}
	return 0, true // a project nobody can be shown to be allowed to see
}

// Clients is how many dashboards are connected.
//
// Test hook: nothing in production calls it. internal/api's tests use it
// to wait until their websocket clients have registered before publishing.
// It is exported because those tests live in another package.
func (h *Hub) Clients() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

func (h *Hub) remove(c *client) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
	h.mu.Unlock()
}

// ServeWS handles GET /ws/events[?project=<id>] for sub: it receives the
// events of sub's org and those about Dash as a whole.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request, sub Subscriber) {
	conn, err := Upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("ws upgrade", "err", err)
		return
	}
	c := &client{send: make(chan model.Event, clientBuf), project: r.URL.Query().Get("project"), sub: sub}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()

	// Reader: only needed for pong/close handling; dashboard is server→client.
	go func() {
		defer h.remove(c)
		conn.SetReadLimit(1024)
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))
		conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(pongWait)) })
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(pingPeriod)
	defer func() { ticker.Stop(); conn.Close() }()
	for {
		select {
		case ev, ok := <-c.send:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			if err := conn.WriteJSON(ev); err != nil {
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
