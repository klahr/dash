package signedtok

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestSealOpen(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	tok := Seal(key, "p", "a.b|c")
	if got, err := Open(key, "p", tok); err != nil || got != "a.b|c" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	bad := map[string]func() (string, error){
		"other purpose": func() (string, error) { return Open(key, "q", tok) },
		"other key":     func() (string, error) { return Open([]byte("another key"), "p", tok) },
		"empty key":     func() (string, error) { return Open(nil, "p", Seal(nil, "p", "x")) },
		"no dot":        func() (string, error) { return Open(key, "p", "abc") },
		"bad base64":    func() (string, error) { return Open(key, "p", "!!."+tok) },
		"tampered":      func() (string, error) { return Open(key, "p", Seal(key, "p", "x")[:2]+tok[2:]) },
		"empty":         func() (string, error) { return Open(key, "p", "") },
	}
	for name, f := range bad {
		if got, err := f(); err != ErrInvalid {
			t.Errorf("%s: Open = %q, %v; want ErrInvalid", name, got, err)
		}
	}
}

// The MAC input is purpose "." payload: tokens already issued (unsubscribe
// links, which never expire) depend on it staying so.
func TestFormat(t *testing.T) {
	key := []byte("k")
	m := hmac.New(sha256.New, key)
	m.Write([]byte("unsubscribe.1.2"))
	want := base64.RawURLEncoding.EncodeToString([]byte("1.2")) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	if got := Seal(key, "unsubscribe", "1.2"); got != want {
		t.Fatalf("Seal = %q, want %q", got, want)
	}
}
