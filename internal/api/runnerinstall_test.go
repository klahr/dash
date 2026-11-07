package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/tunnel"
)

// The runner installer and the hub's version need no sign-in; codes come
// with the hub URL the dialog's one-liner uses.
func TestRunnerInstallerIsPublic(t *testing.T) {
	s, _, _ := boardServer(t)
	s.Peers = &peer.Hub{Self: "home", Runners: &tunnel.Hub{}}
	s.PublicURL = "https://dash.example.com"
	h := s.Routes()

	r := httptest.NewRequest("GET", "/runner/install.sh", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), "#!/bin/sh") || !strings.Contains(w.Body.String(), "HUB=https://dash.example.com\n") {
		t.Fatalf("install.sh: %d %.200s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/runner/version", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"version"`) {
		t.Fatalf("version: %d %s", w.Code, w.Body)
	}

	// Making a code still needs a session.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/runners/codes", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous code: %d", w.Code)
	}
	w = httptest.NewRecorder()
	loggedIn(t, h).ServeHTTP(w, httptest.NewRequest("POST", "/api/runners/codes", nil))
	var c struct{ Code, Hub string }
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil || c.Code == "" || c.Hub != "https://dash.example.com" {
		t.Fatalf("code: %d %s", w.Code, w.Body)
	}
}
