package preview

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/runner"
	"github.com/klahr/dash/internal/store"
)

type events struct {
	mu  sync.Mutex
	evs []model.Event
}

func (e *events) Publish(ev model.Event) { e.mu.Lock(); e.evs = append(e.evs, ev); e.mu.Unlock() }

type todos map[string]model.Todo

func (t todos) GetTodo(id string) (model.Todo, error) {
	if x, ok := t[id]; ok {
		return x, nil
	}
	return model.Todo{}, store.ErrNotFound
}

func setup(t *testing.T, domain string) (*Manager, *runner.Manager, model.Todo) {
	t.Helper()
	procs := runner.NewManager(&events{})
	t.Cleanup(procs.StopAll)
	td := model.Todo{ID: "abc123", ProjectID: "p1", Status: model.StatusReview, Worktree: &model.Worktree{Path: t.TempDir()}}
	m := New(procs, &events{}, todos{td.ID: td}, domain)
	return m, procs, td
}

func waitState(t *testing.T, m *Manager, id string, want model.PreviewState) model.Preview {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if p, ok := m.Get(id); ok && p.State == want {
			return p
		}
		time.Sleep(50 * time.Millisecond)
	}
	p, _ := m.Get(id)
	t.Fatalf("preview never got %q; now %+v", want, p)
	return model.Preview{}
}

func TestStartRequiresRunCommandAndReview(t *testing.T) {
	m, _, td := setup(t, "")
	if _, err := m.Start(td, model.Project{ID: "p1"}); err != ErrNoRunCommand {
		t.Fatalf("want ErrNoRunCommand, got %v", err)
	}
	td.Status = model.StatusQueued
	if _, err := m.Start(td, model.Project{ID: "p1", RunCommand: "true"}); err != ErrNotInReview {
		t.Fatalf("want ErrNotInReview, got %v", err)
	}
}

