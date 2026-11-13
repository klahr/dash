package claudestatus

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type memStore struct{ data []byte }

func (m *memStore) ClaudeStatus() ([]byte, error)  { return m.data, nil }
func (m *memStore) PutClaudeStatus(b []byte) error { m.data = append([]byte{}, b...); return nil }

var t0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func newService(t *testing.T) (*Service, *[]Status) {
	t.Helper()
	var got []Status
	s := &Service{Store: &memStore{}, Now: func() time.Time { return t0 }, Publish: func(st Status) { got = append(got, st) }}
	on := true
	a, err := s.Configure(&on, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Token == "" || a.WebhookPath != WebhookPrefix+a.Token {
		t.Fatalf("turning on made no token: %+v", a)
	}
	return s, &got
}

const incidentHook = `{
  "meta": {"unsubscribe": "https://status.example.com/?unsubscribe=x"},
  "page": {"id": "p1", "status_indicator": "major", "status_description": "Partial System Outage"},
  "incident": {
    "id": "inc1", "name": "Elevated errors on Claude Opus", "status": "identified", "impact": "major",
    "shortlink": "https://stspg.example.com/abc",
    "created_at": "2026-11-13T11:00:00Z", "updated_at": "2026-11-13T11:30:00Z", "resolved_at": null,
    "incident_updates": [
      {"id": "u1", "status": "investigating", "body": "We are investigating.", "created_at": "2026-11-13T11:00:00Z",
       "affected_components": [{"code": "c1", "name": "Claude API", "old_status": "operational", "new_status": "partial_outage"}]},
      {"id": "u2", "status": "identified", "body": "The cause is found.", "created_at": "2026-11-13T11:30:00Z"}
    ]
  }
}`

func TestWebhookIncident(t *testing.T) {
	s, pub := newService(t)
	tok := s.doc.Settings.Token
	if err := s.Webhook("wrong", []byte(incidentHook)); !errors.Is(err, ErrRefused) {
		t.Fatalf("wrong token: %v", err)
	}
	if err := s.Webhook(tok, []byte(incidentHook)); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Status()
	if st.Indicator != "major" || st.Description != "Partial System Outage" {
		t.Errorf("indicator %q %q", st.Indicator, st.Description)
	}
	if len(st.Active) != 1 || st.Active[0].Name != "Elevated errors on Claude Opus" || st.Active[0].Updates[0].ID != "u2" {
		t.Fatalf("active: %+v", st.Active)
	}
	if c := st.Active[0].Components; len(c) != 1 || c[0] != "Claude API" {
		t.Errorf("components: %v", c)
	}
	if len(*pub) != 2 { // Configure, then the webhook
		t.Errorf("published %d times", len(*pub))
	}

	// Resolved: it moves to the recent ones.
	resolved := `{"incident": {"id": "inc1", "name": "Elevated errors on Claude Opus", "status": "resolved", "impact": "major",
	  "created_at": "2026-11-13T11:00:00Z", "resolved_at": "2026-11-13T11:50:00Z",
	  "incident_updates": [{"id": "u3", "status": "resolved", "body": "Fixed.", "created_at": "2026-11-13T11:50:00Z"}]}}`
	if err := s.Webhook(tok, []byte(resolved)); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Status()
	if len(st.Active) != 0 || len(st.Recent) != 1 {
		t.Fatalf("after resolving: %d active, %d recent", len(st.Active), len(st.Recent))
	}
	if n := len(st.Recent[0].Updates); n != 3 {
		t.Errorf("updates kept: %d, want 3", n)
	}
	if st.Recent[0].URL != "https://stspg.example.com/abc" {
		t.Errorf("link lost: %q", st.Recent[0].URL)
	}
	if st.Indicator != "none" {
		t.Errorf("indicator after resolving %q, want none", st.Indicator)
	}

	// It survives a restart.
	again := &Service{Store: s.Store, Now: s.Now}
	if st2, _ := again.Status(); len(st2.Recent) != 1 {
		t.Errorf("not saved: %+v", st2)
	}
}

func TestWebhookComponent(t *testing.T) {
	s, _ := newService(t)
	body := `{"component_update": {"created_at": "2026-11-13T11:00:00Z", "new_status": "major_outage", "old_status": "operational", "component_id": "c1"},
	  "component": {"id": "c1", "name": "Claude Code", "status": "major_outage"}}`
	if err := s.Webhook(s.doc.Settings.Token, []byte(body)); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Status()
	if len(st.Components) != 1 || st.Components[0].Name != "Claude Code" || st.Indicator != "critical" {
		t.Fatalf("status: %+v", st)
	}
	a, _ := s.Admin()
	if len(a.Deliveries) != 1 || a.Deliveries[0].Summary != "Claude Code: major outage" {
		t.Errorf("deliveries: %+v", a.Deliveries)
	}
	evil := `{"incident": {"id": "x", "name": "x", "status": "investigating", "shortlink": "javascript:alert(1)"}}`
	if err := s.Webhook(s.doc.Settings.Token, []byte(evil)); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Status(); st.Active[0].URL != "" {
		t.Errorf("kept link %q", st.Active[0].URL)
	}
	if err := s.Webhook(s.doc.Settings.Token, []byte(`{"hello": 1}`)); !errors.Is(err, ErrBadPayload) {
		t.Errorf("not a webhook: %v", err)
	}
}

func TestOffAndRotate(t *testing.T) {
	s, _ := newService(t)
	tok := s.doc.Settings.Token
	a, err := s.Rotate()
	if err != nil || a.Token == tok {
		t.Fatalf("rotate: %v", err)
	}
	if err := s.Webhook(tok, []byte(incidentHook)); !errors.Is(err, ErrRefused) {
		t.Errorf("old token: %v", err)
	}
	off := false
	if _, err := s.Configure(&off, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Webhook(a.Token, []byte(incidentHook)); !errors.Is(err, ErrRefused) {
		t.Errorf("while off: %v", err)
	}
	if st, _ := s.Status(); st.Enabled || len(st.Active) != 0 {
		t.Errorf("off shows %+v", st)
	}
	bad := "ftp://example.com"
	if _, err := s.Configure(nil, &bad, nil); !errors.Is(err, ErrBadPage) {
		t.Errorf("bad page: %v", err)
	}
}

func TestSync(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/summary.json":
			_, _ = w.Write([]byte(`{"status": {"indicator": "minor", "description": "Minor Service Outage"},
			  "components": [{"id": "c1", "name": "claude.ai", "status": "degraded_performance"}, {"id": "g", "name": "Group", "status": "operational", "group": true}],
			  "incidents": [{"id": "i2", "name": "Slow responses", "status": "investigating", "impact": "minor", "created_at": "2026-11-13T11:00:00Z"}]}`))
		case "/api/v2/incidents.json":
			_, _ = w.Write([]byte(`{"incidents": [
			  {"id": "i2", "name": "Slow responses", "status": "investigating", "impact": "minor", "created_at": "2026-11-13T11:00:00Z"},
			  {"id": "i1", "name": "Login errors", "status": "resolved", "impact": "major", "created_at": "2026-11-12T08:00:00Z", "resolved_at": "2026-11-12T09:00:00Z"},
			  {"id": "i0", "name": "Ancient", "status": "resolved", "impact": "major", "created_at": "2026-08-01T08:00:00Z", "resolved_at": "2026-08-01T09:00:00Z"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s, pub := newService(t)
	page := srv.URL
	if _, err := s.Configure(nil, &page, nil); err != nil {
		t.Fatal(err)
	}
	n := len(*pub)
	st, err := s.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Indicator != "minor" || len(st.Active) != 1 || len(st.Recent) != 1 || len(st.Components) != 1 {
		t.Fatalf("synced: %+v", st)
	}
	if len(*pub) != n+1 {
		t.Errorf("a change wasn't published")
	}
	if _, err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*pub) != n+1 {
		t.Errorf("no change was published")
	}
	if len(s.doc.Incidents) != 2 {
		t.Errorf("kept %d incidents, want the old one dropped", len(s.doc.Incidents))
	}
}
