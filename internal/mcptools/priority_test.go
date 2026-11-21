package mcptools

import (
	"encoding/json"
	"testing"
)

func TestParsePriority(t *testing.T) {
	for _, c := range []struct {
		in   any
		want string // "none", a number, or "error"
	}{
		{nil, "none"}, {"none", "none"}, {"", "none"}, {"high", "0"}, {" Urgent ", "-1"},
		{float64(3), "3"}, {float64(-2), "-2"}, {"7", "7"},
		{1.5, "error"}, {"soon", "error"}, {true, "error"}, {float64(1 << 40), "error"},
	} {
		n, err := ParsePriority(c.in)
		got := "none"
		switch {
		case err != nil:
			got = "error"
		case n != nil:
			b, _ := json.Marshal(*n)
			got = string(b)
		}
		if got != c.want {
			t.Errorf("ParsePriority(%#v) = %s (%v), want %s", c.in, got, err, c.want)
		}
	}
}
