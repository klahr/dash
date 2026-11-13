package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/orgdomain"
	"github.com/klahr/dash/internal/store"
)

// The authorization tests below run every call of recordEndpoints (and the
// org-wide ones of orgEndpoints) through the full router as someone who
// must not reach Default's records, and check that nothing of Default's
// leaked or changed. TestOrgCannotReachAnotherOrgsRecords does the same
// for another org's signed-in user.

// orgEndpoints are calls that name no record but read or change the
// caller's org's (or the caller's own) data.
func orgEndpoints() []endpoint {
	return []endpoint{
		{"GET", "/api/projects", ""},
		{"POST", "/api/projects", `{"name":"intruder"}`},
		{"GET", "/api/goals", ""},
		{"GET", "/api/previews", ""},
		{"DELETE", "/api/previews", ""},
		{"GET", "/api/permissions", ""},
		{"GET", "/api/chat", ""},
		{"DELETE", "/api/chat", ""},
		{"GET", "/api/machines", ""},
		{"GET", "/api/usage", ""},
		{"POST", "/api/uploads?name=x.png", "png"},
		{"POST", "/api/refine", `{"prompt":"x"}`},
		{"POST", "/api/split", `{"prompt":"x"}`},
		{"GET", "/api/hub/repos", ""},
		{"POST", "/api/queue/pause", ""},
		{"POST", "/api/queue/resume", ""},
		{"PUT", "/api/queue/pace", `{"follow":true}`},
		{"GET", "/ws/events", ""},
		{"GET", "/ws/chat", ""},
	}
}

// changes reports whether e is meant to change something.
func (e endpoint) changes() bool { return e.method != "GET" && e.method != "HEAD" }

// noLeak fails the test if w's answer shows anything of Default's.
func (o *twoOrgs) noLeak(t *testing.T, e endpoint, w *httptest.ResponseRecorder) {
	t.Helper()
	for _, secret := range []string{"secret", o.web.ID, o.todo.ID, o.web.Path} {
		if strings.Contains(w.Body.String(), secret) {
			t.Errorf("%s %s leaked %q: %.300s", e.method, e.path, secret, w.Body)
		}
	}
}

// withToken wraps h so every request carries secret as its bearer token.
func withToken(h http.Handler, secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+secret)
		h.ServeHTTP(w, r)
	})
}

