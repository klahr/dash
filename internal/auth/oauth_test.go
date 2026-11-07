package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/store"
)

const (
	base     = "https://dash.example.ts.net:8443"
	callback = "https://claude.ai/api/mcp/auth_callback"
	verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk-and-some-more"
)

func challenge(v string) string {
	h := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func newOAuthServer(t *testing.T) (*Auth, *OAuth, http.Handler) {
	t.Helper()
	return oauthServerAt(t, filepath.Join(t.TempDir(), "dash.db"), "s3cret")
}

func oauthServerAt(t *testing.T, path, password string) (*Auth, *OAuth, http.Handler) {
	t.Helper()
	a := openAuth(t, path, "admin", password)
	o, err := NewOAuth(a, base+"/")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", a.Login)
	o.Routes(mux)
	mux.Handle("POST /mcp", o.Bearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("tools")) })))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("private")) })
	return a, o, a.Middleware(mux)
}

func form(h http.Handler, path string, v url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "10.0.0.1:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("%d %s: %v", w.Code, w.Body, err)
	}
	return m
}

func register(t *testing.T, h http.Handler, body string) map[string]any {
	t.Helper()
	w := do(h, "POST", "/register", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	return decodeJSON(t, w)
}

func authParams(clientID string) url.Values {
	return url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {callback}, "state": {"xyz"},
		"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}, "resource": {base + "/mcp"},
	}
}

// login allows the consent page and returns the redirect's query.
func login(t *testing.T, h http.Handler, p url.Values, password string) (*httptest.ResponseRecorder, url.Values) {
	t.Helper()
	v := url.Values{}
	for k, vs := range p {
		v[k] = vs
	}
	v.Set("username", "admin")
	v.Set("password", password)
	v.Set("action", "allow")
	w := form(h, "/authorize", v)
	if w.Code != http.StatusFound {
		return w, nil
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil || u.Scheme+"://"+u.Host+u.Path != callback {
		t.Fatalf("redirected to %q", w.Header().Get("Location"))
	}
	return w, u.Query()
}

func tokens(t *testing.T, h http.Handler, v url.Values) (int, map[string]any) {
	t.Helper()
	w := form(h, "/token", v)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("token response cacheable")
	}
	return w.Code, decodeJSON(t, w)
}

func callMCP(h http.Handler, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/mcp", strings.NewReader("{}"))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestOAuthMetadata(t *testing.T) {
	_, _, h := newOAuthServer(t)
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		m := decodeJSON(t, do(h, "GET", path, ""))
		if m["resource"] != base+"/mcp" || m["authorization_servers"].([]any)[0] != base {
			t.Errorf("%s: %v", path, m)
		}
	}
	m := decodeJSON(t, do(h, "GET", "/.well-known/oauth-authorization-server", ""))
	for k, want := range map[string]string{
		"issuer": base, "authorization_endpoint": base + "/authorize",
		"token_endpoint": base + "/token", "registration_endpoint": base + "/register",
	} {
		if m[k] != want {
			t.Errorf("%s = %v, want %s", k, m[k], want)
		}
	}
	if pkce := m["code_challenge_methods_supported"].([]any); len(pkce) != 1 || pkce[0] != "S256" {
		t.Errorf("PKCE methods %v", pkce)
	}
}

