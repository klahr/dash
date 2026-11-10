package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/ci"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/store"
)

var t0 = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func sample() Notification {
	return Notification{
		ID: "evt_1", Type: model.NotifyFailed, Time: t0,
		Project: ProjectRef{ID: "p1", Name: "Shop"},
		Task:    &TaskRef{ID: "t1", Title: "Fix <checkout> & pay", Status: model.StatusFailed, URL: "https://dash.example.com/?project=p1&task=t1"},
		Message: "tests failed",
	}
}

func TestJSONPayloadAndSignature(t *testing.T) {
	w := model.Webhook{URL: "https://example.com/hook", Format: model.WebhookJSON, Secret: "whsec_abc"}
	req, err := request(context.Background(), w, sample(), t0, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(req.Body)
	if err := Verify(body, req.Header.Get(SignatureHeaderName), "whsec_abc", t0.Add(time.Minute), 5*time.Minute); err != nil {
		t.Fatalf("signature: %v (%s)", err, req.Header.Get(SignatureHeaderName))
	}
	if err := Verify(body, req.Header.Get(SignatureHeaderName), "other", t0, 5*time.Minute); err == nil {
		t.Fatal("verified with the wrong secret")
	}
	if err := Verify(append(body, ' '), req.Header.Get(SignatureHeaderName), "whsec_abc", t0, 5*time.Minute); err == nil {
		t.Fatal("verified a changed body")
	}
	if err := Verify(body, req.Header.Get(SignatureHeaderName), "whsec_abc", t0.Add(time.Hour), 5*time.Minute); err == nil {
		t.Fatal("verified a stale timestamp")
	}
	if want := "t=" + strconv.FormatInt(t0.Unix(), 10) + ",v1="; !strings.HasPrefix(req.Header.Get(SignatureHeaderName), want) {
		t.Fatalf("header %q, want %s…", req.Header.Get(SignatureHeaderName), want)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	task := got["task"].(map[string]any)
	if got["id"] != "evt_1" || got["type"] != "task.failed" || got["timestamp"] != "2026-11-08T12:00:00Z" || got["created"] != float64(t0.Unix()) ||
		got["project"].(map[string]any)["name"] != "Shop" ||
		task["id"] != "t1" || task["title"] != "Fix <checkout> & pay" || task["status"] != "failed" || task["url"] != "https://dash.example.com/?project=p1&task=t1" {
		t.Fatalf("payload %s", body)
	}
	if req.Header.Get("Dash-Event") != "task.failed" || req.Header.Get("Dash-Delivery") != "evt_1" || req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers %v", req.Header)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("authorization without a token")
	}
}

func TestSlackPayload(t *testing.T) {
	w := model.Webhook{URL: "https://hooks.slack.com/services/x", Format: model.WebhookSlack}
	req, err := request(context.Background(), w, sample(), t0, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := ":x: *Failed* · <https://dash.example.com/?project=p1&amp;task=t1|Fix &lt;checkout&gt; &amp; pay> in _Shop_\n>tests failed"
	if got["text"] != want {
		t.Fatalf("text\n got %q\nwant %q", got["text"], want)
	}
	if req.Header.Get(SignatureHeaderName) != "" {
		t.Fatal("slack deliveries aren't signed")
	}
}

func TestNtfyRequest(t *testing.T) {
	w := model.Webhook{URL: "https://ntfy.example.com/dash", Format: model.WebhookNtfy, Token: "tk_secret"}
	n := sample()
	n.Task.Title = "Fix café"
	req, err := request(context.Background(), w, n, t0, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(req.Body)
	title, err := new(mime.WordDecoder).DecodeHeader(req.Header.Get("Title"))
	if err != nil || title != "Failed · Shop" {
		t.Fatalf("title %q (%v)", req.Header.Get("Title"), err)
	}
	if string(body) != "Fix café\ntests failed" {
		t.Fatalf("body %q", body)
	}
	if req.Header.Get("Priority") != "high" || req.Header.Get("Tags") != "x" || req.Header.Get("Click") != n.Task.URL {
		t.Fatalf("headers %v", req.Header)
	}
	if req.Header.Get("Authorization") != "Bearer tk_secret" {
		t.Fatalf("authorization %q", req.Header.Get("Authorization"))
	}
	if req.Header.Get("Actions") != "" {
		t.Fatal("actions without a permission request")
	}

	// Ready for review: normal priority. A permission request: high, with buttons.
	n.Type = model.NotifyReview
	req, _ = request(context.Background(), w, n, t0, nil)
	if req.Header.Get("Priority") != "default" || req.Header.Get("Tags") != "eyes" {
		t.Fatalf("review headers %v", req.Header)
	}
	n.Type = model.NotifyPermission
	acts := NewActions(nil).buttons("https://dash.example.com", "perm1", t0)
	req, _ = request(context.Background(), w, n, t0, acts)
	a := req.Header.Get("Actions")
	if req.Header.Get("Priority") != "high" || !strings.HasPrefix(a, "http, Approve, https://dash.example.com"+ActionPath) ||
		!strings.Contains(a, "; http, Deny, https://dash.example.com"+ActionPath) || strings.Count(a, "method=POST") != 2 {
		t.Fatalf("permission headers %v", req.Header)
	}
}

func TestActionTokens(t *testing.T) {
	var got []string
	a := NewActions(func(id, decision, _ string) error {
		got = append(got, id+":"+decision)
		if id == "gone" {
			return errors.New("no such request")
		}
		return nil
	})
	now := t0
	a.Now = func() time.Time { return now }
	tok := a.Token("perm1", "allow", t0.Add(time.Minute))
	if id, d, err := a.Parse(tok); err != nil || id != "perm1" || d != "allow" {
		t.Fatalf("parse: %q %q %v", id, d, err)
	}
	if _, _, err := a.Parse(strings.Replace(tok, tok[:4], "AAAA", 1)); err == nil {
		t.Fatal("tampered token accepted")
	}
	if _, _, err := NewActions(nil).Parse(tok); err == nil {
		t.Fatal("another key's token accepted")
	}
	post := func(tok string) int {
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, httptest.NewRequest("POST", ActionPath+tok, nil))
		return rec.Code
	}
	if c := post(tok); c != 200 || len(got) != 1 || got[0] != "perm1:allow" {
		t.Fatalf("approve: %d %v", c, got)
	}
	if c := post(a.Token("gone", "deny", t0.Add(time.Minute))); c != http.StatusConflict {
		t.Fatalf("answered request: %d", c)
	}
	now = t0.Add(2 * time.Minute)
	if c := post(tok); c != http.StatusForbidden || len(got) != 2 {
		t.Fatalf("expired: %d %v", c, got)
	}
}

func TestCheckURL(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1/x", "http://localhost:8080", "http://10.0.0.5/", "http://192.168.1.1", "http://169.254.169.254/latest",
		"http://[::1]/", "http://[fe80::1]/", "http://100.100.1.1/", "http://0.0.0.0/", "http://[::ffff:127.0.0.1]/", "http://foo.localhost/"} {
		if err := CheckURL(u, false); !errors.Is(err, ErrPrivateAddress) {
			t.Errorf("%s: %v", u, err)
		}
		if err := CheckURL(u, true); err != nil {
			t.Errorf("%s allowed: %v", u, err)
		}
	}
	for _, u := range []string{"https://hooks.slack.com/services/x", "https://ntfy.sh/topic", "http://93.184.216.34/"} {
		if err := CheckURL(u, false); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
}

// ---- the notifier, against a fake store and a test server ----

type fakeStore struct {
	mu       sync.Mutex
	projects map[string]model.Project
	todos    map[string]model.Todo
	hooks    []model.Webhook
	records  []string // "<webhook>:<event>:<error>"
}

func (f *fakeStore) ListWebhooks(pid string) ([]model.Webhook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.Webhook
	for _, w := range f.hooks {
		if w.ProjectID == pid {
			out = append(out, w)
		}
	}
	return out, nil
}
func (f *fakeStore) GetProject(id string) (model.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.projects[id]
	if !ok {
		return p, store.ErrNotFound
	}
	return p, nil
}
func (f *fakeStore) GetTodo(id string) (model.Todo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.todos[id]
	if !ok {
		return t, store.ErrNotFound
	}
	return t, nil
}
func (f *fakeStore) ListTodos(string, ...model.TodoStatus) []model.Todo {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.Todo
	for _, t := range f.todos {
		out = append(out, t)
	}
	return out
}
func (f *fakeStore) RecordWebhookDelivery(id string, ev model.NotifyEvent, _ time.Time, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, id+":"+string(ev)+":"+msg)
	return nil
}
func (f *fakeStore) setTodo(t model.Todo) {
	f.mu.Lock()
	f.todos[t.ID] = t
	f.mu.Unlock()
}

// receiver is a test server that records what it receives and answers
// with the next code in codes (then 200).
type receiver struct {
	*httptest.Server
	mu     sync.Mutex
	got    []*http.Request
	bodies []string
	codes  []int
}

func newReceiver(t *testing.T, codes ...int) *receiver {
	r := &receiver{codes: codes}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.got = append(r.got, req)
		r.bodies = append(r.bodies, string(b))
		code := 200
		if len(r.codes) > 0 {
			code, r.codes = r.codes[0], r.codes[1:]
		}
		r.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *receiver) events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, b := range r.bodies {
		var p struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(b), &p)
		out = append(out, p.Type)
	}
	return out
}

