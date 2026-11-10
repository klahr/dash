package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/notify"
	"github.com/klahr/dash/internal/store"
)

func TestWebhookAPI(t *testing.T) {
	s, _, p := boardServer(t)
	s.Notify = &notify.Notifier{Store: raw(s), AllowPrivate: false, Attempts: 1}
	h := s.mux()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		var r *http.Request
		if body != "" {
			r = httptest.NewRequest(method, path, strings.NewReader(body))
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, asOwner(t, s, r))
		return w
	}

	// Private addresses are refused when the Dash doesn't allow them.
	if w := call("POST", "/api/projects/"+p.ID+"/webhooks", `{"url":"http://169.254.169.254/x","format":"json"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("private URL: %d %s", w.Code, w.Body)
	}
	if w := call("POST", "/api/projects/"+p.ID+"/webhooks", `{"url":"https://x.example","format":"carrier-pigeon"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad format: %d %s", w.Code, w.Body)
	}

	w := call("POST", "/api/projects/"+p.ID+"/webhooks", `{"url":"https://hooks.example.com/abc","format":"json","name":"CI","token":"tk_x"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var created struct {
		Webhook model.Webhook `json:"webhook"`
		Secret  string        `json:"secret"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if created.Secret == "" || created.Webhook.ID == "" || !created.Webhook.Enabled || len(created.Webhook.Events) != len(model.DefaultNotifyEvents) || !created.Webhook.HasToken {
		t.Fatalf("created %s", w.Body)
	}

	// Lists never show the secret or the token.
	w = call("GET", "/api/projects/"+p.ID+"/webhooks", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), created.Secret) || strings.Contains(w.Body.String(), "tk_x") || !strings.Contains(w.Body.String(), created.Webhook.ID) {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}

	w = call("PATCH", "/api/webhooks/"+created.Webhook.ID, `{"enabled":false,"events":["task.failed"]}`)
	var up model.Webhook
	_ = json.Unmarshal(w.Body.Bytes(), &up)
	if w.Code != 200 || up.Enabled || len(up.Events) != 1 || !up.HasToken || up.URL != "https://hooks.example.com/abc" {
		t.Fatalf("patch: %d %s", w.Code, w.Body)
	}
	if w := call("PATCH", "/api/webhooks/"+created.Webhook.ID, `{"url":"http://localhost:9000/"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("patch to localhost: %d %s", w.Code, w.Body)
	}
	if w := call("PATCH", "/api/webhooks/"+created.Webhook.ID, `{"token":""}`); w.Code != 200 || strings.Contains(w.Body.String(), `"has_token":true`) {
		t.Fatalf("clear token: %d %s", w.Code, w.Body)
	}

	w = call("POST", "/api/webhooks/"+created.Webhook.ID+"/rotate", "")
	var rot map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &rot)
	if w.Code != 200 || rot["secret"] == "" || rot["secret"] == created.Secret {
		t.Fatalf("rotate: %d %s", w.Code, w.Body)
	}

	if w := call("DELETE", "/api/webhooks/"+created.Webhook.ID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if w := call("DELETE", "/api/webhooks/"+created.Webhook.ID, ""); w.Code != http.StatusNotFound {
		t.Fatalf("delete again: %d", w.Code)
	}
}

func TestWebhookSendTest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get(notify.SignatureHeaderName) == "" {
			w.WriteHeader(400)
		}
	}))
	defer srv.Close()
	s, _, p := boardServer(t)
	s.Notify = &notify.Notifier{Store: raw(s), AllowPrivate: true}
	hook, err := raw(s).CreateWebhook(model.Webhook{ProjectID: p.ID, URL: srv.URL, Format: model.WebhookJSON, Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest("POST", "/api/webhooks/"+hook.ID+"/test", nil)))
	var res struct {
		OK      bool          `json:"ok"`
		Error   string        `json:"error"`
		Webhook model.Webhook `json:"webhook"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if w.Code != 200 || !res.OK || hits.Load() != 1 || res.Webhook.LastEvent != model.NotifyTest || res.Webhook.LastAt.IsZero() {
		t.Fatalf("test: %d %s (%d hits)", w.Code, w.Body, hits.Load())
	}
}

// The ntfy buttons' endpoint needs no session: its token is the credential.
func TestNotifyActionIsPublic(t *testing.T) {
	s, _, _ := boardServer(t)
	var decided string
	s.Notify = &notify.Notifier{Store: raw(s), Actions: notify.NewActions(func(id, d, _ string) error { decided = id + ":" + d; return nil })}
	tok := s.Notify.Actions.Token("perm1", "allow", time.Now().Add(time.Minute))
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, httptest.NewRequest("POST", notify.ActionPath+tok, nil))
	if w.Code != 200 || decided != "perm1:allow" {
		t.Fatalf("action: %d %s, decided %q", w.Code, w.Body, decided)
	}
	w = httptest.NewRecorder()
	s.Routes().ServeHTTP(w, httptest.NewRequest("POST", notify.ActionPath+"forged", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("forged token: %d", w.Code)
	}
	// Everything else under /api/notify still needs a session.
	w = httptest.NewRecorder()
	s.Routes().ServeHTTP(w, httptest.NewRequest("GET", "/api/projects", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("api without a session: %d", w.Code)
	}
}

func TestPersonalWebhookAPI(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	s, _, p := boardServer(t)
	s.Notify = &notify.Notifier{Store: raw(s), AllowPrivate: true, Attempts: 1}
	h := s.mux()
	owner := ownerSession(t, s)
	someoneElse := owner
	someoneElse.User.ID = owner.User.ID + 100
	call := func(sess store.Session, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), sess)))
		return w
	}

	w := call(owner, "POST", "/api/me/webhooks", `{"url":"`+srv.URL+`","format":"json","name":"Mine","project_id":"`+p.ID+`"}`)
	var created struct {
		Webhook model.Webhook `json:"webhook"`
		Secret  string        `json:"secret"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if w.Code != http.StatusCreated || created.Secret == "" || created.Webhook.ProjectID != "" || created.Webhook.UserID != owner.User.ID {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	id := created.Webhook.ID
	if w := call(owner, "GET", "/api/me/webhooks", ""); w.Code != 200 || !strings.Contains(w.Body.String(), id) || strings.Contains(w.Body.String(), created.Secret) {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	// Not among the project's webhooks, and nobody else's to see or change.
	if w := call(owner, "GET", "/api/projects/"+p.ID+"/webhooks", ""); strings.Contains(w.Body.String(), id) {
		t.Fatalf("in the project's list: %s", w.Body)
	}
	if w := call(someoneElse, "GET", "/api/me/webhooks", ""); w.Code != 200 || strings.Contains(w.Body.String(), id) {
		t.Fatalf("someone else's list: %d %s", w.Code, w.Body)
	}
	for _, c := range []endpoint{{"PATCH", "/api/webhooks/" + id, `{"enabled":false}`}, {"DELETE", "/api/webhooks/" + id, ""},
		{"POST", "/api/webhooks/" + id + "/test", ""}, {"GET", "/api/webhooks/" + id + "/deliveries", ""}} {
		if w := call(someoneElse, c.method, c.path, c.body); w.Code != http.StatusNotFound {
			t.Fatalf("someone else %s %s: %d %s", c.method, c.path, w.Code, w.Body)
		}
	}

	// Send test, then its history.
	if w := call(owner, "POST", "/api/webhooks/"+id+"/test", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) || hits.Load() != 1 {
		t.Fatalf("test: %d %s", w.Code, w.Body)
	}
	w = call(owner, "GET", "/api/webhooks/"+id+"/deliveries", "")
	var hist struct {
		Deliveries []model.WebhookDelivery `json:"deliveries"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &hist)
	if w.Code != 200 || len(hist.Deliveries) != 1 || hist.Deliveries[0].Event != model.NotifyTest || hist.Deliveries[0].Status != 200 || hist.Deliveries[0].Attempts != 1 {
		t.Fatalf("deliveries: %d %s", w.Code, w.Body)
	}
	if w := call(owner, "DELETE", "/api/webhooks/"+id, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
}
