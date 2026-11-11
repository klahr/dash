package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/orgdomain"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/totp"
)

// ---- a fake Google: discovery, keys, and a token endpoint ----

// fakeIdPKeys are the fake Google's signing key and the "bad signature"
// key, made once: generating RSA keys took most of these tests' time. Every
// fakeIdP serves on its own httptest server, so sharing them crosses
// nothing.
var fakeIdPKeys = sync.OnceValues(func() ([2]*rsa.PrivateKey, error) {
	var keys [2]*rsa.PrivateKey
	for i := range keys {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return keys, err
		}
		keys[i] = k
	}
	return keys, nil
})

type fakeIdP struct {
	t       *testing.T
	srv     *httptest.Server
	key     *rsa.PrivateKey
	other   *rsa.PrivateKey   // signs "bad signature" tokens, under the same kid
	clients map[string]string // client id → secret
	mu      sync.Mutex
	grants  map[string]fakeGrant // by code
}

type fakeGrant struct {
	claims    map[string]any
	challenge string
	client    string
	badSig    bool
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	p := &fakeIdP{t: t, clients: map[string]string{}, grants: map[string]fakeGrant{}}
	keys, err := fakeIdPKeys()
	if err != nil {
		t.Fatal(err)
	}
	p.key, p.other = keys[0], keys[1]
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": p.srv.URL, "authorization_endpoint": p.srv.URL + "/auth", "token_endpoint": p.srv.URL + "/token",
			"jwks_uri": p.srv.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		pub := p.key.PublicKey
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("POST /token", p.token)
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakeIdP) token(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
	}
	if want, known := p.clients[id]; !known || want != secret {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid_client"}`))
		return
	}
	p.mu.Lock()
	g, ok := p.grants[r.Form.Get("code")]
	delete(p.grants, r.Form.Get("code"))
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
	if !ok || g.client != id || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	key := p.key
	if g.badSig {
		key = p.other
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": p.sign(key, g.claims)})
}

