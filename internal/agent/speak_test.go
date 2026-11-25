package agent

import (
	"context"
	"strings"
	"testing"
)

func TestCleanSpoken(t *testing.T) {
	for in, want := range map[string]string{
		"Claude fixed the login bug.":                                     "Claude fixed the login bug.",
		"\"**Claude** updated `internal/agent/title.go`.\"":               "Claude updated title.go.",
		"- Claude ran the tests.\n- They pass.":                           "Claude ran the tests. They pass.",
		"See [the docs](https://example.com/docs) or https://example.dev": "See the docs or a link",
		"Claude wrote:\n```go\nfunc x() {}\n```\nOkay?":                   "Claude wrote: Okay?",
	} {
		got, err := CleanSpoken(in)
		if err != nil || got != want {
			t.Errorf("CleanSpoken(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := CleanSpoken(" ``` ``` "); err == nil {
		t.Error("nothing left: want an error")
	}
	long, _ := CleanSpoken(strings.Repeat("Claude did one more thing. ", 30))
	if len(long) > maxSpoken || !strings.HasSuffix(long, ".") {
		t.Errorf("long summary not cut at a sentence: %d %q", len(long), long)
	}
}

// Speak asks the small model with the kind's prompt, and refuses an
// unknown kind without asking.
func TestSpeak(t *testing.T) {
	var asked []PromptRequest
	tl := Titler{Prompter: bareProvider{reply: "Claude wants to run the tests, okay?", asked: &asked}}
	got, err := tl.Speak(context.Background(), SpeakApproval, "Bash: go test ./internal/agent")
	if err != nil || got != "Claude wants to run the tests, okay?" {
		t.Fatalf("Speak = %q, %v", got, err)
	}
	if _, err := tl.Speak(context.Background(), "poem", "x"); err == nil {
		t.Error("unknown kind: want an error")
	}
	if len(asked) != 1 || !strings.Contains(asked[0].Input, "permission request") || !strings.Contains(asked[0].Input, "go test ./internal/agent") {
		t.Errorf("asked %+v, want once, with the approval prompt and the text", asked)
	}
}
