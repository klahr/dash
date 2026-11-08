package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/store"
)

// openAuth opens (or reopens, "restarting") the database at path with
// username/password as the env account, the way main does.
func openAuth(t *testing.T, path, username, password string) *Auth {
	t.Helper()
	st, err := store.Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Accounts().Bootstrap(username, password); err != nil {
		t.Fatal(err)
	}
	a := New(st.Accounts())
	a.Legacy(username, password)
	return a
}

// newAuth is an Auth over a new database whose first user is username.
func newAuth(t *testing.T, username, password string) *Auth {
	t.Helper()
	return openAuth(t, filepath.Join(t.TempDir(), "dash.db"), username, password)
}

func serve(a *Auth) http.Handler {
	mux := http.NewServeMux()
	a.Routes(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("private")) })
	return a.Middleware(mux)
}

func newServer(t *testing.T) (*Auth, http.Handler) {
	t.Helper()
	a := newAuth(t, "admin", "s3cret")
	return a, serve(a)
}

// signedIn is a request from the env user's session, for calling methods
// that need an identity directly.
func signedIn(t *testing.T, a *Auth, r *http.Request) *http.Request {
	t.Helper()
	u, err := a.acc.EnvUser()
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := a.acc.Memberships(u.ID)
	id := &Identity{Session: store.Session{User: u}}
	if len(ms) > 0 {
		id.Org = &ms[0]
	}
	return r.WithContext(withIdentity(r.Context(), id))
}

func decode(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %d %s: %v", w.Code, w.Body, err)
	}
}

func do(h http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "10.0.0.1:1234"
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	t.Fatalf("no session cookie (status %d)", w.Code)
	return nil
}

func TestFirstStartNeedsCredentials(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, c := range [][2]string{{"", "x"}, {"x", ""}} {
		if err := st.Accounts().Bootstrap(c[0], c[1]); err != store.ErrNoAccount {
			t.Fatalf("Bootstrap(%q, %q) = %v, want ErrNoAccount", c[0], c[1], err)
		}
	}
}

func TestUnauthenticatedAccess(t *testing.T) {
	_, h := newServer(t)
	if w := do(h, "GET", "/api/projects", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("api: want 401, got %d", w.Code)
	}
	if w := do(h, "GET", "/ws/events", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("ws: want 401, got %d", w.Code)
	}
	w := do(h, "GET", "/", "")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login.html" {
		t.Fatalf("page: want redirect to login, got %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := do(h, "GET", "/login.html", ""); w.Code != http.StatusOK {
		t.Fatalf("login page should be public, got %d", w.Code)
	}
}

func TestLoginLogout(t *testing.T) {
	_, h := newServer(t)
	if w := do(h, "POST", "/api/login", `{"username":"admin","password":"wrong"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: want 401, got %d", w.Code)
	}
	w := do(h, "POST", "/api/login", `{"username":"admin","password":"s3cret"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("login: want 200, got %d", w.Code)
	}
	c := sessionCookie(t, w)
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie flags: %+v", c)
	}
	if w := do(h, "GET", "/api/projects", "", c); w.Code != http.StatusOK || w.Body.String() != "private" {
		t.Fatalf("with session: got %d %q", w.Code, w.Body.String())
	}
	do(h, "POST", "/api/logout", "", c)
	if w := do(h, "GET", "/api/projects", "", c); w.Code != http.StatusUnauthorized {
		t.Fatalf("after logout: want 401, got %d", w.Code)
	}
}

func TestSessionExpires(t *testing.T) {
	a, h := newServer(t)
	now := time.Now()
	a.now = func() time.Time { return now }
	c := sessionCookie(t, do(h, "POST", "/api/login", `{"username":"admin","password":"s3cret"}`))
	now = now.Add(sessionTTL + time.Minute)
	if w := do(h, "GET", "/api/x", "", c); w.Code != http.StatusUnauthorized {
		t.Fatalf("expired session: want 401, got %d", w.Code)
	}
}

func TestLockout(t *testing.T) {
	_, h := newServer(t)
	for i := 0; i < maxFailures; i++ {
		do(h, "POST", "/api/login", `{"username":"admin","password":"nope"}`)
	}
	// Even the right password is refused while locked out.
	if w := do(h, "POST", "/api/login", `{"username":"admin","password":"s3cret"}`); w.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", w.Code)
	}
}