func setup(t *testing.T, hooks ...model.Webhook) (*Notifier, *fakeStore, context.CancelFunc) {
	t.Helper()
	fs := &fakeStore{
		projects: map[string]model.Project{"p1": {ID: "p1", Name: "Shop"}},
		todos:    map[string]model.Todo{"t1": {ID: "t1", ProjectID: "p1", Title: "Checkout", Status: model.StatusInProgress}},
		hooks:    hooks,
	}
	n := &Notifier{Store: fs, AllowPrivate: true, Backoff: time.Millisecond, Timeout: 2 * time.Second,
		Origin: func(string) string { return "https://dash.example.com/" }, Now: func() time.Time { return t0 }}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	n.Start(ctx)
	return n, fs, cancel
}

// settle waits for the run loop to take every queued event and for the
// deliveries it started.
func settle(n *Notifier) {
	n.init()
	done := make(chan struct{})
	n.queue <- model.Event{Type: evFlush, Data: done}
	<-done
	n.wg.Wait()
}

func status(fs *fakeStore, n *Notifier, s model.TodoStatus, errMsg string) {
	t, _ := fs.GetTodo("t1")
	t.Status, t.Error = s, errMsg
	fs.setTodo(t)
	n.Observe(model.Event{Type: model.EvTodoUpdated, ProjectID: "p1", TodoID: "t1", Data: t})
}