func (p *fakeIdP) sign(key *rsa.PrivateKey, claims map[string]any) string {
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	in := enc(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"}) + "." + enc(claims)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		p.t.Fatal(err)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// person is who signs in at the fake Google; over changes the ID token's
// claims (nil values delete them).
type person struct {
	sub, email, hd string
	over           map[string]any
	badSig         bool
}

// authorize plays Google's side of authURL (where the start sent the
// browser): it returns the callback URL Google would send the browser to.
func (p *fakeIdP) authorize(authURL string, who person) string {
	p.t.Helper()
	u, err := url.Parse(authURL)
	if err != nil || !strings.HasPrefix(authURL, p.srv.URL+"/auth") {
		p.t.Fatalf("not sent to the provider: %q", authURL)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" {
		p.t.Fatalf("no PKCE, nonce or state: %s", authURL)
	}
	now := time.Now()
	claims := map[string]any{"iss": p.srv.URL, "aud": q.Get("client_id"), "sub": who.sub, "email": who.email, "email_verified": true,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "nonce": q.Get("nonce")}
	if who.hd != "" {
		claims["hd"] = who.hd
	}
	for k, v := range who.over {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	code := randomString(12)
	p.mu.Lock()
	p.grants[code] = fakeGrant{claims: claims, challenge: q.Get("code_challenge"), client: q.Get("client_id"), badSig: who.badSig}
	p.mu.Unlock()
	cb, _ := url.Parse(q.Get("redirect_uri"))
	return cb.String() + "?" + url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
}

// ---- a browser that keeps every cookie, per host ----

type gBrowser struct {
	t       *testing.T
	h       http.Handler
	cookies map[string]map[string]*http.Cookie
}

func newGBrowser(t *testing.T, h http.Handler) *gBrowser {
	return &gBrowser{t: t, h: h, cookies: map[string]map[string]*http.Cookie{}}
}

func (b *gBrowser) do(method, rawURL, body, contentType string) *httptest.ResponseRecorder {
	b.t.Helper()
	u, _ := url.Parse(rawURL)
	r := httptest.NewRequest(method, rawURL, strings.NewReader(body))
	r.RemoteAddr = "10.0.0.7:1234"
	r.Host = u.Host
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if method != "GET" {
		r.Header.Set("Origin", "https://"+u.Host)
	}
	for _, c := range b.cookies[u.Host] {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	b.h.ServeHTTP(w, r)
	for _, c := range w.Result().Cookies() {
		if b.cookies[u.Host] == nil {
			b.cookies[u.Host] = map[string]*http.Cookie{}
		}
		if c.MaxAge < 0 {
			delete(b.cookies[u.Host], c.Name)
		} else {
			b.cookies[u.Host][c.Name] = c
		}
	}
	return w
}

func (b *gBrowser) get(rawURL string) *httptest.ResponseRecorder { return b.do("GET", rawURL, "", "") }

func (b *gBrowser) postJSON(rawURL, body string) *httptest.ResponseRecorder {
	return b.do("POST", rawURL, body, "application/json")
}

func (b *gBrowser) session(host string) bool { return b.cookies[host][CookieName] != nil }

func (b *gBrowser) login(user, pass string) {
	b.t.Helper()
	if w := b.postJSON("https://"+gHost+"/api/login", fmt.Sprintf(`{"username":%q,"password":%q}`, user, pass)); w.Code != 200 {
		b.t.Fatalf("login: %d %s", w.Code, w.Body)
	}
}

const gHost = baseDomain // dash.example.com: the public URL's host

// gEnv is an Auth signing in with the fake Google.
type gEnv struct {
	t    *testing.T
	a    *Auth
	h    http.Handler
	idp  *fakeIdP
	acme store.Org
}

// newGEnv: the instance's client (unless noInstance), and an org Acme
// owned by the env user "admin", with acme.com verified.
func newGEnv(t *testing.T, domains, noInstance bool) *gEnv {
	t.Helper()
	a := newAuth(t, "admin", "s3cret")
	if domains {
		d, err := orgdomain.New(baseDomain, nil, "preview."+baseDomain)
		if err != nil {
			t.Fatal(err)
		}
		a.Domains = d
	}
	idp := newFakeIdP(t)
	a.googleIssuer, a.rp.client = idp.srv.URL, idp.srv.Client()
	a.PublicURL = "https://" + gHost
	key := make([]byte, SecretKeySize)
	rand.Read(key)
	if err := a.SetSecretKey(key); err != nil {
		t.Fatal(err)
	}
	if !noInstance {
		a.Google = GoogleConfig{ClientID: "instance-client", ClientSecret: "instance-secret"}
		idp.clients["instance-client"] = "instance-secret"
	}
	admin, _ := a.acc.EnvUser()
	acme, err := a.acc.CreateOrg(admin.ID, "Acme")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.acc.AddSSODomain(acme.ID, "acme.com", "admin", admin.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	return &gEnv{t: t, a: a, h: serve(a), idp: idp, acme: acme}
}

func (e *gEnv) setSSO(fn func(*store.OrgSSO)) {
	e.t.Helper()
	s, err := e.a.acc.OrgSSO(e.acme.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	fn(&s)
	if err := e.a.acc.PutOrgSSO(s, time.Now()); err != nil {
		e.t.Fatal(err)
	}
}

// ownClient gives Acme its own Google client.
func (e *gEnv) ownClient() {
	e.t.Helper()
	sealed, err := e.a.sealer.Seal("org-secret", orgSecretContext(e.acme.ID))
	if err != nil {
		e.t.Fatal(err)
	}
	e.setSSO(func(s *store.OrgSSO) { s.GoogleClient, s.GoogleSecret = "org-client", sealed })
	e.idp.clients["org-client"] = "org-secret"
}

// start begins a sign-in with query on b and returns where it sent b.
func (e *gEnv) start(b *gBrowser, query string) string {
	e.t.Helper()
	w := b.get("https://" + gHost + GoogleStartPath + "?" + query)
	if w.Code != http.StatusSeeOther {
		e.t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	return w.Header().Get("Location")
}

// signIn goes all the way: start, Google, callback; it returns the
// callback's answer.
func (e *gEnv) signIn(b *gBrowser, query string, who person) *httptest.ResponseRecorder {
	e.t.Helper()
	return b.get(e.idp.authorize(e.start(b, query), who))
}

var refreshRe = regexp.MustCompile(`http-equiv="refresh" content="0;url=([^"]*)"`)

// refreshTo is where a page goes on to.
func refreshTo(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	m := refreshRe.FindStringSubmatch(w.Body.String())
	if m == nil {
		t.Fatalf("no refresh: %d %s", w.Code, w.Body)
	}
	return html.UnescapeString(m[1])
}

func wantReason(t *testing.T, w *httptest.ResponseRecorder, code int, reason string) {
	t.Helper()
	if w.Code != code || !strings.Contains(w.Body.String(), "<code>"+reason+"</code>") {
		t.Fatalf("want %d %s, got %d %s", code, reason, w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "oidc:") || strings.Contains(w.Body.String(), "oauth2:") {
		t.Fatalf("raw error shown: %s", w.Body)
	}
}

func (e *gEnv) member(email string) (store.User, []store.Membership) {
	e.t.Helper()
	u, _, err := e.a.acc.UserByEmail(email)
	if err != nil {
		return store.User{}, nil
	}
	ms, _ := e.a.acc.Memberships(u.ID)
	return u, ms
}

func roleIn(ms []store.Membership, org int64) store.Role {
	for _, m := range ms {
		if m.ID == org {
			return m.Role
		}
	}
	return ""
}

// ---- tests ----

func TestGoogleNotConfigured(t *testing.T) {
	t.Parallel()

	e := newGEnv(t, false, true)
	b := newGBrowser(t, e.h)
	for _, p := range []string{GoogleStartPath, GoogleStartPath + "?org=" + e.acme.Slug, GoogleCallbackPath + "?state=x&code=y"} {
		if w := b.get("https://" + gHost + p); w.Code != http.StatusNotFound {
			t.Fatalf("%s: want 404, got %d", p, w.Code)
		}
	}
	w := b.get("https://" + gHost + googleStatusPath)
	if !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("status: %s", w.Body)
	}
}

func TestGoogleAutoJoinInstanceClient(t *testing.T) {
	t.Parallel()

	e := newGEnv(t, false, false)
	e.setSSO(func(s *store.OrgSSO) { s.AutoJoin = true })
	b := newGBrowser(t, e.h)
	if !strings.Contains(b.get("https://"+gHost+googleStatusPath).Body.String(), `"enabled":true`) {
		t.Fatal("button not offered")
	}
	w := e.signIn(b, "", person{sub: "g-ann", email: "ann@acme.com", hd: "acme.com"})
	if to := refreshTo(t, w); to != "/" {
		t.Fatalf("goes on to %q", to)
	}
	if !b.session(gHost) {
		t.Fatal("no session")
	}
	u, ms := e.member("ann@acme.com")
	if roleIn(ms, e.acme.ID) != store.RoleMember {
		t.Fatalf("ann's orgs: %+v", ms)
	}
	if id, err := e.a.acc.IdentityBySubject("google", "g-ann"); err != nil || id.UserID != u.ID || id.HD != "acme.com" {
		t.Fatalf("identity %+v %v", id, err)
	}
	if w := b.get("https://" + gHost + "/api/me"); !strings.Contains(w.Body.String(), `"username":"ann"`) || !strings.Contains(w.Body.String(), `"has_password":false`) {
		t.Fatalf("me: %s", w.Body)
	}
	// The same Google account again: the same Dash account, by sub, even
	// with another address.
	b2 := newGBrowser(t, e.h)
	e.signIn(b2, "", person{sub: "g-ann", email: "ann.new@acme.com", hd: "acme.com"})
	if !b2.session(gHost) {
		t.Fatal("no session the second time")
	}
	if id, _ := e.a.acc.IdentityBySubject("google", "g-ann"); id.UserID != u.ID || id.Email != "ann.new@acme.com" {
		t.Fatalf("identity after: %+v", id)
	}
}

func TestGoogleOrgClient(t *testing.T) {
	t.Parallel()

	e := newGEnv(t, false, true) // no instance client: the org's own only
	e.ownClient()
	e.setSSO(func(s *store.OrgSSO) { s.AutoJoin = true })
	b := newGBrowser(t, e.h)
	to := e.start(b, "org="+e.acme.Slug)
	if q, _ := url.Parse(to); q.Query().Get("client_id") != "org-client" || q.Query().Get("hd") != "acme.com" || q.Query().Get("prompt") != "select_account" {
		t.Fatalf("auth URL %s", to)
	}
	b.get(e.idp.authorize(to, person{sub: "g-bo", email: "bo@acme.com", hd: "acme.com"}))
	if _, ms := e.member("bo@acme.com"); !b.session(gHost) || roleIn(ms, e.acme.ID) != store.RoleMember {
		t.Fatalf("bo: %+v", ms)
	}
	// Without an org, there's no client to use.
	if w := b.get("https://" + gHost + GoogleStartPath); w.Code != http.StatusNotFound {
		t.Fatalf("no org: %d", w.Code)
	}
	// The org's client takes only its domains.
	b2 := newGBrowser(t, e.h)
	w := e.signIn(b2, "org="+e.acme.Slug, person{sub: "g-eve", email: "eve@evil.com", hd: "evil.com"})
	wantReason(t, w, http.StatusForbidden, "domain_mismatch")
	if b2.session(gHost) {
		t.Fatal("signed in")
	}
}

func TestGoogleTokenChecks(t *testing.T) {
	t.Parallel()

	e := newGEnv(t, false, false)
	e.a.signupOpen = true
	cases := []struct {
		name   string
		who    person
		reason string
	}{
		{"bad signature", person{badSig: true}, "token_invalid"},
		{"wrong audience", person{over: map[string]any{"aud": "someone-else"}}, "token_invalid"},
		{"wrong issuer", person{over: map[string]any{"iss": "https://evil.example"}}, "token_invalid"},
		{"expired", person{over: map[string]any{"exp": time.Now().Add(-time.Hour).Unix(), "iat": time.Now().Add(-2 * time.Hour).Unix()}}, "token_expired"},
		{"nonce mismatch", person{over: map[string]any{"nonce": "another"}}, "nonce_mismatch"},
		{"no nonce", person{over: map[string]any{"nonce": nil}}, "nonce_mismatch"},
		{"email not verified", person{over: map[string]any{"email_verified": false}}, "email_unverified"},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.who.sub, c.who.email = fmt.Sprintf("g-%d", i), fmt.Sprintf("p%d@gmail.com", i)
			b := newGBrowser(t, e.h)
			w := e.signIn(b, "", c.who)
			if w.Code < 400 || !strings.Contains(w.Body.String(), "<code>"+c.reason+"</code>") {
				t.Fatalf("want %s, got %d %s", c.reason, w.Code, w.Body)
			}
			if b.session(gHost) {
				t.Fatal("signed in")
			}
			if _, _, err := e.a.acc.UserByEmail(c.who.email); err == nil {
				t.Fatal("account made")
			}
		})
		e.a.mu.Lock()
		e.a.failed = map[string]*failures{} // each case is its own guess
		e.a.mu.Unlock()
	}
}

func TestGoogleStateAndBinding(t *testing.T) {
	t.Parallel()

	e := newGEnv(t, false, false)
	e.a.signupOpen = true
	who := person{sub: "g-s", email: "s@gmail.com"}

	// Used twice: the second time the state is gone.
	b := newGBrowser(t, e.h)
	cb := e.idp.authorize(e.start(b, ""), who)
	saved := b.cookies[gHost]
	jar := map[string]*http.Cookie{}
	for k, v := range saved {
		jar[k] = v
	}
	if w := b.get(cb); !b.session(gHost) {
		t.Fatalf("first: %d %s", w.Code, w.Body)
	}
	b2 := newGBrowser(t, e.h)
	b2.cookies[gHost] = jar
	delete(jar, CookieName)
	wantReason(t, b2.get(cb), http.StatusBadRequest, "expired")

	// Another browser comes back with the state: no binding cookie.
	b3, b4 := newGBrowser(t, e.h), newGBrowser(t, e.h)
	cb = e.idp.authorize(e.start(b3, ""), who)
	wantReason(t, b4.get(cb), http.StatusBadRequest, "binding")
	if b4.session(gHost) {
		t.Fatal("signed in without the cookie")
	}
	// A cookie that isn't the flow's.
	b5 := newGBrowser(t, e.h)
	cb = e.idp.authorize(e.start(b5, ""), who)
	for name, c := range b5.cookies[gHost] {
		if strings.HasPrefix(name, googleCookiePrefix) {
			c.Value = "forged"
		}
	}
	wantReason(t, b5.get(cb), http.StatusBadRequest, "binding")
	if b5.session(gHost) {
		t.Fatal("signed in with a forged cookie")
	}
	// Google said no.
	b6 := newGBrowser(t, e.h)
	to, _ := url.Parse(e.start(b6, ""))
	wantReason(t, b6.get("https://"+gHost+GoogleCallbackPath+"?error=access_denied&state="+to.Query().Get("state")), http.StatusBadRequest, "denied")
}

func TestGoogleJoinRules(t *testing.T) {
	t.Parallel()

	e := newGEnv(t, false, false)
	// Auto-join off, signup closed: an acme.com account can't get in.
	b := newGBrowser(t, e.h)
	wantReason(t, e.signIn(b, "org="+e.acme.Slug, person{sub: "g-1", email: "cy@acme.com", hd: "acme.com"}), http.StatusForbidden, "org_not_enabled")
	if u, _ := e.member("cy@acme.com"); u.ID != 0 {
		t.Fatal("account made")
	}
	// The address's domain alone doesn't count: only hd does.
	e.setSSO(func(s *store.OrgSSO) { s.AutoJoin = true })
	wantReason(t, e.signIn(newGBrowser(t, e.h), "org="+e.acme.Slug, person{sub: "g-2", email: "fake@acme.com"}), http.StatusForbidden, "org_not_enabled")

	// Signup open: a new account with no invitation or domain gets its own
	// workspace, as signup would, and isn't in Acme, let alone its owner.
	e.a.signupOpen = true
	e.setSSO(func(s *store.OrgSSO) { s.AutoJoin = false })
	b = newGBrowser(t, e.h)
	e.signIn(b, "org="+e.acme.Slug, person{sub: "g-3", email: "dee@gmail.com"})
	u, ms := e.member("dee@gmail.com")
	if !b.session(gHost) || roleIn(ms, e.acme.ID) != "" || len(ms) != 1 || !ms[0].Personal {
		t.Fatalf("dee: %+v", ms)
	}
	_ = u
	b = newGBrowser(t, e.h)
	e.signIn(b, "org="+e.acme.Slug, person{sub: "g-4", email: "cy@acme.com", hd: "acme.com"})
	if _, ms := e.member("cy@acme.com"); roleIn(ms, e.acme.ID) != "" {
		t.Fatalf("joined with auto-join off: %+v", ms)
	}
	// Auto-join gives the configured role, never owner.
	e.setSSO(func(s *store.OrgSSO) { s.AutoJoin, s.JoinRole = true, store.RoleAdmin })
	e.signIn(newGBrowser(t, e.h), "", person{sub: "g-5", email: "fi@acme.com", hd: "acme.com"})
	if _, ms := e.member("fi@acme.com"); roleIn(ms, e.acme.ID) != store.RoleAdmin {
		t.Fatalf("fi: %+v", ms)
	}
	if err := e.a.acc.PutOrgSSO(store.OrgSSO{OrgID: e.acme.ID, JoinRole: store.RoleOwner}, time.Now()); err == nil {
		t.Fatal("join role owner accepted")
	}
}

func TestGoogleRemovedMemberStaysOut(t *testing.T) {
	t.Parallel()

	e := newGEnv(t, false, false)
	e.setSSO(func(s *store.OrgSSO) { s.AutoJoin = true })
	who := person{sub: "g-gil", email: "gil@acme.com", hd: "acme.com"}
	e.signIn(newGBrowser(t, e.h), "", who)
	u, _ := e.member("gil@acme.com")
	admin, _ := e.a.acc.EnvUser()
	if err := e.a.acc.RemoveMember(admin.ID, e.acme.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	e.signIn(newGBrowser(t, e.h), "", who)
	if _, ms := e.member("gil@acme.com"); roleIn(ms, e.acme.ID) != "" {
		t.Fatal("rejoined by signing in again")
	}
}

// register makes a password account for email in org, by invitation.
func (e *gEnv) register(username, email string, org int64, role store.Role) store.User {
	e.t.Helper()
	admin, _ := e.a.acc.EnvUser()
	tok, _, err := e.a.acc.Invite(admin.ID, org, email, role)
	if err != nil {
		e.t.Fatal(err)
	}
	u, _, err := e.a.acc.RegisterWithInvitation(tok, username, "password1")
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

// linkGoogle links who's Google account to u, as signing in with u's
// password from the link page would.
func (e *gEnv) linkGoogle(u store.User, who person) {
	e.t.Helper()
	id := store.Identity{UserID: u.ID, Provider: store.ProviderGoogle, Subject: who.sub, Email: who.email, HD: who.hd}
	if err := e.a.acc.LinkIdentity(id, time.Now()); err != nil {
		e.t.Fatal(err)
	}
}

var linkRe = regexp.MustCompile(`#google_link=([A-Za-z0-9_-]+)`)

func TestGoogleExistingEmail(t *testing.T) {
	t.Parallel()

	e := newGEnv(t, false, false)
	bob := e.register("bob", "bob@example.org", e.acme.ID, store.RoleMember)

	// Google says it's bob's address, from a consumer account: no silent
	// takeover, the password must confirm.
	b := newGBrowser(t, e.h)
	w := e.signIn(b, "", person{sub: "g-bob", email: "bob@example.org"})
	m := linkRe.FindStringSubmatch(w.Body.String())
	if w.Code != 200 || m == nil || b.session(gHost) {
		t.Fatalf("want a link page: %d %s", w.Code, w.Body)
	}
	if _, err := e.a.acc.IdentityBySubject("google", "g-bob"); err == nil {
		t.Fatal("linked without the password")
	}
	// The token alone, handed to bob to use in his own browser, doesn't
	// link: it's bound to the browser that completed the Google flow.
	victim := newGBrowser(t, e.h)
	if w := victim.postJSON("https://"+gHost+"/api/login", fmt.Sprintf(`{"username":"bob","password":"password1","link":%q}`, m[1])); strings.Contains(w.Body.String(), `"google_linked":true`) {
		t.Fatal("linked from another browser")
	}
	if _, err := e.a.acc.IdentityBySubject("google", "g-bob"); err == nil {
		t.Fatal("linked from another browser")
	}
	// Someone else's password doesn't redeem it.
	e.register("mallory", "mallory@example.org", e.acme.ID, store.RoleMember)
	if w := b.postJSON("https://"+gHost+"/api/login", fmt.Sprintf(`{"username":"mallory","password":"password1","link":%q}`, m[1])); strings.Contains(w.Body.String(), `"google_linked":true`) {
		t.Fatal("linked to the wrong account")
	}
	w = b.postJSON("https://"+gHost+"/api/login", fmt.Sprintf(`{"username":"bob","password":"password1","link":%q}`, m[1]))
	if !strings.Contains(w.Body.String(), `"google_linked":true`) {
		t.Fatalf("login with link: %s", w.Body)
	}
	if id, err := e.a.acc.IdentityBySubject("google", "g-bob"); err != nil || id.UserID != bob.ID {
		t.Fatalf("not linked: %+v %v", id, err)
	}
	b = newGBrowser(t, e.h)
	e.signIn(b, "", person{sub: "g-bob", email: "bob@example.org"})
	if !b.session(gHost) {
		t.Fatal("linked Google doesn't sign in")
	}

	// Even with a domain an org of theirs verified, an account with a
	// password needs it: whoever registered the invitation chose that
	// password, and may not be the address's owner (an inviter who
	// registered a colleague's invitation themselves).
	carl := e.register("carl", "carl@acme.com", e.acme.ID, store.RoleMember)
	b = newGBrowser(t, e.h)
	w = e.signIn(b, "", person{sub: "g-carl", email: "carl@acme.com", hd: "acme.com"})
	m = linkRe.FindStringSubmatch(w.Body.String())
	if m == nil || b.session(gHost) {
		t.Fatalf("auto-linked to a password account by a verified domain: %d %s", w.Code, w.Body)
	}
	if _, err := e.a.acc.IdentityBySubject("google", "g-carl"); err == nil {
		t.Fatal("linked without the password")
	}
	w = b.postJSON("https://"+gHost+"/api/login", fmt.Sprintf(`{"username":"carl","password":"password1","link":%q}`, m[1]))
	if !strings.Contains(w.Body.String(), `"google_linked":true`) {
		t.Fatalf("login with link: %s", w.Body)
	}
	if id, err := e.a.acc.IdentityBySubject("google", "g-carl"); err != nil || id.UserID != carl.ID {
		t.Fatalf("not linked: %+v %v", id, err)
	}
	// Not with a domain only another org verified.
	admin, _ := e.a.acc.EnvUser()
	other, _ := e.a.acc.CreateOrg(admin.ID, "Other")
	e.a.acc.AddSSODomain(other.ID, "other.com", "admin", admin.ID, time.Now())
	e.register("dora", "dora@other.com", e.acme.ID, store.RoleMember)
	w = e.signIn(newGBrowser(t, e.h), "", person{sub: "g-dora", email: "dora@other.com", hd: "other.com"})
	if linkRe.FindString(w.Body.String()) == "" {
		t.Fatalf("auto-linked by another org's domain: %s", w.Body)
	}
}

func TestGoogleInvitation(t *testing.T) {
	t.Parallel()

	e := newGEnv(t, false, false)
	admin, _ := e.a.acc.EnvUser()
	accept := func(tok string, who person) *httptest.ResponseRecorder {
		b := newGBrowser(t, e.h)
		w := b.do("POST", "https://"+gHost+GoogleStartPath, url.Values{"invite": {tok}}.Encode(), "application/x-www-form-urlencoded")
		if w.Code != http.StatusSeeOther {
			t.Fatalf("start: %d %s", w.Code, w.Body)
		}
		return b.get(e.idp.authorize(w.Header().Get("Location"), who))
	}
	tok, _, err := e.a.acc.InviteWith(admin.ID, e.acme.ID, "ida@example.org", store.RoleAdmin, false)
	if err != nil {
		t.Fatal(err)
	}
	wantReason(t, accept(tok, person{sub: "g-x", email: "x@gmail.com"}), http.StatusForbidden, "invite_email_mismatch")
	if _, err := e.a.acc.InvitationByToken(tok); err != nil {
		t.Fatal("invitation used up by the wrong address")
	}
	accept(tok, person{sub: "g-ida", email: "ida@example.org"})
	if _, ms := e.member("ida@example.org"); roleIn(ms, e.acme.ID) != store.RoleAdmin {
		t.Fatalf("ida: %+v", ms)
	}
	if _, err := e.a.acc.InvitationByToken(tok); err == nil {
		t.Fatal("invitation still pending")
	}
	// Any address, when the inviter allowed it.
	tok, _, _ = e.a.acc.InviteWith(admin.ID, e.acme.ID, "jo@example.org", store.RoleMember, true)
	accept(tok, person{sub: "g-jo", email: "jo.private@gmail.com"})
	if _, ms := e.member("jo.private@gmail.com"); roleIn(ms, e.acme.ID) != store.RoleMember {
		t.Fatalf("jo: %+v", ms)
	}
	// The token never travels in a GET's URL.
	b := newGBrowser(t, e.h)
	tok, _, _ = e.a.acc.InviteWith(admin.ID, e.acme.ID, "kim@example.org", store.RoleMember, false)
	to, _ := url.Parse(e.start(b, "invite="+tok))
	b.get(e.idp.authorize(to.String(), person{sub: "g-kim", email: "kim@example.org"}))
	if _, ms := e.member("kim@example.org"); roleIn(ms, e.acme.ID) != "" {
		t.Fatal("GET invite token honoured")
	}
}

func TestGoogleRequired(t *testing.T) {
	t.Parallel()

	e := newGEnv(t, false, false)
	e.setSSO(func(s *store.OrgSSO) { s.RequireGoogle = true })
	dan := e.register("dan", "dan@acme.com", e.acme.ID, store.RoleMember)
	members := fmt.Sprintf("https://%s/api/orgs/%d/members", gHost, e.acme.ID)
	b := newGBrowser(t, e.h)
	b.login("dan", "password1")
	if w := b.get(members); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "google_required") {
		t.Fatalf("password session: %d %s", w.Code, w.Body)
	}
	if w := b.get("https://" + gHost + "/"); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), GoogleStartPath) {
		t.Fatalf("page: %d %s", w.Code, w.Body)
	}
	// Tokens are unaffected.
	tok, _, err := e.a.acc.CreateToken(store.NewToken{Kind: store.TokenAPI, UserID: dan.ID, OrgID: e.acme.ID, Name: "ci", Scopes: []string{store.ScopeRead}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", members, nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("token: %d %s", w.Code, w.Body)
	}
	// A Google session works.
	e.linkGoogle(dan, person{sub: "g-dan", email: "dan@acme.com", hd: "acme.com"})
	g := newGBrowser(t, e.h)
	e.signIn(g, "org="+e.acme.Slug, person{sub: "g-dan", email: "dan@acme.com", hd: "acme.com"})
	if w := g.get(members); w.Code != 200 {
		t.Fatalf("google session: %d %s", w.Code, w.Body)
	}
	// A password session can't mint a token for the org from another org.
	side, err := e.a.acc.CreateOrg(dan.ID, "Side")
	if err != nil {
		t.Fatal(err)
	}
	if w := b.postJSON("https://"+gHost+"/api/auth/switch-org", fmt.Sprintf(`{"org_id":%d}`, side.ID)); w.Code != 200 {
		t.Fatalf("switch-org: %d %s", w.Code, w.Body)
	}
	mint := func(b *gBrowser) *httptest.ResponseRecorder {
		return b.postJSON("https://"+gHost+"/api/tokens", fmt.Sprintf(`{"name":"x","org_id":%d,"scopes":["read","write"],"password":"password1"}`, e.acme.ID))
	}
	if w := mint(b); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "google_required") {
		t.Fatalf("password session token: %d %s", w.Code, w.Body)
	}
	// A Google session can.
	if w := mint(g); w.Code != http.StatusCreated {
		t.Fatalf("google session token: %d %s", w.Code, w.Body)
	}
	// Deprovisioning: the account out of the Workspace (no hd) can't come in.
	wantReason(t, e.signIn(newGBrowser(t, e.h), "org="+e.acme.Slug, person{sub: "g-dan", email: "dan@acme.com"}), http.StatusForbidden, "domain_mismatch")
	// Owners keep their password.
	admin, _ := e.a.acc.EnvUser()
	if err := e.a.acc.SetRole(admin.ID, e.acme.ID, dan.ID, store.RoleOwner); err != nil {
		t.Fatal(err)
	}
	if w := b.get(members); w.Code != 200 {
		t.Fatalf("owner with password: %d %s", w.Code, w.Body)
	}
}

func TestGoogleTestModeNeverSignsIn(t *testing.T) {
	t.Parallel()

	e := newGEnv(t, false, false)
	b := newGBrowser(t, e.h)
	b.login("admin", "s3cret")
	before := b.cookies[gHost][CookieName].Value
	w := e.signIn(b, "purpose=test&org="+e.acme.Slug, person{sub: "g-t", email: "t@acme.com", hd: "acme.com"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Google sign-in works") {
		t.Fatalf("test: %d %s", w.Code, w.Body)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == CookieName {
			t.Fatal("test mode set a session cookie")
		}
	}
	if b.cookies[gHost][CookieName].Value != before {
		t.Fatal("session changed")
	}
	if _, err := e.a.acc.IdentityBySubject("google", "g-t"); err == nil {
		t.Fatal("test mode linked an identity")
	}
	if u, _ := e.member("t@acme.com"); u.ID != 0 {
		t.Fatal("test mode made an account")
	}
	// Test failures come back as a test result too.
	wantReason(t, e.signIn(b, "purpose=test&org="+e.acme.Slug, person{sub: "g-t", email: "t@acme.com", over: map[string]any{"email_verified": false}}), http.StatusForbidden, "email_unverified")
	// Not signed in, or not an admin: no test.
	if w := newGBrowser(t, e.h).get("https://" + gHost + GoogleStartPath + "?purpose=test&org=" + e.acme.Slug); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login.html" {
		t.Fatalf("signed out: %d %s", w.Code, w.Header().Get("Location"))
	}
}

func TestGoogleOrgSubdomainHandoff(t *testing.T) {
	t.Parallel()

	e := newGEnv(t, true, false)
	e.setSSO(func(s *store.OrgSSO) { s.AutoJoin = true })
	slug := e.acme.Slug
	orgHost := slug + "." + baseDomain
	b := newGBrowser(t, e.h)
	// The org's host sends the start to the base domain, for this org.
	w := b.get("https://" + orgHost + GoogleStartPath)
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "https://"+gHost+GoogleStartPath+"?") ||
		!strings.Contains(w.Header().Get("Location"), "org="+slug) {
		t.Fatalf("org host start: %d %s", w.Code, w.Header().Get("Location"))
	}
	w = b.get(e.idp.authorize(e.start(b, "org="+slug+"&next=/board"), person{sub: "g-h", email: "h@acme.com", hd: "acme.com"}))
	to := refreshTo(t, w)
	if to != "/go/"+slug+"?next=%2Fboard" {
		t.Fatalf("goes on to %q", to)
	}
	w = b.get("https://" + gHost + to)
	loc := w.Header().Get("Location")
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "https://"+orgHost+HandoffPath) {
		t.Fatalf("go: %d %s", w.Code, loc)
	}
	w = b.get(loc)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/board" || !b.session(orgHost) {
		t.Fatalf("handoff: %d %s", w.Code, w.Header().Get("Location"))
	}
	// The handed-off session is a Google one too.
	s, err := e.a.acc.Session(sessionKey(b.cookies[orgHost][CookieName].Value), time.Now())
	if err != nil || s.Method != store.MethodGoogle || s.AuthAt.IsZero() {
		t.Fatalf("org host session %+v %v", s, err)
	}
	// The callback lives on the base domain only.
	if w := b.get("https://" + orgHost + GoogleCallbackPath + "?state=x"); w.Code != http.StatusNotFound {
		t.Fatalf("org host callback: %d", w.Code)
	}
}

func TestGoogleSecretNeverShown(t *testing.T) {
	t.Parallel()

	e := newGEnv(t, false, true)
	b := newGBrowser(t, e.h)
	b.login("admin", "s3cret")
	sso := fmt.Sprintf("https://%s/api/orgs/%d/sso", gHost, e.acme.ID)
	w := b.do("PATCH", sso, `{"client_id":"org-client","client_secret":"org-secret-value","password":"s3cret"}`, "application/json")
	if w.Code != 200 {
		t.Fatalf("patch: %d %s", w.Code, w.Body)
	}
	for _, body := range []string{w.Body.String(), b.get(sso).Body.String()} {
		if strings.Contains(body, "org-secret-value") || strings.Contains(body, sealPrefix) || !strings.Contains(body, `"client_secret_set":true`) {
			t.Fatalf("answer: %s", body)
		}
	}
	s, _ := e.a.acc.OrgSSO(e.acme.ID)
	if s.GoogleSecret == "" || strings.Contains(s.GoogleSecret, "org-secret-value") {
		t.Fatalf("stored in the clear: %q", s.GoogleSecret)
	}
	if c, ok := e.a.orgGoogle(e.acme.ID); !ok || c.Secret != "org-secret-value" {
		t.Fatalf("doesn't open: %+v", c)
	}
	// Sealed for this org, it doesn't open for another.
	if _, err := e.a.sealer.Open(s.GoogleSecret, orgSecretContext(e.acme.ID+1)); err == nil {
		t.Fatal("opened in another org's context")
	}
	// Patching other settings keeps the secret.
	if w := b.do("PATCH", sso, `{"auto_join":true,"password":"s3cret"}`, "application/json"); w.Code != 200 || !strings.Contains(w.Body.String(), `"client_secret_set":true`) {
		t.Fatalf("patch: %d %s", w.Code, w.Body)
	}
	if ev, _ := e.a.acc.AuthEvents(e.acme.ID, 10); len(ev) < 2 || ev[0].Kind != "sso.settings" {
		t.Fatalf("audit: %+v", ev)
	}
	// Platform admin settings don't show it either.
	if w := b.do("PUT", "https://"+gHost+"/api/admin/sso/google", `{"client_id":"inst","client_secret":"inst-secret-value","password":"s3cret"}`, "application/json"); w.Code != 200 || strings.Contains(w.Body.String(), "inst-secret-value") {
		t.Fatalf("admin: %d %s", w.Code, w.Body)
	}
}

func TestGoogleOnlyAccount(t *testing.T) {
	t.Parallel()

	e := newGEnv(t, false, false)
	e.setSSO(func(s *store.OrgSSO) { s.AutoJoin = true })
	b := newGBrowser(t, e.h)
	e.signIn(b, "", person{sub: "g-o", email: "o@acme.com", hd: "acme.com"})
	// No password: unlinking would lock them out.
	if w := b.do("DELETE", "https://"+gHost+"/api/account/google", `{"password":""}`, "application/json"); w.Code != http.StatusConflict {
		t.Fatalf("unlink: %d %s", w.Code, w.Body)
	}
	// A fresh Google sign-in confirms what a password would.
	sso := "https://" + gHost + "/api/tokens"
	body := fmt.Sprintf(`{"name":"t","org_id":%d,"scopes":["read"],"password":""}`, e.acme.ID)
	if w := b.postJSON(sso, body); w.Code != http.StatusCreated {
		t.Fatalf("confirm with recent google: %d %s", w.Code, w.Body)
	}
	if b2 := newGBrowser(t, e.h); true {
		e.signIn(b2, "", person{sub: "g-o", email: "o@acme.com", hd: "acme.com"})
		e.a.now = func() time.Time { return time.Now().Add(time.Hour) }
		if w := b2.postJSON(sso, body); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "reauth_google") {
			t.Fatalf("stale google: %d %s", w.Code, w.Body)
		}
	}
}

