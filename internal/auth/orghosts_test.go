package auth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/orgdomain"
	"github.com/klahr/dash/internal/store"
)

const baseDomain = "dash.example.com"

// domainServer is an Auth with org subdomains of baseDomain, serving
// "private" behind the login, and the env user's default org, "default".
func domainServer(t *testing.T) (*Auth, http.Handler) {
	t.Helper()
	a := newAuth(t, "admin", "s3cret")
	d, err := orgdomain.New(baseDomain, nil, "preview."+baseDomain)
	if err != nil {
		t.Fatal(err)
	}
	a.Domains = d
	return a, serve(a)
}

// browser keeps cookies per host, as a browser keeps host-only cookies.
type browser struct {
	t       *testing.T
	h       http.Handler
	cookies map[string]*http.Cookie
	origin  string // sent as Origin on POSTs: the page's ("" = the request's own host)
}

func newBrowser(t *testing.T, h http.Handler) *browser {
	return &browser{t: t, h: h, cookies: map[string]*http.Cookie{}}
}

// do sends a request to rawURL (https://host/path).
func (b *browser) do(method, rawURL, body string) *httptest.ResponseRecorder {
	b.t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		b.t.Fatal(err)
	}
	r := httptest.NewRequest(method, rawURL, strings.NewReader(body))
	r.RemoteAddr = "10.0.0.1:1234"
	r.Host = u.Host
	if method == "POST" || method == "PATCH" || method == "DELETE" {
		o := b.origin
		if o == "" {
			o = "https://" + u.Host
		}
		r.Header.Set("Origin", o)
	}
	if c := b.cookies[u.Host]; c != nil {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	b.h.ServeHTTP(w, r)
	for _, c := range w.Result().Cookies() {
		if c.Name == CookieName {
			if c.MaxAge < 0 {
				delete(b.cookies, u.Host)
			} else {
				b.cookies[u.Host] = c
			}
		}
	}
	return w
}

func (b *browser) login(user, password string) {
	b.t.Helper()
	w := b.do("POST", "https://"+baseDomain+"/api/login", fmt.Sprintf(`{"username":%q,"password":%q}`, user, password))
	if w.Code != http.StatusOK {
		b.t.Fatalf("login: %d %s", w.Code, w.Body)
	}
}

// enter goes to slug's subdomain through /go on the base domain and the
// handoff, and returns the handoff's URL.
func (b *browser) enter(slug, next string) string {
	b.t.Helper()
	u := "https://" + baseDomain + "/go/" + slug
	if next != "" {
		u += "?next=" + url.QueryEscape(next)
	}
	w := b.do("GET", u, "")
	if w.Code != http.StatusSeeOther {
		b.t.Fatalf("go %s: %d %s", slug, w.Code, w.Body)
	}
	hand := w.Header().Get("Location")
	if !strings.HasPrefix(hand, "https://"+slug+"."+baseDomain+HandoffPath+"?t=") {
		b.t.Fatalf("go %s: handed to %q", slug, hand)
	}
	w = b.do("GET", hand, "")
	if w.Code != http.StatusSeeOther {
		b.t.Fatalf("handoff: %d %s", w.Code, w.Body)
	}
	return hand
}

func orgHost(slug string) string { return "https://" + slug + "." + baseDomain }

func TestOrgDomainsOffByDefault(t *testing.T) {
	a, h := newServer(t)
	if a.Domains != nil {
		t.Fatal("org subdomains on by default")
	}
	b := newBrowser(t, h)
	b.login("admin", "s3cret")
	// Without the setting, a subdomain-looking host is just a host: its
	// cookie works there and nothing is resolved from the name.
	b.cookies["acme."+baseDomain] = b.cookies[baseDomain]
	if w := b.do("GET", orgHost("acme")+"/api/me", ""); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "org_domains") {
		t.Fatalf("me on a subdomain without org domains: %d %s", w.Code, w.Body)
	}
	if w := b.do("GET", "https://"+baseDomain+"/", ""); w.Body.String() != "private" {
		t.Fatalf("/ isn't the board without org domains: %d %s", w.Code, w.Body)
	}
	if w := b.do("GET", "https://"+baseDomain+"/go/default", ""); w.Body.String() != "private" {
		t.Fatalf("/go is routed without org domains: %d %s", w.Code, w.Body)
	}
}

