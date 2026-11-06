package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// OAuth is an OAuth 2.1 authorization server for remote MCP clients such as
// claude.ai custom connectors: dynamic client registration (RFC 7591), the
// authorization code grant with PKCE S256 behind the Dash login, and
// rotating refresh tokens. Its access tokens are only good for MCPPath.
type OAuth struct {
	a    *Auth
	base string // public origin: issuer, and base of the protected resource

	// RedirectURIs clients may register, besides http loopback ones.
	RedirectURIs []string

	mu      sync.Mutex
	clients map[string]*oauthClient
	codes   map[string]authCode   // by hash
	tokens  map[string]oauthToken // by hash, so the file can't be used to call Dash
	store   string
}

const (
	MCPPath = "/mcp" // the protected resource

	codeTTL    = time.Minute
	accessTTL  = time.Hour
	refreshTTL = 30 * 24 * time.Hour
	maxClients = 100
)

// DefaultRedirectURIs are claude.ai's (and claude.com's) connector callbacks.
var DefaultRedirectURIs = []string{
	"https://claude.ai/api/mcp/auth_callback",
	"https://claude.com/api/mcp/auth_callback",
}

type oauthClient struct {
	Name         string    `json:"name,omitempty"`
	RedirectURIs []string  `json:"redirect_uris"`
	AuthMethod   string    `json:"auth_method"`
	Secret       string    `json:"secret,omitempty"` // hash; empty for public clients
	Created      time.Time `json:"created"`
}

type authCode struct {
	client, redirectURI, challenge string
	expires                        time.Time
}

type oauthToken struct {
	Client  string    `json:"client"`
	Refresh bool      `json:"refresh,omitempty"`
	Expires time.Time `json:"expires"`
}

type oauthFile struct {
	Account string                  `json:"account"` // hash of the credentials, as in sessionFile
	Clients map[string]*oauthClient `json:"clients"`
	Tokens  map[string]oauthToken   `json:"tokens"`
}

// NewOAuth serves OAuth for a at publicURL, the origin (https://host[:port])
// clients reach Dash on; it is the issuer and publicURL+MCPPath the resource.
func NewOAuth(a *Auth, publicURL string) (*OAuth, error) {
	u, err := url.Parse(strings.TrimRight(publicURL, "/"))
	if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" ||
		!(u.Scheme == "https" || u.Scheme == "http" && loopback(u.Hostname())) {
		return nil, fmt.Errorf("public URL %q: want https://host[:port] with no path", publicURL)
	}
	o := &OAuth{
		a: a, base: u.Scheme + "://" + u.Host,
		RedirectURIs: DefaultRedirectURIs,
		clients:      map[string]*oauthClient{},
		codes:        map[string]authCode{},
		tokens:       map[string]oauthToken{},
	}
	a.oauth = o
	return o, nil
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}

// Resource is the URL of the MCP endpoint the tokens are for.
func (o *OAuth) Resource() string { return o.base + MCPPath }

func (o *OAuth) public(path string) bool {
	if o == nil {
		return false
	}
	switch path {
	case MCPPath, "/authorize", "/token", "/register",
		"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource" + MCPPath,
		"/.well-known/oauth-authorization-server":
		return true
	}
	return false
}

// Keep saves clients and tokens in path, like KeepSessions.
func (o *OAuth) Keep(path string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.store = path
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f oauthFile
	if json.Unmarshal(b, &f) != nil || f.Account != o.a.account() {
		return nil
	}
	for id, c := range f.Clients {
		o.clients[id] = c
	}
	now := o.a.now()
	for k, t := range f.Tokens {
		if now.Before(t.Expires) && o.clients[t.Client] != nil {
			o.tokens[k] = t
		}
	}
	return nil
}

func (o *OAuth) saveLocked() {
	if o.store == "" {
		return
	}
	b, _ := json.Marshal(oauthFile{Account: o.a.account(), Clients: o.clients, Tokens: o.tokens})
	tmp := o.store + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, o.store)
	}
}

