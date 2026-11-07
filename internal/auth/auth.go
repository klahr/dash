// Package auth signs people in to Dash: user accounts with Argon2id
// passwords (store.Accounts), server-side cookie sessions that carry the user
// and the org they're working in, and the org/membership/invitation API.
package auth

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/passhash"
	"github.com/klahr/dash/internal/store"
)

const (
	CookieName = "dash_session"
	sessionTTL = 7 * 24 * time.Hour

	maxFailures   = 5                // per client IP …
	failureWindow = 15 * time.Minute // … within this window, then locked out until it passes
)

// sessionKey is what's kept for a session or token: a hash, so the database
// can't be used to sign in.
func sessionKey(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

type failures struct {
	count int
	first time.Time
}

type Auth struct {
	acc *store.Accounts

	mu     sync.Mutex
	failed map[string]*failures

	now     func() time.Time // for tests
	recheck time.Duration    // how often an open WebSocket re-checks its session
	legacy  string           // the single account's hash in sessions.json / oauth.json, before users

	// TrustProxy honours X-Forwarded-For and X-Forwarded-Proto from a
	// reverse proxy (Caddy, nginx, a Cloudflare Tunnel, Tailscale Serve) that
	// terminates TLS.
	// Only enable it when Dash is reachable solely through that proxy.
	TrustProxy bool

	// PeerToken lets another Dash (a "hub" showing this machine in its
	// dashboard) call the API with "Authorization: Bearer <token>".
	PeerToken string

	oauth *OAuth // set by NewOAuth
}

// New signs people in with the accounts in acc (see Accounts.Bootstrap for
// the first one).
func New(acc *store.Accounts) *Auth {
	passhash.Prime() // so the first unknown-name login isn't slower than the rest
	return &Auth{acc: acc, failed: map[string]*failures{}, now: time.Now, recheck: 15 * time.Second}
}

// Accounts are the users and orgs a signs in.
func (a *Auth) Accounts() *store.Accounts { return a.acc }

// ---- the request's identity ----

// Identity is who made a request: the session's user and active org.
type Identity struct {
	store.Session
	key string // the session's key, for switching its org
}

type identityKey struct{}

func withIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// From returns the signed-in user of a request that came through the
// Middleware with a session. Requests with the peer token, over a runner's
// tunnel and to public paths have none.
func From(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(*Identity)
	return id, ok && id != nil
}

// session returns the identity of r's session cookie, if valid.
func (a *Auth) session(r *http.Request) (*Identity, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return nil, false
	}
	key := sessionKey(c.Value)
	s, err := a.acc.Session(key, a.now())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Error("look up session", "err", err)
		}
		return nil, false
	}
	return &Identity{Session: s, key: key}, true
}

// startSession signs uid in on this browser.
func (a *Auth) startSession(w http.ResponseWriter, r *http.Request, uid int64) error {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	now := a.now()
	_ = a.acc.PruneSessions(now)
	if err := a.acc.CreateSession(sessionKey(tok), uid, now, now.Add(sessionTTL)); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    tok,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   a.Secure(r),
		// Strict: the cookie is never sent on cross-site requests, which also
		// stops other sites from driving the API or opening WebSockets (CSRF).
		SameSite: http.SameSiteStrictMode,
	})
	return nil
}

// ---- lockout ----

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

// reserve counts an attempt from ip as a failure up front, in the same
// critical section as the lockout check, so parallel guesses can't all
// slip past it. It returns false if ip is locked out. A correct password
// takes the reservation back with release.
func (a *Auth) reserve(ip string) (*failures, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, ok := a.failed[ip]
	if !ok || a.now().Sub(f.first) > failureWindow {
		f = &failures{first: a.now()}
		a.failed[ip] = f
	}
	if f.count >= maxFailures {
		return nil, false
	}
	f.count++
	return f, true
}

// release undoes a reservation from reserve, unless its window has since
// been replaced.
func (a *Auth) release(ip string, f *failures) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failed[ip] != f {
		return
	}
	if f.count--; f.count <= 0 {
		delete(a.failed, ip)
	}
}

var (
	errLockedOut = errors.New("too many failed attempts, try again later")
	errBadLogin  = errors.New("invalid username or password")
)

