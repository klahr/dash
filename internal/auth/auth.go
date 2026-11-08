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
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/branding"
	"github.com/klahr/dash/internal/mail"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/orgdomain"
	"github.com/klahr/dash/internal/passhash"
	"github.com/klahr/dash/internal/store"
)

const (
	CookieName = "dash_session"
	sessionTTL = 7 * 24 * time.Hour

	// The default Lockout.
	maxFailures    = 5                // per client IP …
	failureWindow  = 15 * time.Minute // … within this window, then locked out until it passes
	globalFailures = 100              // from all addresses together within the window
)

// Lockout limits failed sign-in attempts: wrong passwords, two-factor codes,
// password-reset and invitation tokens.
type Lockout struct {
	// PerIP failures from one client address within Window lock that
	// address out until the window passes (at least 1).
	PerIP int
	// Global failures from all addresses together within Window lock out
	// every new sign-in until the window passes, so many addresses can't
	// share out the guessing. Signed-in sessions carry on, and confirming
	// one's password isn't affected. 0: no global limit.
	Global int
	Window time.Duration
}

// DefaultLockout is the lockout New starts with.
var DefaultLockout = Lockout{PerIP: maxFailures, Global: globalFailures, Window: failureWindow}

// sessionKey is what's kept for a session or token: a hash, so the database
// can't be used to sign in.
func sessionKey(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

type failures struct {
	count  int
	first  time.Time
	logged bool // its lockout has been logged
}

// hold is a failure counted up front by reserve, until release takes it
// back.
type hold struct {
	key    string
	f      *failures
	global *failures // nil: not counted globally
}

type Auth struct {
	acc *store.Accounts

	mu      sync.Mutex
	failed  map[string]*failures    // by client IP, and by "2fa:<user id>" for second-factor guesses
	global  *failures               // from every address (Lockout.Global)
	limits  map[string]*failures    // requests that send email, by kind and client IP or address (see rateLimit)
	tickets map[string]*loginTicket // by key: passwords checked, waiting for a second factor

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

	// OnRevoke is told about each token a user revokes, so what holds it
	// open (a runner's tunnel) can be closed (optional).
	OnRevoke func(store.Token)

	// OnMembersChanged is told after someone joins or leaves an org, so its
	// subscription's seats follow (billing; optional).
	OnMembersChanged func(orgID int64)
	// InviteNotice is added to an invitation's answer as "billing" when
	// not nil: the org will need a subscription (billing; optional).
	InviteNotice func(orgID int64) any

	// Lockout limits failed sign-ins; set it before serving.
	Lockout Lockout

	oauth *OAuth // set by NewOAuth

	// Brand names Dash in emails, the authenticator app and the connector
	// consent page (empty fields: the defaults).
	Brand branding.Config

	// Landing, when set, answers visitors to / who aren't signed in,
	// instead of sending them to the sign-in page.
	Landing http.Handler

	mail       mail.Sender // SetMail: password reset by email, and what signup needs
	baseURL    string      // the public origin links in emails point to
	signupOpen bool        // EnableSignup

	// Domains, when set, serves each org at <slug>.<Domains.Base> (see
	// orghosts.go). Nil: one host for everything, as without it.
	Domains      *orgdomain.Domain
	handoffKey   []byte               // signs handoff tokens; new on every start
	handoffsUsed map[string]time.Time // spent handoff tokens' nonces, until they'd expire anyway
}

// New signs people in with the accounts in acc (see Accounts.Bootstrap for
// the first one).
func New(acc *store.Accounts) *Auth {
	passhash.Prime() // so the first unknown-name login isn't slower than the rest
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return &Auth{acc: acc, failed: map[string]*failures{}, limits: map[string]*failures{}, tickets: map[string]*loginTicket{}, now: time.Now, Lockout: DefaultLockout, recheck: 15 * time.Second,
		handoffKey: key, handoffsUsed: map[string]time.Time{}}
}

// brand is a.Brand with its defaults.
func (a *Auth) brand() branding.Config { return a.Brand.WithDefaults() }

// Accounts are the users and orgs a signs in.
func (a *Auth) Accounts() *store.Accounts { return a.acc }

// ---- the request's identity ----

// Identity is who made a request: the session's user and active org, or a
// token's user and the org it is pinned to.
type Identity struct {
	store.Session
	// Token is set when the request came with a token instead of a session.
	Token *store.Token
	key   string // the session's key, for switching its org, or the token's hash
}

// Can reports whether the request may do what needs scope: sessions may do
// everything their user may, tokens only what they were given.
func (id *Identity) Can(scope string) bool { return id.Token == nil || id.Token.HasScope(scope) }

// Owner is who the request acts as.
func (id *Identity) Owner() model.Owner {
	o := model.Owner{User: id.User.ID}
	if id.Org != nil {
		o.Org = id.Org.ID
	}
	return o
}

type identityKey struct{}

func withIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// WithIdentity is ctx for a request made as id (in-process calls on behalf
// of a request that was authenticated already).
func WithIdentity(ctx context.Context, id *Identity) context.Context { return withIdentity(ctx, id) }

// WithSession marks ctx as a request of sess (in-process callers and tests;
// the Middleware does this for browser sessions).
func WithSession(ctx context.Context, sess store.Session) context.Context {
	return withIdentity(ctx, &Identity{Session: sess})
}

// From returns the user of a request that came through the Middleware with a
// session or an API token, or through OAuth.Bearer. Requests with the peer
// token, over a runner's tunnel and to public paths have none.
func From(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(*Identity)
	return id, ok && id != nil
}

// ViaToken reports whether ctx's request was authenticated by a token
// rather than a browser session.
func ViaToken(ctx context.Context) bool {
	id, ok := From(ctx)
	return ok && id.Token != nil
}

// ErrSessionRequired is what a token gets for what only a signed-in browser
// may do: managing members, invitations and credentials.
var ErrSessionRequired = errors.New("not available with a token; sign in to Dash to do this")

// RequireSession rejects requests authenticated by a token. It wraps the
// routes that manage members, invitations and credentials, so a leaked token
// can't mint more tokens, let anyone in or lock anyone out.
func RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ViaToken(r.Context()) {
			writeError(w, http.StatusForbidden, ErrSessionRequired.Error())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearer is the request's bearer token, if it sent one.
func bearer(r *http.Request) (string, bool) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	tok = strings.TrimSpace(tok)
	return tok, ok && tok != ""
}

// tokenIdentity authenticates tok as one of kinds.
func (a *Auth) tokenIdentity(tok string, kinds ...store.TokenKind) (*Identity, error) {
	t, s, err := a.acc.Authenticate(tok, a.now(), kinds...)
	if err != nil {
		if !errors.Is(err, store.ErrTokenInvalid) {
			slog.Error("look up token", "err", err)
		}
		return nil, err
	}
	// Like a session, a person's token does nothing until they set up the
	// two-factor sign-in an org of theirs requires. Runners carry on.
	if s.Needs2FA && t.Kind != store.TokenRunner {
		return nil, errTokenNeeds2FA
	}
	return &Identity{Session: s, Token: &t, key: store.TokenHash(tok)}, nil
}

var errTokenNeeds2FA = errors.New("set up two-factor sign-in in Dash to use your tokens")

// scopeFor is the scope r needs: read for GET and HEAD requests (and the
// read-only event stream), write for everything else, including the
// WebSockets that send (chat, terminals).
func scopeFor(r *http.Request) string {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return store.ScopeWrite
	}
	if strings.HasPrefix(r.URL.Path, "/ws/") && r.URL.Path != "/ws/events" {
		return store.ScopeWrite
	}
	return store.ScopeRead
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
	return a.startSessionIn(w, r, uid, 0, newFamily())
}

