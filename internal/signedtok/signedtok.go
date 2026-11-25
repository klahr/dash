// Package signedtok makes and checks HMAC-signed tokens:
// base64url(payload) "." base64url(HMAC-SHA256(key, purpose "." payload)).
//
// The payload is readable by anyone holding the token; the MAC only proves
// it was made with key for purpose. Callers keep their own payload format,
// expiry and parsing.
package signedtok

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// ErrInvalid covers malformed, tampered and wrongly-purposed tokens, and
// any token checked against an empty key.
var ErrInvalid = errors.New("invalid signed token")

var enc = base64.RawURLEncoding

func mac(key []byte, purpose, payload string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(purpose + "." + payload))
	return m.Sum(nil)
}

// Seal signs payload with key for purpose. A token sealed for one purpose
// doesn't open for another.
func Seal(key []byte, purpose, payload string) string {
	return enc.EncodeToString([]byte(payload)) + "." + enc.EncodeToString(mac(key, purpose, payload))
}

// Open checks tok against key and purpose and returns its payload. It
// refuses every token when key is empty.
func Open(key []byte, purpose, tok string) (string, error) {
	if len(key) == 0 {
		return "", ErrInvalid
	}
	p64, s64, ok := strings.Cut(tok, ".")
	if !ok {
		return "", ErrInvalid
	}
	payload, err1 := enc.DecodeString(p64)
	sig, err2 := enc.DecodeString(s64)
	if err1 != nil || err2 != nil || !hmac.Equal(sig, mac(key, purpose, string(payload))) {
		return "", ErrInvalid
	}
	return string(payload), nil
}
