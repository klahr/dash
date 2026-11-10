package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

func TestWithTodoContext(t *testing.T) {
	history := []agent.ChatMessage{{Role: "user", Content: "make it better"}, {Role: "assistant", Content: "ok"}}
	td := model.Todo{Title: "Dark mode", Prompt: "Add a dark theme", Status: model.StatusDraft, Feedback: []string{"use CSS vars"}}
	got := withTodoContext(td, "+body { background: black }", history)
	first := got[0].Content
	for _, want := range []string{"Add a dark theme", "Dark mode", "use CSS vars", "draft", "make it better", "+body { background: black }"} {
		if !strings.Contains(first, want) {
			t.Errorf("context lacks %q:\n%s", want, first)
		}
	}
	if !strings.HasSuffix(first, "make it better") {
		t.Errorf("user's message should come last:\n%s", first)
	}
	if history[0].Content != "make it better" {
		t.Errorf("history was modified: %q", history[0].Content)
	}
	if got[1].Content != history[1].Content {
		t.Errorf("later turns changed: %v", got[1])
	}
}

func TestChatAttachments(t *testing.T) {
	s, _, _ := boardServer(t)
	s.UploadRoot = t.TempDir()
	dir := filepath.Join(s.UploadRoot, "0123456789ab")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shot.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	history := []agent.ChatMessage{{Role: "user", Content: "what's wrong here?", Attachments: []model.Attachment{
		{ID: "0123456789ab", Name: "shot.png", Path: "/etc/passwd"}, // the client's path is ignored
		{ID: "ffffffffffff", Name: "gone.png"},
	}}}
	got := s.as(ownerCaller(t, s)).chatAttachments(history)
	if len(got[0].Attachments) != 1 || got[0].Attachments[0].Path != filepath.Join(dir, "shot.png") {
		t.Fatalf("attachments = %+v", got[0].Attachments)
	}
	prompt := withAttachments(got)[0].Content
	if !strings.HasPrefix(prompt, "what's wrong here?") || !strings.Contains(prompt, filepath.Join(dir, "shot.png")) {
		t.Errorf("prompt = %q", prompt)
	}
	if got[0].Content != "what's wrong here?" {
		t.Errorf("saved message was modified: %q", got[0].Content)
	}
}

// approvalClient records what chatApprovals sends and answers through answer.
func approvalClient(timeout time.Duration) (*chatApprovals, chan agent.ChatDelta) {
	sent := make(chan agent.ChatDelta, 10)
	return &chatApprovals{send: func(d agent.ChatDelta) { sent <- d }, timeout: timeout}, sent
}

func TestChatApprovals(t *testing.T) {
	input := json.RawMessage(`{"command":"uname -a"}`)
	for _, tc := range []struct {
		name    string
		allow   bool
		want    bool
		decided string
	}{{"approve", true, true, "allow"}, {"deny", false, false, "deny"}} {
		t.Run(tc.name, func(t *testing.T) {
			a, sent := approvalClient(time.Minute)
			done := make(chan bool)
			go func() { ok, _ := a.forTurn(context.Background())(context.Background(), "Bash", input); done <- ok }()
			req := <-sent
			if req.Kind != "permission" || req.Tool != "Bash" || string(req.Input) != string(input) || req.ID == "" {
				t.Fatalf("request: %+v", req)
			}
			if !a.answer(req.ID, tc.allow) {
				t.Fatal("request not pending")
			}
			if got := <-done; got != tc.want {
				t.Fatalf("allowed = %v", got)
			}
			if d := <-sent; d.Kind != "permission_decided" || d.ID != req.ID || d.Text != tc.decided {
				t.Fatalf("decided: %+v", d)
			}
			if a.answer(req.ID, true) {
				t.Fatal("answered twice")
			}
		})
	}
}

func TestChatApprovalTimeout(t *testing.T) {
	a, sent := approvalClient(20 * time.Millisecond)
	ok, msg := a.forTurn(context.Background())(context.Background(), "Edit", json.RawMessage(`{}`))
	if ok || msg == "" {
		t.Fatalf("timeout should deny with a reason: %v %q", ok, msg)
	}
	<-sent
	if d := <-sent; d.Text != "timeout" {
		t.Fatalf("decided: %+v", d)
	}
}