// startSessionIn signs uid in on this browser and host, working in org (0:
// their first), as one of family's sessions (see store.Session.Family).
func (a *Auth) startSessionIn(w http.ResponseWriter, r *http.Request, uid, org int64, family string) error {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	now := a.now()
	_ = a.acc.PruneSessions(now)
	a.ensurePersonal(uid) // before the session picks the org to start in
	if err := a.acc.CreateSessionIn(sessionKey(tok), uid, org, family, now, now.Add(sessionTTL)); err != nil {
		return err
	}
	// No Domain: the cookie is the host's alone. On the parent domain it
	// would also go to every org's subdomain and to *.preview hosts, where
	// anyone's code runs.
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

// lockout is a.Lockout with its zero values filled in.
func (a *Auth) lockout() Lockout {
	l := a.Lockout
	if l.PerIP < 1 {
		l.PerIP = maxFailures
	}
	if l.Window <= 0 {
		l.Window = failureWindow
	}
	if l.Global < 0 {
		l.Global = 0
	}
	return l
}

// window is the failures counted for key within the current window,
// starting a new one if needed. a.mu must be held.
func (a *Auth) window(key string, l Lockout) *failures {
	f, ok := a.failed[key]
	if !ok || a.now().Sub(f.first) > l.Window {
		f = &failures{first: a.now()}
		a.failed[key] = f
	}
	return f
}

// globalWindow is the failures from every address within the current
// window. a.mu must be held.
func (a *Auth) globalWindow(l Lockout) *failures {
	if a.global == nil || a.now().Sub(a.global.first) > l.Window {
		a.global = &failures{first: a.now()}
	}
	return a.global
}

// full reports whether f has reached limit, logging the lockout the first
// time it is enforced, for fail2ban and the like. a.mu must be held.
func (a *Auth) full(f *failures, limit int, l Lockout, key string) bool {
	if f.count < limit {
		return false
	}
	if !f.logged {
		f.logged = true
		until := f.first.Add(l.Window).UTC().Format(time.RFC3339)
		if key == "" {
			slog.Warn("sign-in lockout: too many failed attempts from all addresses; new sign-ins are refused",
				"failures", f.count, "until", until)
		} else if uid, ok := strings.CutPrefix(key, "2fa:"); ok {
			slog.Warn("sign-in lockout: too many wrong two-factor codes", "user", uid, "failures", f.count, "until", until)
		} else {
			slog.Warn("sign-in lockout: too many failed attempts", "ip", key, "failures", f.count, "until", until)
		}
	}
	return true
}

// lockedOut reports whether ip has too many recent failed logins, or every
// address together has.
func (a *Auth) lockedOut(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	l := a.lockout()
	if l.Global > 0 && a.full(a.globalWindow(l), l.Global, l, "") {
		return true
	}
	f, ok := a.failed[ip]
	if !ok {
		return false
	}
	if a.now().Sub(f.first) > l.Window {
		delete(a.failed, ip)
		return false
	}
	return a.full(f, l.PerIP, l, ip)
}

// reserve counts an attempt from ip as a failure up front, in the same
// critical section as the lockout check, so parallel guesses can't all
// slip past it. It counts towards the global limit too. It returns false if
// ip, or everyone, is locked out. A correct password takes the reservation
// back with release.
func (a *Auth) reserve(ip string) (*hold, bool) { return a.reserveKey(ip, true) }

// reserveLocal is reserve without the global limit: for checks by someone
// already signed in (confirming their password, enrolling a second factor),
// so a lockout of new sign-ins doesn't stop them, and for keys other than
// the client's address.
func (a *Auth) reserveLocal(key string) (*hold, bool) { return a.reserveKey(key, false) }

func (a *Auth) reserveKey(key string, global bool) (*hold, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	l := a.lockout()
	h := &hold{key: key, f: a.window(key, l)}
	if a.full(h.f, l.PerIP, l, key) {
		return nil, false
	}
	if global && l.Global > 0 {
		h.global = a.globalWindow(l)
		if a.full(h.global, l.Global, l, "") {
			return nil, false
		}
		h.global.count++
	}
	h.f.count++
	return h, true
}

// release undoes a reservation from reserve, unless its window has since
// been replaced.
func (a *Auth) release(h *hold) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if h.global != nil && a.global == h.global && h.global.count > 0 {
		h.global.count--
	}
	if a.failed[h.key] != h.f {
		return
	}
	if h.f.count--; h.f.count <= 0 {
		delete(a.failed, h.key)
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
// towards its lockout. A right password doesn't yet wipe ip's earlier
// failures: for an account with two-factor sign-in that waits for the code
// (signedInFrom).
func (a *Auth) attempt(ip, login, password string) (store.User, error) {
	f, ok := a.reserve(ip)
	if !ok {
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
	a.release(f)
	// Only someone with the password learns the account waits for its link.
	if unverified, err := a.acc.Unverified(u.ID); err != nil || unverified {
		if err != nil {
			return store.User{}, err
		}
		return store.User{}, errUnverified
	}
	return u, nil
}

// signedInFrom forgets ip's failed attempts once someone has signed in from
// it with every factor their account has.
func (a *Auth) signedInFrom(ip string) {
	a.mu.Lock()
	delete(a.failed, ip)
	a.mu.Unlock()
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
	if id.Token != nil {
		return ErrSessionRequired
	}
	// Per address only: a global lockout of new sign-ins mustn't stop
	// someone already signed in.
	f, ok := a.reserveLocal(a.clientIP(r))
	if !ok {
		return errLockedOut
	}
	hash, err := a.acc.PasswordHash(id.User.ID)
	if !a.verify(id.User, hash, err == nil, password) {
		time.Sleep(300 * time.Millisecond)
		return ErrBadPassword
	}
	a.release(f)
	return nil
}

type hubKey struct{}

// WithHub marks ctx as a request the hub sent through this runner's tunnel.
// The tunnel authenticated it; the hub asked its own user for passwords.
func WithHub(ctx context.Context) context.Context { return context.WithValue(ctx, hubKey{}, true) }

// FromHub reports whether ctx is a request from the hub over a runner tunnel.
func FromHub(ctx context.Context) bool { v, _ := ctx.Value(hubKey{}).(bool); return v }

type peerKey struct{}

// FromMachine reports whether ctx is a request from another Dash rather than
// a person: the hub over a runner's tunnel, or a hub with the peer token.
// Those act for the whole of this Dash; the other Dash checked its own user.
func FromMachine(ctx context.Context) bool {
	v, _ := ctx.Value(peerKey{}).(bool)
	return v || FromHub(ctx)
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

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// Login handles POST /api/login {"username","password"}; username may also
// be the account's email. Without two-factor sign-in it starts a session and
// answers {"ok":true}, with "enrol_2fa":true when an org of theirs requires
// two-factor sign-in they haven't set up. With it, it answers
// {"totp_required":true,"ticket":…} and the code goes to /api/login/totp.
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
	case errors.Is(err, errUnverified):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error(), "code": "unverified"})
		return
	case err != nil:
		slog.Error("login", "err", err)
		writeError(w, http.StatusInternalServerError, "sign-in failed")
		return
	}
	if u.TOTP {
		writeJSON(w, http.StatusOK, map[string]any{"totp_required": true, "ticket": a.newTicket(u.ID)})
		return
	}
	a.signedInFrom(ip)
	if err := a.startSession(w, r, u.ID); err != nil {
		slog.Error("create session", "err", err)
		writeError(w, http.StatusInternalServerError, "sign-in failed")
		return
	}
	enrol, err := a.acc.Requires2FA(u.ID)
	if err != nil {
		slog.Error("login", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "enrol_2fa": enrol})
}

