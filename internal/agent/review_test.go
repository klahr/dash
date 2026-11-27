package agent

import (
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

func TestReviewNote(t *testing.T) {
	comments := []model.ReviewComment{
		{File: "web/app.js", Side: "new", Line: 12, Code: "+const x = 1;", Text: "Make this a constant at the top."},
		{File: "main.go", Side: "new", Line: 3, EndLine: 5, Code: " a\n-b\n+c\n", Text: "  Why this change?  "},
		{File: "old.go", Side: "old", Line: 7, Code: "-gone()", Text: "Keep this call."},
		{File: "skip.go", Side: "new", Line: 1, Code: "+x", Text: "   "}, // empty: left out
	}
	got := ReviewNote(comments, "  And run the tests. ")
	want := "Review comments on your changes. Each names the file and lines it is about and quotes them as the diff shows them (+ added, - removed):\n" +
		"\n### 1. web/app.js, line 12\n\n```diff\n+const x = 1;\n```\n\nMake this a constant at the top.\n" +
		"\n### 2. main.go, lines 3–5\n\n```diff\n a\n-b\n+c\n```\n\nWhy this change?\n" +
		"\n### 3. old.go, removed line 7 (as numbered before the change)\n\n```diff\n-gone()\n```\n\nKeep this call.\n" +
		"\n### Also\n\nAnd run the tests."
	if got != want {
		t.Errorf("ReviewNote:\n%s\n\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "skip.go") {
		t.Error("an empty comment was sent")
	}
}

func TestReviewNoteNoteOnly(t *testing.T) {
	if got := ReviewNote(nil, " just this "); got != "just this" {
		t.Errorf("note only: %q", got)
	}
	if got := ReviewNote(nil, ""); got != "" {
		t.Errorf("nothing: %q", got)
	}
	got := ReviewNote([]model.ReviewComment{{File: "a", Side: "old", Line: 2, EndLine: 4, Text: "x"}}, "")
	if !strings.Contains(got, "removed lines 2–4") || strings.Contains(got, "Also") || strings.Contains(got, "```") {
		t.Errorf("a comment without code or note: %q", got)
	}
}

// Quoted lines with backticks get a longer fence, so they can't end it.
func TestReviewNoteFence(t *testing.T) {
	got := ReviewNote([]model.ReviewComment{{File: "README.md", Side: "new", Line: 1, Code: "+```sh\n+make\n+```", Text: "Use go build."}}, "")
	if !strings.Contains(got, "````diff\n+```sh\n+make\n+```\n````") {
		t.Errorf("fence:\n%s", got)
	}
}