func TestSignOutMember(t *testing.T) {
	t.Parallel()

	e := newGEnv(t, false, false)
	eva := e.register("eva", "eva@example.org", e.acme.ID, store.RoleMember)
	b := newGBrowser(t, e.h)
	b.login("eva", "password1")
	admin := newGBrowser(t, e.h)
	admin.login("admin", "s3cret")
	if w := admin.postJSON(fmt.Sprintf("https://%s/api/orgs/%d/members/%d/sign-out", gHost, e.acme.ID, eva.ID), `{"password":"s3cret"}`); w.Code != 200 {
		t.Fatalf("sign out: %d %s", w.Code, w.Body)
	}
	if w := b.get("https://" + gHost + "/api/me"); w.Code != http.StatusUnauthorized {
		t.Fatalf("still signed in: %d", w.Code)
	}
	// A member can't sign out the owner.
	b.login("eva", "password1")
	owner, _ := e.a.acc.EnvUser()
	if w := b.postJSON(fmt.Sprintf("https://%s/api/orgs/%d/members/%d/sign-out", gHost, e.acme.ID, owner.ID), `{"password":"password1"}`); w.Code != http.StatusForbidden {
		t.Fatalf("member signs out owner: %d", w.Code)
	}
}

func TestDomainClaims(t *testing.T) {
	t.Parallel()

	e := newGEnv(t, false, false)
	admin, _ := e.a.acc.EnvUser()
	other, _ := e.a.acc.CreateOrg(admin.ID, "Other")
	if _, err := e.a.acc.AddSSODomain(other.ID, "acme.com", "admin", admin.ID, time.Now()); err != store.ErrDomainTaken {
		t.Fatalf("verified twice: %v", err)
	}
	// An unverified claim, proved by an owner's Google sign-in with that hd.
	d, err := e.a.acc.AddSSODomain(other.ID, "other.com", "", 0, time.Now())
	if err != nil || d.Verified {
		t.Fatalf("claim: %+v %v", d, err)
	}
	b := newGBrowser(t, e.h)
	b.login("admin", "s3cret")
	e.signIn(b, "purpose=test&org="+other.Slug, person{sub: "g-adm", email: "boss@other.com", hd: "other.com"})
	if s, _ := e.a.acc.OrgSSO(other.ID); !s.VerifiedDomain("other.com") {
		t.Fatalf("not proved: %+v", s.Domains)
	}
}

