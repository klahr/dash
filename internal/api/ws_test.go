package api

import (
	"strings"
	"testing"

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
