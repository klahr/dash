package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/notify"
	"github.com/klahr/dash/internal/store"
)

func TestNotifySettingsAPI(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
	}))
	defer srv.Close()
	s, _, p := boardServer(t)
	s.Notify = &notify.Notifier{Store: raw(s), AllowPrivate: true, Attempts: 1, Origin: func(string) string { return "https://dash.example.com" }}
	key, _ := notify.NewSummaryKey()
	k, _ := notify.ParseSummaryKey(key)
	s.Summaries = &notify.Summaries{Store: raw(s), Notifier: s.Notify, Key: k} // no SMTP
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
	type view struct {
		Settings       model.NotifySettings `json:"settings"`
		State          model.NotifyState    `json:"state"`
		EmailAvailable bool                 `json:"email_available"`
		EmailNote      string               `json:"email_note"`
		Webhooks       []model.Webhook      `json:"webhooks"`
		Projects       []struct{ ID string }
		PushChoices    []model.NotifyEvent `json:"push_choices"`
	}
	read := func(w *httptest.ResponseRecorder) view {
		t.Helper()
		var v view
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		return v
	}

	v := read(call(owner, "GET", "/api/me/notifications", ""))
	if v.Settings.On() || v.EmailAvailable || !strings.Contains(v.EmailNote, "SMTP") || len(v.Projects) == 0 || len(v.PushChoices) != len(model.PushChoices) {
		t.Fatalf("defaults %+v", v)
	}
	w := call(owner, "POST", "/api/me/webhooks", `{"url":"`+srv.URL+`","format":"json","name":"Mine","events":[]}`)
	var created struct {
		Webhook model.Webhook `json:"webhook"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	id := created.Webhook.ID

	// Email without SMTP is refused; at least one destination is needed.
	if w := call(owner, "PUT", "/api/me/notifications", `{"summary":"daily","time":"08:00","time_zone":"Europe/Stockholm","all_projects":true,"email":true}`); w.Code != http.StatusBadRequest {
		t.Fatalf("email without SMTP: %d %s", w.Code, w.Body)
	}
	if w := call(owner, "PUT", "/api/me/notifications", `{"summary":"daily","time":"08:00","all_projects":true,"email":false}`); w.Code != http.StatusBadRequest {
		t.Fatalf("no destination: %d %s", w.Code, w.Body)
	}
	body := `{"summary":"weekly","time":"17:45","weekday":5,"time_zone":"Europe/Stockholm","all_projects":false,"projects":["` + p.ID + `"],
		"email":false,"webhooks":["` + id + `"],"include":{"issues":true,"min_severity":"high","failed":true},"push":["task.failed","issue.new"]}`
	v = read(call(owner, "PUT", "/api/me/notifications", body))
	if v.Settings.Summary != "weekly" || v.Settings.Weekday != 5 || v.Settings.Webhooks[0] != id || v.Settings.Include.MinSeverity != "high" ||
		!v.Settings.Include.Review || len(v.Settings.Push) != 2 || v.State.NextAt.IsZero() {
		t.Fatalf("saved %+v", v)
	}
	if again := read(call(owner, "GET", "/api/me/notifications", "")); again.Settings.Time != "17:45" || again.Settings.Projects[0] != p.ID || !again.State.NextAt.Equal(v.State.NextAt) {
		t.Fatalf("read back %+v", again)
	}
	// Someone else can't name the owner's webhook, and has their own settings.
	if w := call(someoneElse, "PUT", "/api/me/notifications", body); w.Code != http.StatusNotFound {
		t.Fatalf("someone else's webhook: %d %s", w.Code, w.Body)
	}

	// Send it now: to the webhook, though it ticked no events.
	w = call(owner, "POST", "/api/me/notifications/test-summary", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("test summary: %d %s", w.Code, w.Body)
	}
	mu.Lock()
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"type":"summary"`) || !strings.Contains(bodies[0], `"period_start"`) {
		t.Fatalf("delivered %v", bodies)
	}
	mu.Unlock()

	// The unsubscribe link needs no session.
	tok := s.Summaries.UnsubscribeToken(owner.User.ID, owner.Org.Org.ID)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest("GET", notify.UnsubscribePath+tok, nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Stop the summary") {
		t.Fatalf("unsubscribe page: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest("POST", notify.UnsubscribePath+tok, nil))
	if rec.Code != 200 {
		t.Fatalf("unsubscribe: %d %s", rec.Code, rec.Body)
	}
	if v := read(call(owner, "GET", "/api/me/notifications", "")); v.Settings.On() || !v.State.NextAt.IsZero() {
		t.Fatalf("still on %+v", v)
	}
	rec = httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest("POST", notify.UnsubscribePath+"forged", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("forged: %d", rec.Code)
	}
}
