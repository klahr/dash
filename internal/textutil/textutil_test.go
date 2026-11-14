package textutil

import (
	"testing"
	"unicode/utf8"
)

func TestClip(t *testing.T) {
	for _, c := range []struct {
		s    string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly", 7, "exactly"},
		{"abcdef", 3, "abc…"},
		{"åäö", 3, "å…"}, // byte 3 is inside "ä"
		{"åäö", 4, "åä…"},
		{"x", 0, "…"},
	} {
		got := Clip(c.s, c.n)
		if got != c.want {
			t.Errorf("Clip(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("Clip(%q, %d) = %q is not valid UTF-8", c.s, c.n, got)
		}
	}
}

func TestFirstLine(t *testing.T) {
	for s, want := range map[string]string{
		"":                  "",
		"  one  ":           "one",
		"\n one \n two\n":   "one",
		"one\r\ntwo":        "one",
		"one line, no more": "one line, no more",
	} {
		if got := FirstLine(s); got != want {
			t.Errorf("FirstLine(%q) = %q, want %q", s, got, want)
		}
	}
}

func TestSummary(t *testing.T) {
	if got := Summary("ls -la\necho hi", 200); got != "ls -la …" {
		t.Errorf("Summary = %q", got)
	}
	if got := Summary("abcdef", 3); got != "abc…" {
		t.Errorf("Summary = %q", got)
	}
}

func TestOneLine(t *testing.T) {
	for _, c := range []struct {
		s    string
		n    int
		want string
	}{
		{"", 5, ""},
		{"  one \n two\t three ", 20, "one two three"},
		{"abcdef", 6, "abcdef"},
		{"abcdef", 5, "abcd…"},
		{"ab cdef", 4, "ab…"},
		{"åäöåäö", 4, "åäö…"},
		{"abc", 0, ""},
	} {
		if got := OneLine(c.s, c.n); got != c.want {
			t.Errorf("OneLine(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
		}
	}
}