func TestNewOAuthNeedsHTTPSOrigin(t *testing.T) {
	a := newAuth(t, "admin", "s3cret")
	for _, u := range []string{"", "http://dash.example.net", "https://dash.example.net/dash", "ftp://x"} {
		if _, err := NewOAuth(a, u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
	if _, err := NewOAuth(a, "http://127.0.0.1:7788"); err != nil {
		t.Errorf("loopback: %v", err)
	}
}

func TestMCPNeedsBearerToken(t *testing.T) {
	_, _, h := newOAuthServer(t)
	want := `Bearer resource_metadata="` + base + `/.well-known/oauth-protected-resource"`
	w := callMCP(h, "")
	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != want {
		t.Errorf("no token: %d %q", w.Code, w.Header().Get("WWW-Authenticate"))
	}
	w = callMCP(h, "made-up")
	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != want+`, error="invalid_token"` {
		t.Errorf("bad token: %d %q", w.Code, w.Header().Get("WWW-Authenticate"))
	}
	// The rest of Dash still wants a session.
	if w := do(h, "GET", "/api/projects", ""); w.Code == http.StatusOK {
		t.Errorf("private page served")
	}
}

func TestOAuthFlow(t *testing.T) {
	a, _, h := newOAuthServer(t)
	c := register(t, h, `{"client_name":"Claude","redirect_uris":["`+callback+`"],"grant_types":["authorization_code","refresh_token"],"token_endpoint_auth_method":"none"}`)
	id := c["client_id"].(string)
	if _, ok := c["client_secret"]; ok {
		t.Errorf("public client got a secret")
	}

	// Consent page names the client and where the code goes.
	q := authParams(id)
	w := do(h, "GET", "/authorize?"+q.Encode(), "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Claude") || !strings.Contains(w.Body.String(), "claude.ai") {
		t.Fatalf("consent: %d %s", w.Code, w.Body)
	}
	if w.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("consent page can be framed")
	}

	// Wrong password: the page again, no code.
	if w, _ := login(t, h, q, "nope"); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: %d", w.Code)
	}
	_, res := login(t, h, q, "s3cret")
	if res.Get("state") != "xyz" || res.Get("iss") != base || res.Get("code") == "" {
		t.Fatalf("redirect %v", res)
	}
	code := res.Get("code")

	exchange := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {id},
		"redirect_uri": {callback}, "code_verifier": {verifier}, "resource": {base + "/mcp"}}
	status, tok := tokens(t, h, exchange)
	if status != http.StatusOK || tok["token_type"] != "Bearer" || tok["access_token"] == "" || tok["refresh_token"] == "" {
		t.Fatalf("token: %d %v", status, tok)
	}
	access := tok["access_token"].(string)
	if w := callMCP(h, access); w.Code != http.StatusOK || w.Body.String() != "tools" {
		t.Errorf("MCP with token: %d %s", w.Code, w.Body)
	}
	// Access tokens are not sessions or peer tokens.
	r := httptest.NewRequest("GET", "/api/projects", nil)
	r.Header.Set("Authorization", "Bearer "+access)
	if _, ok := a.session(r); ok || a.viaPeer(r) {
		t.Errorf("access token opens the API")
	}
	if w := httptest.NewRecorder(); func() int { h.ServeHTTP(w, r); return w.Code }() != http.StatusUnauthorized {
		t.Errorf("access token opens the API: %d", w.Code)
	}
	// Codes work once.
	if status, m := tokens(t, h, exchange); status != http.StatusBadRequest || m["error"] != "invalid_grant" {
		t.Errorf("reused code: %d %v", status, m)
	}
	// Expiry.
	a.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if w := callMCP(h, access); w.Code != http.StatusUnauthorized {
		t.Errorf("expired access token: %d", w.Code)
	}
}

func TestOAuthPKCE(t *testing.T) {
	_, _, h := newOAuthServer(t)
	id := register(t, h, `{"redirect_uris":["`+callback+`"]}`)["client_id"].(string)

	// No PKCE, or plain: rejected back at the client.
	for _, method := range []string{"", "plain"} {
		q := authParams(id)
		q.Set("code_challenge_method", method)
		w := do(h, "GET", "/authorize?"+q.Encode(), "")
		loc, _ := url.Parse(w.Header().Get("Location"))
		if w.Code != http.StatusFound || loc.Query().Get("error") != "invalid_request" {
			t.Errorf("method %q: %d %s", method, w.Code, w.Header().Get("Location"))
		}
	}

	_, res := login(t, h, authParams(id), "s3cret")
	status, m := tokens(t, h, url.Values{"grant_type": {"authorization_code"}, "code": {res.Get("code")}, "client_id": {id},
		"redirect_uri": {callback}, "code_verifier": {strings.Repeat("x", 50)}})
	if status != http.StatusBadRequest || m["error"] != "invalid_grant" {
		t.Errorf("wrong verifier: %d %v", status, m)
	}
	// The code is spent even after a wrong verifier.
	status, _ = tokens(t, h, url.Values{"grant_type": {"authorization_code"}, "code": {res.Get("code")}, "client_id": {id},
		"redirect_uri": {callback}, "code_verifier": {verifier}})
	if status != http.StatusBadRequest {
		t.Errorf("code reusable after a wrong verifier: %d", status)
	}
}

