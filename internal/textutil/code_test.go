package textutil

import "testing"

func TestWithoutCode(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"no code /dash here", "no code /dash here"},
		{"run `/dash` now", "run   now"},
		{"span ``x ` /dash`` here", "span   here"},
		{"a ` lone tick /dash", "a ` lone tick /dash"},
		{"before\n```\n/dash\n```\nafter", "before\n after"},
		{"~~~go\n/dash\n~~~\n/claude", " /claude"},
		{"```\nunclosed /dash", " "},
	} {
		if got := WithoutCode(c.in); got != c.want {
			t.Errorf("WithoutCode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
