// Package auth provides single-account login for Dash: one username and
// password from the environment (.env), and server-side cookie sessions.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	CookieName = "dash_session"
	sessionTTL = 7 * 24 * time.Hour

	maxFailures   = 5                // per client IP …
	failureWindow = 15 * time.Minute // … within this window, then locked out until it passes
)

type session struct {
	Expires time.Time `json:"expires"`
}

// sessionKey is what's kept (and saved) for a session: a hash, so the
// sessions file can't be used to log in.
func sessionKey(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// KeepSessions saves sessions in path, so restarting or updating Dash
// doesn't log everyone out. Sessions from other credentials are dropped.
func (a *Auth) KeepSessions(path string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.store = path
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f sessionFile
	if json.Unmarshal(b, &f) != nil || f.Account != a.account() {
		return nil
	}
	now := a.now()
	for k, s := range f.Sessions {
		if now.Before(s.Expires) {
			a.sessions[k] = s
		}
	}
	return nil
}

type sessionFile struct {
	Account  string             `json:"account"` // hash of the credentials the sessions belong to
	Sessions map[string]session `json:"sessions"`
}

func (a *Auth) account() string {
	h := sha256.Sum256(append(a.user[:], a.pass[:]...))
	return hex.EncodeToString(h[:])
}

func (a *Auth) saveLocked() {
	if a.store == "" {
		return
	}
	b, _ := json.Marshal(sessionFile{Account: a.account(), Sessions: a.sessions})
	tmp := a.store + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, a.store)
	}
}

type failures struct {
	count int
	first time.Time
}

type Auth struct {
	user [32]byte // sha256 digests, so comparisons are constant-time and length-independent
	pass [32]byte
	name string // shown in the dashboard's account menu

	mu       sync.Mutex
	sessions map[string]session
	failed   map[string]*failures

	now   func() time.Time // for tests
	store string           // sessions file, if any

	// TrustProxy honours X-Forwarded-For and X-Forwarded-Proto from a
	// reverse proxy (Caddy, nginx, Tailscale Serve) that terminates TLS.
	// Only enable it when Dash is reachable solely through that proxy.
	TrustProxy bool

	// PeerToken lets another Dash (a "hub" showing this machine in its
	// dashboard) call the API with "Authorization: Bearer <token>".
	PeerToken string

	oauth *OAuth // set by NewOAuth
}

// Username is the account's name, for display.
func (a *Auth) Username() string { return a.name }

// New returns an authenticator for the single account. Both values are required.
func New(username, password string) (*Auth, error) {
	if username == "" || password == "" {
		return nil, errors.New("DASH_USERNAME and DASH_PASSWORD must be set (see .env.example)")
	}
	return &Auth{
		user:     sha256.Sum256([]byte(username)),
		name:     username,
		pass:     sha256.Sum256([]byte(password)),
		sessions: map[string]session{},
		failed:   map[string]*failures{},
		now:      time.Now,
	}, nil
}

func (a *Auth) check(username, password string) bool {
	u := sha256.Sum256([]byte(username))
	p := sha256.Sum256([]byte(password))
	// Evaluate both so timing doesn't reveal which one was wrong.
	uok := subtle.ConstantTimeCompare(u[:], a.user[:])
	pok := subtle.ConstantTimeCompare(p[:], a.pass[:])
	return uok&pok == 1
}

func (a *Auth) valid(r *http.Request) bool {
	if a.PeerToken != "" {
		if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			return subtle.ConstantTimeCompare([]byte(tok), []byte(a.PeerToken)) == 1
		}
	}
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := sessionKey(c.Value)
	s, ok := a.sessions[key]
	if !ok {
		return false
	}
	if a.now().After(s.Expires) {
		delete(a.sessions, key)
		return false
	}
	return true
}

func (a *Auth) newSession() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for k, s := range a.sessions { // prune
		if now.After(s.Expires) {
			delete(a.sessions, k)
		}
	}
	a.sessions[sessionKey(tok)] = session{Expires: now.Add(sessionTTL)}
	a.saveLocked()
	return tok
}

// lockedOut reports whether ip has too many recent failed logins.
func (a *Auth) lockedOut(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, ok := a.failed[ip]
	if !ok {
		return false
	}
	if a.now().Sub(f.first) > failureWindow {
		delete(a.failed, ip)
		return false
	}
	return f.count >= maxFailures
}