// Logout handles POST /api/logout. It signs out the session's whole
// family: with org subdomains, the base domain's and every org's this
// browser was handed to. A form post (the org picker's) goes on to the
// sign-in page.
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		_ = a.acc.DeleteSessionFamily(sessionKey(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: a.Secure(r), SameSite: http.SameSiteStrictMode})
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		http.Redirect(w, r, "/login.html", http.StatusSeeOther)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Public paths reachable without a session: the login and invitation pages
// and their assets.
// notifyActionPrefix is notify.ActionPath: the Approve/Deny buttons of
// ntfy notifications, whose signed token in the path is the credential.
const notifyActionPrefix = "/api/notify/permission/"

// publicPath reports whether path is served without signing in.
func publicPath(path string) bool {
	return public[path] || strings.HasPrefix(path, notifyActionPrefix)
}

var public = map[string]bool{
	"/login.html":           true,
	"/login.js":             true,
	"/twofactor.js":         true,
	"/invite.html":          true,
	"/invite.js":            true,
	"/style.css":            true,
	"/favicon.svg":          true,
	"/manifest.webmanifest": true,
	"/sw.js":                true, // service worker updates must not hit the login redirect
	"/api/login":            true,
	"/api/login/totp":       true, // the login's ticket is its credential
	// An invitation link's token is its own credential.
	"/api/invitations/lookup":   true,
	"/api/invitations/register": true,
	"/api/invitations/decline":  true,
	// Signing up and resetting a password: before there's a session.
	"/api/signup":          true,
	"/api/signup/resend":   true,
	"/api/signup/verify":   true,
	"/api/password/forgot": true,
	"/api/password/reset":  true,
	// Claude Code's permission prompts and the chat's Dash tools; authenticated
	// by a per-run bearer token instead.
	"/mcp/permissions": true,
	"/mcp/dash":        true,
	// Runners: a registration code, or the runner's own token.
	"/runner/register": true,
	"/runner/connect":  true,
	"/runner/report":   true,
	"/runner/mcp/dash": true,
	// The runner installer and the hub's version: nothing secret.
	"/runner/install.sh": true,
	"/runner/version":    true,
	// Stripe's events: signed with the webhook secret (billing).
	"/api/billing/webhook": true,
}

