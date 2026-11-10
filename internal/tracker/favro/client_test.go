package favro

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tracker"
	"github.com/klahr/dash/internal/tracker/favro/favrotest"
)

// world is a fake Favro with one account, organization and board.
type world struct {
	fv     *favrotest.Server
	org    string
	coll   string
	widget string
	cols   []string // Backlog, To do, Doing, Review, Done
}

const (
	email = "ada@example.com"
	token = "tok-SECRET-1"
)

func newWorld(t *testing.T) *world {
	t.Helper()
	fv := favrotest.New(t)
	AllowBaseURL(fv.APIURL())
	fv.AddAccount(email, token)
	w := &world{fv: fv}
	w.org = fv.AddOrg("Acme", email)
	w.coll = fv.AddCollection(w.org, "Engineering")
	w.widget, w.cols = fv.AddBoard(w.org, w.coll, "Sprint", "Backlog", "To do", "Doing", "Review", "Done")
	return w
}

func (w *world) config(extra map[string]any) tracker.ProjectConfig {
	c := map[string]any{"email": email, "organization_id": w.org, "widget_common_id": w.widget, "base_url": w.fv.APIURL()}
	for k, v := range extra {
		c[k] = v
	}
	raw, _ := json.Marshal(c)
	return tracker.ProjectConfig{ProjectID: "p1", Backend: Name, Config: raw, Credentials: map[string]string{"token": token},
		StatusMap: Registration().DefaultStatusMap}
}

func (w *world) backend(t *testing.T, extra map[string]any) *Backend {
	t.Helper()
	b, err := New(w.config(extra), map[string]string{"token": token})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sleeps records a client's waits instead of waiting.
type sleeps struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *sleeps) sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d = append(s.d, d)
	return nil
}

func (s *sleeps) all() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.d...)
}