// verify checks password against uid's account, upgrading a hash made with
// weaker parameters. It takes as long whether or not the account exists.
func (a *Auth) verify(u store.User, hash string, found bool, password string) bool {
	if !found {
		passhash.SpendVerifyTime(password)
		return false
	}
	ok, rehash, err := passhash.Verify(hash, password)
	if err != nil {
		slog.Error("verify password", "user", u.ID, "err", err)
	}
	if ok && rehash {
		if h, err := passhash.Hash(password); err == nil {
			_ = a.acc.Rehash(u.ID, h)
		}
	}
	return ok
}

// attempt checks a login (a username or email) from ip, counting failures
// towards its lockout.
func (a *Auth) attempt(ip, login, password string) (store.User, error) {
	if _, ok := a.reserve(ip); !ok {
		return store.User{}, errLockedOut
	}
	u, hash, err := a.acc.Credentials(login)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.User{}, err
	}
	if !a.verify(u, hash, err == nil, password) {
		time.Sleep(300 * time.Millisecond) // blunt online guessing a little more
		return store.User{}, errBadLogin
	}
	a.mu.Lock()
	delete(a.failed, ip)
	a.mu.Unlock()
	return u, nil
}

// ErrBadPassword and ErrLockedOut are what ConfirmPassword reports.
var (
	ErrBadPassword = errors.New("wrong password")
	ErrLockedOut   = errLockedOut
)

// ConfirmPassword re-checks the signed-in user's password before something
// destructive or sensitive. Wrong guesses count towards the same lockout as
// logins. A request from a hub with the peer token passes: the hub asked
// its user.
func (a *Auth) ConfirmPassword(r *http.Request, password string) error {
	if a.viaPeer(r) || FromHub(r.Context()) {
		return nil
	}
	id, ok := From(r.Context())
	if !ok {
		return ErrBadPassword
	}
	ip := a.clientIP(r)
	f, ok := a.reserve(ip)
	if !ok {
		return errLockedOut
	}
	hash, err := a.acc.PasswordHash(id.User.ID)
	if !a.verify(id.User, hash, err == nil, password) {
		time.Sleep(300 * time.Millisecond)
		return ErrBadPassword
	}
	a.release(ip, f)
	return nil
}

type hubKey struct{}

// WithHub marks ctx as a request the hub sent through this runner's tunnel.
// The tunnel authenticated it; the hub asked its own user for passwords.
func WithHub(ctx context.Context) context.Context { return context.WithValue(ctx, hubKey{}, true) }

// FromHub reports whether ctx is a request from the hub over a runner tunnel.
func FromHub(ctx context.Context) bool { v, _ := ctx.Value(hubKey{}).(bool); return v }

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

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// Login handles POST /api/login {"username","password"}; username may also
// be the account's email.
func (a *Auth) Login(w http.ResponseWriter, r *http.Request) {
	ip := a.clientIP(r)
	if a.lockedOut(ip) {
		writeError(w, http.StatusTooManyRequests, errLockedOut.Error())
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	u, err := a.attempt(ip, strings.TrimSpace(req.Username), req.Password)
	switch {
	case errors.Is(err, errLockedOut):
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	case errors.Is(err, errBadLogin):
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	case err != nil:
		slog.Error("login", "err", err)
		writeError(w, http.StatusInternalServerError, "sign-in failed")
		return
	}
	if err := a.startSession(w, r, u.ID); err != nil {
		slog.Error("create session", "err", err)
		writeError(w, http.StatusInternalServerError, "sign-in failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Logout handles POST /api/logout.
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		_ = a.acc.DeleteSession(sessionKey(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: a.Secure(r), SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

// Public paths reachable without a session: the login and invitation pages
// and their assets.
var public = map[string]bool{
	"/login.html":           true,
	"/login.js":             true,
	"/invite.html":          true,
	"/invite.js":            true,
	"/style.css":            true,
	"/favicon.svg":          true,
	"/manifest.webmanifest": true,
	"/sw.js":                true, // service worker updates must not hit the login redirect
	"/api/login":            true,
	// An invitation link's token is its own credential.
	"/api/invitations/lookup":   true,
	"/api/invitations/register": true,
	"/api/invitations/decline":  true,
	// Claude Code's permission prompts and the chat's Dash tools; authenticated
	// by a per-run bearer token instead.
	"/mcp/permissions": true,
	"/mcp/dash":        true,
	// Runners: a registration code, or the runner's own token.
	"/runner/register": true,
	"/runner/connect":  true,
}

// orgFree are the API paths a signed-in user with no org may use: who am I,
// sign out, pick an org, and join one.
func orgFree(path string) bool {
	switch path {
	case "/api/me", "/api/logout", "/api/orgs", "/api/auth/switch-org", "/api/invitations/accept":
		return true
	}
	return false
}

// Middleware requires a valid session for everything except public paths,
// and a membership of the session's org for the API. It checks both on
// every request, so removing a member or signing a session out takes effect
// at once; open WebSockets check again every few seconds and are closed.
// Unauthenticated API and WebSocket calls get 401; page loads are redirected
// to the login page.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static artwork (login page images, icons) is public too.
		if public[r.URL.Path] || strings.HasPrefix(r.URL.Path, "/img/") || a.oauth.public(r.URL.Path) || a.viaPeer(r) {
			next.ServeHTTP(w, r)
			return
		}
		api := strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") || strings.HasPrefix(r.URL.Path, "/peer/")
		id, ok := a.session(r)
		if !ok {
			if api {
				writeError(w, http.StatusUnauthorized, "login required")
				return
			}
			login := "/login.html"
			if strings.HasPrefix(r.URL.Path, "/preview/") {
				// Opening a preview (say from its QR code) continues there after signing in.
				login += "?next=" + url.QueryEscape(r.URL.RequestURI())
			}
			http.Redirect(w, r, login, http.StatusSeeOther)
			return
		}
		if id.Org == nil && api && !orgFree(r.URL.Path) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "you aren't a member of any organization", "code": "no_org"})
			return
		}
		r = r.WithContext(withIdentity(r.Context(), id))
		if strings.HasPrefix(r.URL.Path, "/ws/") {
			w = &watchedConn{ResponseWriter: w, a: a, r: r, id: id}
		}
		next.ServeHTTP(w, r)
	})
}