func TestChatApprovalTurnEnds(t *testing.T) {
	a, sent := approvalClient(time.Minute)
	turn, endTurn := context.WithCancel(context.Background())
	done := make(chan bool)
	go func() { ok, _ := a.forTurn(turn)(context.Background(), "Bash", json.RawMessage(`{}`)); done <- ok }()
	req := <-sent
	endTurn() // the client disconnected or sent a new message
	if <-done {
		t.Fatal("ended turn should deny")
	}
	if a.answer(req.ID, true) {
		t.Fatal("request should no longer be pending")
	}
}

// stubChat answers with err, or (when err is nil) waits for its turn to end.
type stubChat struct{ err error }

func (c stubChat) Stream(ctx context.Context, _ string, _ []agent.ChatMessage, _ func(agent.ChatDelta)) error {
	if c.err != nil {
		return c.err
	}
	<-ctx.Done()
	return ctx.Err()
}

// echoChat answers "hi" and records where it ran.
type echoChat struct {
	calls atomic.Int32
	mu    sync.Mutex
	ran   string
}

func (c *echoChat) Stream(ctx context.Context, dir string, _ []agent.ChatMessage, emit func(agent.ChatDelta)) error {
	c.calls.Add(1)
	c.mu.Lock()
	c.ran = dir
	c.mu.Unlock()
	emit(agent.ChatDelta{Kind: "text", Text: "hi"})
	emit(agent.ChatDelta{Kind: "done"})
	return nil
}

func (c *echoChat) dir() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ran
}

func chatSocket(t *testing.T, s *Server) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.chatWS(w, asOwner(t, s, r)) }))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for " + what)
		}
	}
}

