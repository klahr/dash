package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// fakeSmall answers Claude's short calls, saying where it ran.
type fakeSmall struct {
	where string
	mu    sync.Mutex
	calls []string
}

func (f *fakeSmall) call(kind, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, kind)
	return kind + " from " + f.where, nil
}
func (f *fakeSmall) Title(_ context.Context, p string) (string, error)   { return f.call("title", p) }
func (f *fakeSmall) Summary(_ context.Context, r string) (string, error) { return f.call("summary", r) }
func (f *fakeSmall) Refine(_ context.Context, p string) (string, error)  { return f.call("refine", p) }
func (f *fakeSmall) n() int                                              { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

// noChat fails the test if the hub runs a chat turn itself.
type noChat struct{ t *testing.T }

func (c noChat) Stream(context.Context, string, []agent.ChatMessage, func(agent.ChatDelta)) error {
	c.t.Error("the hub ran Claude for the chat")
	return errors.New("the hub must not run Claude")
}

// claudeFleet is a hub that doesn't run Claude, with a runner of the
// owner's (online), and a second member of the org without one.
type claudeFleet struct {
	hub, runner      *Server
	hubSmall, small  *fakeSmall
	creds            tunnel.Credentials
	rh               *tunnel.Hub
	owner, other     http.Handler // the hub's routes, signed in as each
	ownerWS, otherWS string       // ws:// URLs of their /ws/chat
	stop             func()
}

func newClaudeFleet(t *testing.T) *claudeFleet {
	t.Helper()
	f := &claudeFleet{hubSmall: &fakeSmall{where: "hub"}, small: &fakeSmall{where: "runner"}}
	hub, _, _ := boardServer(t)
	f.hub = hub
	f.rh = &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: f.rh}
	hub.LocalClaude = func() bool { return false }
	hub.Titler, hub.Refiner, hub.Chat = f.hubSmall, f.hubSmall, noChat{t}
	srv := httptest.NewServer(hub.Routes())
	t.Cleanup(srv.Close)

	owner := testOwner(t, hub)
	member(t, hub, owner.Org, "bea", store.RoleMember)
	f.owner = loginAs(t, hub.Routes(), "u", "p")
	f.other = loginAs(t, hub.Routes(), "bea", "long-enough-bea")
	for _, x := range []struct {
		h   http.Handler
		url *string
	}{{f.owner, &f.ownerWS}, {f.other, &f.otherWS}} {
		s := httptest.NewServer(x.h)
		t.Cleanup(s.Close)
		*x.url = "ws" + strings.TrimPrefix(s.URL, "http") + "/ws/chat"
	}

	code, _, err := f.rh.NewCode(owner)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if f.creds, err = tunnel.Register(ctx, srv.URL, code, "laptop", ""); err != nil {
		t.Fatal(err)
	}
	if f.creds.UserID != owner.User {
		t.Fatalf("runner registered for user %d, want %d", f.creds.UserID, owner.User)
	}
	runner, h, _ := runnerSide(t, f.creds.OrgID)
	runner.HubUser, runner.Small, runner.Chat = f.creds.UserID, f.small, &echoChat{}
	runner.ChatDir = filepath.Join(t.TempDir(), "chat")
	f.runner = runner
	rctx, stop := context.WithCancel(ctx)
	stopped := make(chan struct{})
	client := &tunnel.Client{Creds: f.creds, Handler: h, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { _ = client.Run(rctx); close(stopped) }()
	f.stop = func() {
		stop()
		<-stopped
		eventually(t, "the runner to go offline", func() bool { r, _ := f.rh.Get(f.creds.ID); return !r.Online })
	}
	eventually(t, "the runner to come online", func() bool { r, _ := f.rh.Get(f.creds.ID); return r.Online })
	return f
}

func (f *claudeFleet) refine(h http.Handler) *httptest.ResponseRecorder {
	return send(h, "POST", "/api/refine", `{"prompt":"hey! could you add a dark mode? thanks"}`)
}

// newTask creates an untitled task as h's user and waits for its title.
func (f *claudeFleet) newTask(t *testing.T, h http.Handler, prompt string) string {
	t.Helper()
	pid := project(t, f.hub, "web").ID
	b, _ := json.Marshal(map[string]string{"prompt": prompt})
	w := send(h, "POST", "/api/projects/"+pid+"/todos", string(b))
	var td model.Todo
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &td) != nil {
		t.Fatalf("create task: %d %s", w.Code, w.Body)
	}
	var title string
	eventually(t, "the task's title", func() bool {
		got, _ := raw(f.hub).GetTodo(td.ID)
		title = got.Title
		return title != ""
	})
	return title
}