// Removing a verified domain switches "require Google" off for its people:
// only owners do it, and like the other SSO changes it needs the password.
func TestRemoveDomain(t *testing.T) {
	t.Parallel()

	e := newGEnv(t, false, false)
	e.setSSO(func(s *store.OrgSSO) { s.RequireGoogle = true })
	e.register("ada", "ada@example.org", e.acme.ID, store.RoleAdmin)
	if _, err := e.a.acc.AddSSODomain(e.acme.ID, "pending.com", "", 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	url := func(d string) string {
		return fmt.Sprintf("https://%s/api/orgs/%d/sso/domains/%s", gHost, e.acme.ID, d)
	}
	ada := newGBrowser(t, e.h)
	ada.login("ada", "password1")
	if w := ada.do("DELETE", url("acme.com"), `{"password":"password1"}`, "application/json"); w.Code != http.StatusForbidden {
		t.Fatalf("admin removes verified: %d %s", w.Code, w.Body)
	}
	if w := ada.do("DELETE", url("pending.com"), `{"password":"wrong"}`, "application/json"); w.Code == http.StatusNoContent {
		t.Fatal("removed with a wrong password")
	}
	if w := ada.do("DELETE", url("pending.com"), `{"password":"password1"}`, "application/json"); w.Code != http.StatusNoContent {
		t.Fatalf("admin removes pending: %d %s", w.Code, w.Body)
	}
	owner := newGBrowser(t, e.h)
	owner.login("admin", "s3cret")
	if w := owner.do("DELETE", url("acme.com"), `{"password":"nope"}`, "application/json"); w.Code == http.StatusNoContent {
		t.Fatal("owner removed without the password")
	}
	if s, _ := e.a.acc.OrgSSO(e.acme.ID); !s.VerifiedDomain("acme.com") {
		t.Fatalf("gone too early: %+v", s.Domains)
	}
	if w := owner.do("DELETE", url("acme.com"), `{"password":"s3cret"}`, "application/json"); w.Code != http.StatusNoContent {
		t.Fatalf("owner removes verified: %d %s", w.Code, w.Body)
	}
	if s, _ := e.a.acc.OrgSSO(e.acme.ID); len(s.Domains) != 0 {
		t.Fatalf("left: %+v", s.Domains)
	}
}

var totpTicketRe = regexp.MustCompile(`#google_totp=([A-Za-z0-9_-]+)`)

func TestGoogleThenTOTP(t *testing.T) {
	t.Parallel()

	e := newGEnv(t, false, false)
	fay := e.register("fay", "fay@acme.com", e.acme.ID, store.RoleMember)
	secret := totp.NewSecret()
	if err := e.a.acc.BeginTOTP(fay.ID, secret); err != nil {
		t.Fatal(err)
	}
	if err := e.a.acc.ConfirmTOTP(fay.ID, secret, 1, nil); err != nil {
		t.Fatal(err)
	}
	who := person{sub: "g-fay", email: "fay@acme.com", hd: "acme.com"}
	e.linkGoogle(fay, who)
	// Off (the default): Google alone signs in.
	b := newGBrowser(t, e.h)
	e.signIn(b, "org="+e.acme.Slug, who)
	if !b.session(gHost) {
		t.Fatal("no session with 2FA after Google off")
	}
	// On: the authenticator code too.
	e.setSSO(func(s *store.OrgSSO) { s.Google2FA = true })
	b = newGBrowser(t, e.h)
	w := e.signIn(b, "org="+e.acme.Slug, who)
	m := totpTicketRe.FindStringSubmatch(refreshTo(t, w))
	if m == nil || b.session(gHost) {
		t.Fatalf("want a TOTP step: %s", w.Body)
	}
	w = b.postJSON("https://"+gHost+"/api/login/totp", fmt.Sprintf(`{"ticket":%q,"code":%q}`, m[1], codeAt(t, secret, time.Now())))
	if w.Code != 200 || !b.session(gHost) {
		t.Fatalf("totp: %d %s", w.Code, w.Body)
	}
	s, err := e.a.acc.Session(sessionKey(b.cookies[gHost][CookieName].Value), time.Now())
	if err != nil || s.Method != store.MethodGoogle || s.Org == nil || s.Org.ID != e.acme.ID {
		t.Fatalf("session %+v %v", s, err)
	}
}
