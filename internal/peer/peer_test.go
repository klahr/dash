package peer

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/store/storetest"
	"github.com/klahr/dash/internal/tunnel"
)

// runnerAPI is a stand-in runner: it echoes what it saw.
func runnerAPI() http.Handler {
	up := websocket.Upgrader{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws/events" {
			c, err := up.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close()
			_ = c.WriteMessage(websocket.TextMessage, []byte("hello from runner"))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "dash_session", Value: "runner-session"})
		io.WriteString(w, r.Method+" "+r.URL.RequestURI()+" cookie="+r.Header.Get("Cookie"))
	})
}

// hubWithRunner is a hub named "home" with one runner, "work", connected
// through its tunnel.
func hubWithRunner(t *testing.T) (*Hub, *httptest.Server) {
	t.Helper()
	acc, owner := storetest.Accounts(t)
	h := &Hub{Self: "home"}
	h.Runners = &tunnel.Hub{Accounts: acc, Taken: h.Taken}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /runner/register", h.Runners.ServeRegister)
	mux.HandleFunc("GET /runner/connect", h.Runners.ServeConnect)
	mux.Handle("/peer/{name}/", h)
	mux.HandleFunc("/api/machines", func(w http.ResponseWriter, r *http.Request) {
		for _, m := range h.Machines(r.Context()) {
			io.WriteString(w, m.Name+":"+map[bool]string{true: "ok", false: "down"}[m.OK]+" ")
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	code, _, err := h.Runners.NewCode(owner)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := tunnel.Register(ctx, srv.URL, code, "work", "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	c := &tunnel.Client{Creds: creds, Handler: runnerAPI(), MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { _ = c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if r, _ := h.Runners.Get(creds.ID); r.Online {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the runner never came online")
		}
	}
	return h, srv
}

func TestForwardsToRunners(t *testing.T) {
	_, hub := hubWithRunner(t)

	req, _ := http.NewRequest("GET", hub.URL+"/peer/work/api/projects?x=1", nil)
	req.Header.Set("Cookie", "dash_session=hub-secret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if got := string(body); got != "GET /api/projects?x=1 cookie=" {
		t.Fatalf("runner saw %q; want the path rewritten and no hub cookie", got)
	}
	if res.Header.Get("Set-Cookie") != "" {
		t.Fatal("the runner's cookies must not reach the browser")
	}

	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(hub.URL, "http")+"/peer/work/ws/events", http.Header{"Origin": {hub.URL}})
	if err != nil {
		t.Fatalf("websocket through the hub: %v", err)
	}
	_, msg, err := ws.ReadMessage()
	ws.Close()
	if err != nil || string(msg) != "hello from runner" {
		t.Fatalf("websocket message %q, %v", msg, err)
	}

	// A page on another origin (e.g. a preview subdomain) must not reach the runner.
	_, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(hub.URL, "http")+"/peer/work/ws/events", http.Header{"Origin": {"http://abc.preview.example.com"}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin websocket through the hub: want 403, got %v, %v", resp, err)
	}

	if res, _ := http.Get(hub.URL + "/peer/nobody/api/projects"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown computer: want 404, got %d", res.StatusCode)
	}

	res, _ = http.Get(hub.URL + "/api/machines")
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if string(body) != "home:ok work:ok " {
		t.Fatalf("machines: %q", body)
	}
}

func TestWithoutRunners(t *testing.T) {
	h := &Hub{Self: "home"}
	if ms := h.Machines(context.Background()); len(ms) != 1 || !ms[0].Local || !ms[0].OK || ms[0].Prefix != "" {
		t.Fatalf("machines: %+v", ms)
	}
	if !h.Taken("home") || h.Taken("work") {
		t.Fatal("Taken should be this computer's name only")
	}
	r := httptest.NewRequest("GET", "/peer/work/api/me", nil)
	r.SetPathValue("name", "work")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("no runners: want 404, got %d", w.Code)
	}
}