// publicAsset reports whether path is static artwork the sign-in and
// invitation pages need before there's a session: images and the
// self-hosted fonts.
func publicAsset(path string) bool {
	return strings.HasPrefix(path, "/img/") || strings.HasPrefix(path, "/vendor/fonts/")
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
		if a.viaPeer(r) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), peerKey{}, true)))
			return
		}
		if a.Domains != nil {
			var done bool
			if r, done = a.serveDomains(w, r, next); done {
				return
			}
		}
		if publicPath(r.URL.Path) || publicAsset(r.URL.Path) || a.oauth.public(r.URL.Path) || r.URL.Path == branding.LogoPath {
			next.ServeHTTP(w, r)
			return
		}
		api := isAPI(r.URL.Path)
		if tok, ok := bearer(r); ok {
			a.serveToken(w, r, tok, api, next)
			return
		}
		id, ok := a.session(r)
		if !ok {
			if api {
				writeError(w, http.StatusUnauthorized, "login required")
				return
			}
			if r.URL.Path == "/" && a.Landing != nil {
				a.Landing.ServeHTTP(w, r)
				return
			}
			login := "/login.html"
			if strings.HasPrefix(r.URL.Path, "/preview/") || a.Domains != nil && strings.HasPrefix(r.URL.Path, "/go/") {
				// Opening a preview (say from its QR code), or an org's
				// subdomain, continues there after signing in.
				login += "?next=" + url.QueryEscape(r.URL.RequestURI())
			}
			http.Redirect(w, r, login, http.StatusSeeOther)
			return
		}
		a.serveSession(w, r, id, api, next)
	})
}