// Routes adds the metadata, registration, authorization and token endpoints.
func (o *OAuth) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", o.resourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource"+MCPPath, o.resourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", o.serverMetadata)
	mux.HandleFunc("POST /register", o.register)
	mux.HandleFunc("GET /authorize", o.authorize)
	mux.HandleFunc("POST /authorize", o.authorize)
	mux.HandleFunc("POST /token", o.token)
}

// Bearer lets requests with a valid access token through to next and answers
// the rest with 401 and where to find the authorization server.
func (o *OAuth) Bearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, sent := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if sent && o.valid(tok) {
			next.ServeHTTP(w, r)
			return
		}
		v := fmt.Sprintf("Bearer resource_metadata=%q", o.base+"/.well-known/oauth-protected-resource")
		if sent {
			v += `, error="invalid_token"`
		}
		w.Header().Set("WWW-Authenticate", v)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
	})
}

func (o *OAuth) valid(tok string) bool {
	if tok == "" {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	t, ok := o.tokens[sessionKey(tok)]
	return ok && !t.Refresh && o.a.now().Before(t.Expires)
}

// ---- metadata ----

func (o *OAuth) resourceMetadata(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{ // RFC 9728
		"resource":                 o.Resource(),
		"authorization_servers":    []string{o.base},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Dash",
	})
}

func (o *OAuth) serverMetadata(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{ // RFC 8414
		"issuer":                                         o.base,
		"authorization_endpoint":                         o.base + "/authorize",
		"token_endpoint":                                 o.base + "/token",
		"registration_endpoint":                          o.base + "/register",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          authMethods,
		"authorization_response_iss_parameter_supported": true,
	})
}

var authMethods = []string{"none", "client_secret_post", "client_secret_basic"}

// ---- registration ----

func oauthErr(w http.ResponseWriter, code int, kind, desc string) {
	writeJSON(w, code, map[string]string{"error": kind, "error_description": desc})
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (o *OAuth) allowedRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.Host == "" {
		return false
	}
	return slices.Contains(o.RedirectURIs, raw) || u.Scheme == "http" && loopback(u.Hostname())
}