// A question is saved as it arrives, so a turn cut off by a restart (here the
// connection dropping) can be asked again.
func TestGeneralChatSavesQuestion(t *testing.T) {
	s, _, _ := boardServer(t)
	s.Chat = stubChat{}
	prev := []agent.ChatMessage{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}
	if err := raw(s).SetChat(ownerCaller(t, s).Org, ownerCaller(t, s).User, prev); err != nil {
		t.Fatal(err)
	}
	conn := chatSocket(t, s)
	msgs := append(prev, agent.ChatMessage{Role: "user", Content: "and then?"})
	if err := conn.WriteJSON(map[string]any{"type": "send", "messages": msgs}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the busy turn", func() bool { return s.generalBusy(ownerCaller(t, s)) })
	if got := raw(s).Chat(ownerCaller(t, s).Org, ownerCaller(t, s).User); len(got) != 3 || got[2].Content != "and then?" {
		t.Fatalf("saved %+v", got)
	}
	conn.Close()
	waitFor(t, "the turn to end", func() bool { return !s.generalBusy(ownerCaller(t, s)) })
	if got := raw(s).Chat(ownerCaller(t, s).Org, ownerCaller(t, s).User); len(got) != 3 {
		t.Fatalf("question lost when the turn was cut off: %+v", got)
	}
}

// A turn that fails takes its question back out, as the client does.
func TestGeneralChatFailedTurn(t *testing.T) {
	s, _, _ := boardServer(t)
	s.Chat = stubChat{err: errors.New("boom")}
	prev := []agent.ChatMessage{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}
	if err := raw(s).SetChat(ownerCaller(t, s).Org, ownerCaller(t, s).User, prev); err != nil {
		t.Fatal(err)
	}
	conn := chatSocket(t, s)
	if err := conn.WriteJSON(map[string]any{"type": "send", "messages": append(prev, agent.ChatMessage{Role: "user", Content: "x"})}); err != nil {
		t.Fatal(err)
	}
	var d agent.ChatDelta
	if err := conn.ReadJSON(&d); err != nil || d.Kind != "error" {
		t.Fatalf("got %+v, %v", d, err)
	}
	waitFor(t, "the question to be dropped", func() bool { return len(raw(s).Chat(ownerCaller(t, s).Org, ownerCaller(t, s).User)) == 2 })
}

// partialChat says "so far" and then waits for its turn to end.
type partialChat struct{}

func (partialChat) Stream(ctx context.Context, _ string, _ []agent.ChatMessage, emit func(agent.ChatDelta)) error {
	emit(agent.ChatDelta{Kind: "text", Text: "so far"})
	<-ctx.Done()
	return ctx.Err()
}

// Stopping a reply keeps what it said so far, instead of leaving the question
// to be asked again.
func TestGeneralChatStopKeepsPartialReply(t *testing.T) {
	s, _, _ := boardServer(t)
	s.Chat = partialChat{}
	conn := chatSocket(t, s)
	if err := conn.WriteJSON(map[string]any{"type": "send", "messages": []agent.ChatMessage{{Role: "user", Content: "q"}}}); err != nil {
		t.Fatal(err)
	}
	var d agent.ChatDelta
	if err := conn.ReadJSON(&d); err != nil || d.Kind != "text" {
		t.Fatalf("got %+v, %v", d, err)
	}
	if err := conn.WriteJSON(map[string]any{"type": "cancel"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the partial reply to be saved", func() bool {
		got := raw(s).Chat(ownerCaller(t, s).Org, ownerCaller(t, s).User)
		return len(got) == 2 && got[1].Role == "assistant" && got[1].Content == "so far"
	})
}

// slowStopChat says "so far", waits for its turn to end and then takes a
// while to return, as a turn on a runner can.
type slowStopChat struct{ returned *atomic.Int32 }

func (c slowStopChat) Stream(ctx context.Context, _ string, _ []agent.ChatMessage, emit func(agent.ChatDelta)) error {
	defer c.returned.Add(1)
	emit(agent.ChatDelta{Kind: "text", Text: "so far"})
	<-ctx.Done()
	time.Sleep(200 * time.Millisecond)
	return ctx.Err()
}

// A question sent right after stopping a reply waits for the stopped turn to
// save, so that save can't overwrite the conversation the new turn saved.
func TestGeneralChatSendAfterStop(t *testing.T) {
	s, _, _ := boardServer(t)
	returned := new(atomic.Int32)
	s.Chat = slowStopChat{returned: returned}
	conn := chatSocket(t, s)
	q1 := agent.ChatMessage{Role: "user", Content: "q1"}
	if err := conn.WriteJSON(map[string]any{"type": "send", "messages": []agent.ChatMessage{q1}}); err != nil {
		t.Fatal(err)
	}
	var d agent.ChatDelta
	if err := conn.ReadJSON(&d); err != nil || d.Kind != "text" {
		t.Fatalf("got %+v, %v", d, err)
	}
	if err := conn.WriteJSON(map[string]any{"type": "cancel"}); err != nil {
		t.Fatal(err)
	}
	msgs := []agent.ChatMessage{q1, {Role: "assistant", Content: "so far"}, {Role: "user", Content: "q2"}}
	if err := conn.WriteJSON(map[string]any{"type": "send", "messages": msgs}); err != nil {
		t.Fatal(err)
	}
	// The second turn has saved its question once it says something (the
	// stopped turn says nothing more), and the stopped one has saved once it
	// returns.
	if err := conn.ReadJSON(&d); err != nil || d.Kind != "text" {
		t.Fatalf("got %+v, %v", d, err)
	}
	waitFor(t, "the stopped turn to return", func() bool { return returned.Load() >= 1 })
	time.Sleep(50 * time.Millisecond) // and to save, were it still to
	got := raw(s).Chat(ownerCaller(t, s).Org, ownerCaller(t, s).User)
	if len(got) != 3 || got[1].Content != "so far" || got[2].Role != "user" || got[2].Content != "q2" {
		t.Fatalf("saved %+v", got)
	}
}

// Stopping a reply before it said anything takes the question back out.
func TestGeneralChatStopBeforeReply(t *testing.T) {
	s, _, _ := boardServer(t)
	s.Chat = stubChat{}
	prev := []agent.ChatMessage{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}
	if err := raw(s).SetChat(ownerCaller(t, s).Org, ownerCaller(t, s).User, prev); err != nil {
		t.Fatal(err)
	}
	conn := chatSocket(t, s)
	if err := conn.WriteJSON(map[string]any{"type": "send", "messages": append(prev, agent.ChatMessage{Role: "user", Content: "x"})}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the busy turn", func() bool { return s.generalBusy(ownerCaller(t, s)) })
	if err := conn.WriteJSON(map[string]any{"type": "cancel"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the question to be dropped", func() bool { return len(raw(s).Chat(ownerCaller(t, s).Org, ownerCaller(t, s).User)) == 2 })
}

// Stopping a task chat's reply keeps what it said so far on the task's
// timeline, after its question.
func TestTodoChatStopKeepsPartialReply(t *testing.T) {
	s, _, p := boardServer(t)
	s.Chat = partialChat{}
	td, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "t", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	conn := chatSocket(t, s)
	if err := conn.WriteJSON(map[string]any{"type": "send", "project": p.ID, "todo": td.ID, "messages": []agent.ChatMessage{{Role: "user", Content: "q"}}}); err != nil {
		t.Fatal(err)
	}
	var d agent.ChatDelta
	if err := conn.ReadJSON(&d); err != nil || d.Kind != "text" {
		t.Fatalf("got %+v, %v", d, err)
	}
	if err := conn.WriteJSON(map[string]any{"type": "cancel"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the partial reply to be saved", func() bool {
		got := raw(s).TodoChat(td.ID)
		return len(got) == 2 && got[0].Role == "user" && got[0].Content == "q" && got[1].Role == "assistant" && got[1].Content == "so far"
	})
}

// Stopping a task chat's reply before it said anything saves nothing.
func TestTodoChatStopBeforeReply(t *testing.T) {
	s, _, p := boardServer(t)
	s.Chat = stubChat{}
	td, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "t", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	conn := chatSocket(t, s)
	if err := conn.WriteJSON(map[string]any{"type": "send", "project": p.ID, "todo": td.ID, "messages": []agent.ChatMessage{{Role: "user", Content: "q"}}}); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(map[string]any{"type": "cancel"}); err != nil {
		t.Fatal(err)
	}
	// The socket handles messages in order and a new turn waits for the
	// stopped one to finish saving: once this general-chat question is
	// saved, the task's turn is over.
	if err := conn.WriteJSON(map[string]any{"type": "send", "messages": []agent.ChatMessage{{Role: "user", Content: "next"}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the next turn", func() bool { return len(raw(s).Chat(ownerCaller(t, s).Org, ownerCaller(t, s).User)) == 1 })
	if got := raw(s).TodoChat(td.ID); len(got) != 0 {
		t.Fatalf("saved %+v", got)
	}
}

// usageChat answers "hi" and reports what the answer took.
type usageChat struct{}

func (usageChat) Stream(_ context.Context, _ string, _ []agent.ChatMessage, emit func(agent.ChatDelta)) error {
	emit(agent.ChatDelta{Kind: "text", Text: "hi"})
	emit(agent.ChatDelta{Kind: "usage", Usage: &model.ChatUsage{InputTokens: 10, OutputTokens: 20, ThinkingMS: 1500, DurationMS: 3000}})
	emit(agent.ChatDelta{Kind: "done"})
	return nil
}

// The answer's tokens and time are saved with it, so the chat shows them
// after a reload.
func TestGeneralChatSavesUsage(t *testing.T) {
	s, _, _ := boardServer(t)
	s.Chat = usageChat{}
	conn := chatSocket(t, s)
	if err := conn.WriteJSON(map[string]any{"type": "send", "messages": []agent.ChatMessage{{Role: "user", Content: "q"}}}); err != nil {
		t.Fatal(err)
	}
	var sawUsage bool
	for {
		var d agent.ChatDelta
		if err := conn.ReadJSON(&d); err != nil {
			t.Fatal(err)
		}
		sawUsage = sawUsage || (d.Kind == "usage" && d.Usage != nil && d.Usage.OutputTokens == 20)
		if d.Kind == "done" {
			break
		}
	}
	if !sawUsage {
		t.Error("the client wasn't sent the usage")
	}
	got := raw(s).Chat(ownerCaller(t, s).Org, ownerCaller(t, s).User)
	if len(got) != 2 || got[1].Usage == nil || got[1].Usage.ThinkingMS != 1500 || got[1].Usage.InputTokens != 10 {
		t.Fatalf("saved %+v", got)
	}
}