func TestAuthHeaders(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	b := w.backend(t, nil)
	if _, err := b.List(context.Background(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Options(context.Background(), w.config(nil), map[string]string{"token": token}, "organization_id"); err != nil {
		t.Fatal(err)
	}
	reqs := w.fv.Requests()
	if len(reqs) < 3 {
		t.Fatalf("requests: %+v", reqs)
	}
	for _, r := range reqs {
		if r.Email != email || r.Token != token {
			t.Errorf("%s %s: basic auth %q/%q", r.Method, r.Path, r.Email, r.Token)
		}
		if want := w.org; r.Path != "/api/v1/organizations" && r.Org != want {
			t.Errorf("%s %s: organizationId %q, want %q", r.Method, r.Path, r.Org, want)
		}
	}

	// A wrong token is refused, and the error doesn't carry it.
	bad, err := New(w.config(nil), map[string]string{"token": "tok-WRONG-9"})
	if err != nil {
		t.Fatal(err)
	}
	err = bad.Test(context.Background())
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 401 || strings.Contains(err.Error(), "tok-WRONG-9") || strings.Contains(err.Error(), token) {
		t.Fatalf("wrong token: %v", err)
	}
}

func TestMissingSettings(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	cfg := w.config(map[string]any{"widget_common_id": ""})
	if _, err := New(cfg, map[string]string{"token": token}); err == nil || !strings.Contains(err.Error(), "board") {
		t.Fatalf("no board: %v", err)
	}
	if _, err := New(w.config(nil), nil); err == nil || !strings.Contains(err.Error(), "API token") {
		t.Fatalf("no token: %v", err)
	}
	cfg = w.config(map[string]any{"base_url": "https://evil.example.com/api/v1"})
	if _, err := New(cfg, map[string]string{"token": token}); err == nil {
		t.Fatal("a config sent the token to another host")
	}
}

func TestPagination(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		w.fv.AddCard(w.org, favrotest.Card{WidgetCommonID: w.widget, ColumnID: w.cols[0], Name: n})
	}
	w.fv.ResetRequests()
	b := w.backend(t, nil)
	items, err := b.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 {
		t.Fatalf("got %d cards, want 5 (over 3 pages)", len(items))
	}
	var pages []string
	for _, r := range w.fv.Requests() {
		if r.Path == "/api/v1/cards" {
			pages = append(pages, r.Query)
			if strings.Contains(r.Query, "requestId=") && r.Backend != "be-1" {
				t.Errorf("page without the backend identifier: %+v", r)
			}
		}
	}
	if len(pages) != 3 || !strings.Contains(pages[1], "page=1") || !strings.Contains(pages[2], "page=2") || !strings.Contains(pages[2], "requestId=") {
		t.Fatalf("pages: %q", pages)
	}
}

func TestRateLimitBackoff(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := NewClient(w.fv.APIURL(), email, token, w.org)
	var sl sleeps
	c.sleep = sl.sleep
	w.fv.Throttle(2, 3*time.Second)
	cols, err := listAll[column](context.Background(), c, "/columns", map[string][]string{"widgetCommonId": {w.widget}})
	if err != nil || len(cols) != 5 {
		t.Fatalf("after 429s: %v %d", err, len(cols))
	}
	got := sl.all()
	if len(got) < 2 {
		t.Fatalf("waits %v, want one per 429", got)
	}
	for _, d := range got[:2] {
		if d < 2*time.Second || d > 3*time.Second {
			t.Errorf("waited %v, want until X-RateLimit-Reset (~3s)", d)
		}
	}

	// Too many 429s give up with Favro's error.
	w.fv.Throttle(maxRetries+1, time.Millisecond)
	_, err = listAll[column](context.Background(), c, "/columns", map[string][]string{"widgetCommonId": {w.widget}})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusTooManyRequests {
		t.Fatalf("endless 429: %v", err)
	}
}

func TestRateLimitRemainingZeroWaits(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := NewClient(w.fv.APIURL(), email, token, w.org)
	var sl sleeps
	c.sleep = sl.sleep
	w.fv.LowAfter(1, 4*time.Second)
	ctx := context.Background()
	if _, err := listAll[tag](ctx, c, "/tags", nil); err != nil {
		t.Fatal(err)
	}
	if len(sl.all()) != 0 {
		t.Fatalf("waited before the quota ran out: %v", sl.all())
	}
	if _, err := listAll[tag](ctx, c, "/tags", nil); err != nil {
		t.Fatal(err)
	}
	if got := sl.all(); len(got) != 1 || got[0] < 3*time.Second {
		t.Fatalf("waits %v, want one until the reset", got)
	}
}

// Two projects with the same account share its backoff; another account
// doesn't wait.
func TestBackoffIsPerAccount(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.fv.AddAccount("bob@example.com", "tok-BOB")
	other := w.fv.AddOrg("Bob's", "bob@example.com", email)
	a := NewClient(w.fv.APIURL(), email, token, w.org)
	b := NewClient(w.fv.APIURL(), email, token, other) // same account, other org/project
	c := NewClient(w.fv.APIURL(), "bob@example.com", "tok-BOB", other)
	var sa, sb, sc sleeps
	a.sleep, b.sleep, c.sleep = sa.sleep, sb.sleep, sc.sleep
	ctx := context.Background()
	w.fv.Throttle(1, 30*time.Second)
	if _, err := listAll[tag](ctx, a, "/tags", nil); err != nil {
		t.Fatal(err)
	}
	if len(sa.all()) == 0 {
		t.Fatal("a didn't back off")
	}
	if _, err := listAll[tag](ctx, b, "/tags", nil); err != nil {
		t.Fatal(err)
	}
	if got := sb.all(); len(got) != 1 || got[0] < 20*time.Second {
		t.Fatalf("same account: waits %v, want the shared backoff", got)
	}
	if _, err := listAll[tag](ctx, c, "/tags", nil); err != nil {
		t.Fatal(err)
	}
	if got := sc.all(); len(got) != 0 {
		t.Fatalf("other account waited %v", got)
	}
}

func TestParseReset(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		h    http.Header
		want time.Time
	}{
		{http.Header{"X-Ratelimit-Reset": {"2026-01-01T12:00:30Z"}}, now.Add(30 * time.Second)},
		{http.Header{"X-Ratelimit-Reset": {"1767268830"}}, time.Unix(1767268830, 0)},
		{http.Header{"Retry-After": {"7"}}, now.Add(7 * time.Second)},
	} {
		got, ok := parseReset(tc.h, now)
		if !ok || !got.Equal(tc.want) {
			t.Errorf("%v: %v %v, want %v", tc.h, got, ok, tc.want)
		}
	}
	if _, ok := parseReset(http.Header{}, now); ok {
		t.Error("no header parsed")
	}
}

