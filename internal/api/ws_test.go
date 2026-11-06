package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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
	if got[1] != history[1] {
		t.Errorf("later turns changed: %v", got[1])
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