func TestOrgHostResolvesOrg(t *testing.T) {
	a, h := domainServer(t)
	admin := mustEnv(t, a)
	acme, err := a.acc.CreateOrg(admin, "Acme")
	if err != nil {
		t.Fatal(err)
	}
	b := newBrowser(t, h)
	b.login("admin", "s3cret")
	b.enter("acme", "/board?x=1")

	// The host decides the org, whatever the session's active org is.
	var me meResp
	w := b.do("GET", orgHost("acme")+"/api/me", "")
	decode(t, w, &me)
	if me.Org == nil || me.Org.ID != acme.ID {
		t.Fatalf("org on acme's host: %+v", me.Org)
	}
	if !strings.Contains(w.Body.String(), `"org_url":"https://acme.`+baseDomain+`"`) {
		t.Errorf("me lacks the org's address: %s", w.Body)
	}
	if w := b.do("GET", orgHost("acme")+"/anything", ""); w.Body.String() != "private" {
		t.Fatalf("page on acme's host: %d %s", w.Code, w.Body)
	}
	// Switching orgs is going to the other org's host.
	if w := b.do("POST", orgHost("acme")+"/api/auth/switch-org", fmt.Sprintf(`{"org_id":%d}`, acme.ID)); w.Code != http.StatusConflict {
		t.Errorf("switch-org on an org host: %d", w.Code)
	}
}

func TestOrgHostBaseDomainPicker(t *testing.T) {
	a, h := domainServer(t)
	b := newBrowser(t, h)
	// Signed out: the base domain's / is the sign-in page (or landing).
	if w := b.do("GET", "https://"+baseDomain+"/", ""); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login.html" {
		t.Fatalf("signed-out /: %d %q", w.Code, w.Header().Get("Location"))
	}
	b.login("admin", "s3cret")
	// One org: straight there.
	if w := b.do("GET", "https://"+baseDomain+"/", ""); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/go/default" {
		t.Fatalf("picker with one org: %d %q", w.Code, w.Header().Get("Location"))
	}
	if _, err := a.acc.CreateOrg(mustEnv(t, a), "Acme"); err != nil {
		t.Fatal(err)
	}
	w := b.do("GET", "https://"+baseDomain+"/", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `href="/go/acme"`) || !strings.Contains(w.Body.String(), `href="/go/default"`) {
		t.Fatalf("picker: %d %s", w.Code, w.Body)
	}
	// Sign-in pages stay on the base domain.
	if w := b.do("GET", orgHost("acme")+"/login.html", ""); w.Header().Get("Location") != "https://"+baseDomain+"/go/acme" {
		t.Errorf("login on an org host goes to %q", w.Header().Get("Location"))
	}
	if w := b.do("POST", orgHost("acme")+"/api/login", `{"username":"admin","password":"s3cret"}`); w.Code != http.StatusNotFound {
		t.Errorf("api login on an org host: %d", w.Code)
	}
}

func TestOrgHostSignedOutGoesThroughBase(t *testing.T) {
	_, h := domainServer(t)
	b := newBrowser(t, h)
	w := b.do("GET", orgHost("acme")+"/x?y=1", "")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "https://"+baseDomain+"/go/acme?next=%2Fx%3Fy%3D1" {
		t.Fatalf("signed-out page: %d %q", w.Code, w.Header().Get("Location"))
	}
	// The same for an org that doesn't exist: nothing told.
	w2 := b.do("GET", orgHost("nosuch")+"/x?y=1", "")
	if w2.Code != w.Code || strings.Replace(w2.Header().Get("Location"), "nosuch", "acme", 1) != w.Header().Get("Location") {
		t.Fatalf("unknown org answers differently: %d %q", w2.Code, w2.Header().Get("Location"))
	}
	if w := b.do("GET", orgHost("acme")+"/api/me", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("signed-out API: %d", w.Code)
	}
	// /go on the base domain then asks to sign in, and comes back.
	w = b.do("GET", "https://"+baseDomain+"/go/acme?next=%2Fx", "")
	if loc := w.Header().Get("Location"); w.Code != http.StatusSeeOther || loc != "/login.html?next="+url.QueryEscape("/go/acme?next=%2Fx") {
		t.Fatalf("signed-out /go: %d %q", w.Code, loc)
	}
}