func TestEventsAndFiltering(t *testing.T) {
	all := newReceiver(t)
	reviewOnly := newReceiver(t)
	off := newReceiver(t)
	n, fs, _ := setup(t,
		model.Webhook{ID: "all", ProjectID: "p1", URL: all.URL, Format: model.WebhookJSON, Enabled: true, Events: model.NotifyEvents, Secret: "s"},
		model.Webhook{ID: "rev", ProjectID: "p1", URL: reviewOnly.URL, Format: model.WebhookJSON, Enabled: true, Events: []model.NotifyEvent{model.NotifyReview}, Secret: "s"},
		model.Webhook{ID: "off", ProjectID: "p1", URL: off.URL, Format: model.WebhookJSON, Enabled: false, Events: model.NotifyEvents, Secret: "s"},
		model.Webhook{ID: "other", ProjectID: "p2", URL: off.URL, Format: model.WebhookJSON, Enabled: true, Events: model.NotifyEvents, Secret: "s"},
	)
	status(fs, n, model.StatusReview, "")
	settle(n)
	status(fs, n, model.StatusReview, "") // no change: nothing new
	status(fs, n, model.StatusQueued, "") // not notified
	status(fs, n, model.StatusInProgress, "")
	settle(n)
	status(fs, n, model.StatusFailed, "tests failed")
	settle(n)
	status(fs, n, model.StatusMerged, "")
	settle(n)
	// A permission request; its answer isn't news.
	req := perm.Request{ID: "perm1", TodoID: "t1", ProjectID: "p1", Tool: "Bash"}
	n.Observe(model.Event{Type: model.EvPermission, ProjectID: "p1", TodoID: "t1", Data: req})
	req.Decision = "allow"
	n.Observe(model.Event{Type: model.EvPermission, ProjectID: "p1", TodoID: "t1", Data: req})
	settle(n)
	// The first build status seen is the baseline; passing → failing notifies once.
	failing := ci.Status{ProjectID: "p1", Branch: "main", State: "failing", Runs: []gitops.Run{{Status: "completed", Conclusion: "failure", URL: "https://github.com/x/runs/1"}}}
	n.Observe(model.Event{Type: model.EvCI, ProjectID: "p1", Data: failing})
	n.Observe(model.Event{Type: model.EvCI, ProjectID: "p1", Data: ci.Status{ProjectID: "p1", Branch: "main", State: "passing"}})
	n.Observe(model.Event{Type: model.EvCI, ProjectID: "p1", Data: failing})
	n.Observe(model.Event{Type: model.EvCI, ProjectID: "p1", Data: failing})
	settle(n)
	// Unrelated events are ignored.
	n.Observe(model.Event{Type: model.EvTodoOutput, ProjectID: "p1", TodoID: "t1", Data: "hello"})
	settle(n)

	want := "task.review task.failed task.merged permission.needed build.failing"
	if got := strings.Join(all.events(), " "); got != want {
		t.Fatalf("all events: %s, want %s", got, want)
	}
	if got := strings.Join(reviewOnly.events(), " "); got != "task.review" {
		t.Fatalf("review-only: %s", got)
	}
	if len(off.events()) != 0 {
		t.Fatalf("disabled or other project's webhooks got %v", off.events())
	}

	// The failed task's payload carries its error and a link back.
	var p struct {
		Message string  `json:"message"`
		Task    TaskRef `json:"task"`
	}
	_ = json.Unmarshal([]byte(all.bodies[1]), &p)
	if p.Message != "tests failed" || p.Task.URL != "https://dash.example.com/?project=p1&task=t1" || p.Task.Title != "Checkout" {
		t.Fatalf("failed payload %s", all.bodies[1])
	}
	var b struct {
		Build BuildRef `json:"build"`
	}
	_ = json.Unmarshal([]byte(all.bodies[4]), &b)
	if b.Build.Branch != "main" || b.Build.URL != "https://github.com/x/runs/1" {
		t.Fatalf("build payload %s", all.bodies[4])
	}
	if err := Verify([]byte(all.bodies[0]), all.got[0].Header.Get(SignatureHeaderName), "s", t0, time.Minute); err != nil {
		t.Fatalf("delivered signature: %v", err)
	}
}