func (a *Auth) recordFailure(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, ok := a.failed[ip]
	if !ok || a.now().Sub(f.first) > failureWindow {
		f = &failures{first: a.now()}
		a.failed[ip] = f
	}
	f.count++
}

var (
	errLockedOut = errors.New("too many failed attempts, try again later")
	errBadLogin  = errors.New("invalid username or password")
)

// attempt checks a login from ip, counting failures towards its lockout.
func (a *Auth) attempt(ip, username, password string) error {
	if a.lockedOut(ip) {
		return errLockedOut
	}
	if !a.check(username, password) {
		a.recordFailure(ip)
		time.Sleep(300 * time.Millisecond) // blunt online guessing a little more
		return errBadLogin
	}
	a.mu.Lock()
	delete(a.failed, ip)
	a.mu.Unlock()
	return nil
}

// ErrBadPassword and ErrLockedOut are what ConfirmPassword reports.
var (
	ErrBadPassword = errors.New("wrong password")
	ErrLockedOut   = errLockedOut
)

// ConfirmPassword re-checks the account password before something
// destructive. Wrong guesses count towards the same lockout as logins.
// A request from a hub with the peer token passes: the hub asked its user.
func (a *Auth) ConfirmPassword(r *http.Request, password string) error {
	if a.viaPeer(r) {
		return nil
	}
	ip := a.clientIP(r)
	if a.lockedOut(ip) {
		return errLockedOut
	}
	p := sha256.Sum256([]byte(password))
	if subtle.ConstantTimeCompare(p[:], a.pass[:]) != 1 {
		a.recordFailure(ip)
		time.Sleep(300 * time.Millisecond)
		return ErrBadPassword
	}
	return nil
}

func (a *Auth) viaPeer(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && a.PeerToken != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(a.PeerToken)) == 1
}

// Secure reports whether the browser reached Dash over HTTPS.
func (a *Auth) Secure(r *http.Request) bool {
	return r.TLS != nil || (a.TrustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
}

func (a *Auth) clientIP(r *http.Request) string {
	if a.TrustProxy {
		// The proxy appends the address it saw; earlier entries are client-supplied.
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
	}
	return clientIP(r)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Login handles POST /api/login {"username","password"}.
func (a *Auth) Login(w http.ResponseWriter, r *http.Request) {
	ip := a.clientIP(r)
	if a.lockedOut(ip) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many failed attempts, try again later"})
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	switch err := a.attempt(ip, req.Username, req.Password); err {
	case errLockedOut:
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
		return
	case errBadLogin:
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    a.newSession(),
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   a.Secure(r),
		// Strict: the cookie is never sent on cross-site requests, which also
		// stops other sites from driving the API or opening WebSockets (CSRF).
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Logout handles POST /api/logout.
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		a.mu.Lock()
		delete(a.sessions, sessionKey(c.Value))
		a.saveLocked()
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: a.Secure(r), SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

// Public paths reachable without a session: the login page and its assets.
var public = map[string]bool{
	"/login.html":           true,
	"/login.js":             true,
	"/style.css":            true,
	"/favicon.svg":          true,
	"/manifest.webmanifest": true,
	"/sw.js":                true, // service worker updates must not hit the login redirect
	"/api/login":            true,
	// Claude Code's permission prompts and the chat's Dash tools; authenticated
	// by a per-run bearer token instead.
	"/mcp/permissions": true,
	"/mcp/dash":        true,
}

// Middleware requires a valid session for everything except public paths.
// Unauthenticated API and WebSocket calls get 401; page loads are redirected
// to the login page.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static artwork (login page images, icons) is public too.
		if public[r.URL.Path] || strings.HasPrefix(r.URL.Path, "/img/") || a.oauth.public(r.URL.Path) || a.valid(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") || strings.HasPrefix(r.URL.Path, "/peer/") {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
			return
		}
		login := "/login.html"
		if strings.HasPrefix(r.URL.Path, "/preview/") {
			// Opening a preview (say from its QR code) continues there after signing in.
			login += "?next=" + url.QueryEscape(r.URL.RequestURI())
		}
		http.Redirect(w, r, login, http.StatusSeeOther)
	})
}
