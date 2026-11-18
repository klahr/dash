package textutil

import "testing"

func TestNoMentions(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"no handles here", "no handles here"},
		{"@claude fix it", "`@claude` fix it"},
		{"ask @dash or @claude.", "ask `@dash` or `@claude`."},
		{"(@someone), @a-b-c!", "(`@someone`), `@a-b-c`!"},
		{"line one\n@claude on line two", "line one\n`@claude` on line two"},
		{"install @types/node", "install `@types/node`"},
		{"cc @org/team-x.", "cc `@org/team-x`."},
		{"@claude-", "`@claude`-"},
		// Left alone.
		{"mail a@example.com", "mail a@example.com"},
		{"first.last+tag@example.dev", "first.last+tag@example.dev"},
		{"https://example.com/@someone", "https://example.com/@someone"},
		{"already `@claude` quoted", "already `@claude` quoted"},
		{"span ``x @claude y`` here", "span ``x @claude y`` here"},
		{"a lone @ sign, @-1", "a lone @ sign, @-1"},
		{"x@", "x@"},
		// An unclosed backtick is literal: what follows is still text.
		{"a ` @claude", "a ` `@claude`"},
		// Fenced code blocks.
		{"```\n@claude\n```\n@dash", "```\n@claude\n```\n`@dash`"},
		{"~~~go\nx := \"@claude\"\n~~~\n", "~~~go\nx := \"@claude\"\n~~~\n"},
		{"````\n```\n@claude\n````\n@x", "````\n```\n@claude\n````\n`@x`"},
		{"```\nunclosed @claude", "```\nunclosed @claude"},
	} {
		if got := NoMentions(c.in); got != c.want {
			t.Errorf("NoMentions(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