func TestUnknownTodoDoesNotNotify(t *testing.T) {
	r := newReceiver(t)
	n, fs, _ := setup(t, model.Webhook{ID: "a", ProjectID: "p1", URL: r.URL, Format: model.WebhookSlack, Enabled: true, Events: model.NotifyEvents})
	// t2 appeared without Dash having seen it before (e.g. a dropped event):
	// its first status is only remembered.
	t2 := model.Todo{ID: "t2", ProjectID: "p1", Status: model.StatusReview}
	fs.setTodo(t2)
	n.Observe(model.Event{Type: model.EvTodoUpdated, ProjectID: "p1", TodoID: "t2", Data: t2})
	settle(n)
	if len(r.bodies) != 0 {
		t.Fatalf("got %v", r.bodies)
	}
}

func TestRetriesServerErrors(t *testing.T) {
	r := newReceiver(t, 500, 503)
	n, fs, _ := setup(t, model.Webhook{ID: "a", ProjectID: "p1", URL: r.URL, Format: model.WebhookJSON, Enabled: true, Events: model.NotifyEvents, Secret: "s"})
	status(fs, n, model.StatusReview, "")
	settle(n)
	if len(r.got) != 3 {
		t.Fatalf("%d attempts, want 3", len(r.got))
	}
	// Each attempt is a new delivery of the same event, freshly signed.
	if r.got[0].Header.Get("Dash-Delivery") != r.got[2].Header.Get("Dash-Delivery") {
		t.Fatal("retries changed the event id")
	}
	if len(fs.records) != 1 || fs.records[0] != "a:task.review:" {
		t.Fatalf("records %v", fs.records)
	}
}

func TestGivesUpAfterAttempts(t *testing.T) {
	r := newReceiver(t, 500, 500, 500, 500, 500, 500)
	n, fs, _ := setup(t, model.Webhook{ID: "a", ProjectID: "p1", URL: r.URL, Format: model.WebhookSlack, Enabled: true, Events: model.NotifyEvents})
	n.Attempts = 3
	status(fs, n, model.StatusReview, "")
	settle(n)
	if len(r.got) != 3 {
		t.Fatalf("%d attempts, want 3", len(r.got))
	}
	if len(fs.records) != 1 || !strings.Contains(fs.records[0], "500") {
		t.Fatalf("records %v", fs.records)
	}
}

