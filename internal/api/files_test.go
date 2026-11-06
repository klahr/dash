package api

import "testing"

func TestCleanRepoPath(t *testing.T) {
	for in, want := range map[string]string{"a.go": "a.go", "./web/x.js": "web/x.js", "a/../b": "b"} {
		if got, err := cleanRepoPath(in); err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", ".", "..", "../x", "a/../../x", "/etc/passwd"} {
		if got, err := cleanRepoPath(in); err == nil {
			t.Errorf("%q: got %q, want an error", in, got)
		}
	}
}
