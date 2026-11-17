package gitops

import (
	"fmt"
	"strings"
	"testing"
)

// A tailBuffer keeps the last lines of a long output in bounded memory.
func TestTailBuffer(t *testing.T) {
	t.Parallel()

	tb := &tailBuffer{max: 1000}
	var all strings.Builder
	for i := range 5000 {
		line := fmt.Sprintf("line %d\n", i)
		all.WriteString(line)
		_, _ = tb.Write([]byte(line))
		if cap(tb.buf) > 4*tb.max {
			t.Fatalf("buffer grew to %d bytes", cap(tb.buf))
		}
	}
	want := lastLines(all.String(), 20)
	if got := lastLines(string(tb.Bytes()), 20); got != want {
		t.Errorf("last lines:\n%s\nwant:\n%s", got, want)
	}
	if got := string(tb.Bytes()); !strings.HasPrefix(got, "line ") || len(got) > tb.max {
		t.Errorf("kept %d bytes, not from a line start: %.20q", len(got), got)
	}

	// One write larger than the buffer.
	tb = &tailBuffer{max: 10}
	_, _ = tb.Write([]byte("a\nbcdefghijklmnop\nxyz"))
	if got := string(tb.Bytes()); got != "xyz" {
		t.Errorf("big write kept %q, want %q", got, "xyz")
	}

	// Short output is kept whole.
	tb = &tailBuffer{max: 100}
	_, _ = tb.Write([]byte("one\ntwo\n"))
	if got := string(tb.Bytes()); got != "one\ntwo\n" {
		t.Errorf("short output: %q", got)
	}
}
