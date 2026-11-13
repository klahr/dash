package tunnel

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/disk"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store/storetest"
)

// pair connects two Sessions over a real WebSocket.
func pair(t *testing.T) (dialer, listener *Session) {
	t.Helper()
	got := make(chan *Session, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		got <- NewSession(ws, false)
	}))
	t.Cleanup(srv.Close)
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	dialer = NewSession(ws, true)
	listener = <-got
	t.Cleanup(func() { dialer.Close(); listener.Close() })
	return dialer, listener
}

func TestStreamsCarryMoreThanTheWindow(t *testing.T) {
	t.Parallel()

	a, b := pair(t)
	payload := make([]byte, 3*streamWindow+12345)
	_, _ = rand.Read(payload)

	// b echoes every stream back.
	go func() {
		for {
			c, err := b.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.CopyN(c, c, int64(len(payload)))
			}()
		}
	}()

	var wg sync.WaitGroup
	for range 3 { // concurrent streams don't block each other
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := a.Open()
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			go func() { _, _ = c.Write(payload) }()
			got, err := io.ReadAll(c)
			if err != nil {
				t.Error(err)
				return
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("echo: got %d bytes, want %d identical", len(got), len(payload))
			}
		}()
	}
	wg.Wait()
}

func TestClosingTheSessionFailsStreams(t *testing.T) {
	t.Parallel()

	a, b := pair(t)
	c, err := a.Open()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Accept(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := c.Read(make([]byte, 1)); done <- err }()
	b.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("read succeeded on a dead tunnel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read hung after the tunnel closed")
	}
	if _, err := a.Open(); err == nil {
		select {
		case <-a.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the other end never noticed the tunnel close")
		}
	}
}

func TestReadDeadline(t *testing.T) {
	t.Parallel()

	a, _ := pair(t)
	c, _ := a.Open()
	_ = c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read past its deadline: %v", err)
	}
}

// ---- registration ----

func TestRegistrationCodeIsSingleUseAndExpires(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	acc, owner := storetest.Accounts(t)
	h := &Hub{now: func() time.Time { return now }, Accounts: acc}
	path := filepath.Join(t.TempDir(), "runners.json")
	if err := h.Keep(path); err != nil {
		t.Fatal(err)
	}

	code, _, _ := h.NewCode(owner)
	if _, _, _, err := h.register(code, "Bad Name!", ""); !errors.Is(err, ErrBadName) {
		t.Fatalf("bad name: %v", err)
	}
	id, token, _, err := h.register(strings.ToLower(code), "work", "") // codes aren't case-sensitive
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || !strings.HasPrefix(token, "dr_") {
		t.Fatalf("id %q token %q", id, token)
	}
	if _, _, _, err := h.register(code, "other", ""); !errors.Is(err, ErrBadCode) {
		t.Fatalf("second use: %v", err)
	}

	code2, _, _ := h.NewCode(owner)
	if _, _, _, err := h.register(code2, "work", ""); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	now = now.Add(codeTTL + time.Second)
	if _, _, _, err := h.register(code2, "laptop", ""); !errors.Is(err, ErrBadCode) {
		t.Fatalf("expired code: %v", err)
	}

	// Saved, with the token only as a hash.
	h2 := &Hub{Accounts: acc}
	if err := h2.Keep(path); err != nil {
		t.Fatal(err)
	}
	if !h2.Has("work") {
		t.Fatal("runner not saved")
	}
	if _, ok := h2.authenticate(token); !ok {
		t.Fatal("saved token hash doesn't match")
	}
	if b, _ := json.Marshal(h2.runners); bytes.Contains(b, []byte(token)) {
		t.Fatal("token stored in plain text")
	}
}

func TestRegistrationNamesTakenByTheHub(t *testing.T) {
	t.Parallel()

	acc, owner := storetest.Accounts(t)
	h := &Hub{Taken: func(n string) bool { return n == "home" }, Accounts: acc}
	code, _, _ := h.NewCode(owner)
	if _, _, _, err := h.register(code, "home", ""); !errors.Is(err, ErrNameHub) {
		t.Fatalf("hub's own name: %v", err)
	}
}