func TestOrgHostNonMemberGets404(t *testing.T) {
	a, h := domainServer(t)
	admin := mustEnv(t, a)
	if _, err := a.acc.CreateOrg(admin, "Acme"); err != nil {
		t.Fatal(err)
	}
	def, _ := a.acc.DefaultOrg()
	addUser(t, a, def.ID, "bob", store.RoleMember)
	b := newBrowser(t, h)
	b.login("bob", "pw-bob-long")

	// /go: the same for an org bob isn't in and one that doesn't exist.
	w1 := b.do("GET", "https://"+baseDomain+"/go/acme", "")
	w2 := b.do("GET", "https://"+baseDomain+"/go/nosuch", "")
	if w1.Code != http.StatusNotFound || w2.Code != http.StatusNotFound || w1.Body.String() != w2.Body.String() {
		t.Fatalf("/go for another org: %d, unknown: %d", w1.Code, w2.Code)
	}
	if !strings.Contains(w1.Body.String(), `href="https://`+baseDomain+`/"`) {
		t.Errorf("404 page doesn't link to the base domain: %s", w1.Body)
	}
	// A session on the host (say a member removed since) gets the same.
	b.enter("default", "")
	b.cookies["acme."+baseDomain] = b.cookies["default."+baseDomain]
	b.cookies["nosuch."+baseDomain] = b.cookies["default."+baseDomain]
	for _, p := range []string{"/", "/api/me", "/api/projects"} {
		w1, w2 := b.do("GET", orgHost("acme")+p, ""), b.do("GET", orgHost("nosuch")+p, "")
		if w1.Code != http.StatusNotFound || w2.Code != http.StatusNotFound || w1.Body.String() != w2.Body.String() {
			t.Errorf("%s: non-member %d %s, unknown %d %s", p, w1.Code, w1.Body, w2.Code, w2.Body)
		}
	}
	// Removing bob from the org takes effect at the next request.
	if err := a.acc.RemoveMember(admin, def.ID, mustUser(t, a, "bob")); err != nil {
		t.Fatal(err)
	}
	if w := b.do("GET", orgHost("default")+"/api/me", ""); w.Code != http.StatusNotFound {
		t.Errorf("removed member on the org host: %d", w.Code)
	}
}

func mustUser(t *testing.T, a *Auth, name string) int64 {
	t.Helper()
	u, _, err := a.acc.Credentials(name)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func TestOrgHostInvalidAndReservedNames(t *testing.T) {
	_, h := domainServer(t)
	b := newBrowser(t, h)
	if w := b.do("GET", "https://www."+baseDomain+"/a?b=1", ""); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "https://"+baseDomain+"/a?b=1" {
		t.Errorf("www: %d %q", w.Code, w.Header().Get("Location"))
	}
	for _, host := range []string{"admin." + baseDomain, "a." + baseDomain, "x.y." + baseDomain, "-ab." + baseDomain} {
		if w := b.do("GET", "https://"+host+"/", ""); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "No such organization") {
			t.Errorf("%s: %d", host, w.Code)
		}
	}
}

func TestSessionCookieIsHostOnly(t *testing.T) {
	_, h := domainServer(t)
	b := newBrowser(t, h)
	w := b.do("POST", "https://"+baseDomain+"/api/login", `{"username":"admin","password":"s3cret"}`)
	for _, c := range w.Header().Values("Set-Cookie") {
		if strings.Contains(strings.ToLower(c), "domain=") {
			t.Errorf("login cookie has a Domain: %s", c)
		}
	}
	w = b.do("GET", "https://"+baseDomain+"/go/default", "")
	w = b.do("GET", w.Header().Get("Location"), "")
	cs := w.Header().Values("Set-Cookie")
	if len(cs) == 0 {
		t.Fatal("handoff set no cookie")
	}
	for _, c := range cs {
		if strings.Contains(strings.ToLower(c), "domain=") {
			t.Errorf("handoff cookie has a Domain: %s", c)
		}
		if !strings.Contains(c, "HttpOnly") || !strings.Contains(c, "SameSite=Strict") {
			t.Errorf("handoff cookie: %s", c)
		}
	}
}

