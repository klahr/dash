package notify

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ActionPath is where ntfy's Approve/Deny buttons post: ActionPath+<token>.
// The signed token is the request's only credential, so the path is
// public (auth lets it through without a session).
const ActionPath = "/api/notify/permission/"

// DefaultActionTTL is how long a notification's buttons work.
const DefaultActionTTL = 15 * time.Minute

// ErrActionToken covers unknown, tampered and expired action tokens.
var ErrActionToken = errors.New("this button has expired or isn't valid")

// Actions signs the short-lived tokens behind ntfy's Approve/Deny buttons
// for permission requests, and answers the requests they make. Its key
// lives in memory only: a restart ends the buttons, as it ends the runs
// waiting for an answer.
type Actions struct {
	// Decide answers a permission request: "allow" or "deny".
	Decide func(id, decision, message string) error
	TTL    time.Duration // DefaultActionTTL
	Now    func() time.Time
	key    []byte
}

// NewActions makes Actions with a fresh random key.
func NewActions(decide func(id, decision, message string) error) *Actions {
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return &Actions{Decide: decide, key: k}
}

func (a *Actions) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Actions) sig(msg string) []byte {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}

// Token lets its bearer give decision ("allow" or "deny") on permission
// request id until exp.
func (a *Actions) Token(id, decision string, exp time.Time) string {
	msg := id + "." + decision + "." + strconv.FormatInt(exp.Unix(), 10)
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(msg)) + "." + enc.EncodeToString(a.sig(msg))
}

// Parse checks a token and returns what it allows.
func (a *Actions) Parse(tok string) (id, decision string, err error) {
	enc := base64.RawURLEncoding
	m, s, ok := strings.Cut(tok, ".")
	if !ok {
		return "", "", ErrActionToken
	}
	msg, err1 := enc.DecodeString(m)
	sig, err2 := enc.DecodeString(s)
	if err1 != nil || err2 != nil || !hmac.Equal(sig, a.sig(string(msg))) {
		return "", "", ErrActionToken
	}
	parts := strings.Split(string(msg), ".")
	if len(parts) != 3 || (parts[1] != "allow" && parts[1] != "deny") {
		return "", "", ErrActionToken
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || a.now().After(time.Unix(exp, 0)) {
		return "", "", ErrActionToken
	}
	return parts[0], parts[1], nil
}

// buttons are the Approve and Deny buttons for permission request id.
func (a *Actions) buttons(origin, id string, now time.Time) []ntfyAction {
	ttl := a.TTL
	if ttl <= 0 {
		ttl = DefaultActionTTL
	}
	exp := now.Add(ttl)
	return []ntfyAction{
		{Label: "Approve", URL: origin + ActionPath + a.Token(id, "allow", exp)},
		{Label: "Deny", URL: origin + ActionPath + a.Token(id, "deny", exp)},
	}
}

// ServeHTTP handles POST ActionPath{token}: a button was pressed.
func (a *Actions) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, decision, err := a.Parse(strings.TrimPrefix(r.URL.Path, ActionPath))
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if err := a.Decide(id, decision, "answered from a notification"); err != nil {
		// Most likely already answered, or the run ended.
		slog.Info("notification action", "permission", id, "decision", decision, "err", err)
		http.Error(w, "this request was already answered or has ended", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if decision == "allow" {
		_, _ = w.Write([]byte("Approved\n"))
	} else {
		_, _ = w.Write([]byte("Denied\n"))
	}
}