func TestCrashKeepsLog(t *testing.T) {
	m, procs, td := setup(t, "")
	p, err := m.Start(td, model.Project{ID: "p1", RunCommand: "echo boom on $PORT; exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	if p.State != model.PreviewStarting || p.Port == 0 || p.URL != "" || !strings.HasPrefix(p.LocalURL, "http://localhost:") {
		t.Fatalf("unexpected start %+v", p)
	}
	c := waitState(t, m, td.ID, model.PreviewCrashed)
	if c.ExitCode == nil || *c.ExitCode != 3 {
		t.Fatalf("exit code: %+v", c)
	}
	b, _ := procs.Tail(c.ProcessID, 1024)
	if !strings.Contains(string(b), "boom on ") {
		t.Fatalf("log: %q", b)
	}
	// Starting again replaces the crashed one.
	p2, err := m.Start(td, model.Project{ID: "p1", RunCommand: "sleep 30"})
	if err != nil || p2.ProcessID == c.ProcessID || p2.State != model.PreviewStarting {
		t.Fatalf("restart: %+v %v", p2, err)
	}
}

func TestProxyAuthAndLifecycle(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("needs python3 for a test server")
	}
	m, _, td := setup(t, "preview.dash.test")
	r0 := httptest.NewRequest("GET", "http://dash.test/api/previews", nil)
	m.SetOrigin(r0)
	p, err := m.Start(td, model.Project{ID: "p1", RunCommand: "echo hello > index.html; exec python3 -m http.server --bind 127.0.0.1 $PORT"})
	if err != nil {
		t.Fatal(err)
	}
	if p.URL != "http://abc123.preview.dash.test/" || p.OpenURL != "/preview/abc123" {
		t.Fatalf("urls: %+v", p)
	}
	waitState(t, m, td.ID, model.PreviewLive)

	dash := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "dash") })
	h := m.Wrap(dash)
	do := func(method, url string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, url, nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// No cookie: off to Dash to sign in, keeping the path.
	w := do("GET", "http://abc123.preview.dash.test/index.html?x=1", nil)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "http://dash.test/preview/abc123?next=%2Findex.html%3Fx%3D1" {
		t.Fatalf("unauthenticated: %d %q", w.Code, w.Header().Get("Location"))
	}
	// A bad or foreign token is refused.
	if w := do("GET", "http://abc123.preview.dash.test/__dash/auth?t=1.abc", nil); w.Code != http.StatusForbidden {
		t.Fatalf("bad token: %d", w.Code)
	}
	other := strings.Replace(m.AuthURL("zzz999", "/"), "zzz999", "abc123", 1)
	if w := do("GET", other, nil); w.Code != http.StatusForbidden {
		t.Fatalf("token for another preview: %d", w.Code)
	}
	// Dash's token becomes the preview's cookie; open redirects are refused.
	w = do("GET", m.AuthURL(td.ID, "//evil.example/"), nil)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Fatalf("auth: %d %q", w.Code, w.Header().Get("Location"))
	}
	cookie := w.Result().Cookies()[0]
	if cookie.Name != cookieName || !cookie.HttpOnly {
		t.Fatalf("cookie: %+v", cookie)
	}
	w = do("GET", "http://abc123.preview.dash.test/index.html", map[string]string{"Cookie": cookie.String() + "; dash_session=secret"})
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "hello" {
		t.Fatalf("proxied: %d %q", w.Code, w.Body)
	}
	// The cookie is only good for its own preview host.
	if w := do("GET", "http://zzz999.preview.dash.test/", map[string]string{"Cookie": cookie.String()}); w.Code != http.StatusFound {
		t.Fatalf("cookie on another host: %d", w.Code)
	}
	// Dash itself still answers, but not to writes from a preview's pages.
	if w := do("GET", "http://dash.test/", nil); w.Body.String() != "dash" {
		t.Fatalf("dash: %q", w.Body)
	}
	if w := do("POST", "http://dash.test/api/todos/x/approve", map[string]string{"Origin": "http://abc123.preview.dash.test"}); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin write from a preview: %d", w.Code)
	}

	// Leaving review stops it.
	td.Status = model.StatusMerging
	m.Observe(model.Event{Type: model.EvTodoUpdated, TodoID: td.ID, Data: td})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := m.Get(td.ID); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("preview not stopped when the task was approved")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if w := do("GET", "http://abc123.preview.dash.test/", map[string]string{"Cookie": cookie.String()}); w.Code != http.StatusNotFound {
		t.Fatalf("stopped preview: %d", w.Code)
	}
}

func TestIdleSweep(t *testing.T) {
	m, _, td := setup(t, "")
	if _, err := m.Start(td, model.Project{ID: "p1", RunCommand: "sleep 30"}); err != nil {
		t.Fatal(err)
	}
	m.IdleAfter = time.Minute
	m.sweep(time.Now())
	if _, ok := m.Get(td.ID); !ok {
		t.Fatal("stopped a preview that isn't idle")
	}
	m.sweep(time.Now().Add(2 * time.Minute))
	if _, ok := m.Get(td.ID); ok {
		t.Fatal("idle preview kept running")
	}
}

// Hot reload: WebSockets pass through the proxy, and their traffic counts as activity.
func TestProxyWebSocket(t *testing.T) {
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "http://"+r.Host }}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		typ, b, _ := c.ReadMessage()
		_ = c.WriteMessage(typ, append([]byte("echo "), b...))
	}))
	defer backend.Close()
	addr := strings.TrimPrefix(backend.URL, "http://")
	_, port, _ := net.SplitHostPort(addr)
	m := New(nil, nil, nil, "preview.dash.test")
	e := &entry{}
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := strconv.Atoi(port)
		m.proxy(addr, p, "http://"+r.Host).ServeHTTP(touchWriter{w, e}, r)
	}))
	defer front.Close()
	// The backend only accepts its own origin (localhost:port), as dev servers do.
	hdr := http.Header{"Origin": {front.URL}}
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(front.URL, "http"), hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	e.seen.Store(0)
	if err := c.WriteMessage(websocket.TextMessage, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	_, b, err := c.ReadMessage()
	if err != nil || string(b) != "echo hi" {
		t.Fatalf("got %q %v", b, err)
	}
	if e.seen.Load() == 0 {
		t.Fatal("WebSocket traffic didn't count as activity")
	}
}
