package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// gateChat answers "hi" once released, so a test can act while a turn runs.
type gateChat struct {
	echoChat
	started, release chan struct{}
}

func (c *gateChat) Stream(ctx context.Context, dir string, h []agent.ChatMessage, emit func(agent.ChatDelta)) error {
	c.started <- struct{}{}
	select {
	case <-c.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return c.echoChat.Stream(ctx, dir, h, emit)
}

func chatContents(msgs []model.ChatMessage) string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Content)
	}
	return strings.Join(out, " | ")
}

// A project's chat is each member's own: the owner (an admin) doesn't see a
// member's, nor the member the owner's; clearing one leaves the other; and
// each change is told to its user only.
func TestProjectChatIsEachUsersOwn(t *testing.T) {
	s, evs, p := boardServer(t)
	s.Chat = &echoChat{}
	owner := testOwner(t, s)
	mid := member(t, s, owner.Org, "m", store.RoleMember)
	m := loginAs(t, s.Routes(), "m", "long-enough-m")
	path := "/api/projects/" + p.ID + "/chat"

	conn := chatSocket(t, s)
	if err := conn.WriteJSON(map[string]any{"type": "send", "project": p.ID, "messages": []agent.ChatMessage{cmdMsg("owner's question")}}); err != nil {
		t.Fatal(err)
	}
	if k := kindsOf(readTurn(t, conn, 0)); k[len(k)-1] != "done" {
		t.Fatalf("owner's turn: %v", k)
	}
	if err := raw(s).AppendProjectChat(p.ID, mid, model.ChatMessage{Role: "user", Content: "member's question", CreatedBy: mid}); err != nil {
		t.Fatal(err)
	}

	get := func(h http.Handler) string {
		t.Helper()
		w := send(h, "GET", path, "")
		var msgs []model.ChatMessage
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &msgs) != nil {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
		}
		return chatContents(msgs)
	}
	if got := get(asOwnerHandler(t, s)); got != "owner's question | hi" {
		t.Errorf("owner reads %q", got)
	}
	if got := get(m); got != "member's question" {
		t.Errorf("member reads %q", got)
	}

	if w := send(m, "DELETE", path, ""); w.Code != http.StatusNoContent {
		t.Fatalf("member clears: %d %s", w.Code, w.Body)
	}
	if got := get(m); got != "" {
		t.Errorf("member's chat after clearing: %q", got)
	}
	if got := get(asOwnerHandler(t, s)); got != "owner's question | hi" {
		t.Errorf("clearing the member's chat changed the owner's: %q", got)
	}

	evs.mu.Lock()
	defer evs.mu.Unlock()
	var to []int64
	for _, ev := range evs.evs {
		if ev.Type == model.EvProjectChat {
			to = append(to, ev.UserID)
		}
	}
	if len(to) != 3 || to[0] != owner.User || to[1] != mid || to[2] != mid {
		t.Errorf("project chat events for users %v, want [owner member member]", to)
	}
}

