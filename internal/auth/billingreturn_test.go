package auth

import (
	"net/http"
	"strings"
	"testing"
)

// Coming back from Stripe is cross-site, so the browser leaves the
// SameSite=Strict session cookie off: a signed-in user's /?billing=done
// looked signed out. The return hop is public and goes on to it with a
// page of Dash's own, whose navigation does carry the cookie.
func TestBillingReturn(t *testing.T) {
	t.Parallel()

	_, h := newServer(t)
	c := sessionCookie(t, do(h, "POST", "/api/login", `{"username":"admin","password":"s3cret"}`))
	if c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie: %+v, want SameSite=Strict", c)
	}

	// What used to happen: the cookie-less landing on /?billing=done.
	if w := do(h, "GET", "/?billing=done", ""); w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/login.html") {
		t.Fatalf("cookie-less /?billing=done: %d %q, want the sign-in redirect", w.Code, w.Header().Get("Location"))
	}

	for _, to := range []string{"done", "canceled", "portal"} {
		// No cookie: the hop is public.
		w := do(h, "GET", BillingReturnPath+"?to="+to, "")
		want := `<meta http-equiv="refresh" content="0;url=/?billing=` + to + `">`
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
			t.Fatalf("%s: %d, want a page refreshing to /?billing=%s:\n%s", to, w.Code, to, w.Body)
		}
		if w.Header().Get("Location") != "" {
			t.Errorf("%s: redirects (%q): the next request would still be cross-site", to, w.Header().Get("Location"))
		}
	}
	// Where it goes on to works with the cookie the same-site request carries.
	if w := do(h, "GET", "/?billing=done", "", c); w.Code != http.StatusOK || w.Body.String() != "private" {
		t.Fatalf("/?billing=done with the session: %d %q", w.Code, w.Body)
	}

	for _, to := range []string{"", "https://evil.example", "//evil.example", "/settings", "done&x=1", "DONE"} {
		w := do(h, "GET", BillingReturnPath+"?to="+strings.ReplaceAll(to, "&", "%26"), "")
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "http-equiv") {
			t.Errorf("to=%q: %d, want 400 and no refresh:\n%s", to, w.Code, w.Body)
		}
	}
}
