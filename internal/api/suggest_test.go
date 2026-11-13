package api

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/agent"
)

// readTurn reads conn's deltas up to "done" (or "error"), then, with wait,
// up to a suggestion within wait after it. A read that times out leaves
// conn unusable.
func readTurn(t *testing.T, conn *websocket.Conn, wait time.Duration) []agent.ChatDelta {
	t.Helper()
	var out []agent.ChatDelta
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	ended := false
	for {
		var d agent.ChatDelta
		if err := conn.ReadJSON(&d); err != nil {
			if ended {
				return out // nothing more within wait
			}
			t.Fatal(err)
		}
		out = append(out, d)
		if !ended && (d.Kind == "done" || d.Kind == "error") {
			if wait == 0 {
				return out
			}
			ended = true
			_ = conn.SetReadDeadline(time.Now().Add(wait))
		}
		if d.Kind == "suggestion" {
			return out
		}
	}
}

func kindsOf(ds []agent.ChatDelta) []string {
	var k []string
	for _, d := range ds {
		k = append(k, d.Kind)
	}
	return k
}

func (f *fakeSmall) count(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, k := range f.calls {
		if k == kind {
			n++
		}
	}
	return n
}

// With "suggest", a chat turn ends with a guess at the user's next message,
// made from the conversation with its new reply; without it, Claude isn't
// asked and nothing comes after "done".
func TestChatSuggestsNextMessage(t *testing.T) {
	s, _, p := boardServer(t)
	small := &fakeSmall{where: "here"}
	s.Small, s.Chat = small, &echoChat{}
	ask := []map[string]string{{"role": "user", "content": "Why does login fail?"}}

	off := chatSocket(t, s)
	if err := off.WriteJSON(map[string]any{"type": "send", "project": p.ID, "messages": ask}); err != nil {
		t.Fatal(err)
	}
	got := readTurn(t, off, 300*time.Millisecond)
	if k := kindsOf(got); slices.Contains(k, "suggestion") || k[len(k)-1] != "done" {
		t.Errorf("without suggest: deltas %v", k)
	}
	if n := small.count("suggest"); n != 0 {
		t.Errorf("suggest off: the small model was asked %d times", n)
	}

	on := chatSocket(t, s)
	if err := on.WriteJSON(map[string]any{"type": "send", "project": p.ID, "suggest": true, "messages": ask}); err != nil {
		t.Fatal(err)
	}
	got = readTurn(t, on, 5*time.Second)
	k := kindsOf(got)
	if i := slices.Index(k, "done"); i < 0 || k[len(k)-1] != "suggestion" || got[len(got)-1].Text != "suggest from here" {
		t.Fatalf("with suggest: deltas %+v", got)
	}
	small.mu.Lock()
	transcript := small.texts[len(small.texts)-1]
	small.mu.Unlock()
	if transcript != "User: Why does login fail?\n\nClaude: hi" {
		t.Errorf("the small model was given %q", transcript)
	}
}

// A suggestion still coming when the user sends the next message is dropped.
func TestChatDropsLateSuggestion(t *testing.T) {
	s, _, p := boardServer(t)
	slow := &slowSmall{fakeSmall: fakeSmall{where: "here"}, release: make(chan struct{})}
	s.Small, s.Chat = slow, &echoChat{}
	conn := chatSocket(t, s)
	msgs := []map[string]string{{"role": "user", "content": "hi"}}
	if err := conn.WriteJSON(map[string]any{"type": "send", "project": p.ID, "suggest": true, "messages": msgs}); err != nil {
		t.Fatal(err)
	}
	readTurn(t, conn, 0)
	waitFor(t, "the suggestion call", func() bool { return slow.count("suggest") == 1 })
	// The next turn, without a suggestion; then the first one's guess comes.
	msgs = append(msgs, map[string]string{"role": "assistant", "content": "hi"}, map[string]string{"role": "user", "content": "again"})
	if err := conn.WriteJSON(map[string]any{"type": "send", "project": p.ID, "messages": msgs}); err != nil {
		t.Fatal(err)
	}
	got := readTurn(t, conn, 0)
	close(slow.release)
	got = append(got, readTurn2(conn, 300*time.Millisecond)...)
	if k := kindsOf(got); slices.Contains(k, "suggestion") {
		t.Errorf("a late suggestion was sent: %v", k)
	}
}

// readTurn2 reads whatever conn sends within wait.
func readTurn2(conn *websocket.Conn, wait time.Duration) []agent.ChatDelta {
	var out []agent.ChatDelta
	_ = conn.SetReadDeadline(time.Now().Add(wait))
	for {
		var d agent.ChatDelta
		if conn.ReadJSON(&d) != nil {
			return out
		}
		out = append(out, d)
	}
}

// slowSmall answers a suggestion once released.
type slowSmall struct {
	fakeSmall
	release chan struct{}
}

func (f *slowSmall) Suggest(ctx context.Context, t string) (string, error) {
	out, _ := f.call("suggest", t)
	<-f.release
	return out, nil
}

// On a hub, the suggestion is a short call on the user's own runner.
func TestHubAsksTheUsersRunnerForSuggestions(t *testing.T) {
	parallel(t)
	f := newClaudeFleet(t)
	conn, _, err := websocket.DefaultDialer.Dial(f.ownerWS, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(map[string]any{"type": "send", "suggest": true, "messages": []map[string]string{{"role": "user", "content": "what's up?"}}}); err != nil {
		t.Fatal(err)
	}
	got := readTurn(t, conn, 5*time.Second)
	if last := got[len(got)-1]; last.Kind != "suggestion" || last.Text != "suggest from runner" {
		t.Fatalf("deltas %+v", got)
	}
	if f.hubSmall.count("suggest") != 0 || f.small.count("suggest") != 1 {
		t.Errorf("suggest calls: hub %d, runner %d", f.hubSmall.count("suggest"), f.small.count("suggest"))
	}
	f.small.mu.Lock()
	defer f.small.mu.Unlock()
	if !strings.HasPrefix(f.small.texts[len(f.small.texts)-1], "User: what's up?") {
		t.Errorf("the runner was given %q", f.small.texts)
	}
}
