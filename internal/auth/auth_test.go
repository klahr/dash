package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