// chat sends one general chat question to url and returns the deltas'
// kinds and texts.
func chatTurn(t *testing.T, url string, extra map[string]any) (kinds, texts []string) {
	t.Helper()
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	msg := map[string]any{"type": "send", "messages": []map[string]string{{"role": "user", "content": "what's up?"}}}
	for k, v := range extra {
		msg[k] = v
	}
	if err := ws.WriteJSON(msg); err != nil {
		t.Fatal(err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		var d agent.ChatDelta
		if err := ws.ReadJSON(&d); err != nil {
			t.Fatal(err)
		}
		kinds, texts = append(kinds, d.Kind), append(texts, d.Text)
		if d.Kind == "done" || d.Kind == "error" {
			return kinds, texts
		}
	}
}

// A hub that doesn't run Claude runs a user's chat, titles and clean-up on
// that user's own runner, never on the hub nor on someone else's runner;
// without a runner of theirs online, titles come from the instructions and
// the chat and clean-up say why they can't run.
func TestHubRunsClaudeOnTheUsersOwnRunner(t *testing.T) {
	f := newClaudeFleet(t)
	ownerID := testOwner(t, f.hub).User

	// The owner: everything on their runner.
	if w := f.refine(f.owner); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "refine from runner") {
		t.Fatalf("owner's clean-up: %d %s", w.Code, w.Body)
	}
	if title := f.newTask(t, f.owner, "Add a dark mode\n\nwith a toggle"); title != "title from runner" {
		t.Errorf("owner's task title %q", title)
	}
	kinds, texts := chatTurn(t, f.ownerWS, nil)
	if kinds[0] != "runner" || texts[0] != "laptop" || kinds[len(kinds)-1] != "done" || !strings.Contains(strings.Join(texts, ""), "hi") {
		t.Fatalf("owner's chat: %v %q", kinds, texts)
	}
	eventually(t, "the hub to save the chat", func() bool {
		got := raw(f.hub).Chat(testOwner(t, f.hub).Org, ownerID)
		return len(got) == 2 && got[1].Content == "hi"
	})
	if got := raw(f.runner).Chat(ownerCaller(t, f.runner).Org, ownerCaller(t, f.runner).User); len(got) != 0 {
		t.Errorf("the runner kept the hub's conversation: %+v", got)
	}
	var info struct {
		Local   bool
		Runners []struct{ ID, Name string }
	}
	if w := send(f.owner, "GET", "/api/claude", ""); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &info) != nil ||
		info.Local || len(info.Runners) != 1 || info.Runners[0].Name != "laptop" {
		t.Fatalf("owner's /api/claude: %s", w.Body)
	}

	// Another member of the org: the owner's runner isn't theirs to use.
	before := f.small.n()
	if w := f.refine(f.other); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "no runner of yours") {
		t.Fatalf("other user's clean-up: %d %s", w.Code, w.Body)
	}
	if title := f.newTask(t, f.other, "## Fix the login form\nIt breaks on Safari"); title != "Fix the login form" {
		t.Errorf("other user's task title %q, want the first line", title)
	}
	if kinds, texts := chatTurn(t, f.otherWS, nil); kinds[len(kinds)-1] != "error" || !strings.Contains(texts[len(texts)-1], "no runner of yours") {
		t.Fatalf("other user's chat: %v %q", kinds, texts)
	}
	if kinds, texts := chatTurn(t, f.otherWS, map[string]any{"runner": f.creds.ID}); kinds[len(kinds)-1] != "error" || !strings.Contains(texts[len(texts)-1], "isn't yours") {
		t.Fatalf("other user picking the owner's runner: %v %q", kinds, texts)
	}
	if w := send(f.other, "GET", "/api/claude", ""); !strings.Contains(w.Body.String(), `"runners":[]`) {
		t.Errorf("other user's /api/claude lists runners: %s", w.Body)
	}
	if f.small.n() != before {
		t.Error("the owner's runner ran Claude for another user")
	}

	// The runner offline: nothing runs anywhere.
	f.stop()
	before = f.small.n()
	if w := f.refine(f.owner); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "no runner of yours is online") {
		t.Fatalf("clean-up with the runner offline: %d %s", w.Code, w.Body)
	}
	if title := f.newTask(t, f.owner, "Make the build faster, please, it takes forever on every single change we make to the code"); title != "Make the build faster, please, it takes forever on every…" {
		t.Errorf("title with the runner offline %q", title)
	}
	if kinds, texts := chatTurn(t, f.ownerWS, nil); kinds[len(kinds)-1] != "error" || !strings.Contains(texts[len(texts)-1], "online") {
		t.Fatalf("chat with the runner offline: %v %q", kinds, texts)
	}
	if f.hubSmall.n() != 0 || f.small.n() != before {
		t.Errorf("Claude ran with the runner offline: hub %v, runner %d calls", f.hubSmall.calls, f.small.n()-before)
	}
}

