package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SignatureHeaderName carries the signature of a generic JSON delivery, in
// the scheme Stripe uses (internal/billing/webhook.go verifies Stripe's):
//
//	Dash-Signature: t=<unix seconds>,v1=<hex HMAC-SHA256 of "<t>.<body>">
//
// A receiver recomputes the HMAC with the webhook's secret, compares it in
// constant time and refuses timestamps far from now (replays).
const SignatureHeaderName = "Dash-Signature"

// ErrSignature is returned by Verify for a missing, malformed, wrong or
// stale signature.
var ErrSignature = errors.New("bad " + SignatureHeaderName)

func mac(body []byte, secret string, ts int64) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(m, "%d.", ts)
	m.Write(body)
	return m.Sum(nil)
}

// Sign is the Dash-Signature header value for body sent at t.
func Sign(body []byte, secret string, t time.Time) string {
	return fmt.Sprintf("t=%d,v1=%s", t.Unix(), hex.EncodeToString(mac(body, secret, t.Unix())))
}

// Verify checks a Dash-Signature header against body: what a receiver
// does (and the tests).
func Verify(body []byte, header, secret string, now time.Time, tolerance time.Duration) error {
	var ts int64
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts, _ = strconv.ParseInt(v, 10, 64)
		case "v1":
			sigs = append(sigs, v)
		}
	}
	if ts == 0 || len(sigs) == 0 {
		return ErrSignature
	}
	if d := now.Sub(time.Unix(ts, 0)); d > tolerance || d < -tolerance {
		return fmt.Errorf("%w: timestamp too far from now", ErrSignature)
	}
	want := mac(body, secret, ts)
	for _, s := range sigs {
		if got, err := hex.DecodeString(s); err == nil && hmac.Equal(got, want) {
			return nil
		}
	}
	return ErrSignature
}