func (o *OAuth) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RedirectURIs  []string `json:"redirect_uris"`
		ClientName    string   `json:"client_name"`
		GrantTypes    []string `json:"grant_types"`
		ResponseTypes []string `json:"response_types"`
		AuthMethod    string   `json:"token_endpoint_auth_method"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		oauthErr(w, http.StatusBadRequest, "invalid_client_metadata", "bad JSON")
		return
	}
	if len(req.RedirectURIs) == 0 {
		oauthErr(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris is required")
		return
	}
	for _, u := range req.RedirectURIs {
		if !o.allowedRedirect(u) {
			oauthErr(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uri "+u+" is not allowed")
			return
		}
	}
	if req.GrantTypes == nil {
		req.GrantTypes = []string{"authorization_code"}
	}
	for _, g := range req.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			oauthErr(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported grant type "+g)
			return
		}
	}
	if req.ResponseTypes == nil {
		req.ResponseTypes = []string{"code"}
	}
	if len(req.ResponseTypes) != 1 || req.ResponseTypes[0] != "code" {
		oauthErr(w, http.StatusBadRequest, "invalid_client_metadata", "response_types must be [code]")
		return
	}
	if req.AuthMethod == "" {
		req.AuthMethod = "none"
	}
	if !slices.Contains(authMethods, req.AuthMethod) {
		oauthErr(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported token_endpoint_auth_method")
		return
	}
	if len(req.ClientName) > 100 {
		req.ClientName = req.ClientName[:100]
	}

	id, secret := randomToken(), ""
	c := &oauthClient{Name: req.ClientName, RedirectURIs: req.RedirectURIs, AuthMethod: req.AuthMethod, Created: o.a.now()}
	if req.AuthMethod != "none" {
		secret = randomToken()
		c.Secret = sessionKey(secret)
	}
	o.mu.Lock()
	o.pruneClientsLocked()
	if len(o.clients) >= maxClients {
		o.mu.Unlock()
		oauthErr(w, http.StatusServiceUnavailable, "temporarily_unavailable", "too many registered clients")
		return
	}
	o.clients[id] = c
	o.saveLocked()
	o.mu.Unlock()

	res := map[string]any{
		"client_id":                  id,
		"client_id_issued_at":        c.Created.Unix(),
		"client_name":                c.Name,
		"redirect_uris":              c.RedirectURIs,
		"grant_types":                req.GrantTypes,
		"response_types":             req.ResponseTypes,
		"token_endpoint_auth_method": c.AuthMethod,
	}
	if secret != "" {
		res["client_secret"], res["client_secret_expires_at"] = secret, 0
	}
	writeJSON(w, http.StatusCreated, res)
}

// pruneClientsLocked drops clients that registered over an hour ago and
// hold no tokens: abandoned or never authorized.
func (o *OAuth) pruneClientsLocked() {
	if len(o.clients) < maxClients {
		return
	}
	used := map[string]bool{}
	for _, t := range o.tokens {
		used[t.Client] = true
	}
	for id, c := range o.clients {
		if !used[id] && o.a.now().Sub(c.Created) > time.Hour {
			delete(o.clients, id)
		}
	}
}

func (o *OAuth) client(id string) *oauthClient {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.clients[id]
}

// ---- authorization ----

type authRequest struct {
	ClientID, RedirectURI, State, Challenge, Resource string
	ClientName, RedirectHost, Error                   string
}

func (o *OAuth) authorize(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY") // no clickjacking the Allow button
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f := r.Form
	q := authRequest{ClientID: f.Get("client_id"), RedirectURI: f.Get("redirect_uri"), State: f.Get("state"),
		Challenge: f.Get("code_challenge"), Resource: f.Get("resource")}
	c := o.client(q.ClientID)
	if c == nil {
		http.Error(w, "Unknown client. Remove the connector and add it again.", http.StatusBadRequest)
		return
	}
	// Never redirect anywhere that isn't registered: it would leak the code.
	if !slices.Contains(c.RedirectURIs, q.RedirectURI) {
		http.Error(w, "redirect_uri is not registered for this client.", http.StatusBadRequest)
		return
	}
	reply := func(params url.Values) {
		u, _ := url.Parse(q.RedirectURI)
		v := u.Query()
		for k, vs := range params {
			v[k] = vs
		}
		if q.State != "" {
			v.Set("state", q.State)
		}
		v.Set("iss", o.base) // RFC 9207
		u.RawQuery = v.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	}
	fail := func(kind, desc string) { reply(url.Values{"error": {kind}, "error_description": {desc}}) }
	switch {
	case f.Get("response_type") != "code":
		fail("unsupported_response_type", "only response_type=code is supported")
		return
	case f.Get("code_challenge_method") != "S256" || len(q.Challenge) != 43:
		fail("invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	case q.Resource != "" && strings.TrimRight(q.Resource, "/") != o.Resource():
		fail("invalid_target", "unknown resource "+q.Resource)
		return
	}
	q.ClientName = c.Name
	if u, err := url.Parse(q.RedirectURI); err == nil {
		q.RedirectHost = u.Host
	}
	if r.Method == http.MethodGet {
		o.consent(w, http.StatusOK, q)
		return
	}
	if r.PostForm.Get("action") != "allow" {
		fail("access_denied", "the user declined")
		return
	}
	switch err := o.a.attempt(o.a.clientIP(r), r.PostForm.Get("username"), r.PostForm.Get("password")); err {
	case errLockedOut:
		q.Error = err.Error()
		o.consent(w, http.StatusTooManyRequests, q)
		return
	case errBadLogin:
		q.Error = err.Error()
		o.consent(w, http.StatusUnauthorized, q)
		return
	}
	code := randomToken()
	o.mu.Lock()
	now := o.a.now()
	for k, c := range o.codes {
		if now.After(c.expires) {
			delete(o.codes, k)
		}
	}
	o.codes[sessionKey(code)] = authCode{client: q.ClientID, redirectURI: q.RedirectURI, challenge: q.Challenge, expires: now.Add(codeTTL)}
	o.mu.Unlock()
	reply(url.Values{"code": {code}})
}

var consentPage = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Connect to Dash</title>
<style>
body{font:16px system-ui,sans-serif;background:#111;color:#eee;display:flex;justify-content:center;padding:2rem 1rem;margin:0}
main{max-width:24rem;width:100%}h1{font-size:1.4rem}p{color:#bbb;line-height:1.4}
label{display:block;margin:.8rem 0 .3rem}input{width:100%;box-sizing:border-box;padding:.6rem;border-radius:6px;border:1px solid #444;background:#1c1c1c;color:#eee;font-size:1rem}
.err{color:#f77}.row{display:flex;gap:.6rem;margin-top:1.2rem}button{flex:1;padding:.7rem;border-radius:6px;border:0;font-size:1rem;cursor:pointer}
.allow{background:#d97757;color:#fff}.deny{background:#333;color:#eee}
</style></head><body><main>
<h1>Connect {{if .ClientName}}“{{.ClientName}}”{{else}}an app{{end}} to Dash?</h1>
<p>It will be able to see your projects and tasks and create, run, approve, rework, cancel and delete tasks on every computer in this dashboard. Tasks run Claude Code on those computers.</p>
<p>After you allow it, you go back to <b>{{.RedirectHost}}</b>.</p>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/authorize">
<input type="hidden" name="response_type" value="code">
<input type="hidden" name="code_challenge_method" value="S256">
<input type="hidden" name="client_id" value="{{.ClientID}}">
<input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
<input type="hidden" name="state" value="{{.State}}">
<input type="hidden" name="code_challenge" value="{{.Challenge}}">
<input type="hidden" name="resource" value="{{.Resource}}">
<label for="u">Dash username</label><input id="u" name="username" autocomplete="username">
<label for="p">Password</label><input id="p" name="password" type="password" autocomplete="current-password">
<div class="row"><button class="deny" name="action" value="deny" formnovalidate>Deny</button>
<button class="allow" name="action" value="allow">Allow</button></div>
</form></main></body></html>
`))

