// Package peer lets one Dash show other computers' Dash servers in the same
// dashboard. Each computer keeps its own repositories, worktrees and Claude
// login; the Dash you open (the hub) forwards /peer/<name>/… to the other one.
//
// The other computers are runners (`dash runner`), which connect out to the
// hub through a tunnel (package tunnel); the hub reaches their API through it.
package peer

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/tunnel"
)

// Machine is one computer in the dashboard.
type Machine struct {
	Name   string `json:"name"`
	Local  bool   `json:"local"`           // the Dash serving this page
	Prefix string `json:"prefix"`          // path prefix for its API: "" or "/peer/<name>"
	OK     bool   `json:"ok"`              // reachable
	Error  string `json:"error,omitempty"` // why not
	// Runner: it connects to this hub (`dash runner`) and is listed even
	// while offline; LastSeen is when it last connected or disconnected.
	Runner   bool       `json:"runner,omitempty"`
	LastSeen *time.Time `json:"last_seen,omitempty"`
}

// Hub forwards requests to runners and reports which machines are reachable.
type Hub struct {
	Self string // this machine's name
	// Runners are computers that connect to this hub themselves (optional).
	Runners *tunnel.Hub
}

// ForwardedPath is the path a /peer/{name}/… request reaches on the runner,
// decoded as it is sent (so "projects%2Fx" becomes "projects/x"). Decide
// what a forwarded request does from this, not from the hub's route pattern,
// which matches on escaped segments.
func ForwardedPath(r *http.Request) string {
	return strings.TrimPrefix(r.URL.Path, "/peer/"+r.PathValue("name"))
}

// ServeHTTP handles /peer/{name}/….
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var px http.Handler
	if h.Runners != nil {
		px, _ = h.Runners.Proxy(r.PathValue("name"))
	}
	if px == nil {
		http.Error(w, `{"error":"no such computer"}`, http.StatusNotFound)
		return
	}
	// The runner sees no cookie and no Origin, so it can't tell a browser
	// socket from a CLI one: check the browser's origin here, as the hub's
	// own WebSocket endpoints do, before forwarding the upgrade.
	if isUpgrade(r) && !hub.Upgrader.CheckOrigin(r) {
		http.Error(w, `{"error":"origin not allowed"}`, http.StatusForbidden)
		return
	}
	px.ServeHTTP(w, r)
}

// isUpgrade reports whether r asks to switch protocols (e.g. to WebSocket).
func isUpgrade(r *http.Request) bool {
	return r.Header.Get("Upgrade") != "" ||
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// Machines lists this computer and every runner.
func (h *Hub) Machines(ctx context.Context) []Machine {
	out := []Machine{{Name: h.Self, Local: true, OK: true}}
	if h.Runners != nil {
		for _, r := range h.Runners.List() {
			m := Machine{Name: r.Name, Prefix: "/peer/" + r.Name, OK: r.Online, Runner: true}
			if !r.LastSeen.IsZero() {
				m.LastSeen = &r.LastSeen
			}
			if !r.Online {
				m.Error = "offline"
			}
			out = append(out, m)
		}
	}
	return out
}

// Taken reports whether name is this computer's, for naming runners.
func (h *Hub) Taken(name string) bool { return name == h.Self }