// token makes an API token of uid's pinned to org.
func (o *twoOrgs) token(t *testing.T, uid, org int64, scopes []string) string {
	t.Helper()
	secret, _, err := o.s.Auth.Accounts().CreateToken(store.NewToken{Kind: store.TokenAPI, Name: "t", UserID: uid, OrgID: org, Scopes: scopes}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

// join makes uid a member of org too.
func (o *twoOrgs) join(t *testing.T, uid, org int64, email string) {
	t.Helper()
	acc := o.s.Auth.Accounts()
	tok, _, err := acc.Invite(ownerSession(t, o.s).User.ID, org, email, store.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acc.AcceptInvitation(tok, uid); err != nil {
		t.Fatal(err)
	}
}

// Requests without a session or a valid token are refused before they
// reach any handler.
func TestSignedOutRequestsAreRefused(t *testing.T) {
	parallel(t)

	o := newTwoOrgs(t)
	acc := o.s.Auth.Accounts()
	revoked, tok, err := acc.CreateToken(store.NewToken{Kind: store.TokenAPI, Name: "old", UserID: o.cUser.ID, OrgID: ownerSession(t, o.s).Org.ID, Scopes: store.UserScopes}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acc.RevokeToken(o.cUser.ID, tok.ID); err != nil {
		t.Fatal(err)
	}
	expired, _, err := acc.CreateToken(store.NewToken{Kind: store.TokenAPI, Name: "expired", UserID: o.cUser.ID, OrgID: ownerSession(t, o.s).Org.ID,
		Scopes: store.UserScopes, Expires: time.Now().Add(-time.Hour)}, time.Now().Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	stale := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.AddCookie(&http.Cookie{Name: auth.PlainCookieName, Value: "not-a-session"})
		o.h.ServeHTTP(w, r)
	})
	who := []struct {
		name string
		h    http.Handler
	}{
		{"no credentials", o.h},
		{"an unknown session cookie", stale},
		{"a made-up token", withToken(o.h, "dash_not-a-token")},
		{"a revoked token", withToken(o.h, revoked)},
		{"an expired token", withToken(o.h, expired)},
	}
	for _, c := range who {
		for _, e := range append(o.recordEndpoints(), orgEndpoints()...) {
			w := do(t, c.h, e.method, e.path, e.body)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with %s: want 401, got %d %.200s", e.method, e.path, c.name, w.Code, w.Body)
			}
			o.noLeak(t, e, w)
		}
		o.untouched(t, c.name)
	}
}

// refused reports whether w turned the request down: 404 for a record the
// caller can't see, or 403 for a call tokens may not make at all.
func refused(w *httptest.ResponseRecorder) bool {
	return w.Code == http.StatusNotFound || w.Code == http.StatusForbidden
}

// A token pinned to one org works in that org only, even for a user who is
// a member of both: it reaches nothing of another org of theirs.
func TestOrgPinnedTokenStaysInItsOrg(t *testing.T) {
	parallel(t)

	o := newTwoOrgs(t)
	o.join(t, o.cUser.ID, o.beta.ID, "c@example.com")
	def := ownerSession(t, o.s).Org.ID
	inBeta := withToken(o.h, o.token(t, o.cUser.ID, o.beta.ID, store.UserScopes))
	for _, e := range o.recordEndpoints() {
		w := do(t, inBeta, e.method, e.path, e.body)
		if !refused(w) {
			t.Errorf("%s %s with a token pinned to another org: want refusal, got %d %.200s", e.method, e.path, w.Code, w.Body)
		}
		o.noLeak(t, e, w)
		o.untouched(t, e.method+" "+e.path)
	}
	for _, e := range orgEndpoints() {
		if e.changes() || strings.HasPrefix(e.path, "/ws/") {
			continue
		}
		w := do(t, inBeta, e.method, e.path, e.body)
		o.noLeak(t, e, w)
	}
	o.untouched(t, "listing")

	// The platform admin's token, pinned to Beta, doesn't reach Default's
	// records either.
	if _, err := o.s.Auth.Accounts().Membership(ownerSession(t, o.s).User.ID, o.beta.ID); err == nil {
		admin := withToken(o.h, o.token(t, ownerSession(t, o.s).User.ID, o.beta.ID, store.UserScopes))
		for _, e := range o.recordEndpoints() {
			if w := do(t, admin, e.method, e.path, e.body); !refused(w) {
				t.Errorf("%s %s with an admin's token pinned to another org: want refusal, got %d %.200s", e.method, e.path, w.Code, w.Body)
			}
		}
		o.untouched(t, "the admin's token")
	}

	// The same user's token pinned to Default does reach them (a sample),
	// so the 404s above are about the org.
	inDefault := withToken(o.h, o.token(t, o.cUser.ID, def, store.UserScopes))
	for _, path := range []string{"/api/projects/" + o.web.ID, "/api/todos/" + o.todo.ID, "/api/projects/" + o.web.ID + "/webhooks"} {
		if w := do(t, inDefault, "GET", path, ""); w.Code != http.StatusOK {
			t.Errorf("GET %s with a token of the org: %d %s", path, w.Code, w.Body)
		}
	}
}

// A read-only token may look at its org's records but change none of them.
func TestReadOnlyTokenChangesNothing(t *testing.T) {
	parallel(t)

	o := newTwoOrgs(t)
	ro := withToken(o.h, o.token(t, o.cUser.ID, ownerSession(t, o.s).Org.ID, []string{"read"}))
	for _, e := range append(o.recordEndpoints(), orgEndpoints()...) {
		if !e.changes() {
			continue
		}
		if w := do(t, ro, e.method, e.path, e.body); w.Code != http.StatusForbidden {
			t.Errorf("%s %s with a read-only token: want 403, got %d %.200s", e.method, e.path, w.Code, w.Body)
		}
		o.untouched(t, e.method+" "+e.path)
	}
	if w := do(t, ro, "GET", "/api/todos/"+o.todo.ID, ""); w.Code != http.StatusOK {
		t.Errorf("reading with a read-only token: %d %s", w.Code, w.Body)
	}
}

// crossOrigin is a signed-in browser of c's, in Default, that sends each
// request from the page at origin ("": no Origin header) to host.
func (o *twoOrgs) crossOrigin(t *testing.T, cookies []*http.Cookie, host string) func(e endpoint, origin string) *httptest.ResponseRecorder {
	return func(e endpoint, origin string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(e.method, "https://"+host+e.path, strings.NewReader(e.body))
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if strings.HasPrefix(e.path, "/ws/") {
			r.Header.Set("Connection", "Upgrade")
			r.Header.Set("Upgrade", "websocket")
			r.Header.Set("Sec-WebSocket-Version", "13")
			r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		}
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		o.h.ServeHTTP(w, r)
		return w
	}
}

// login signs name in at host and returns the session's cookies.
func (o *twoOrgs) login(t *testing.T, host, name, password string) []*http.Cookie {
	t.Helper()
	r := httptest.NewRequest("POST", "https://"+host+"/api/login", strings.NewReader(`{"username":"`+name+`","password":"`+password+`"}`))
	r.Header.Set("Origin", "https://"+host)
	w := httptest.NewRecorder()
	o.h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || len(w.Result().Cookies()) == 0 {
		t.Fatalf("login %s at %s: %d %s", name, host, w.Code, w.Body)
	}
	return w.Result().Cookies()
}

// statefulEndpoints are the calls of recordEndpoints and orgEndpoints that
// change something, or open a socket that can.
func (o *twoOrgs) statefulEndpoints() []endpoint {
	var out []endpoint
	for _, e := range append(o.recordEndpoints(), orgEndpoints()...) {
		if e.changes() || strings.HasPrefix(e.path, "/ws/") {
			out = append(out, e)
		}
	}
	return out
}

// A page of another site (or another org's subdomain) can't use a
// signed-in browser's cookie to change anything, nor open a socket.
func TestCrossOriginWritesAreRefused(t *testing.T) {
	parallel(t)

	o := newTwoOrgs(t)
	d, err := orgdomain.New("dash.example.com", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	d.UseDefaults("https://dash.example.com")
	o.s.Auth.Domains = d
	o.s.muxOnce, o.s.muxv = sync.Once{}, nil // the subdomain routes are added when the router is built
	o.h = o.s.Routes()
	base := o.login(t, "dash.example.com", "c", "c-password")

	// Hand the session over to Default's subdomain, as /go/ does.
	r := httptest.NewRequest("GET", "https://dash.example.com/go/default", nil)
	for _, c := range base {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	o.h.ServeHTTP(w, r)
	if w.Header().Get("Location") == "" {
		t.Fatalf("/go/default: %d %s", w.Code, w.Body)
	}
	r = httptest.NewRequest("GET", w.Header().Get("Location"), nil)
	w = httptest.NewRecorder()
	o.h.ServeHTTP(w, r)
	org := w.Result().Cookies()
	if w.Code != http.StatusSeeOther || len(org) == 0 {
		t.Fatalf("handoff to the org's subdomain: %d %s", w.Code, w.Body)
	}

	for _, c := range []struct {
		host    string
		cookies []*http.Cookie
	}{{"default.dash.example.com", org}, {"dash.example.com", base}} {
		send := o.crossOrigin(t, c.cookies, c.host)
		for _, from := range []string{"https://evil.example.net", "https://beta.dash.example.com", "null"} {
			if from == "https://"+c.host {
				continue
			}
			for _, e := range o.statefulEndpoints() {
				if w := send(e, from); w.Code != http.StatusForbidden {
					t.Errorf("%s https://%s%s from %s: want 403, got %d %.200s", e.method, c.host, e.path, from, w.Code, w.Body)
				}
				o.untouched(t, e.method+" "+e.path+" from "+from)
			}
		}
	}

	// Reading from another origin is the browser's business (CORS: Dash
	// sends no Access-Control-Allow-Origin); the same page may write.
	send := o.crossOrigin(t, org, "default.dash.example.com")
	if w := send(endpoint{"GET", "/api/todos/" + o.todo.ID, ""}, "https://evil.example.net"); w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("cross-origin read: %d %v", w.Code, w.Header())
	}
	if w := send(endpoint{"DELETE", "/api/chat", ""}, "https://default.dash.example.com"); w.Code != http.StatusNoContent {
		t.Errorf("same-origin write: %d %s", w.Code, w.Body)
	}
}

// Without org subdomains Dash is one origin: a page elsewhere (another
// site, or another app on a subdomain of the same site, which SameSite
// cookies don't keep out) can't use the cookie to change anything either.
func TestCrossOriginWritesAreRefusedWithoutOrgDomains(t *testing.T) {
	parallel(t)

	o := newTwoOrgs(t)
	cookies := o.login(t, "dash.example.com", "c", "c-password")
	send := o.crossOrigin(t, cookies, "dash.example.com")
	for _, from := range []string{"https://evil.example.net", "https://wiki.example.com", "null"} {
		for _, e := range o.statefulEndpoints() {
			if w := send(e, from); w.Code != http.StatusForbidden {
				t.Errorf("%s %s from %s: want 403, got %d %.200s", e.method, e.path, from, w.Code, w.Body)
			}
			o.untouched(t, e.method+" "+e.path+" from "+from)
		}
	}
	if w := send(endpoint{"DELETE", "/api/chat", ""}, "https://dash.example.com"); w.Code != http.StatusNoContent {
		t.Errorf("same-origin write: %d %s", w.Code, w.Body)
	}
	if w := send(endpoint{"DELETE", "/api/chat", ""}, ""); w.Code != http.StatusNoContent {
		t.Errorf("write without an Origin (not a browser): %d %s", w.Code, w.Body)
	}
}
