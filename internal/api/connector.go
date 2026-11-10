package api

import (
	"net/http"
)

// connectorStatus tells the web UI's "Connect Claude" guide whether this
// dashboard serves /mcp for a claude.ai custom connector (-public-url), the
// URL to paste into claude.ai, and how many connectors are signed in.
func (s *Server) connectorStatus(w http.ResponseWriter, r *http.Request) {
	type status struct {
		Enabled   bool   `json:"enabled"`
		URL       string `json:"url,omitempty"`
		Connected int    `json:"connected"`
	}
	var st status
	if s.OAuth != nil {
		st.Enabled, st.URL, st.Connected = true, s.OAuth.ResourceFor(r), s.OAuth.Connected()
	}
	writeJSON(w, http.StatusOK, st)
}