func TestSameComputerRegistersAgain(t *testing.T) {
	t.Parallel()

	th := newTestHub(t)
	old := th.register(t, "work")

	// Another computer can't take the name.
	code, _, _ := th.NewCode(th.owner)
	if _, err := Register(context.Background(), th.srv.URL, code, "work", "dr_not-a-token"); err == nil || !strings.Contains(err.Error(), ErrNameTaken.Error()) {
		t.Fatalf("other computer: %v", err)
	}

	// The same one, showing its old token, replaces its old registration.
	again, err := Register(context.Background(), th.srv.URL, code, "work", old.Token)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == old.ID {
		t.Fatal("same id again")
	}
	if rs := th.List(); len(rs) != 1 || rs[0].ID != again.ID {
		t.Fatalf("runners after re-registering: %+v", rs)
	}
	if _, ok := th.authenticate(old.Token); ok {
		t.Fatal("old token still works")
	}
	if _, ok := th.authenticate(again.Token); !ok {
		t.Fatal("new token doesn't work")
	}
}

// List sorts by name ignoring case, then by exact name, then by id, so runners
// of one name keep their places from one call to the next.
func TestListSortsByNameThenID(t *testing.T) {
	t.Parallel()

	th := newTestHub(t)
	beta := th.register(t, "beta")
	// register lower-cases names and refuses a taken one; a runners file
	// written before that can still hold these (Keep loads it as it is).
	th.mu.Lock()
	th.runners = append(th.runners,
		&record{ID: "same-2", Name: "same"},
		&record{ID: "a-lower", Name: "alpha"},
		&record{ID: "same-1", Name: "same"},
		&record{ID: "a-upper", Name: "Alpha"},
	)
	th.mu.Unlock()

	want := []string{"Alpha/a-upper", "alpha/a-lower", "beta/" + beta.ID, "same/same-1", "same/same-2"}
	rng := mrand.New(mrand.NewPCG(1, 2))
	for i := range 20 {
		th.mu.Lock()
		rng.Shuffle(len(th.runners), func(a, b int) { th.runners[a], th.runners[b] = th.runners[b], th.runners[a] })
		th.mu.Unlock()
		var got []string
		for _, r := range th.List() {
			got = append(got, r.Name+"/"+r.ID)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("call %d: List() = %v, want %v", i, got, want)
		}
	}
}

func TestWrongCodesPauseRegistration(t *testing.T) {
	t.Parallel()

	acc, owner := storetest.Accounts(t)
	h := &Hub{Accounts: acc}
	code, _, _ := h.NewCode(owner)
	for range maxBadCodes {
		if _, _, _, err := h.register("AAAA-AAAA", "x", ""); !errors.Is(err, ErrBadCode) {
			t.Fatal(err)
		}
	}
	if _, _, _, err := h.register(code, "x", ""); !errors.Is(err, ErrTooManyBad) {
		t.Fatalf("after %d wrong codes: %v", maxBadCodes, err)
	}
}

// ---- hub and runner end to end ----

type testHub struct {
	*Hub
	srv   *httptest.Server
	owner model.Owner
}

func newTestHub(t *testing.T) testHub {
	t.Helper()
	acc, owner := storetest.Accounts(t)
	h := &Hub{Accounts: acc}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /runner/register", h.ServeRegister)
	mux.HandleFunc("GET /runner/connect", h.ServeConnect)
	mux.HandleFunc("/peer/{name}/", func(w http.ResponseWriter, r *http.Request) {
		px, ok := h.Proxy(r.PathValue("name"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		px.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return testHub{h, srv, owner}
}

func (th testHub) register(t *testing.T, name string) Credentials {
	t.Helper()
	code, _, _ := th.NewCode(th.owner)
	c, err := Register(context.Background(), th.srv.URL, code, name, "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// runnerAPI is a stand-in for a runner's Dash API.
func runnerAPI() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			http.Error(w, "browser credentials leaked to the runner", http.StatusTeapot)
			return
		}
		_, _ = io.WriteString(w, "runner:"+r.URL.Path)
	})
	// who answers whom the hub said a request is for.
	mux.HandleFunc("GET /api/who", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `"`+r.Header.Get(UserHeader)+`"`)
	})
	mux.HandleFunc("GET /ws/who", func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		_ = ws.WriteMessage(websocket.TextMessage, []byte(r.Header.Get(UserHeader)))
	})
	mux.HandleFunc("GET /ws/echo", func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			typ, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			_ = ws.WriteMessage(typ, msg)
		}
	})
	return mux
}

// stateLog records OnState calls.
type stateLog struct {
	ch chan bool
}

func (s *stateLog) on(c bool) { s.ch <- c }

