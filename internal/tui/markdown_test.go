package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderMarkdown(t *testing.T) {
	in := "# Title\nSome **bold**, *it* and `co**de**`.\n- [x] done\n1. one\n> quote\n```go\n  x := 1\n```\n[docs](https://x.dev)"
	got := ansi.Strip(strings.Join(renderMarkdown(in, 80), "\n"))
	for _, want := range []string{"Title", "Some bold, it and co**de**.", "• ☑ done", "1. one", "│ quote", "│   x := 1", "docs (https://x.dev)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, gone := range []string{"# Title", "```", "**bold**"} {
		if strings.Contains(got, gone) {
			t.Errorf("unexpected %q in:\n%s", gone, got)
		}
	}
}