// serveSession passes a request with a valid session on to next as id, once
// id has set up the second factor an org requires and has an org to work in.
func (a *Auth) serveSession(w http.ResponseWriter, r *http.Request, id *Identity, api bool, next http.Handler) {
	if id.Needs2FA && !twoFactorFree(r.URL.Path) {
		// An org of theirs requires two-factor sign-in: set it up first.
		if api {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "set up two-factor sign-in to continue", "code": "2fa_required"})
			return
		}
		http.Redirect(w, r, "/login.html?enrol=1", http.StatusSeeOther)
		return
	}
	if id.Org == nil && api && !orgFree(r.URL.Path) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "you aren't a member of any organization", "code": "no_org"})
		return
	}
	r = r.WithContext(withIdentity(r.Context(), id))
	if base, _ := r.Context().Value(baseHostKey{}).(bool); base && (r.URL.Path == "/" || r.URL.Path == "/index.html") &&
		(r.Method == http.MethodGet || r.Method == http.MethodHead) {
		// The base domain's front page is the org picker: the board is on
		// each org's subdomain.
		a.picker(w, r, id)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/ws/") {
		w = &watchedConn{ResponseWriter: w, a: a, r: r, id: id}
	}
	next.ServeHTTP(w, r)
}

// serveToken passes an API request with an API token on to next, as the
// token's user in the token's org, if the token is valid and has the scope
// the request needs.
func (a *Auth) serveToken(w http.ResponseWriter, r *http.Request, tok string, api bool, next http.Handler) {
	if !api {
		writeError(w, http.StatusUnauthorized, "tokens only work on the API")
		return
	}
	id, err := a.tokenIdentity(tok, store.TokenAPI)
	if errors.Is(err, errTokenNeeds2FA) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error(), "code": "2fa_required"})
		return
	}
	if err != nil || !a.pinned(r, id) { // on an org's subdomain, only that org's tokens
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		writeError(w, http.StatusUnauthorized, store.ErrTokenInvalid.Error())
		return
	}
	if need := scopeFor(r); !id.Can(need) {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer error="insufficient_scope", scope=%q`, need))
		writeError(w, http.StatusForbidden, "this token doesn't have the "+need+" scope")
		return
	}
	r = r.WithContext(withIdentity(r.Context(), id))
	if strings.HasPrefix(r.URL.Path, "/ws/") {
		w = &watchedConn{ResponseWriter: w, a: a, r: r, id: id}
	}
	next.ServeHTTP(w, r)
}

// stillValid reports whether the session or token behind id still exists,
// its user still belongs to the org they connected in, and (for a session)
// no org requires a second factor they lack.
func (a *Auth) stillValid(id *Identity) bool {
	if id.Token != nil {
		_, _, err := a.acc.AuthenticateHash(id.key, a.now(), id.Token.Kind)
		return err == nil
	}
	s, err := a.acc.Session(id.key, a.now())
	if err != nil || s.Needs2FA {
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
