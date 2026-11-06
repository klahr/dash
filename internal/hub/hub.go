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
			if fh := r.Header.Get("X-Forwarded-Host"); fh != "" && u.Host == fh {
				return true
			}
		}
		return u.Host == r.Host
	},
}

// TrustProxy accepts X-Forwarded-Host from a reverse proxy in front of Dash
// when checking WebSocket origins. Set it before serving.
var TrustProxy bool

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 50 * time.Second
	clientBuf  = 256
)

type client struct {
	send    chan model.Event
	project string // optional filter: only events for this project (plus global ones)
}

type Hub struct {
	mu      sync.RWMutex
	clients map[*client]struct{}
}

func New() *Hub { return &Hub{clients: map[*client]struct{}{}} }

// Publish never blocks: a client whose buffer is full is disconnected and
// is expected to reconnect and re-fetch state over REST.
func (h *Hub) Publish(ev model.Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	h.mu.RLock()
	var slow []*client
	for c := range h.clients {
		if c.project != "" && ev.ProjectID != "" && ev.ProjectID != c.project {
			continue
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
}

func (h *Hub) remove(c *client) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
	h.mu.Unlock()
}

// ServeWS handles GET /ws/events[?project=<id>].
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := Upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("ws upgrade", "err", err)
		return
	}
	c := &client{send: make(chan model.Event, clientBuf), project: r.URL.Query().Get("project")}
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