func (o *OAuth) consent(w http.ResponseWriter, code int, q authRequest) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = consentPage.Execute(w, q)
}

// ---- tokens ----

func (o *OAuth) token(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		oauthErr(w, http.StatusBadRequest, "invalid_request", "bad form")
		return
	}
	f := r.PostForm
	id, secret, basic := r.BasicAuth()
	if basic { // form-encoded, RFC 6749 §2.3.1
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = f.Get("client_id"), f.Get("client_secret")
	}
	c := o.client(id)
	if c == nil || c.Secret != "" && subtle.ConstantTimeCompare([]byte(sessionKey(secret)), []byte(c.Secret)) != 1 {
		oauthErr(w, http.StatusUnauthorized, "invalid_client", "unknown client or wrong secret")
		return
	}
	if res := f.Get("resource"); res != "" && strings.TrimRight(res, "/") != o.Resource() {
		oauthErr(w, http.StatusBadRequest, "invalid_target", "unknown resource "+res)
		return
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.a.now()
	switch f.Get("grant_type") {
	case "authorization_code":
		k := sessionKey(f.Get("code"))
		code, ok := o.codes[k]
		delete(o.codes, k) // single use, whatever happens next
		if !ok || code.client != id || now.After(code.expires) || code.redirectURI != f.Get("redirect_uri") {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "unknown or expired code, or wrong redirect_uri")
			return
		}
		if !verifierMatches(f.Get("code_verifier"), code.challenge) {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match")
			return
		}
	case "refresh_token":
		k := sessionKey(f.Get("refresh_token"))
		t, ok := o.tokens[k]
		if !ok || !t.Refresh || t.Client != id || now.After(t.Expires) {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "unknown or expired refresh token")
			return
		}
		delete(o.tokens, k) // rotation: each refresh token works once
	default:
		oauthErr(w, http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token")
		return
	}

	for k, t := range o.tokens {
		if now.After(t.Expires) {
			delete(o.tokens, k)
		}
	}
	access, refresh := randomToken(), randomToken()
	o.tokens[sessionKey(access)] = oauthToken{Client: id, Expires: now.Add(accessTTL)}
	o.tokens[sessionKey(refresh)] = oauthToken{Client: id, Refresh: true, Expires: now.Add(refreshTTL)}
	o.saveLocked()
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(accessTTL.Seconds()),
		"refresh_token": refresh,
	})
}

func verifierMatches(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	h := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(h[:])), []byte(challenge)) == 1
}
