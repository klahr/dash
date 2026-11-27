package agent

import (
	"fmt"
	"strings"

	"github.com/klahr/dash/internal/model"
)

// ReviewNote is the rework note for a reviewer's comments on lines of a
// task's diff, and their note for the whole of it (either may be empty):
// each comment with its file, its line numbers and the lines it quotes, so
// Claude knows exactly where it applies.
func ReviewNote(comments []model.ReviewComment, note string) string {
	note = strings.TrimSpace(note)
	var b strings.Builder
	n := 0
	for _, c := range comments {
		text := strings.TrimSpace(c.Text)
		if text == "" {
			continue
		}
		if n == 0 {
			b.WriteString("Review comments on your changes. Each names the file and lines it is about and quotes them as the diff shows them (+ added, - removed):\n")
		}
		n++
		fmt.Fprintf(&b, "\n### %d. %s, %s\n\n", n, c.File, CommentLines(c))
		if code := strings.TrimRight(c.Code, "\n"); code != "" {
			fence := CodeFence(code)
			fmt.Fprintf(&b, "%sdiff\n%s\n%s\n\n", fence, code, fence)
		}
		b.WriteString(text)
		b.WriteString("\n")
	}
	switch {
	case n == 0:
		return note
	case note != "":
		fmt.Fprintf(&b, "\n### Also\n\n%s\n", note)
	}
	return strings.TrimRight(b.String(), "\n")
}

// CommentLines says which lines comment c is on: "line 12", "lines 12–14",
// or for removed lines "removed lines 7–9 (as numbered before the change)".
func CommentLines(c model.ReviewComment) string {
	s := fmt.Sprintf("line %d", c.Line)
	if c.EndLine > c.Line {
		s = fmt.Sprintf("lines %d–%d", c.Line, c.EndLine)
	}
	if c.Side == "old" {
		return "removed " + s + " (as numbered before the change)"
	}
	return s
}

// CodeFence is a backtick fence longer than any run of backticks in code,
// so the quoted lines can't close it early.
func CodeFence(code string) string {
	longest, run := 0, 0
	for _, r := range code {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}
