// Package totp implements time-based one-time passwords (RFC 6238) as
// authenticator apps make them: HMAC-SHA1, 6 digits, a new code every 30
// seconds.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	Period = 30 // seconds per code
	Digits = 6
	// Skew is how many periods either side of now are accepted, for clocks
	// that are a little off and codes typed just as they change.
	Skew = 1
)

var enc = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a random 160-bit secret, base32 as apps expect it.
func NewSecret() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return enc.EncodeToString(b)
}

// Step is the period t falls in.
func Step(t time.Time) int64 { return t.Unix() / Period }

// code is the code for secret at step.
func code(key []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	n := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", Digits, n%1_000_000)
}

func decode(secret string) ([]byte, error) {
	return enc.DecodeString(strings.ToUpper(strings.TrimRight(strings.ReplaceAll(secret, " ", ""), "=")))
}

// Code is the code for secret at t, as an authenticator app would show it.
//
// Production code checks codes with Verify and never needs to make one; Code
// is exported for tests (here and in internal/auth) that must type a valid
// code. It lives outside a _test.go file because test files aren't visible
// to other packages' tests.
func Code(secret string, t time.Time) (string, error) {
	key, err := decode(secret)
	if err != nil {
		return "", err
	}
	return code(key, Step(t)), nil
}

// Clean strips the spaces and dashes people type in a code.
func Clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '\t' {
			return -1
		}
		return r
	}, s)
}

// LooksLikeCode reports whether s (cleaned) is the shape of a TOTP code
// rather than, say, a recovery code.
func LooksLikeCode(s string) bool {
	if len(s) != Digits {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Verify checks code against secret at t, allowing Skew periods either side,
// and returns the step it matched. Callers must refuse a step at or before
// the last one used, so a code can't be replayed.
func Verify(secret, c string, t time.Time) (int64, bool) {
	c = Clean(c)
	key, err := decode(secret)
	if err != nil || !LooksLikeCode(c) {
		return 0, false
	}
	now := Step(t)
	var matched int64
	found := false
	for s := now - Skew; s <= now+Skew; s++ { // every step, matched or not: same time either way
		if subtle.ConstantTimeCompare([]byte(code(key, s)), []byte(c)) == 1 && !found {
			matched, found = s, true
		}
	}
	return matched, found
}

// URI is the otpauth:// link an authenticator app reads from the QR code.
func URI(issuer, account, secret string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", fmt.Sprint(Digits))
	v.Set("period", fmt.Sprint(Period))
	return "otpauth://totp/" + label + "?" + v.Encode()
}
