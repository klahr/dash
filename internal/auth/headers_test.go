package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHeaders(t *testing.T) {
	a := &Auth{}
	h := a.Headers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/own" { // a handler's own policy replaces the default
			w.Header().Set("Content-Security-Policy", "default-src 'none'")
		}
	}))
	get := func(path string) http.Header {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w.Header()
	}
	hd := get("/")
	csp := hd.Get("Content-Security-Policy")
	for _, want := range []string{"script-src 'self';", "object-src 'none'", "base-uri 'self'", "frame-ancestors 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-eval") || strings.Contains(strings.Split(csp, "script-src")[1], "unsafe-inline;") {
		t.Errorf("CSP %q allows inline script or eval", csp)
	}
	if hd.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("no nosniff")
	}
	if hd.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS on a private Dash")
	}
	if got := get("/own").Values("Content-Security-Policy"); len(got) != 1 || got[0] != "default-src 'none'" {
		t.Errorf("handler's own CSP = %q", got)
	}

	a.Public = true
	if got := get("/").Get("Strict-Transport-Security"); got != HSTS {
		t.Errorf("public Dash HSTS = %q, want %q", got, HSTS)
	}
}

// An internet-facing Dash's session cookie is Secure even when the proxy
// doesn't say the browser came over HTTPS.
func TestPublicSecureCookie(t *testing.T) {
	a, h := newServer(t)
	login := func() *http.Cookie {
		r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"admin","password":"s3cret"}`))
		r.RemoteAddr = "127.0.0.1:5555"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return sessionCookie(t, w)
	}
	if login().Secure {
		t.Fatal("plain HTTP on a private Dash: want no Secure flag")
	}
	a.Public = true
	if !login().Secure {
		t.Fatal("internet-facing Dash: want a Secure cookie")
	}
	r := httptest.NewRequest("POST", "/api/logout", nil)
	w := httptest.NewRecorder()
	a.Logout(w, r)
	for _, c := range w.Result().Cookies() {
		if c.Name == CookieName && !c.Secure {
			t.Fatal("logout cookie not Secure")
		}
	}
}
