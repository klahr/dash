package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newServer(t *testing.T) (*Auth, http.Handler) {
	t.Helper()
	a, err := New("admin", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", a.Login)
	mux.HandleFunc("POST /api/logout", a.Logout)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("private")) })
	return a, a.Middleware(mux)
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

func TestNewRequiresCredentials(t *testing.T) {
	if _, err := New("", "x"); err == nil {
		t.Fatal("empty username accepted")
	}
	if _, err := New("x", ""); err == nil {
		t.Fatal("empty password accepted")
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
	a, _ := New("admin", "s3cret")
	const n = 4 * maxFailures
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- a.attempt("10.0.0.1", "admin", "nope")
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

func TestConfirmPasswordSuccessDoesNotCount(t *testing.T) {
	a, _ := New("u", "p")
	r := httptest.NewRequest("DELETE", "/api/projects/x", nil)
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
	path := filepath.Join(t.TempDir(), "sessions.json")
	a1, h1 := newServer(t)
	if err := a1.KeepSessions(path); err != nil {
		t.Fatal(err)
	}
	c := sessionCookie(t, do(h1, "POST", "/api/login", `{"username":"admin","password":"s3cret"}`))
	if b, _ := os.ReadFile(path); strings.Contains(string(b), c.Value) {
		t.Fatal("the sessions file must not contain usable tokens")
	}

	a2, h2 := newServer(t) // "restarted"
	if err := a2.KeepSessions(path); err != nil {
		t.Fatal(err)
	}
	if w := do(h2, "GET", "/api/projects", "", c); w.Code != http.StatusOK {
		t.Fatalf("session should survive a restart, got %d", w.Code)
	}
	a3, _ := New("admin", "new-password") // credentials changed: old sessions are dropped
	_ = a3.KeepSessions(path)
	if len(a3.sessions) != 0 {
		t.Fatal("sessions must not survive a password change")
	}
}

func TestConfirmPassword(t *testing.T) {
	a, _ := New("u", "p")
	a.PeerToken = "peer-secret"
	r := httptest.NewRequest("DELETE", "/api/projects/x", nil)
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