func TestHandoffTokenSingleUseAndExpiring(t *testing.T) {
	a, h := domainServer(t)
	b := newBrowser(t, h)
	b.login("admin", "s3cret")
	hand := b.enter("default", "/after")
	if w := b.do("GET", orgHost("default")+"/api/me", ""); w.Code != http.StatusOK {
		t.Fatalf("after handoff: %d", w.Code)
	}

	// Used once: another browser (or the same one again) can't use it.
	other := newBrowser(t, h)
	if w := other.do("GET", hand, ""); w.Code != http.StatusForbidden || other.cookies["default."+baseDomain] != nil {
		t.Fatalf("reused handoff: %d", w.Code)
	}

	// Expired.
	w := b.do("GET", "https://"+baseDomain+"/go/default", "")
	hand = w.Header().Get("Location")
	now := time.Now()
	a.now = func() time.Time { return now.Add(HandoffTTL + time.Second) }
	if w := newBrowser(t, h).do("GET", hand, ""); w.Code != http.StatusForbidden {
		t.Fatalf("expired handoff: %d", w.Code)
	}
	a.now = time.Now

	// Only for the host it was made for, and not tampered with.
	if _, err := a.acc.CreateOrg(mustEnv(t, a), "Acme"); err != nil {
		t.Fatal(err)
	}
	hand = b.do("GET", "https://"+baseDomain+"/go/default", "").Header().Get("Location")
	if w := newBrowser(t, h).do("GET", strings.Replace(hand, "default.", "acme.", 1), ""); w.Code != http.StatusForbidden {
		t.Errorf("handoff on another org's host: %d", w.Code)
	}
	u, _ := url.Parse(hand)
	tok := u.Query().Get("t")
	p64, sig, _ := strings.Cut(tok, ".")
	pb, _ := base64Decode(p64)
	forged := strings.Replace(string(pb), "|1|", "|2|", 1)
	q := u.Query()
	q.Set("t", base64Encode([]byte(forged))+"."+sig)
	u.RawQuery = q.Encode()
	if w := newBrowser(t, h).do("GET", u.String(), ""); w.Code != http.StatusForbidden {
		t.Errorf("tampered handoff: %d", w.Code)
	}
	// The untouched one still works (the failures above didn't spend it).
	if w := newBrowser(t, h).do("GET", hand, ""); w.Code != http.StatusSeeOther {
		t.Errorf("good handoff after bad ones: %d", w.Code)
	}
	// next stays on the host.
	for _, next := range []string{"//evil.example", "https://evil.example/", "/\\evil.example"} {
		b2 := newBrowser(t, h)
		b2.login("admin", "s3cret")
		hand := b2.do("GET", "https://"+baseDomain+"/go/default?next="+url.QueryEscape(next), "").Header().Get("Location")
		if w := b2.do("GET", hand, ""); w.Header().Get("Location") != "/" {
			t.Errorf("next %q: went to %q", next, w.Header().Get("Location"))
		}
	}
}

func TestLogoutOnOrgHostSignsOutEverywhere(t *testing.T) {
	_, h := domainServer(t)
	b := newBrowser(t, h)
	b.login("admin", "s3cret")
	b.enter("default", "")
	base := b.cookies[baseDomain]
	if w := b.do("POST", orgHost("default")+"/api/logout", ""); w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", w.Code)
	}
	b.cookies[baseDomain] = base // the org host can't clear the base domain's cookie …
	if w := b.do("GET", "https://"+baseDomain+"/api/me", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("base session after signing out on the org host: %d", w.Code) // … but its session is gone
	}
}

func TestOrgHostOriginCheck(t *testing.T) {
	a, h := domainServer(t)
	if _, err := a.acc.CreateOrg(mustEnv(t, a), "Acme"); err != nil {
		t.Fatal(err)
	}
	b := newBrowser(t, h)
	b.login("admin", "s3cret")
	b.enter("acme", "")
	for _, o := range []string{"https://default." + baseDomain, "https://x.preview." + baseDomain, "https://" + baseDomain, "https://evil.example", "null"} {
		b.origin = o
		if w := b.do("POST", orgHost("acme")+"/api/orgs", `{"name":"x"}`); w.Code != http.StatusForbidden {
			t.Errorf("POST from %s: %d", o, w.Code)
		}
	}
	b.origin = ""
	if w := b.do("POST", orgHost("acme")+"/api/orgs", `{"name":"x"}`); w.Code != http.StatusCreated {
		t.Errorf("same-origin POST: %d %s", w.Code, w.Body)
	}
	// Pages may only be framed by their own host.
	if w := b.do("GET", orgHost("acme")+"/", ""); !strings.HasSuffix(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'self'") {
		t.Errorf("CSP: %q", w.Header().Get("Content-Security-Policy"))
	}
}

