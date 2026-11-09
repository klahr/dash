package localpath

import "testing"

func TestSafe(t *testing.T) {
	ok := []string{"/", "/a", "/a/b?x=1&y=%2F%2F#frag", "/tasks/123", "/a%09b"}
	for _, s := range ok {
		if got := Safe(s); got != s {
			t.Errorf("Safe(%q) = %q, want unchanged", s, got)
		}
	}
	bad := []string{
		"", "a", "http://evil.com", "//evil.com", "/\\evil.com", "/\t/evil.com", "/\n/evil.com",
		"/\r/evil.com", "\t//evil.com", "/a\\b", "/x\x7f", "/x\x00", "https:/evil.com",
	}
	for _, s := range bad {
		if got := Safe(s); got != "/" {
			t.Errorf("Safe(%q) = %q, want /", s, got)
		}
	}
}