func TestOAuthRedirectURI(t *testing.T) {
	_, _, h := newOAuthServer(t)
	for _, uri := range []string{"https://evil.example/cb", "http://evil.example/cb", "https://claude.ai.evil.example/api/mcp/auth_callback", "javascript:alert(1)"} {
		if w := do(h, "POST", "/register", `{"redirect_uris":["`+uri+`"]}`); w.Code != http.StatusBadRequest ||
			decodeJSON(t, w)["error"] != "invalid_redirect_uri" {
			t.Errorf("registered %s: %d", uri, w.Code)
		}
	}
	register(t, h, `{"redirect_uris":["http://localhost:33418/callback"]}`) // Claude Code / Desktop

	id := register(t, h, `{"redirect_uris":["`+callback+`"]}`)["client_id"].(string)
	q := authParams(id)
	q.Set("redirect_uri", "https://evil.example/cb")
	w := do(h, "GET", "/authorize?"+q.Encode(), "")
	if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" {
		t.Errorf("unregistered redirect_uri: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w, _ := login(t, h, q, "s3cret"); w.Code != http.StatusBadRequest {
		t.Errorf("unregistered redirect_uri on POST: %d", w.Code)
	}
	q.Set("client_id", "unknown")
	if w := do(h, "GET", "/authorize?"+authParams("unknown").Encode(), ""); w.Code != http.StatusBadRequest {
		t.Errorf("unknown client: %d", w.Code)
	}

	// The code is bound to the redirect_uri it was issued for.
	id2 := register(t, h, `{"redirect_uris":["`+callback+`","https://claude.com/api/mcp/auth_callback"]}`)["client_id"].(string)
	_, res := login(t, h, authParams(id2), "s3cret")
	status, m := tokens(t, h, url.Values{"grant_type": {"authorization_code"}, "code": {res.Get("code")}, "client_id": {id2},
		"redirect_uri": {"https://claude.com/api/mcp/auth_callback"}, "code_verifier": {verifier}})
	if status != http.StatusBadRequest || m["error"] != "invalid_grant" {
		t.Errorf("other redirect_uri at /token: %d %v", status, m)
	}
}

func TestOAuthDeny(t *testing.T) {
	_, _, h := newOAuthServer(t)
	id := register(t, h, `{"redirect_uris":["`+callback+`"]}`)["client_id"].(string)
	v := authParams(id)
	v.Set("action", "deny")
	w := form(h, "/authorize", v)
	loc, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusFound || loc.Query().Get("error") != "access_denied" || loc.Query().Get("code") != "" {
		t.Errorf("deny: %d %s", w.Code, w.Header().Get("Location"))
	}
}

func TestOAuthSharesLockout(t *testing.T) {
	_, _, h := newOAuthServer(t)
	id := register(t, h, `{"redirect_uris":["`+callback+`"]}`)["client_id"].(string)
	for range maxFailures {
		login(t, h, authParams(id), "nope")
	}
	if w, _ := login(t, h, authParams(id), "s3cret"); w.Code != http.StatusTooManyRequests {
		t.Errorf("locked out consent: %d", w.Code)
	}
	if w := do(h, "POST", "/api/login", `{"username":"admin","password":"s3cret"}`); w.Code != http.StatusTooManyRequests {
		t.Errorf("locked out /api/login: %d", w.Code)
	}
}

// fullFlow registers a client and returns its id and first token response.
func fullFlow(t *testing.T, h http.Handler, registration string) (string, map[string]any) {
	t.Helper()
	c := register(t, h, registration)
	id := c["client_id"].(string)
	_, res := login(t, h, authParams(id), "s3cret")
	v := url.Values{"grant_type": {"authorization_code"}, "code": {res.Get("code")}, "client_id": {id},
		"redirect_uri": {callback}, "code_verifier": {verifier}}
	if s, ok := c["client_secret"].(string); ok {
		v.Set("client_secret", s)
	}
	status, tok := tokens(t, h, v)
	if status != http.StatusOK {
		t.Fatalf("token: %d %v", status, tok)
	}
	return id, tok
}

func TestOAuthRefreshRotation(t *testing.T) {
	_, _, h := newOAuthServer(t)
	id, tok := fullFlow(t, h, `{"redirect_uris":["`+callback+`"]}`)
	refresh := tok["refresh_token"].(string)

	status, next := tokens(t, h, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {id}})
	if status != http.StatusOK || next["refresh_token"] == refresh || next["access_token"] == tok["access_token"] {
		t.Fatalf("refresh: %d %v", status, next)
	}
	if w := callMCP(h, next["access_token"].(string)); w.Code != http.StatusOK {
		t.Errorf("new access token: %d", w.Code)
	}
	// The old refresh token is spent.
	if status, m := tokens(t, h, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {id}}); status != http.StatusBadRequest || m["error"] != "invalid_grant" {
		t.Errorf("reused refresh token: %d %v", status, m)
	}
	// A refresh token is not an access token, nor usable by another client.
	if w := callMCP(h, next["refresh_token"].(string)); w.Code != http.StatusUnauthorized {
		t.Errorf("refresh token as access token: %d", w.Code)
	}
	other := register(t, h, `{"redirect_uris":["`+callback+`"]}`)["client_id"].(string)
	if status, _ := tokens(t, h, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {next["refresh_token"].(string)}, "client_id": {other}}); status != http.StatusBadRequest {
		t.Errorf("refresh by another client: %d", status)
	}
}

