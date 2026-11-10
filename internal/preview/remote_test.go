package preview

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store/storetest"
	"github.com/klahr/dash/internal/tunnel"
)

// devServer stands in for a dev server with hot reload (Vite and the like):
// pages, a redirect to its own localhost address, and an HMR WebSocket that
// only accepts its own origin and pushes updates when told to.
type devServer struct {
	*httptest.Server
	port   int
	update chan string
}

func newDevServer(t *testing.T) *devServer {
	t.Helper()
	d := &devServer{update: make(chan string, 4)}
	local := func(r *http.Request) bool { return r.Host == "localhost:"+strconv.Itoa(d.port) }
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		return r.Header.Get("Origin") == "http://localhost:"+strconv.Itoa(d.port)
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if !local(r) {
			http.Error(w, "blocked host "+r.Host, http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("X-Frame-Options", "DENY")
		_, _ = io.WriteString(w, `<!doctype html><html><head><title>app</title></head><body>hello from the runner</body></html>`)
	})
	mux.HandleFunc("GET /old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:"+strconv.Itoa(d.port)+"/new", http.StatusFound)
	})
	mux.HandleFunc("GET /big", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 3<<20))
	})
	mux.HandleFunc("GET /hmr", func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"connected"}`))
		go func() {
			for {
				_, b, err := c.ReadMessage()
				if err != nil {
					return
				}
				if string(b) == "ping" {
					d.update <- "pong"
				}
			}
		}()
		for msg := range d.update {
			if err := c.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
				return
			}
		}
	})
	d.Server = httptest.NewServer(mux)
	t.Cleanup(d.Close)
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(d.URL, "http://"))
	d.port, _ = strconv.Atoi(p)
	return d
}

// remoteSetup runs a hub (preview proxy and runner tunnels on one listener)
// and a runner "work" connected to it, whose preview manager is returned.
func remoteSetup(t *testing.T) (hubSrv *httptest.Server, hub *Manager, runner *Manager) {
	t.Helper()
	hub = New(nil, nil, nil, "preview.dash.test")
	acc, owner := storetest.Accounts(t)
	th := &tunnel.Hub{PreviewURL: hub.URLTemplate, Accounts: acc}
	hub.Remote = th
	mux := http.NewServeMux()
	mux.HandleFunc("POST /runner/register", th.ServeRegister)
	mux.HandleFunc("GET /runner/connect", th.ServeConnect)
	hubSrv = httptest.NewServer(hub.Wrap(mux))
	t.Cleanup(hubSrv.Close)

	code, _, _ := th.NewCode(owner)
	creds, err := tunnel.Register(context.Background(), hubSrv.URL, code, "work", "")
	if err != nil {
		t.Fatal(err)
	}
	runner = New(nil, nil, nil, "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	cl := &tunnel.Client{Creds: creds, Handler: http.NotFoundHandler(), Previews: runner,
		OnPreviewURL: func(tpl string) { runner.SetHub(tpl, creds.Name) }}
	go func() { _ = cl.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return hubSrv, hub, runner
}

// addLive registers a live preview on m without running a process.
func addLive(m *Manager, todoID string, d *devServer) *entry {
	e := &entry{done: make(chan struct{}), addr: strings.TrimPrefix(d.URL, "http://"),
		info: model.Preview{TodoID: todoID, State: model.PreviewLive, Port: d.port, StartedAt: time.Now()}}
	m.mu.Lock()
	m.entries[todoID] = e
	m.mu.Unlock()
	return e
}

func TestRunnerPreviewThroughTheTunnel(t *testing.T) {
	t.Parallel()

	dev := newDevServer(t)
	hubSrv, hub, runner := remoteSetup(t)
	e := addLive(runner, "abc123", dev)
	_, hubPort, _ := net.SplitHostPort(strings.TrimPrefix(hubSrv.URL, "http://"))
	host := "abc123-work.preview.dash.test:" + hubPort
	cookie := (&http.Cookie{Name: cookieName, Value: hub.sign("c", "abc123-work", time.Now().Add(time.Hour))}).String()

	do := func(path string, hdr map[string]string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", hubSrv.URL+path, nil)
		req.Host = host
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}

	// The runner learns where the hub serves its previews.
	for deadline := time.Now().Add(10 * time.Second); ; {
		p, _ := runner.Get("abc123")
		if p.URL == "http://"+host+"/" && p.OpenURL == "/preview/abc123" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runner's preview URL: %+v", p)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Signing in goes through the hub, like a local preview.
	if res, _ := do("/", nil); res.StatusCode != http.StatusFound {
		t.Fatalf("without a cookie: %d", res.StatusCode)
	}
	// Wait for the preview connection.
	var res *http.Response
	var body string
	for deadline := time.Now().Add(10 * time.Second); ; {
		res, body = do("/", map[string]string{"Cookie": cookie, "Sec-Fetch-Dest": "iframe"})
		if res.StatusCode != http.StatusBadGateway || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "hello from the runner") {
		t.Fatalf("page: %d %q", res.StatusCode, body)
	}
	// The hub's rewriting applies: frame headers, capture script, redirects.
	if res.Header.Get("X-Frame-Options") != "" || !strings.Contains(body, "/__dash/capture.js") {
		t.Fatalf("not rewritten: %v %q", res.Header, body)
	}
	if res, _ := do("/old", map[string]string{"Cookie": cookie}); res.Header.Get("Location") != "http://"+host+"/new" {
		t.Fatalf("redirect: %q", res.Header.Get("Location"))
	}
	if _, b := do("/big", map[string]string{"Cookie": cookie}); len(b) != 3<<20 {
		t.Fatalf("big response: %d bytes", len(b))
	}

	// Hot reload: the HMR WebSocket goes through, both ways, and counts as
	// activity on the runner.
	d := websocket.Dialer{NetDial: func(_, _ string) (net.Conn, error) {
		return net.Dial("tcp", strings.TrimPrefix(hubSrv.URL, "http://"))
	}}
	ws, _, err := d.Dial("ws://"+host+"/hmr", http.Header{"Origin": {"http://" + host}, "Cookie": {cookie}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	read := func(want string) {
		t.Helper()
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, b, err := ws.ReadMessage()
		if err != nil || string(b) != want {
			t.Fatalf("hmr: got %q %v, want %q", b, err, want)
		}
	}
	read(`{"type":"connected"}`)
	e.seen.Store(0)
	dev.update <- `{"type":"update","path":"/src/App.tsx"}` // a file changed
	read(`{"type":"update","path":"/src/App.tsx"}`)
	if err := ws.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	read("pong")
	if e.seen.Load() == 0 {
		t.Fatal("tunnelled traffic didn't keep the runner's preview alive")
	}

	// Other states are shown as such.
	runner.mu.Lock()
	runner.entries["def456"] = &entry{done: make(chan struct{}), info: model.Preview{TodoID: "def456", State: model.PreviewStarting}}
	runner.mu.Unlock()
	starting := (&http.Cookie{Name: cookieName, Value: hub.sign("c", "def456-work", time.Now().Add(time.Hour))}).String()
	host = "def456-work.preview.dash.test:" + hubPort
	if res, body := do("/", map[string]string{"Cookie": starting}); res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "Starting") {
		t.Fatalf("starting: %d %q", res.StatusCode, body)
	}
	none := (&http.Cookie{Name: cookieName, Value: hub.sign("c", "fff000-work", time.Now().Add(time.Hour))}).String()
	host = "fff000-work.preview.dash.test:" + hubPort
	if res, _ := do("/", map[string]string{"Cookie": none}); res.StatusCode != http.StatusNotFound {
		t.Fatalf("not running: %d", res.StatusCode)
	}
	// A runner that isn't registered isn't a preview host at all.
	other := (&http.Cookie{Name: cookieName, Value: hub.sign("c", "abc123-home", time.Now().Add(time.Hour))}).String()
	host = "abc123-home.preview.dash.test:" + hubPort
	if res, _ := do("/", map[string]string{"Cookie": other}); res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown runner: %d", res.StatusCode)
	}
}

// The runner only forwards to previews it started, at the address their
// server answered on; the hub can't name another port.
func TestRunnerDialsOnlyItsOwnPreviews(t *testing.T) {
	t.Parallel()

	m := New(nil, nil, nil, "")
	if _, _, err := m.DialPreview("abc123"); err != tunnel.ErrPreviewNotRunning {
		t.Fatalf("unknown todo: %v", err)
	}
	dev := newDevServer(t)
	addLive(m, "abc123", dev)
	c, port, err := m.DialPreview("abc123")
	if err != nil || port != dev.port {
		t.Fatalf("dial: %d %v", port, err)
	}
	c.Close()
	for _, id := range []string{"127.0.0.1:22", "localhost", "../abc123", ""} {
		if _, _, err := m.DialPreview(id); err == nil {
			t.Fatalf("dialled %q", id)
		}
	}
}
