package api

import (
	"net"
	"net/http"

	"github.com/klahr/dash/internal/auth"
)

// connectorStatus tells the web UI's "Connect Claude" guide whether Dash
// serves /mcp for a claude.ai custom connector (-public-url), the URL to
// paste into claude.ai, and how many connectors are signed in. When it's
// off, Local is where a proxy (Caddy, a Cloudflare Tunnel, Tailscale Funnel)
// should forward to.
func (s *Server) connectorStatus(w http.ResponseWriter, r *http.Request) {
	type status struct {
		Enabled   bool     `json:"enabled"`
		URL       string   `json:"url,omitempty"`
		Connected int      `json:"connected"`
		Local     string   `json:"local"`
		Paths     []string `json:"paths"`
	}
	st := status{
		Local: localURL(s.Addr),
		Paths: []string{auth.MCPPath, "/authorize", "/token", "/register", "/.well-known"},
	}
	if s.OAuth != nil {
		st.Enabled, st.URL, st.Connected = true, s.OAuth.ResourceFor(r), s.OAuth.Connected()
	}
	writeJSON(w, http.StatusOK, st)
}

// localURL is the plain-HTTP origin a proxy on this machine reaches Dash on.
func localURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return "http://127.0.0.1:7788"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