// The server keeps a project's chat: a turn sent from an out-of-date copy
// (another device added to the chat, or cleared it) is refused as "stale"
// and removes nothing; turns from two devices both stay, in the order they
// were saved. While one runs, the chat says it is busy, on every device.
func TestProjectChatStaleCopyKeepsNewerMessages(t *testing.T) {
	s, _, p := boardServer(t)
	gate := &gateChat{started: make(chan struct{}, 1), release: make(chan struct{})}
	s.Chat = gate
	owner := testOwner(t, s)
	saved := func() []model.ChatMessage { return raw(s).ProjectChat(p.ID, owner.User) }

	// Another device saved two turns.
	newer := []model.ChatMessage{
		{Role: "user", Content: "q1", CreatedBy: owner.User}, {Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2", CreatedBy: owner.User}, {Role: "assistant", Content: "a2"},
	}
	if err := raw(s).AppendProjectChat(p.ID, owner.User, newer...); err != nil {
		t.Fatal(err)
	}
	conn := chatSocket(t, s)
	turn := func(base *int, msgs ...model.ChatMessage) []string {
		t.Helper()
		req := map[string]any{"type": "send", "project": p.ID, "messages": msgs}
		if base != nil {
			req["base"] = *base
		}
		if err := conn.WriteJSON(req); err != nil {
			t.Fatal(err)
		}
		return kindsOf(readTurn(t, conn, 0))
	}
	two := 2
	for name, msgs := range map[string][]model.ChatMessage{
		"an older copy":                   append(newer[:2:2], cmdMsg("from the old copy")),
		"a copy from before it was saved": {cmdMsg("from an empty copy")},
		"a different copy":                {newer[0], {Role: "assistant", Content: "something else"}, newer[2], newer[3], cmdMsg("q3")},
	} {
		if k := turn(nil, msgs...); len(k) != 1 || k[0] != "stale" {
			t.Errorf("%s: %v, want stale", name, k)
		}
	}
	// A base that isn't what is saved.
	if k := turn(&two, append(newer[:4:4], cmdMsg("q3"))...); len(k) != 1 || k[0] != "stale" {
		t.Errorf("wrong base: %v, want stale", k)
	}
	if got := chatContents(saved()); got != "q1 | a1 | q2 | a2" {
		t.Fatalf("a stale copy changed the chat: %q", got)
	}
	if gate.calls.Load() != 0 {
		t.Fatal("Claude was asked from a stale copy")
	}

	// Up to date: it runs. Meanwhile the chat is busy, and another device's
	// turn is saved; neither turn loses the other's messages.
	if err := conn.WriteJSON(map[string]any{"type": "send", "project": p.ID, "base": 4, "messages": append(newer[:4:4], cmdMsg("q3"))}); err != nil {
		t.Fatal(err)
	}
	<-gate.started
	h := asOwnerHandler(t, s)
	if w := send(h, "GET", "/api/projects/"+p.ID+"/chat", ""); w.Header().Get("X-Chat-Busy") != "1" {
		t.Error("the chat isn't busy while a turn runs")
	}
	if err := raw(s).AppendProjectChat(p.ID, owner.User, cmdMsg("elsewhere"), model.ChatMessage{Role: "assistant", Content: "there"}); err != nil {
		t.Fatal(err)
	}
	close(gate.release)
	if k := kindsOf(readTurn(t, conn, 0)); k[len(k)-1] != "done" {
		t.Fatalf("turn: %v", k)
	}
	if got := chatContents(saved()); got != "q1 | a1 | q2 | a2 | elsewhere | there | q3 | hi" {
		t.Fatalf("saved %q", got)
	}
	if got := saved()[6]; got.CreatedBy != owner.User {
		t.Errorf("question saved without its author: %+v", got)
	}
	waitFor(t, "the turn to end", func() bool {
		return send(h, "GET", "/api/projects/"+p.ID+"/chat", "").Header().Get("X-Chat-Busy") == ""
	})

	// Cleared on another device: the old copy is stale too.
	if err := raw(s).ClearProjectChat(p.ID, owner.User); err != nil {
		t.Fatal(err)
	}
	if k := turn(nil, append(saved(), cmdMsg("after clearing"))...); k[len(k)-1] != "done" {
		t.Fatalf("an up-to-date copy after clearing: %v", k)
	}
}

// A turn of a user's project chat tells that user's dashboards (and only
// theirs) that it is busy, and when it is done.
func TestProjectChatBusyEventsGoToItsUser(t *testing.T) {
	s, _, p := boardServer(t)
	gate := &gateChat{started: make(chan struct{}, 1), release: make(chan struct{})}
	s.Chat = gate
	owner := testOwner(t, s)
	member(t, s, owner.Org, "m", store.RoleMember)
	routes := s.Routes()
	srv := httptest.NewServer(routes)
	t.Cleanup(srv.Close)
	dial := func(name, password string) *websocket.Conn {
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"`+name+`","password":"`+password+`"}`)))
		hdr := http.Header{}
		for _, c := range w.Result().Cookies() {
			hdr.Add("Cookie", c.Name+"="+c.Value)
		}
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/events", hdr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	mine, theirs := dial("u", "p"), dial("m", "long-enough-m")
	waitFor(t, "both dashboards", func() bool { return s.Hub.Clients() == 2 })

	conn := chatSocket(t, s)
	if err := conn.WriteJSON(map[string]any{"type": "send", "project": p.ID, "messages": []agent.ChatMessage{cmdMsg("q")}}); err != nil {
		t.Fatal(err)
	}
	<-gate.started
	close(gate.release)
	readTurn(t, conn, 0)
	s.Hub.Publish(model.Event{Type: model.EvUsage, Data: "the end"})

	read := func(c *websocket.Conn) (string, any) {
		t.Helper()
		var ev struct {
			Type      string `json:"type"`
			ProjectID string `json:"project_id"`
			Data      any    `json:"data"`
		}
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if err := c.ReadJSON(&ev); err != nil {
			t.Fatal(err)
		}
		return ev.Type, ev.Data
	}
	for _, busy := range []bool{true, false} {
		typ, data := read(mine)
		if d, _ := data.(map[string]any); typ != string(model.EvProjectChat) || d["busy"] != busy {
			t.Fatalf("user's dashboard got %s %v, want busy %v", typ, data, busy)
		}
	}
	if typ, data := read(theirs); typ != string(model.EvUsage) {
		t.Fatalf("another member's dashboard got %s %v", typ, data)
	}
}
