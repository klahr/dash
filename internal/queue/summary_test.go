package queue

import "testing"

func TestFirstSentence(t *testing.T) {
	for in, want := range map[string]string{
		"Fixed the bug. Also ran tests.": "Fixed the bug.",
		"Added v1.2\nsupport":            "Added v1.2 support",
		"## Done\n\nAll good! Next":      "## Done All good!",
	} {
		if got := firstSentence(in); got != want {
			t.Errorf("firstSentence(%q) = %q; want %q", in, got, want)
		}
	}
}