func TestNoRetryOnClientErrors(t *testing.T) {
	r := newReceiver(t, 404)
	n, fs, _ := setup(t, model.Webhook{ID: "a", ProjectID: "p1", URL: r.URL, Format: model.WebhookSlack, Enabled: true, Events: model.NotifyEvents})
	status(fs, n, model.StatusReview, "")
	settle(n)
	if len(r.got) != 1 {
		t.Fatalf("%d attempts, want 1", len(r.got))
	}
	if len(fs.records) != 1 || !strings.Contains(fs.records[0], "404") {
		t.Fatalf("records %v", fs.records)
	}
}

func TestRetriesNetworkErrors(t *testing.T) {
	r := newReceiver(t)
	url := r.URL
	r.Close() // nothing listens there any more
	n, fs, _ := setup(t, model.Webhook{ID: "a", ProjectID: "p1", URL: url + "/secret-path", Format: model.WebhookSlack, Enabled: true, Events: model.NotifyEvents})
	n.Attempts = 2
	status(fs, n, model.StatusReview, "")
	settle(n)
	if len(fs.records) != 1 || fs.records[0] == "a:task.review:" {
		t.Fatalf("records %v", fs.records)
	}
	if strings.Contains(fs.records[0], "secret-path") {
		t.Fatalf("the error shows the URL: %s", fs.records[0])
	}
}

func TestBlocksPrivateAddresses(t *testing.T) {
	r := newReceiver(t)
	n, fs, _ := setup(t, model.Webhook{ID: "a", ProjectID: "p1", URL: r.URL, Format: model.WebhookSlack, Enabled: true, Events: model.NotifyEvents})
	n.client = newClient(time.Second, false) // as on a hosted Dash
	status(fs, n, model.StatusReview, "")
	settle(n)
	if len(r.got) != 0 {
		t.Fatal("delivered to a loopback address")
	}
	if len(fs.records) != 1 || !strings.Contains(fs.records[0], "private") {
		t.Fatalf("records %v", fs.records)
	}
}

func TestTestDelivery(t *testing.T) {
	r := newReceiver(t, 500)
	n, fs, _ := setup(t)
	w := model.Webhook{ID: "a", ProjectID: "p1", URL: r.URL, Format: model.WebhookNtfy, Enabled: false}
	// A test goes out once, even to a paused webhook, and reports failure.
	if err := n.Test(context.Background(), w); err == nil || len(r.got) != 1 {
		t.Fatalf("test: %v, %d requests", err, len(r.got))
	}
	if err := n.Test(context.Background(), w); err != nil {
		t.Fatalf("second test: %v", err)
	}
	if r.got[1].Header.Get("Tags") != "wave" || r.got[1].Header.Get("Click") != "https://dash.example.com/?project=p1" {
		t.Fatalf("test headers %v", r.got[1].Header)
	}
	if len(fs.records) != 2 || fs.records[1] != "a:webhook.test:" {
		t.Fatalf("records %v", fs.records)
	}
}

func TestNtfyPermissionButtons(t *testing.T) {
	r := newReceiver(t)
	n, _, _ := setup(t, model.Webhook{ID: "a", ProjectID: "p1", URL: r.URL, Format: model.WebhookNtfy, Enabled: true, Events: model.NotifyEvents})
	var decided []string
	n.Actions = NewActions(func(id, d, _ string) error { decided = append(decided, id+":"+d); return nil })
	n.Actions.Now = func() time.Time { return t0 }
	n.Observe(model.Event{Type: model.EvPermission, ProjectID: "p1", TodoID: "t1", Data: perm.Request{ID: "perm1", TodoID: "t1", ProjectID: "p1", Tool: "Bash"}})
	settle(n)
	if len(r.got) != 1 {
		t.Fatalf("%d requests", len(r.got))
	}
	acts := r.got[0].Header.Get("Actions")
	// Press Deny: its URL's token decides the request.
	i := strings.Index(acts, "http, Deny, ")
	u := strings.TrimPrefix(acts[i:], "http, Deny, ")
	u, _, _ = strings.Cut(u, ",")
	tok := strings.TrimPrefix(u, "https://dash.example.com"+ActionPath)
	rec := httptest.NewRecorder()
	n.Actions.ServeHTTP(rec, httptest.NewRequest("POST", ActionPath+tok, nil))
	if rec.Code != 200 || len(decided) != 1 || decided[0] != "perm1:deny" {
		t.Fatalf("deny: %d %v (%s)", rec.Code, decided, acts)
	}
}
