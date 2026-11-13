package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHeaders(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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

// A preview under Dash's host can set cookies for Dash's domain. Over HTTPS
// the session cookie is __Host- (which browsers refuse to take from another
// host or with a Domain), so a planted plain dash_session is ignored, and a
// request with two session cookies is refused rather than taking the first.
func TestPlantedSessionCookieRefused(t *testing.T) {
	t.Parallel()

	_, h := newServer(t)
	const base = "https://dash.example.com"
	w := do(h, "POST", base+"/api/login", `{"username":"admin","password":"s3cret"}`)
	mine := sessionCookie(t, w)
	if mine.Name != CookieName || !mine.Secure || mine.Path != "/" {
		t.Fatalf("session cookie over HTTPS = %s, want a Secure %s on /", mine, CookieName)
	}
	planted := &http.Cookie{Name: PlainCookieName, Value: mine.Value}
	if w := do(h, "GET", base+"/api/me", "", planted); w.Code != http.StatusUnauthorized {
		t.Errorf("plain %s over HTTPS: want 401, got %d", PlainCookieName, w.Code)
	}
	if w := do(h, "GET", base+"/api/me", "", mine); w.Code != http.StatusOK {
		t.Fatalf("own session: want 200, got %d", w.Code)
	}
	second := &http.Cookie{Name: CookieName, Value: "attackers-session"}
	if w := do(h, "GET", base+"/api/me", "", mine, second); w.Code != http.StatusUnauthorized {
		t.Errorf("two session cookies: want 401, got %d", w.Code)
	}
	if w := do(h, "GET", base+"/api/me", "", second, mine); w.Code != http.StatusUnauthorized {
		t.Errorf("two session cookies, planted first: want 401, got %d", w.Code)
	}

	// Plain HTTP has no __Host- cookies; two are still refused.
	plain := sessionCookie(t, do(h, "POST", "/api/login", `{"username":"admin","password":"s3cret"}`))
	if plain.Name != PlainCookieName {
		t.Fatalf("session cookie over HTTP = %s, want %s", plain.Name, PlainCookieName)
	}
	if w := do(h, "GET", "/api/me", "", &http.Cookie{Name: PlainCookieName, Value: "x"}, plain); w.Code != http.StatusUnauthorized {
		t.Errorf("two plain session cookies: want 401, got %d", w.Code)
	}
}

// Without org subdomains, too, a page of another origin can't use the
// session cookie to change anything: SameSite lets it through from a
// sibling subdomain or another port of the same host.
func TestCrossOriginRefusedWithoutDomains(t *testing.T) {
	t.Parallel()

	a, h := newServer(t)
	cookie := sessionCookie(t, do(h, "POST", "http://localhost:8080/api/login", `{"username":"admin","password":"s3cret"}`))
	send := func(method, path, origin string, hdr ...string) int {
		t.Helper()
		r := httptest.NewRequest(method, "http://localhost:8080"+path, strings.NewReader(`{}`))
		r.RemoteAddr = "10.0.0.1:1234"
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for _, o := range []string{"http://localhost:3000", "http://evil.example", "null"} {
		if code := send("POST", "/api/projects", o); code != http.StatusForbidden {
			t.Errorf("POST from %s: %d", o, code)
		}
		if code := send("GET", "/ws/events", o, "Connection", "Upgrade", "Upgrade", "websocket"); code != http.StatusForbidden {
			t.Errorf("WebSocket from %s: %d", o, code)
		}
		if code := send("POST", "/api/login", o); code != http.StatusForbidden {
			t.Errorf("login from %s: %d", o, code)
		}
	}
	for name, code := range map[string]int{
		"same origin":          send("POST", "/api/projects", "http://localhost:8080"),
		"no Origin":            send("POST", "/api/projects", ""),
		"cross-origin GET":     send("GET", "/api/projects", "http://evil.example"),
		"a notify action link": send("POST", notifyActionPrefix+"tok", "https://ntfy.sh"),
	} {
		if code == http.StatusForbidden {
			t.Errorf("%s: refused", name)
		}
	}
	// Behind a trusted proxy, the host it forwards is the page's.
	proxied := send("POST", "/api/projects", "https://dash.example.com", "X-Forwarded-Host", "dash.example.com")
	if proxied != http.StatusForbidden {
		t.Errorf("X-Forwarded-Host without -trust-proxy: %d", proxied)
	}
	a.TrustProxy = true
	if code := send("POST", "/api/projects", "https://dash.example.com", "X-Forwarded-Host", "dash.example.com"); code == http.StatusForbidden {
		t.Errorf("X-Forwarded-Host from a trusted proxy: refused")
	}
}
