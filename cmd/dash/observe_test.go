package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNewLogger(t *testing.T) {
	var b bytes.Buffer
	l, err := newLogger(&b, "json", "warn")
	if err != nil {
		t.Fatal(err)
	}
	l.Info("hidden")
	l.Warn("shown", "n", 1)
	var rec map[string]any
	if err := json.Unmarshal(b.Bytes(), &rec); err != nil {
		t.Fatalf("not one JSON object: %q: %v", b.String(), err)
	}
	if rec["msg"] != "shown" || rec["level"] != "WARN" || rec["n"] != float64(1) {
		t.Errorf("record = %v", rec)
	}

	b.Reset()
	if l, err = newLogger(&b, "", ""); err != nil {
		t.Fatal(err)
	}
	l.Info("hello", "k", "v")
	if !strings.Contains(b.String(), "level=INFO msg=hello k=v") {
		t.Errorf("text = %q", b.String())
	}

	if _, err := newLogger(&b, "xml", ""); err == nil {
		t.Error("xml accepted")
	}
	if _, err := newLogger(&b, "", "loud"); err == nil {
		t.Error("level loud accepted")
	}
}

func TestCheckMetrics(t *testing.T) {
	long := strings.Repeat("x", minMetricsToken)
	for _, c := range []struct {
		addr, token string
		ok          bool
	}{
		{"", "", true},
		{"127.0.0.1:9090", "", true},
		{"localhost:9090", "", true},
		{"0.0.0.0:9090", "", false},
		{":9090", "", false},
		{"0.0.0.0:9090", long, true},
		{"", "short", false},
		{"nonsense", long, false},
	} {
		if err := checkMetrics(c.addr, c.token); (err == nil) != c.ok {
			t.Errorf("checkMetrics(%q, %q) = %v", c.addr, c.token, err)
		}
	}
}
