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

func TestMentioned(t *testing.T) {
	for _, c := range []struct {
		text, name string
		want       bool
	}{
		{"@pia can you look?", "pia", true},
		{"Thanks @Pia.", "pia", true},
		{"(@pia)", "pia", true},
		{"@pia's turn", "pia", true},
		{"ask @dev, then @pia", "dev", true},
		{"@pia2 not her", "pia", false},
		{"@pia_x not her", "pia", false},
		{"mail pia@example.com", "example.com", false},
		{"mail x@pia", "pia", false},
		{"https://example.com/@pia", "pia", false},
		{"@ana.berg here", "ana.berg", true},
		{"pia", "pia", false},
		{"@pia", "", false},
		{"@x @pia", "pia", true},
	} {
		if got := Mentioned(c.text, c.name); got != c.want {
			t.Errorf("Mentioned(%q, %q) = %v, want %v", c.text, c.name, got, c.want)
		}
	}
}