func (s *stateLog) want(t *testing.T, v bool) {
	t.Helper()
	select {
	case got := <-s.ch:
		if got != v {
			t.Fatalf("state %v, want %v", got, v)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("no state change to %v", v)
	}
}

// waitOnline waits until the hub has the runner's tunnel (the runner may
// report connected a moment before the hub has registered it).
func (th testHub) waitOnline(t *testing.T, name string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); th.session(name) == nil; {
		if time.Now().After(deadline) {
			t.Fatalf("%s never came online", name)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func startRunner(t *testing.T, c Credentials) (*stateLog, chan error) {
	t.Helper()
	states := &stateLog{ch: make(chan bool, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	cl := &Client{Creds: c, Handler: runnerAPI(), OnState: states.on, MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond}
	go func() { done <- cl.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runner didn't stop")
		}
	})
	return states, done
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Cookie", "dash_session=browser-secret")
	req.Header.Set("Authorization", "Bearer something")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestRunnerServesTheHubThroughItsTunnel(t *testing.T) {
	t.Parallel()

	th := newTestHub(t)
	states, _ := startRunner(t, th.register(t, "work"))
	states.want(t, true)
	th.waitOnline(t, "work")

	code, body := get(t, th.srv.URL+"/peer/work/api/me")
	if code != 200 || body != "runner:/api/me" {
		t.Fatalf("GET through tunnel: %d %q", code, body)
	}
	if rs := th.List(); len(rs) != 1 || !rs[0].Online || rs[0].LastSeen.IsZero() {
		t.Fatalf("runners: %+v", rs)
	}

	// WebSockets (event stream, chat) pass through too.
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(th.srv.URL, "http")+"/peer/work/ws/echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if err := ws.WriteMessage(websocket.TextMessage, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, msg, err := ws.ReadMessage(); err != nil || string(msg) != "hello" {
		t.Fatalf("websocket echo: %q %v", msg, err)
	}
}

func TestRunnerReconnectsAfterTheTunnelDrops(t *testing.T) {
	t.Parallel()

	th := newTestHub(t)
	states, _ := startRunner(t, th.register(t, "work"))
	states.want(t, true)
	th.waitOnline(t, "work")

	old := th.session("work")
	old.Close() // the link dies
	states.want(t, false)
	states.want(t, true)
	for deadline := time.Now().Add(10 * time.Second); th.session("work") == nil || th.session("work") == old; {
		if time.Now().After(deadline) {
			t.Fatal("hub never saw the reconnect")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if code, body := get(t, th.srv.URL+"/peer/work/api/me"); code != 200 {
		t.Fatalf("after reconnect: %d %s", code, body)
	}
}

func TestOfflineRunnerAnswersFast(t *testing.T) {
	t.Parallel()

	th := newTestHub(t)
	th.register(t, "work") // never connects
	code, body := get(t, th.srv.URL+"/peer/work/api/me")
	if code != http.StatusBadGateway || !strings.Contains(body, "offline") {
		t.Fatalf("offline runner: %d %s", code, body)
	}
	if code, _ := get(t, th.srv.URL+"/peer/nobody/api/me"); code != http.StatusNotFound {
		t.Fatalf("unknown runner: %d", code)
	}
}

func TestRevokedRunnerIsCutOffAndStops(t *testing.T) {
	t.Parallel()

	th := newTestHub(t)
	creds := th.register(t, "work")
	states, done := startRunner(t, creds)
	states.want(t, true)
	th.waitOnline(t, "work")

	if err := th.Revoke(creds.ID); err != nil {
		t.Fatal(err)
	}
	states.want(t, false)
	select {
	case err := <-done:
		if !errors.Is(err, ErrRevoked) {
			t.Fatalf("runner stopped with %v, want ErrRevoked", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("revoked runner kept retrying")
	}
	if code, _ := get(t, th.srv.URL+"/peer/work/api/me"); code != http.StatusNotFound {
		t.Fatalf("revoked runner still reachable: %d", code)
	}
}

func TestConnectNeedsAValidToken(t *testing.T) {
	t.Parallel()

	th := newTestHub(t)
	th.register(t, "work")
	c := &Client{Creds: Credentials{Server: th.srv.URL, Token: "dr_forged"}, Handler: runnerAPI()}
	if err := c.serveOnce(context.Background()); !errors.Is(err, ErrRevoked) {
		t.Fatalf("forged token: %v", err)
	}
	// A browser page can't open the tunnel (it can't set Authorization, and
	// its Origin is refused).
	hdr := http.Header{"Origin": {"https://evil.example"}}
	if _, res, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(th.srv.URL, "http")+"/runner/connect", hdr); err == nil || res == nil || res.StatusCode == http.StatusSwitchingProtocols {
		t.Fatal("tunnel opened without a token")
	}
}

func TestCheckServer(t *testing.T) {
	t.Parallel()

	for in, ok := range map[string]bool{
		"https://hub.example.com":  true,
		"https://hub.example.com/": true,
		"http://127.0.0.1:7788":    true,
		"http://hub.example.com":   false,
		"hub.example.com":          false,
		"ftp://hub":                false,
	} {
		if _, err := CheckServer(in); (err == nil) != ok {
			t.Errorf("CheckServer(%q) = %v", in, err)
		}
	}
}

func TestHubRecordsTheRunnersVersion(t *testing.T) {
	t.Parallel()

	th := newTestHub(t)
	creds := th.register(t, "work")
	states := &stateLog{ch: make(chan bool, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	cl := &Client{Creds: creds, Handler: runnerAPI(), OnState: states.on, Version: "v1.2.3"}
	go func() { _ = cl.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	states.want(t, true)
	th.waitOnline(t, "work")
	for deadline := time.Now().Add(5 * time.Second); ; {
		l := th.List()
		if len(l) == 1 && l[0].Version == "v1.2.3" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runners %+v, want version v1.2.3", l)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := cleanVersion("v1 <script>"); got != "" {
		t.Fatalf("junk version kept: %q", got)
	}
}

// The hub tells a runner whom a call is for (its owner registered it, and
// learns their id), and nobody else can: a browser's request through the
// proxy loses the header.
func TestHubTellsTheRunnerWhomACallIsFor(t *testing.T) {
	t.Parallel()

	th := newTestHub(t)
	c := th.register(t, "work")
	if c.UserID != th.owner.User || c.UserID == 0 {
		t.Fatalf("registration gave user %d, want the owner %d", c.UserID, th.owner.User)
	}
	states, _ := startRunner(t, c)
	states.want(t, true)
	th.waitOnline(t, "work")

	ctx := WithUser(context.Background(), 42)
	var who string
	if err := th.Call(ctx, c.ID, "GET", "/api/who", nil, &who); err != nil || who != "42" {
		t.Fatalf("Call for user 42: %q %v", who, err)
	}
	if err := th.Call(context.Background(), c.ID, "GET", "/api/who", nil, &who); err != nil || who != "" {
		t.Fatalf("Call for nobody: %q %v", who, err)
	}
	ws, err := th.DialWS(ctx, c.ID, "/ws/who")
	if err != nil {
		t.Fatal(err)
	}
	if _, msg, err := ws.ReadMessage(); err != nil || string(msg) != "42" {
		t.Fatalf("DialWS for user 42: %q %v", msg, err)
	}
	ws.Close()

	req, _ := http.NewRequest("GET", th.srv.URL+"/peer/work/api/who", nil)
	req.Header.Set(UserHeader, "42")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != `""` {
		t.Fatalf("a browser's claimed user reached the runner: %s", b)
	}
	if _, err := th.DialWS(ctx, "nope", "/ws/who"); !errors.Is(err, ErrUnknownName) {
		t.Errorf("DialWS to an unknown runner: %v", err)
	}
}

// The proxy sets X-Dash-User to the user the hub checked (WithUser), over
// whatever the browser claimed, for plain requests and WebSocket upgrades;
// with no checked user it sends none.
func TestProxyNamesTheCheckedUserNotTheClaimedOne(t *testing.T) {
	t.Parallel()

	th := newTestHub(t)
	c := th.register(t, "work")
	states, _ := startRunner(t, c)
	states.want(t, true)
	th.waitOnline(t, "work")

	px, ok := th.Proxy("work")
	if !ok {
		t.Fatal("no proxy for the runner")
	}
	var uid int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if uid != 0 {
			r = r.WithContext(WithUser(r.Context(), uid))
		}
		px.ServeHTTP(w, r)
	}))
	defer srv.Close()

	get := func(forged string) string {
		req, _ := http.NewRequest("GET", srv.URL+"/peer/work/api/who", nil)
		if forged != "" {
			req.Header.Set(UserHeader, forged)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	dial := func(forged string) string {
		hdr := http.Header{}
		if forged != "" {
			hdr.Set(UserHeader, forged)
		}
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/peer/work/ws/who", hdr)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.Close()
		_, msg, err := ws.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		return string(msg)
	}

	uid = 42
	if got := get("7"); got != `"42"` {
		t.Errorf("request for user 42 claiming 7 reached the runner as %s", got)
	}
	if got := get(""); got != `"42"` {
		t.Errorf("request for user 42 reached the runner as %s", got)
	}
	if got := dial("7"); got != "42" {
		t.Errorf("websocket for user 42 claiming 7 reached the runner as %q", got)
	}
	uid = 0
	if got := get("7"); got != `""` {
		t.Errorf("request for nobody claiming 7 reached the runner as %s", got)
	}
	if got := dial("7"); got != "" {
		t.Errorf("websocket for nobody claiming 7 reached the runner as %q", got)
	}
}

// A runner reports the Claude account it is on (bounded), and the hub may
// answer its report; an older hub's 204 leaves the reply as it was.
func TestReportCarriesTheAccountAndTheReply(t *testing.T) {
	t.Parallel()

	acc, owner := storetest.Accounts(t)
	h := &Hub{Accounts: acc}
	code, _, _ := h.NewCode(owner)
	id, token, _, err := h.register(code, "work", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(h.ServeReport))
	defer srv.Close()
	creds := Credentials{Server: srv.URL, Token: token}
	report := map[string]any{"parallel": 2, "claude": map[string]any{"version": "2.1.0",
		"account": map[string]any{"email": " Ann@Example.com ", "org_id": "o-1", "subscription": strings.Repeat("x", 100)}}}

	reply := map[string]any{"kept": true}
	if err := ReportReply(context.Background(), creds, report, &reply); err != nil || !reply["kept"].(bool) {
		t.Fatalf("204: %v %v", err, reply)
	}
	r, _ := h.Get(id)
	if a := r.Reported.Claude.Account; a == nil || a.Email != "Ann@Example.com" || a.Subscription != strings.Repeat("x", 40)+"…" || r.Account() != "ann@example.com" {
		t.Fatalf("account %+v (%q)", a, r.Account())
	}

	h.ReportReply = func(rid string) any { return map[string]string{"for": rid} }
	var got struct{ For string }
	if err := ReportReply(context.Background(), creds, report, &got); err != nil || got.For != id {
		t.Fatalf("reply: %v %+v", err, got)
	}
	if err := Report(context.Background(), creds, report); err != nil {
		t.Fatalf("a runner that ignores the reply: %v", err)
	}
	// Logged out: no account.
	report["claude"] = map[string]any{"version": "2.1.0"}
	if err := Report(context.Background(), creds, report); err != nil {
		t.Fatal(err)
	}
	if r, _ := h.Get(id); r.Account() != "" {
		t.Fatalf("logged out, still %q", r.Account())
	}
}

// A runner reports its free disk space; the hub works out under the
// runner's thresholds whether it is low or too full to start a task.
func TestReportCarriesDiskSpace(t *testing.T) {
	t.Parallel()

	acc, owner := storetest.Accounts(t)
	h := &Hub{Accounts: acc}
	code, _, _ := h.NewCode(owner)
	id, token, _, err := h.register(code, "work", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(h.ServeReport))
	defer srv.Close()
	creds := Credentials{Server: srv.URL, Token: token}
	send := func(free uint64) Runner {
		t.Helper()
		rep := map[string]any{"parallel": 1, "disk": disk.Status{Volumes: []disk.Volume{
			{Label: "projects", Path: "/home/u/dash-projects", Free: free, Total: 500 * disk.GB},
			{Label: "bogus", Path: "/x", Free: 0, Total: 1},
		}}}
		if err := Report(context.Background(), creds, rep); err != nil {
			t.Fatal(err)
		}
		r, _ := h.Get(id)
		return r
	}
	if r := send(100 * disk.GB); r.DiskLow != "" || r.DiskFull || len(r.Reported.Disk.Volumes) != 1 {
		t.Fatalf("100 GB free: %q %v %+v", r.DiskLow, r.DiskFull, r.Reported.Disk)
	}
	if r := send(5 * disk.GB); r.DiskLow != "work has only 5.0 GB free of 500 GB on the disk of its projects" || r.DiskFull {
		t.Fatalf("5 GB free: %q %v", r.DiskLow, r.DiskFull)
	}
	if r := send(disk.GB); !r.DiskFull {
		t.Fatal("1 GB free is below the 2 GB to start a task")
	}
	if err := h.SetDisk(id, disk.Settings{WarnGB: 0.5, MinFreeGB: 0.5, TrimGoCacheGB: 40}); err != nil {
		t.Fatal(err)
	}
	if r, _ := h.Get(id); r.DiskLow != "" || r.DiskFull || r.Disk.TrimGoCacheGB != 40 {
		t.Fatalf("with lower thresholds: %q %v %+v", r.DiskLow, r.DiskFull, r.Disk)
	}
}
