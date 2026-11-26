package runtoken

import "testing"

func TestMap(t *testing.T) {
	var m Map[string]
	a, releaseA := m.Register("a")
	b, releaseB := m.Register("b")
	if a == "" || a == b {
		t.Fatalf("tokens %q, %q: want two distinct tokens", a, b)
	}
	if v, ok := m.Lookup(a); !ok || v != "a" {
		t.Fatalf("Lookup(a) = %q, %v", v, ok)
	}
	releaseA()
	if _, ok := m.Lookup(a); ok {
		t.Fatal("token still known after release")
	}
	if v, ok := m.Lookup(b); !ok || v != "b" {
		t.Fatalf("Lookup(b) = %q, %v", v, ok)
	}
	releaseB()
	if _, ok := m.Lookup(""); ok {
		t.Fatal("empty token found")
	}
}
