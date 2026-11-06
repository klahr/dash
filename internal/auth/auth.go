// Package auth provides single-account login for Dash: one username and
// password from the environment (.env), and server-side cookie sessions.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
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

type session struct{ expires time.Time }

type failures struct {
	count int
	first time.Time
}

type Auth struct {
	user [32]byte // sha256 digests, so comparisons are constant-time and length-independent
	pass [32]byte

	mu       sync.Mutex
	sessions map[string]session
	failed   map[string]*failures

	now func() time.Time // for tests
}

// New returns an authenticator for the single account. Both values are required.
func New(username, password string) (*Auth, error) {
	if username == "" || password == "" {
		return nil, errors.New("DASH_USERNAME and DASH_PASSWORD must be set (see .env.example)")
	}
	return &Auth{
		user:     sha256.Sum256([]byte(username)),
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
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[c.Value]
	if !ok {
		return false
	}
	if a.now().After(s.expires) {
		delete(a.sessions, c.Value)
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
		if now.After(s.expires) {
			delete(a.sessions, k)
		}
	}
	a.sessions[tok] = session{expires: now.Add(sessionTTL)}
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
	ip := clientIP(r)
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
	if !a.check(req.Username, req.Password) {
		a.recordFailure(ip)
		time.Sleep(300 * time.Millisecond) // blunt online guessing a little more
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid username or password"})
		return
	}
	a.mu.Lock()
	delete(a.failed, ip)
	a.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    a.newSession(),
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil,
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
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

// Public paths reachable without a session: the login page and its assets.
var public = map[string]bool{
	"/login.html": true,
	"/login.js":   true,
	"/style.css":  true,
	"/api/login":  true,
	// Claude Code's permission prompts; authenticated by a per-run bearer token instead.
	"/mcp/permissions": true,
}

// Middleware requires a valid session for everything except public paths.
// Unauthenticated API and WebSocket calls get 401; page loads are redirected
// to the login page.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if public[r.URL.Path] || a.valid(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
			return
		}
		http.Redirect(w, r, "/login.html", http.StatusSeeOther)
	})
}
