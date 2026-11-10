package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/orgdomain"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/tunnel"
)

// The runner installer and the hub's version need no sign-in; codes come
// with the hub URL the dialog's one-liner uses.
func TestRunnerInstallerIsPublic(t *testing.T) {
	parallel(t)

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

// With org subdomains, a runner added from an org's subdomain is told that
// address: the install command and the registration point there.
func TestRunnerInstallUsesOrgSubdomain(t *testing.T) {
	parallel(t)

	s, _, _ := boardServer(t)
	s.Peers = &peer.Hub{Self: "home", Runners: &tunnel.Hub{}}
	s.PublicURL = "https://dash.example.com"
	d, err := orgdomain.New("dash.example.com", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	d.UseDefaults(s.PublicURL)
	s.Auth.Domains = d
	h := s.Routes()

	send := func(method, url, body string, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, url, strings.NewReader(body))
		if method != "GET" {
			r.Header.Set("Origin", "https://"+r.Host)
		}
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := send("POST", "https://dash.example.com/api/login", `{"username":"u","password":"p"}`, nil)
	base := w.Result().Cookies()
	w = send("GET", "https://dash.example.com/go/default", "", base)
	w = send("GET", w.Header().Get("Location"), "", base)
	org := w.Result().Cookies()
	if w.Code != http.StatusSeeOther || len(org) == 0 {
		t.Fatalf("handoff: %d %s", w.Code, w.Body)
	}

	w = send("POST", "https://default.dash.example.com/api/runners/codes", "", org)
	var c struct{ Code, Hub string }
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil || c.Code == "" || c.Hub != "https://default.dash.example.com" {
		t.Fatalf("code on the org's subdomain: %d %s", w.Code, w.Body)
	}
	w = send("GET", "https://default.dash.example.com/runner/install.sh", "", nil)
	if !strings.Contains(w.Body.String(), "HUB=https://default.dash.example.com\n") {
		t.Fatalf("install.sh on the org's subdomain: %.300s", w.Body)
	}
	// The base domain still hands out the public URL.
	w = send("GET", "https://dash.example.com/runner/install.sh", "", nil)
	if !strings.Contains(w.Body.String(), "HUB=https://dash.example.com\n") {
		t.Fatalf("install.sh on the base domain: %.300s", w.Body)
	}
}