func TestOAuthConfidentialClient(t *testing.T) {
	_, _, h := newOAuthServer(t)
	c := register(t, h, `{"redirect_uris":["`+callback+`"],"token_endpoint_auth_method":"client_secret_post"}`)
	id, secret := c["client_id"].(string), c["client_secret"].(string)
	_, res := login(t, h, authParams(id), "s3cret")
	v := url.Values{"grant_type": {"authorization_code"}, "code": {res.Get("code")}, "client_id": {id},
		"redirect_uri": {callback}, "code_verifier": {verifier}, "client_secret": {"wrong"}}
	if status, m := tokens(t, h, v); status != http.StatusUnauthorized || m["error"] != "invalid_client" {
		t.Errorf("wrong secret: %d %v", status, m)
	}
	_, res = login(t, h, authParams(id), "s3cret")
	v.Set("code", res.Get("code"))
	v.Del("client_secret")
	v.Del("client_id")
	r := httptest.NewRequest("POST", "/token", strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetBasicAuth(url.QueryEscape(id), url.QueryEscape(secret))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("basic auth: %d %s", w.Code, w.Body)
	}
}

func TestOAuthSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	db, path := filepath.Join(dir, "dash.db"), filepath.Join(dir, "oauth.json")
	_, o, h := oauthServerAt(t, db, "s3cret")
	if err := o.Keep(path); err != nil {
		t.Fatal(err)
	}
	id, tok := fullFlow(t, h, `{"redirect_uris":["`+callback+`"]}`)

	// Same credentials: clients and tokens are back.
	_, o2, h2 := oauthServerAt(t, db, "s3cret")
	if err := o2.Keep(path); err != nil {
		t.Fatal(err)
	}
	if w := callMCP(h2, tok["access_token"].(string)); w.Code != http.StatusOK {
		t.Errorf("access token after restart: %d", w.Code)
	}
	status, tok2 := tokens(t, h2, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}, "client_id": {id}})
	if status != http.StatusOK {
		t.Errorf("refresh after restart: %d", status)
	}
	o2.mu.Lock()
	for k := range o2.tokens {
		if k == tok["access_token"] {
			t.Errorf("token stored in the clear")
		}
	}
	o2.mu.Unlock()

	// A new DASH_PASSWORD: the account's tokens stop working.
	_, o3, h3 := oauthServerAt(t, db, "changed")
	if err := o3.Keep(path); err != nil {
		t.Fatal(err)
	}
	if w := callMCP(h3, tok2["access_token"].(string)); w.Code != http.StatusUnauthorized {
		t.Errorf("access token after a password change: %d", w.Code)
	}
	if status, _ := tokens(t, h3, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok2["refresh_token"].(string)}, "client_id": {id}}); status != http.StatusBadRequest {
		t.Errorf("refresh after a password change: %d", status)
	}
}

// Tokens in an oauth.json from before users belong to the env account.
func TestOAuthImportsLegacyTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oauth.json")
	a, o, h := newOAuthServer(t)
	b, _ := json.Marshal(map[string]any{
		"account": a.legacy,
		"clients": map[string]any{"c1": map[string]any{"redirect_uris": []string{callback}, "auth_method": "none", "created": time.Now()}},
		"tokens":  map[string]any{sessionKey("old-access"): map[string]any{"client": "c1", "expires": time.Now().Add(time.Hour)}},
	})
	os.WriteFile(path, b, 0o600)
	if err := o.Keep(path); err != nil {
		t.Fatal(err)
	}
	if w := callMCP(h, "old-access"); w.Code != http.StatusOK {
		t.Fatalf("legacy token: %d", w.Code)
	}
}

// A token stops working once its user belongs to no org.
func TestOAuthTokenNeedsMembership(t *testing.T) {
	a, _, h := newOAuthServer(t)
	_, tok := fullFlow(t, h, `{"redirect_uris":["`+callback+`"]}`)
	u, _ := a.acc.EnvUser()
	org, _ := a.acc.DefaultOrg()
	other := addUser(t, a, org.ID, "other", store.RoleOwner)
	if err := a.acc.RemoveMember(other, org.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	if w := callMCP(h, tok["access_token"].(string)); w.Code != http.StatusUnauthorized {
		t.Fatalf("token of a removed member: %d", w.Code)
	}
}