// stillValid reports whether the session behind id still exists and its
// user still belongs to the org they connected in.
func (a *Auth) stillValid(id *Identity) bool {
	s, err := a.acc.Session(id.key, a.now())
	if err != nil {
		return false
	}
	if id.Org == nil {
		return true
	}
	_, err = a.acc.Membership(s.User.ID, id.Org.ID)
	return err == nil
}

// watchedConn closes a hijacked (WebSocket) connection once its session
// stops being valid.
type watchedConn struct {
	http.ResponseWriter
	a  *Auth
	r  *http.Request
	id *Identity
}

func (w *watchedConn) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *watchedConn) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		return conn, rw, err
	}
	go func() {
		t := time.NewTicker(w.a.recheck)
		defer t.Stop()
		for {
			select {
			case <-w.r.Context().Done(): // the handler returned
				return
			case <-t.C:
				if !w.a.stillValid(w.id) {
					conn.Close()
					return
				}
			}
		}
	}()
	return conn, rw, nil
}

// ---- before users ----

// Legacy names the single account of Dash before users, so its sessions
// (ImportSessions) and OAuth tokens (OAuth.Keep) carry over to the account
// Bootstrap made from it.
func (a *Auth) Legacy(username, password string) {
	u, p := sha256.Sum256([]byte(username)), sha256.Sum256([]byte(password))
	h := sha256.Sum256(append(u[:], p[:]...))
	a.legacy = hex.EncodeToString(h[:])
}

// legacyUser is the account the legacy files' sessions and tokens belong
// to, if they were made for the current credentials.
func (a *Auth) legacyUser(account string) (int64, bool) {
	if a.legacy == "" || subtle.ConstantTimeCompare([]byte(account), []byte(a.legacy)) != 1 {
		return 0, false
	}
	u, err := a.acc.EnvUser()
	return u.ID, err == nil
}

// ImportSessions moves the sessions of a sessions.json from before users
// into the database, as the env account's, so upgrading signs nobody out.
// The file is renamed to <path>.imported.
func (a *Auth) ImportSessions(path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f struct {
		Account  string `json:"account"`
		Sessions map[string]struct {
			Expires time.Time `json:"expires"`
		} `json:"sessions"`
	}
	if json.Unmarshal(b, &f) == nil {
		if uid, ok := a.legacyUser(f.Account); ok {
			now := a.now()
			for key, s := range f.Sessions {
				if now.Before(s.Expires) {
					if err := a.acc.CreateSession(key, uid, now, s.Expires); err != nil {
						return err
					}
				}
			}
		}
	}
	return os.Rename(path, path+".imported")
}