func TestLockoutParallel(t *testing.T) {
	a := newAuth(t, "admin", "s3cret")
	const n = 4 * maxFailures
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.attempt("10.0.0.1", "admin", "nope")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	guesses := 0
	for err := range errs {
		if err == errBadLogin {
			guesses++
		}
	}
	if guesses != maxFailures {
		t.Fatalf("parallel guesses checked: %d, want %d", guesses, maxFailures)
	}
}

func TestGlobalLockout(t *testing.T) {
	a := newAuth(t, "admin", "s3cret")
	now := time.Now()
	a.now = func() time.Time { return now }
	a.Lockout = Lockout{PerIP: 3, Global: 5, Window: time.Minute}
	// Five addresses, one wrong guess each: under every per-address limit,
	// but the global budget is spent.
	for i := range 5 {
		if _, err := a.attempt("10.0.1."+string(rune('0'+i)), "admin", "nope"); err != errBadLogin {
			t.Fatalf("guess %d: %v", i, err)
		}
	}
	if _, err := a.attempt("10.0.2.1", "admin", "s3cret"); err != errLockedOut {
		t.Fatalf("new address after the global budget: %v, want locked out", err)
	}
	if !a.lockedOut("10.0.2.2") {
		t.Fatal("lockedOut should report the global lockout")
	}
	// Someone signed in can still confirm their password.
	r := signedIn(t, a, httptest.NewRequest("DELETE", "/api/projects/x", nil))
	if err := a.ConfirmPassword(r, "s3cret"); err != nil {
		t.Fatalf("confirm while globally locked out: %v", err)
	}
	now = now.Add(time.Minute + time.Second)
	if _, err := a.attempt("10.0.2.1", "admin", "s3cret"); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

func TestGlobalLockoutRightPasswordsDontCount(t *testing.T) {
	a := newAuth(t, "admin", "s3cret")
	a.Lockout = Lockout{PerIP: 3, Global: 2, Window: time.Minute}
	for i := range 5 {
		if _, err := a.attempt("10.0.3."+string(rune('0'+i)), "admin", "s3cret"); err != nil {
			t.Fatalf("login %d: %v", i, err)
		}
	}
}

func TestGlobalLockoutOff(t *testing.T) {
	a := newAuth(t, "admin", "s3cret")
	a.Lockout = Lockout{PerIP: 1, Global: 0, Window: time.Minute}
	for i := range 5 {
		a.attempt("10.0.4."+string(rune('0'+i)), "admin", "nope")
	}
	if _, err := a.attempt("10.0.5.1", "admin", "s3cret"); err != nil {
		t.Fatalf("no global limit: %v", err)
	}
	if _, err := a.attempt("10.0.4.0", "admin", "s3cret"); err != errLockedOut {
		t.Fatalf("per-address limit of 1: %v, want locked out", err)
	}
}

func TestConfirmPasswordSuccessDoesNotCount(t *testing.T) {
	a := newAuth(t, "u", "p")
	r := signedIn(t, a, httptest.NewRequest("DELETE", "/api/projects/x", nil))
	for range 2 * maxFailures {
		if err := a.ConfirmPassword(r, "p"); err != nil {
			t.Fatalf("right password: %v", err)
		}
	}
}

func TestLoadDotEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(path, []byte("# comment\nexport DASH_T_A=one\nDASH_T_B=\"two words\"\nDASH_T_C='three'\nDASH_T_PRESET=fromfile\n"), 0o600)
	t.Setenv("DASH_T_PRESET", "fromenv")
	for _, k := range []string{"DASH_T_A", "DASH_T_B", "DASH_T_C"} {
		t.Setenv(k, "") // registers cleanup …
		os.Unsetenv(k)  // … but start unset
	}
	if err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"DASH_T_A": "one", "DASH_T_B": "two words", "DASH_T_C": "three", "DASH_T_PRESET": "fromenv"}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatalf("missing file should be fine: %v", err)
	}
}

