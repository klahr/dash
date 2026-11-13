package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/claudestatus"
	"github.com/klahr/dash/internal/store"
)

func TestClaudeStatusAPI(t *testing.T) {
	s, _, _ := boardServer(t)
	s.ClaudeStatus = &claudestatus.Service{Store: raw(s)}
	mux, public := s.mux(), s.Routes()
	admin := ownerSession(t, s)
	member := admin
	member.User.PlatformAdmin = false
	call := func(sess *store.Session, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		h := public // past the sign-in check, which signs in only with a cookie
		if sess != nil {
			r, h = r.WithContext(auth.WithSession(r.Context(), *sess)), mux
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// Only platform admins see and change the settings.
	if w := call(&member, "GET", "/api/admin/claude-status", ""); w.Code != http.StatusForbidden {
		t.Fatalf("member reads settings: %d", w.Code)
	}
	if w := call(&member, "PUT", "/api/admin/claude-status", `{"enabled":true}`); w.Code != http.StatusForbidden {
		t.Fatalf("member changes settings: %d", w.Code)
	}
	if w := call(&admin, "PUT", "/api/admin/claude-status", `{"page_url":"file:///etc"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad page: %d %s", w.Code, w.Body)
	}
	w := call(&admin, "PUT", "/api/admin/claude-status", `{"enabled":true}`)
	var v struct {
		WebhookURL  string `json:"webhook_url"`
		WebhookPath string `json:"webhook_path"`
		PageURL     string `json:"page_url"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	if w.Code != 200 || v.WebhookPath == "" || !strings.HasSuffix(v.WebhookURL, v.WebhookPath) || v.PageURL != claudestatus.DefaultPage {
		t.Fatalf("turn on: %d %s", w.Code, w.Body)
	}

	// The status page posts without a session: the token is its credential.
	hook := `{"page":{"status_indicator":"minor","status_description":"Minor Service Outage"},
	  "incident":{"id":"i1","name":"Slow Claude Code","status":"investigating","impact":"minor","created_at":"2026-11-13T11:00:00Z",
	  "incident_updates":[{"id":"u1","status":"investigating","body":"Looking into it.","created_at":"2026-11-13T11:00:00Z"}]}}`
	if w := call(nil, "POST", claudestatus.WebhookPrefix+"wrong", hook); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", w.Code)
	}
	if w := call(nil, "POST", v.WebhookPath, "not json"); w.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", w.Code)
	}
	if w := call(nil, "POST", v.WebhookPath, hook); w.Code != 200 {
		t.Fatalf("webhook: %d %s", w.Code, w.Body)
	}

	// Everyone signed in sees the status; nobody else does.
	if w := call(nil, "GET", "/api/claude-status", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("status without a session: %d", w.Code)
	}
	w = call(&member, "GET", "/api/claude-status", "")
	var st claudestatus.Status
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	if w.Code != 200 || st.Indicator != "minor" || len(st.Active) != 1 || st.Active[0].Name != "Slow Claude Code" {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), v.WebhookPath) {
		t.Fatal("the status shows the webhook's token")
	}

	// Only platform admins choose the components that count.
	if w := call(&member, "PUT", "/api/admin/claude-status", `{"components":["Cowork"]}`); w.Code != http.StatusForbidden {
		t.Fatalf("member chooses components: %d", w.Code)
	}
	if w := call(nil, "PUT", "/api/admin/claude-status", `{"components":["Cowork"]}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("components without a session: %d", w.Code)
	}
	var adm struct {
		Components []struct {
			Name    string `json:"name"`
			Watched bool   `json:"watched"`
		} `json:"components"`
		DefaultWatch bool `json:"default_watch"`
	}
	w = call(&admin, "GET", "/api/admin/claude-status", "")
	_ = json.Unmarshal(w.Body.Bytes(), &adm)
	if w.Code != 200 || !adm.DefaultWatch || len(adm.Components) != len(claudestatus.DefaultComponents) {
		t.Fatalf("default components: %d %s", w.Code, w.Body)
	}
	// The incident above names no component: it counts whatever is chosen.
	w = call(&admin, "PUT", "/api/admin/claude-status", `{"components":["Cowork"]}`)
	adm.Components, adm.DefaultWatch = nil, true
	_ = json.Unmarshal(w.Body.Bytes(), &adm)
	if w.Code != 200 || adm.DefaultWatch || len(adm.Components) != 1 || adm.Components[0].Name != "Cowork" || !adm.Components[0].Watched {
		t.Fatalf("choose components: %d %s", w.Code, w.Body)
	}
	if w := call(&admin, "PUT", "/api/admin/claude-status", `{"components":["`+strings.Repeat("x", 300)+`"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("too long a name: %d", w.Code)
	}
	w = call(&admin, "PUT", "/api/admin/claude-status", `{"default_components":true}`)
	_ = json.Unmarshal(w.Body.Bytes(), &adm)
	if w.Code != 200 || !adm.DefaultWatch {
		t.Fatalf("back to defaults: %d %s", w.Code, w.Body)
	}

	// A new URL: the old one stops working.
	w = call(&admin, "POST", "/api/admin/claude-status/rotate", "")
	if w.Code != 200 {
		t.Fatalf("rotate: %d", w.Code)
	}
	if w := call(nil, "POST", v.WebhookPath, hook); w.Code != http.StatusUnauthorized {
		t.Fatalf("old URL after rotating: %d", w.Code)
	}
}