func TestWithFrameAncestorsSelf(t *testing.T) {
	for in, want := range map[string]string{
		"":                                     "frame-ancestors 'self'",
		ContentSecurityPolicy:                  ContentSecurityPolicy,
		"script-src 'self'":                    "script-src 'self'; frame-ancestors 'self'",
		"frame-ancestors *; script-src 'self'": "script-src 'self'; frame-ancestors 'self'",
	} {
		if got := withFrameAncestorsSelf(in); got != want {
			t.Errorf("withFrameAncestorsSelf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOrgHostTrustProxy(t *testing.T) {
	a, h := domainServer(t)
	a.TrustProxy = true
	b := newBrowser(t, h)
	b.login("admin", "s3cret")
	hand := b.do("GET", "https://"+baseDomain+"/go/default", "").Header().Get("Location")
	u, _ := url.Parse(hand)
	// Behind a proxy: Host is the upstream's, X-Forwarded-Host the browser's.
	r := httptest.NewRequest("GET", "http://127.0.0.1:7788"+u.RequestURI(), nil)
	r.Header.Set("X-Forwarded-Host", u.Host)
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("handoff through the proxy: %d %s", w.Code, w.Body)
	}
	// A forwarded host that isn't Dash's is ignored, not trusted.
	if got := a.reqHost(withHeaders("127.0.0.1:7788", "X-Forwarded-Host", "evil.example")); got != "127.0.0.1:7788" {
		t.Errorf("foreign forwarded host taken: %q", got)
	}
	if got := a.reqHost(withHeaders("127.0.0.1:7788", "X-Forwarded-Host", "acme."+baseDomain)); got != "acme."+baseDomain {
		t.Errorf("org forwarded host not taken: %q", got)
	}
}

func withHeaders(host string, kv ...string) *http.Request {
	r := httptest.NewRequest("GET", "http://"+host+"/", nil)
	for i := 0; i+1 < len(kv); i += 2 {
		r.Header.Set(kv[i], kv[i+1])
	}
	return r
}

func TestRenameOrgSlug(t *testing.T) {
	a, h := domainServer(t)
	def, _ := a.acc.DefaultOrg()
	addUser(t, a, def.ID, "bob", store.RoleMember)
	path := fmt.Sprintf("https://%s/api/orgs/%d", baseDomain, def.ID)

	bob := newBrowser(t, h)
	bob.login("bob", "pw-bob-long")
	if w := bob.do("PATCH", path, `{"slug":"mine","password":"pw-bob-long"}`); w.Code != http.StatusForbidden {
		t.Errorf("member renames: %d", w.Code)
	}
	owner := newBrowser(t, h)
	owner.login("admin", "s3cret")
	for slug, code := range map[string]int{"admin": http.StatusBadRequest, "preview": http.StatusBadRequest, "ab": http.StatusBadRequest, "Bad_Name": http.StatusBadRequest} {
		if w := owner.do("PATCH", path, fmt.Sprintf(`{"slug":%q,"password":"s3cret"}`, slug)); w.Code != code {
			t.Errorf("slug %q: %d %s", slug, w.Code, w.Body)
		}
	}
	if w := owner.do("PATCH", path, `{"slug":"acme","password":"s3cret"}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"slug":"acme"`) {
		t.Fatalf("rename: %d %s", w.Code, w.Body)
	}
	// The old host leads members to the new one.
	bob.enter("acme", "")
	bob.cookies["default."+baseDomain] = bob.cookies["acme."+baseDomain]
	if w := bob.do("GET", orgHost("default")+"/p?q=1", ""); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != orgHost("acme")+"/p?q=1" {
		t.Errorf("old host: %d %q", w.Code, w.Header().Get("Location"))
	}
	// … and /go with the old name too.
	w := bob.do("GET", "https://"+baseDomain+"/go/default", "")
	if !strings.HasPrefix(w.Header().Get("Location"), orgHost("acme")+HandoffPath) {
		t.Errorf("/go old name: %q", w.Header().Get("Location"))
	}
	var members struct {
		URL       string               `json:"url"`
		Redirects map[string]time.Time `json:"slug_redirects"`
	}
	decode(t, owner.do("GET", fmt.Sprintf("https://%s/api/orgs/%d/members", baseDomain, def.ID), ""), &members)
	if members.URL != orgHost("acme") || members.Redirects["default"].IsZero() {
		t.Errorf("members: %+v", members)
	}
}

func TestMCPTokenPinnedBySubdomain(t *testing.T) {
	a, _, h := newOAuthServer(t)
	d, err := orgdomain.New(baseDomain, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	a.Domains = d
	admin := mustEnv(t, a)
	acme, err := a.acc.CreateOrg(admin, "Acme")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.acc.CreateOrg(admin, "Beta"); err != nil {
		t.Fatal(err)
	}
	host := "acme." + baseDomain

	// The metadata on acme's host names acme's host.
	var meta map[string]any
	w := hostReq(h, "GET", host, "/.well-known/oauth-authorization-server", "", "")
	if err := json.Unmarshal(w.Body.Bytes(), &meta); err != nil || meta["issuer"] != "https://"+host || meta["token_endpoint"] != "https://"+host+"/token" {
		t.Fatalf("metadata on an org host: %v %s", err, w.Body)
	}

	// Authorizing there needs no org picked (admin is in three) and pins acme.
	c := register(t, h, `{"redirect_uris":["`+callback+`"]}`)
	p := authParams(c["client_id"].(string))
	p.Set("resource", "https://"+host+"/mcp")
	p.Set("username", "admin")
	p.Set("password", "s3cret")
	p.Set("action", "allow")
	w = hostForm(h, host, "/authorize", p)
	if w.Code != http.StatusFound {
		t.Fatalf("authorize on acme's host: %d %s", w.Code, w.Body)
	}
	loc, _ := url.Parse(w.Header().Get("Location"))
	if loc.Query().Get("iss") != "https://"+host {
		t.Errorf("iss: %q", loc.Query().Get("iss"))
	}
	tv := url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "client_id": {c["client_id"].(string)},
		"redirect_uri": {callback}, "code_verifier": {verifier}, "resource": {"https://" + host + "/mcp"}}
	w = hostForm(h, host, "/token", tv)
	if w.Code != http.StatusOK {
		t.Fatalf("token on acme's host: %d %s", w.Code, w.Body)
	}
	access := decodeJSON(t, w)["access_token"].(string)
	_, s, err := a.acc.Authenticate(access, time.Now(), store.TokenOAuth)
	if err != nil || s.Org == nil || s.Org.ID != acme.ID {
		t.Fatalf("token's org: %+v %v", s.Org, err)
	}
	if w := hostReq(h, "POST", host, "/mcp", "{}", access); w.Code != http.StatusOK {
		t.Errorf("acme's token on acme's host: %d", w.Code)
	}
	for _, other := range []string{"beta." + baseDomain, "default." + baseDomain, "nosuch." + baseDomain} {
		if w := hostReq(h, "POST", other, "/mcp", "{}", access); w.Code != http.StatusUnauthorized ||
			!strings.Contains(w.Header().Get("WWW-Authenticate"), "https://"+other+"/.well-known/") {
			t.Errorf("acme's token on %s: %d %q", other, w.Code, w.Header().Get("WWW-Authenticate"))
		}
	}
	// The base domain takes tokens of any org, as before.
	if w := hostReq(h, "POST", "dash.example.com", "/mcp", "{}", access); w.Code != http.StatusOK {
		t.Errorf("acme's token on the base domain: %d", w.Code)
	}

	// Someone who isn't a member gets no code on the org's host.
	def, _ := a.acc.DefaultOrg()
	addUser(t, a, def.ID, "eve", store.RoleMember)
	p.Set("username", "eve")
	p.Set("password", "pw-eve-long")
	if w := hostForm(h, host, "/authorize", p); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "isn&#39;t a member") {
		t.Errorf("non-member authorizes on acme's host: %d %s", w.Code, w.Body)
	}
}

func TestAPITokenPinnedBySubdomain(t *testing.T) {
	a, h := domainServer(t)
	admin := mustEnv(t, a)
	acme, _ := a.acc.CreateOrg(admin, "Acme")
	tok, _, err := a.acc.CreateToken(store.NewToken{Kind: store.TokenAPI, Name: "ci", UserID: admin, OrgID: acme.ID, Scopes: []string{store.ScopeRead}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if w := hostReq(h, "GET", "acme."+baseDomain, "/api/me", "", tok); w.Code != http.StatusOK {
		t.Errorf("acme token on acme: %d %s", w.Code, w.Body)
	}
	if w := hostReq(h, "GET", "default."+baseDomain, "/api/me", "", tok); w.Code != http.StatusUnauthorized {
		t.Errorf("acme token on default: %d", w.Code)
	}
}

func hostReq(h http.Handler, method, host, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://"+host+path, strings.NewReader(body))
	r.Host = host
	r.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func hostForm(h http.Handler, host, path string, v url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "https://"+host+path, strings.NewReader(v.Encode()))
	r.Host = host
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "10.0.0.1:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func base64Decode(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
func base64Encode(b []byte) string          { return base64.RawURLEncoding.EncodeToString(b) }