func TestWebhookVerification(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	cfg := w.config(nil)
	cfg.WebhookURL = "https://dash.example.com/api/projects/p1/tracker/webhook"
	b, err := New(cfg, cfg.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"payloadId":"pl-1","action":"updated","card":{"cardId":"card9"}}`)
	call := func(sig string) *http.Request {
		r := httptest.NewRequest("POST", "/api/projects/p1/tracker/webhook", nil)
		if sig != "" {
			r.Header.Set(SignatureHeader, sig)
		}
		return r
	}
	ids, err := b.VerifyWebhook(call(Sign("s3cret", "pl-1", cfg.WebhookURL)), body, "s3cret")
	if err != nil || len(ids) != 1 || ids[0] != "card9" {
		t.Fatalf("good call: %v %v", ids, err)
	}
	if favrotest.Sign("s3cret", "pl-1", cfg.WebhookURL) != Sign("s3cret", "pl-1", cfg.WebhookURL) {
		t.Fatal("the fake signs differently")
	}
	for name, sig := range map[string]string{
		"other secret": Sign("nope", "pl-1", cfg.WebhookURL),
		"other url":    Sign("s3cret", "pl-1", "https://evil.example.com/hook"),
		"other id":     Sign("s3cret", "pl-2", cfg.WebhookURL),
		"unsigned":     "",
	} {
		if _, err := b.VerifyWebhook(call(sig), body, "s3cret"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := b.VerifyWebhook(call("x"), []byte("not json"), "s3cret"); err == nil {
		t.Error("not JSON: accepted")
	}

	// Without a public URL the hook's own url is signed, still with the secret.
	nb := w.backend(t, nil)
	ping := []byte(`{"payloadId":"pl-3","action":"ping","hook":{"url":"https://x.example/h"}}`)
	if _, err := nb.VerifyWebhook(call(Sign("s3cret", "pl-3", "https://x.example/h")), ping, "s3cret"); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if _, err := nb.VerifyWebhook(call(Sign("nope", "pl-3", "https://x.example/h")), ping, "s3cret"); err == nil {
		t.Fatal("ping with another secret: accepted")
	}
}

func TestOptions(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	other := w.fv.AddCollection(w.org, "Design")
	w.fv.AddBoard(w.org, other, "Mockups", "Todo")
	w.fv.AddNumberField(w.org, "Priority")
	ctx := context.Background()
	creds := map[string]string{"token": token}
	labels := func(field string, cfg tracker.ProjectConfig) []string {
		t.Helper()
		opts, err := Options(ctx, cfg, creds, field)
		if err != nil {
			t.Fatalf("%s: %v", field, err)
		}
		var out []string
		for _, o := range opts {
			out = append(out, o.Label)
		}
		return out
	}
	// Organizations need only the email and token.
	raw, _ := json.Marshal(map[string]string{"email": email, "base_url": w.fv.APIURL()})
	if got := labels("organization_id", tracker.ProjectConfig{Backend: Name, Config: raw}); strings.Join(got, ",") != "Acme" {
		t.Fatalf("orgs: %v", got)
	}
	cfg := w.config(map[string]any{"widget_common_id": ""})
	if got := labels("collection_id", cfg); strings.Join(got, ",") != "All collections,Design,Engineering" {
		t.Fatalf("collections: %v", got)
	}
	if got := labels("widget_common_id", cfg); strings.Join(got, ",") != "Mockups,Sprint" {
		t.Fatalf("boards: %v", got)
	}
	if got := labels("widget_common_id", w.config(map[string]any{"collection_id": other})); strings.Join(got, ",") != "Mockups" {
		t.Fatalf("boards in a collection: %v", got)
	}
	if got := labels("priority_field", cfg); strings.Join(got, ",") != "None,Priority" {
		t.Fatalf("priority fields: %v", got)
	}
	if _, err := Options(ctx, cfg, creds, "tag"); !errors.Is(err, tracker.ErrNoOptions) {
		t.Fatalf("a text field: %v", err)
	}
	// Through the registry, as the API asks.
	cfg.Credentials = creds
	if _, err := tracker.Options(ctx, cfg, "organization_id"); err != nil {
		t.Fatal(err)
	}
}

func TestColumnMapping(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	b := w.backend(t, nil)
	// Map by name, ignoring case and punctuation, or by column id.
	b.pc.StatusMap = map[model.TodoStatus]string{
		model.StatusDraft: "backlog", model.StatusQueued: "TO-DO", model.StatusReview: w.cols[3], model.StatusMerged: "Done",
	}
	ctx := context.Background()
	if _, err := b.loadColumns(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{w.cols[0]: "backlog", w.cols[1]: "TO-DO", w.cols[2]: "Doing", w.cols[3]: w.cols[3], w.cols[4]: "Done"} {
		if got := b.rawColumn(id); got != want {
			t.Errorf("column %s reads %q, want %q", id, got, want)
		}
		if b.pc.StatusFor(b.rawColumn(id)) == "" && id != w.cols[2] {
			t.Errorf("column %s maps to no status", id)
		}
	}
	for v, want := range map[string]string{"TO-DO": w.cols[1], w.cols[3]: w.cols[3], "done": w.cols[4]} {
		if got, err := b.columnID(ctx, v); err != nil || got != want {
			t.Errorf("%q → %q %v, want %q", v, got, err, want)
		}
	}
	if _, err := b.columnID(ctx, "Shipped"); err == nil || !strings.Contains(err.Error(), "status mapping") {
		t.Errorf("a missing column: %v", err)
	}
	cols, err := b.Columns(ctx)
	if err != nil || len(cols) != 5 || cols[0].Name != "Backlog" || cols[4].ID != "Done" {
		t.Fatalf("columns: %v %v", cols, err)
	}
}
