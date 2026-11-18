package tui

import "testing"

func TestListKey(t *testing.T) {
	for _, c := range []struct {
		s              string
		n, row, length int
		want           int
		wantHandled    bool
	}{
		{"j", 1, 0, 3, 1, true},
		{"down", 5, 1, 3, 2, true},
		{"k", 1, 2, 3, 1, true},
		{"up", 5, 1, 3, 0, true},
		{"G", 1, 0, 3, 2, true},
		{"G", 1, 0, 0, 0, true}, // empty list: never -1
		{"j", 1, 0, 0, 0, true},
		{"k", 1, 0, 0, 0, true},
		{"x", 1, 2, 3, 2, false},
	} {
		got, ok := listKey(c.s, c.n, c.row, c.length)
		if got != c.want || ok != c.wantHandled {
			t.Errorf("listKey(%q, %d, %d, %d) = %d, %v; want %d, %v", c.s, c.n, c.row, c.length, got, ok, c.want, c.wantHandled)
		}
	}
}

func TestScrollKeyFromBottom(t *testing.T) {
	for _, c := range []struct {
		s          string
		off        int
		fromBottom bool
		want       int
	}{
		{"j", 5, false, 6},
		{"k", 0, false, 0},
		{"ctrl+f", 0, false, 10},
		{"G", 5, false, 1 << 30},
		{"j", 5, true, 4},
		{"j", 0, true, 0},
		{"k", 5, true, 6},
		{"ctrl+d", 10, true, 5},
		{"ctrl+u", 0, true, 5},
		{"ctrl+b", 0, true, 10},
		{"space", 15, true, 5},
		{"G", 5, true, 0},
		{"x", 5, true, 5},
	} {
		if got := scrollKey(c.s, 1, c.off, 10, c.fromBottom); got != c.want {
			t.Errorf("scrollKey(%q, off %d, fromBottom %v) = %d; want %d", c.s, c.off, c.fromBottom, got, c.want)
		}
	}
}
