package totp

import (
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B, SHA-1, truncated to 6 digits.
func TestRFCVectors(t *testing.T) {
	secret := enc.EncodeToString([]byte("12345678901234567890"))
	for unix, want := range map[int64]string{
		59:          "287082",
		1111111109:  "081804",
		1111111111:  "050471",
		1234567890:  "005924",
		2000000000:  "279037",
		20000000000: "353130",
	} {
		got, err := Code(secret, time.Unix(unix, 0))
		if err != nil || got != want {
			t.Errorf("Code at %d = %q, %v; want %q", unix, got, err, want)
		}
	}
}

func TestVerify(t *testing.T) {
	s := NewSecret()
	now := time.Unix(1_700_000_000, 0)
	c, _ := Code(s, now)
	if step, ok := Verify(s, c[:3]+" "+c[3:], now); !ok || step != Step(now) {
		t.Fatalf("own code: %d %v", step, ok)
	}
	if _, ok := Verify(s, c, now.Add(Period*time.Second)); !ok {
		t.Fatal("code from the previous period should pass")
	}
	if _, ok := Verify(s, c, now.Add(3*Period*time.Second)); ok {
		t.Fatal("code from three periods ago should fail")
	}
	if _, ok := Verify(s, "abcdef", now); ok {
		t.Fatal("non-digits passed")
	}
}

func TestURI(t *testing.T) {
	u := URI("Dash", "ada@example.com", "ABC")
	if !strings.HasPrefix(u, "otpauth://totp/Dash:ada@example.com?") || !strings.Contains(u, "secret=ABC") || !strings.Contains(u, "issuer=Dash") {
		t.Fatal(u)
	}
}
