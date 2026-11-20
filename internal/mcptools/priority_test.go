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

// The checklist move's body takes a level too, and still sends a number.
func TestChecklistMovePriority(t *testing.T) {
	var m ChecklistMove
	if err := json.Unmarshal([]byte(`{"ref":"r","queue":true,"priority":"urgent","depends_on":["a"]}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Ref != "r" || !m.Queue || m.Priority == nil || *m.Priority != -1 || len(m.DependsOn) != 1 {
		t.Errorf("decoded %+v", m)
	}
	b, _ := json.Marshal(m)
	var back ChecklistMove
	if err := json.Unmarshal(b, &back); err != nil || back.Priority == nil || *back.Priority != -1 {
		t.Errorf("round trip %s: %+v %v", b, back, err)
	}
	if err := json.Unmarshal([]byte(`{"ref":"r","priority":"later"}`), &m); err == nil {
		t.Error("unknown level accepted")
	}
}