func TestTrustProxy(t *testing.T) {
	a, h := newServer(t)
	login := func(proto, xff string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"admin","password":"s3cret"}`))
		r.RemoteAddr = "127.0.0.1:5555"
		r.Header.Set("X-Forwarded-Proto", proto)
		r.Header.Set("X-Forwarded-For", xff)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if c := sessionCookie(t, login("https", "")); c.Secure {
		t.Fatal("forwarded headers must be ignored unless TrustProxy is set")
	}
	a.TrustProxy = true
	if c := sessionCookie(t, login("https", "")); !c.Secure {
		t.Fatal("want a Secure cookie behind an HTTPS proxy")
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if ip := a.clientIP(r); ip != "203.0.113.9" {
		t.Fatalf("want the address the proxy saw, not a client-supplied one; got %s", ip)
	}
}

func TestSessionsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dash.db")
	h1 := serve(openAuth(t, path, "admin", "s3cret"))
	c := sessionCookie(t, do(h1, "POST", "/api/login", `{"username":"admin","password":"s3cret"}`))
	if b, _ := os.ReadFile(path); strings.Contains(string(b), c.Value) {
		t.Fatal("the database must not contain usable session tokens")
	}

	h2 := serve(openAuth(t, path, "admin", "s3cret")) // "restarted"
	if w := do(h2, "GET", "/api/projects", "", c); w.Code != http.StatusOK {
		t.Fatalf("session should survive a restart, got %d", w.Code)
	}
	// DASH_PASSWORD changed: the env account's sessions are signed out, and
	// the new password is the one that works.
	h3 := serve(openAuth(t, path, "admin", "new-password"))
	if w := do(h3, "GET", "/api/projects", "", c); w.Code != http.StatusUnauthorized {
		t.Fatalf("session after a password change: want 401, got %d", w.Code)
	}
	if w := do(h3, "POST", "/api/login", `{"username":"admin","password":"s3cret"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("old password: want 401, got %d", w.Code)
	}
	if w := do(h3, "POST", "/api/login", `{"username":"admin","password":"new-password"}`); w.Code != http.StatusOK {
		t.Fatalf("new password: want 200, got %d", w.Code)
	}
}

// Sessions from sessions.json (Dash before users) carry over to the env
// account, if they were made for the same credentials.
func TestImportLegacySessions(t *testing.T) {
	dir := t.TempDir()
	key := sessionKey("old-cookie")
	write := func(account string) string {
		p := filepath.Join(dir, "sessions.json")
		b, _ := json.Marshal(map[string]any{"account": account, "sessions": map[string]any{
			key: map[string]any{"expires": time.Now().Add(time.Hour)}}})
		os.WriteFile(p, b, 0o600)
		return p
	}
	a := newAuth(t, "admin", "s3cret")
	stale := newAuth(t, "admin", "other") // its legacy hash is of other credentials
	if err := a.ImportSessions(write(stale.legacy)); err != nil {
		t.Fatal(err)
	}
	c := &http.Cookie{Name: CookieName, Value: "old-cookie"}
	if w := do(serve(a), "GET", "/api/projects", "", c); w.Code != http.StatusUnauthorized {
		t.Fatalf("session of other credentials: want 401, got %d", w.Code)
	}
	p := write(a.legacy)
	if err := a.ImportSessions(p); err != nil {
		t.Fatal(err)
	}
	if w := do(serve(a), "GET", "/api/projects", "", c); w.Code != http.StatusOK {
		t.Fatalf("imported session: want 200, got %d", w.Code)
	}
	if _, err := os.Stat(p + ".imported"); err != nil {
		t.Fatalf("sessions.json not renamed: %v", err)
	}
}

func TestConfirmPassword(t *testing.T) {
	a := newAuth(t, "u", "p")
	a.PeerToken = "peer-secret"
	r := signedIn(t, a, httptest.NewRequest("DELETE", "/api/projects/x", nil))
	if err := a.ConfirmPassword(httptest.NewRequest("DELETE", "/api/projects/x", nil), "p"); err != ErrBadPassword {
		t.Fatalf("no signed-in user: %v", err)
	}
	if err := a.ConfirmPassword(r, "p"); err != nil {
		t.Fatalf("right password: %v", err)
	}
	if err := a.ConfirmPassword(r, "nope"); err != ErrBadPassword {
		t.Fatalf("wrong password: %v", err)
	}
	r.Header.Set("Authorization", "Bearer peer-secret")
	if err := a.ConfirmPassword(r, ""); err != nil {
		t.Fatalf("hub with the peer token: %v", err)
	}
}