// The runner's own check: a short call for anyone but its owner, or for
// nobody in particular, is refused even if the hub sent it.
func TestRunnerRunsShortCallsOnlyForItsOwner(t *testing.T) {
	s, h, _ := runnerServer(t)
	small := &fakeSmall{where: "runner"}
	s.HubUser, s.Small = 42, small
	call := func(user, kind string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/hub/claude/"+kind, strings.NewReader(`{"text":"do a thing"}`))
		if user != "" {
			r.Header.Set(tunnel.UserHeader, user)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, user := range []string{"", "7", "x"} {
		if w := call(user, "title"); w.Code != http.StatusForbidden {
			t.Errorf("title for user %q: %d %s", user, w.Code, w.Body)
		}
	}
	if small.n() != 0 {
		t.Fatal("ran Claude for someone other than the owner")
	}
	for _, kind := range []string{"title", "summary", "refine"} {
		if w := call("42", kind); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), kind+" from runner") {
			t.Errorf("%s for the owner: %d %s", kind, w.Code, w.Body)
		}
	}
	// Not from the hub: the route isn't there for anyone else.
	w := httptest.NewRecorder()
	r := asOwner(t, s, httptest.NewRequest("POST", "/api/hub/claude/title", strings.NewReader(`{"text":"x"}`)))
	r.Header.Set(tunnel.UserHeader, "42")
	s.mux().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("short call not from the hub: %d %s", w.Code, w.Body)
	}
	// The old clean-up route isn't offered to the hub any more.
	if w := hubCall(h, "POST", "/api/refine", `{"prompt":"x"}`); w.Code != http.StatusForbidden {
		t.Errorf("POST /api/refine from the hub: %d %s", w.Code, w.Body)
	}
}

func TestPlainTitle(t *testing.T) {
	for in, want := range map[string]string{
		"Fix the bug":                "Fix the bug",
		"\n\n## Heading here\nbody":  "Heading here",
		"- a list item\n- another":   "a list item",
		"   spaced    out   words  ": "spaced out words",
		strings.Repeat("word ", 30):  "word word word word word word word word word word word word…",
		"":                           "",
		"Ünïcödé " + strings.Repeat("ä", 70): "Ünïcödé " + strings.Repeat("ä", 52) + "…",
	} {
		if got := plainTitle(in); got != want {
			t.Errorf("plainTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
