package peer

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

const token = "0123456789abcdef0123456789abcdef"

// upstream is a stand-in peer: it requires the token and echoes what it saw.
func upstream(t *testing.T) *httptest.Server {
	up := websocket.Upgrader{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/ws/events" {
			c, err := up.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close()
			_ = c.WriteMessage(websocket.TextMessage, []byte("hello from peer"))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "dash_session", Value: "peer-session"})
		io.WriteString(w, r.Method+" "+r.URL.RequestURI()+" cookie="+r.Header.Get("Cookie"))
	}))
}

func hubServer(t *testing.T, peerURL, tok string) *httptest.Server {
	p, err := Parse("work=" + peerURL)
	if err != nil {
		t.Fatal(err)
	}
	h := &Hub{Self: "home", Token: tok, Peers: []Peer{p}}
	mux := http.NewServeMux()
	mux.Handle("/peer/{name}/", h)
	mux.HandleFunc("/api/machines", func(w http.ResponseWriter, r *http.Request) {
		for _, m := range h.Machines(r.Context()) {
			io.WriteString(w, m.Name+":"+map[bool]string{true: "ok", false: "down"}[m.OK]+" ")
		}
	})
	return httptest.NewServer(mux)
}

func TestProxyForwardsWithTokenOnly(t *testing.T) {
	up := upstream(t)
	defer up.Close()
	hub := hubServer(t, up.URL, token)
	defer hub.Close()

	req, _ := http.NewRequest("GET", hub.URL+"/peer/work/api/projects?x=1", nil)
	req.Header.Set("Cookie", "dash_session=hub-secret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	if got := string(body); got != "GET /api/projects?x=1 cookie=" {
		t.Fatalf("peer saw %q; want the path rewritten and no hub cookie", got)
	}
	if res.Header.Get("Set-Cookie") != "" {
		t.Fatal("the peer's cookies must not reach the browser")
	}

	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(hub.URL, "http")+"/peer/work/ws/events", http.Header{"Origin": {hub.URL}})
	if err != nil {
		t.Fatalf("websocket through the hub: %v", err)
	}
	_, msg, err := ws.ReadMessage()
	ws.Close()
	if err != nil || string(msg) != "hello from peer" {
		t.Fatalf("websocket message %q, %v", msg, err)
	}

	// A page on another origin (e.g. a preview subdomain) must not reach the peer.
	_, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(hub.URL, "http")+"/peer/work/ws/events", http.Header{"Origin": {"http://abc.preview.example.com"}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin websocket through the hub: want 403, got %v, %v", resp, err)
	}

	res, _ = http.Get(hub.URL + "/api/machines")
	body, _ = io.ReadAll(res.Body)
	if string(body) != "home:ok work:ok " {
		t.Fatalf("machines: %q", body)
	}
}

func TestWrongTokenIsReported(t *testing.T) {
	up := upstream(t)
	defer up.Close()
	p, _ := Parse("work=" + up.URL)
	h := &Hub{Self: "home", Token: "wrong", Peers: []Peer{p}}
	ms := h.Machines(context.Background())
	if ms[1].OK || !strings.Contains(ms[1].Error, "DASH_PEER_TOKEN") {
		t.Fatalf("want a token error, got %+v", ms[1])
	}
	// A 401 from the peer must not look like the browser's session expired.
	hub := hubServer(t, up.URL, "wrong")
	defer hub.Close()
	res, _ := http.Get(hub.URL + "/peer/work/api/projects")
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", res.StatusCode)
	}
}

func TestParse(t *testing.T) {
	for _, bad := range []string{"work", "Work Laptop=https://x", "w=ftp://x", "w="} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
	p, err := Parse("work-laptop=https://work.tail1234.ts.net/")
	if err != nil || p.Name != "work-laptop" || p.URL.String() != "https://work.tail1234.ts.net" {
		t.Fatalf("got %+v %v", p, err)
	}
}
