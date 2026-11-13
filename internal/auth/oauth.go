package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/branding"
	"github.com/klahr/dash/internal/netutil"
	"github.com/klahr/dash/internal/store"
)

// OAuth is an OAuth 2.1 authorization server for remote MCP clients such as
// claude.ai custom connectors: dynamic client registration (RFC 7591), the
// authorization code grant with PKCE S256 behind the Dash login, and
// rotating refresh tokens. The user picks the org the client acts in; what
// the client gets is a grant in the token table (store.TokenOAuth), pinned
// to that org, listed and revocable with the user's other tokens. Its access
// tokens are only good for MCPPath, as are API tokens there.
type OAuth struct {
	a    *Auth
	base string // public origin: issuer, and base of the protected resource

	// RedirectURIs clients may register, besides http loopback ones.
	RedirectURIs []string

	mu      sync.Mutex
	codes   map[string]authCode      // by hash; short-lived, so not stored
	tickets map[string]consentTicket // by hash
}

const (
	MCPPath = "/mcp" // the protected resource

	codeTTL          = time.Minute
	consentTicketTTL = 10 * time.Minute
	accessTTL        = time.Hour
	refreshTTL       = 30 * 24 * time.Hour
	maxClients       = 100
)

// DefaultRedirectURIs are claude.ai's (and claude.com's) connector callbacks.
var DefaultRedirectURIs = []string{
	"https://claude.ai/api/mcp/auth_callback",
	"https://claude.com/api/mcp/auth_callback",
}

type authCode struct {
	client, redirectURI, challenge string
	user, org                      int64
	scopes                         []string
	expires                        time.Time
}

// consentTicket stands for a signed-in user on the consent page's second
// step, where someone in several orgs picks one: their password isn't sent
// again. It is bound to the authorization request it was made for.
type consentTicket struct {
	client, redirectURI, challenge string
	user                           int64
	expires                        time.Time
}

// NewOAuth serves OAuth for a at publicURL, the origin (https://host[:port])
// clients reach Dash on; it is the issuer and publicURL+MCPPath the resource.
func NewOAuth(a *Auth, publicURL string) (*OAuth, error) {
	u, err := url.Parse(strings.TrimRight(publicURL, "/"))
	if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" ||
		!(u.Scheme == "https" || u.Scheme == "http" && netutil.LoopbackHost(u.Hostname())) {
		return nil, fmt.Errorf("public URL %q: want https://host[:port] with no path", publicURL)
	}
	o := &OAuth{
		a: a, base: u.Scheme + "://" + u.Host,
		RedirectURIs: DefaultRedirectURIs,
		codes:        map[string]authCode{},
		tickets:      map[string]consentTicket{},
	}
	a.oauth = o
	return o, nil
}

// Resource is the URL of the MCP endpoint the tokens are for.
func (o *OAuth) Resource() string { return o.base + MCPPath }

// issuer is the authorization server r reached: the public URL's origin, or
// on an org's subdomain (Auth.Domains) that subdomain's, whose tokens are
// pinned to that org with no org to pick.
func (o *OAuth) issuer(r *http.Request) string {
	if origin, ok := o.a.OrgOrigin(r); ok {
		return origin
	}
	return o.base
}

// ResourceFor is the MCP endpoint r reached (see issuer).
func (o *OAuth) ResourceFor(r *http.Request) string { return o.issuer(r) + MCPPath }

// Connected counts the clients signed in now: those holding a grant that
// can still be refreshed.
func (o *OAuth) Connected() int { return o.a.acc.ConnectedClients(o.a.now()) }

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

// oauthFile is oauth.json, where clients and tokens were kept before the
// token table.
type oauthFile struct {
	// Account is set in files from before users: a hash of the single
	// account's credentials, whose tokens then had no User.
	Account string `json:"account,omitempty"`
	Clients map[string]struct {
		Name         string    `json:"name,omitempty"`
		RedirectURIs []string  `json:"redirect_uris"`
		AuthMethod   string    `json:"auth_method"`
		Secret       string    `json:"secret,omitempty"` // hash
		Created      time.Time `json:"created"`
	} `json:"clients"`
	Tokens map[string]struct { // by hash
		Client  string    `json:"client"`
		User    int64     `json:"user"`
		Issued  time.Time `json:"issued"`
		Refresh bool      `json:"refresh,omitempty"`
		Expires time.Time `json:"expires"`
	} `json:"tokens"`
}

// ImportLegacy moves the clients and refresh tokens of an oauth.json into
// the database, then renames it to <path>.imported. Tokens weren't pinned to
// an org then: one whose user is in exactly one org becomes a grant pinned
// to it (connectors refresh their access token and carry on); the rest are
// dropped, and those clients must be connected again.
func (o *OAuth) ImportLegacy(path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f oauthFile
	if json.Unmarshal(b, &f) == nil {
		for id, c := range f.Clients {
			if err := o.a.acc.ImportOAuthClient(store.OAuthClient{ID: id, Name: c.Name, RedirectURIs: c.RedirectURIs,
				AuthMethod: c.AuthMethod, SecretHash: c.Secret, Created: c.Created}); err != nil {
				return err
			}
		}
		now := o.a.now()
		legacy, isLegacy := o.a.legacyUser(f.Account) // tokens from before users belong to the env account
		for hash, t := range f.Tokens {
			if t.User == 0 && isLegacy {
				t.User, t.Issued = legacy, now
			}
			if !t.Refresh || t.User == 0 || !now.Before(t.Expires) || f.Clients[t.Client].RedirectURIs == nil {
				continue
			}
			err := o.a.acc.ImportGrant(t.Client, t.User, hash, t.Issued, t.Expires, now)
			if errors.Is(err, store.ErrNoSoleOrg) {
				slog.Warn("OAuth client must connect again: its user is in several orgs", "client", t.Client, "user", t.User)
				continue
			}
			if err != nil {
				return err
			}
		}
	}
	return os.Rename(path, path+".imported")
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

// Bearer lets requests with a valid OAuth access token or API token through
// to next, as the token's user in its org (From), and answers the rest with
// 401 and where to find the authorization server. Scopes are next's to check
// (Identity.Can).
func (o *OAuth) Bearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, sent := bearer(r)
		if sent {
			// On an org's subdomain, only tokens of that org.
			if id, err := o.a.tokenIdentity(tok, store.TokenOAuth, store.TokenAPI); err == nil && o.a.pinned(r, id) {
				next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), id)))
				return
			}
		}
		v := fmt.Sprintf("Bearer resource_metadata=%q", o.issuer(r)+"/.well-known/oauth-protected-resource")
		if sent {
			v += `, error="invalid_token"`
		}
		w.Header().Set("WWW-Authenticate", v)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
	})
}

// ---- metadata ----

func (o *OAuth) resourceMetadata(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{ // RFC 9728
		"resource":                 o.ResourceFor(r),
		"authorization_servers":    []string{o.issuer(r)},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            o.a.brand().Name,
		"scopes_supported":         store.UserScopes,
	})
}

func (o *OAuth) serverMetadata(w http.ResponseWriter, r *http.Request) {
	iss := o.issuer(r)
	writeJSON(w, http.StatusOK, map[string]any{ // RFC 8414
		"issuer":                                         iss,
		"authorization_endpoint":                         iss + "/authorize",
		"token_endpoint":                                 iss + "/token",
		"registration_endpoint":                          iss + "/register",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          authMethods,
		"authorization_response_iss_parameter_supported": true,
		"scopes_supported":                               store.UserScopes,
	})
}

var authMethods = []string{"none", "client_secret_post", "client_secret_basic"}

// ---- registration ----

func oauthErr(w http.ResponseWriter, code int, kind, desc string) {
	writeJSON(w, code, map[string]string{"error": kind, "error_description": desc})
}

func (o *OAuth) allowedRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.Host == "" {
		return false
	}
	return slices.Contains(o.RedirectURIs, raw) || u.Scheme == "http" && netutil.LoopbackHost(u.Hostname())
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

	secret := ""
	c := store.OAuthClient{ID: randomString(32), Name: req.ClientName, RedirectURIs: req.RedirectURIs, AuthMethod: req.AuthMethod, Created: o.a.now()}
	if req.AuthMethod != "none" {
		secret = randomString(32)
		c.SecretHash = sessionKey(secret)
	}
	if err := o.a.acc.CreateOAuthClient(c, maxClients, o.a.now()); errors.Is(err, store.ErrTooManyClients) {
		oauthErr(w, http.StatusServiceUnavailable, "temporarily_unavailable", "too many registered clients")
		return
	} else if err != nil {
		slog.Error("register OAuth client", "err", err)
		oauthErr(w, http.StatusInternalServerError, "server_error", "registration failed")
		return
	}
	id := c.ID

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

func (o *OAuth) client(id string) *store.OAuthClient {
	if id == "" {
		return nil
	}
	c, err := o.a.acc.OAuthClient(id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Error("look up OAuth client", "err", err)
		}
		return nil
	}
	return &c
}

// ---- authorization ----

type authRequest struct {
	ClientID, RedirectURI, State, Challenge, Resource, Scope string
	ClientName, RedirectHost, Error                          string
	Scopes                                                   []string
	// The second step, for someone in several orgs: who signed in (the
	// ticket) and the orgs to pick from.
	Ticket string
	Orgs   []store.Membership
	OrgID  int64
	// Brand names Dash on the page (set by consent).
	Brand branding.Config
}

// ReadOnly reports whether the client asked for read access only.
func (q authRequest) ReadOnly() bool { return !slices.Contains(q.Scopes, store.ScopeWrite) }

// parseScope checks an OAuth scope parameter: read and/or write, both if
// it's empty.
func parseScope(scope string) ([]string, bool) {
	if strings.TrimSpace(scope) == "" {
		return slices.Clone(store.UserScopes), true
	}
	var out []string
	for _, sc := range strings.Fields(scope) {
		if !slices.Contains(store.UserScopes, sc) {
			return nil, false
		}
		if !slices.Contains(out, sc) {
			out = append(out, sc)
		}
	}
	slices.Sort(out)
	return out, true
}

// Errors from pickOrg.
var (
	errNoOrgs     = errors.New("your account isn't a member of any organization")
	errChooseOrg  = errors.New("choose the organization to connect")
	errNotInOrgID = errors.New("you aren't a member of that organization")
)

// pickOrg checks the org the user chose for a client to act in. Someone in
// one org acts in it; someone in several must choose; a choice must be one of
// theirs. There is no default: never the first org.
func pickOrg(orgs []store.Membership, orgID int64) (store.Membership, error) {
	switch {
	case orgID != 0:
		for _, m := range orgs {
			if m.ID == orgID {
				return m, nil
			}
		}
		return store.Membership{}, errNotInOrgID
	case len(orgs) == 1:
		return orgs[0], nil
	case len(orgs) > 1:
		return store.Membership{}, errChooseOrg
	}
	return store.Membership{}, errNoOrgs
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
		Challenge: f.Get("code_challenge"), Resource: f.Get("resource"), Scope: f.Get("scope")}
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
		v.Set("iss", o.issuer(r)) // RFC 9207
		u.RawQuery = v.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	}
	fail := func(kind, desc string) { reply(url.Values{"error": {kind}, "error_description": {desc}}) }
	scopes, scopeOK := parseScope(q.Scope)
	switch {
	case f.Get("response_type") != "code":
		fail("unsupported_response_type", "only response_type=code is supported")
		return
	case f.Get("code_challenge_method") != "S256" || len(q.Challenge) != 43:
		fail("invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	case q.Resource != "" && strings.TrimRight(q.Resource, "/") != o.ResourceFor(r):
		fail("invalid_target", "unknown resource "+q.Resource)
		return
	case !scopeOK:
		fail("invalid_scope", "scope must be read and/or write")
		return
	}
	q.Scopes = scopes
	q.ClientName = c.Name
	if u, err := url.Parse(q.RedirectURI); err == nil {
		q.RedirectHost = u.Host
	}
	if r.Method == http.MethodGet {
		o.consent(w, http.StatusOK, q)
		return
	}
	ticket := r.PostForm.Get("ticket")
	if r.PostForm.Get("action") != "allow" {
		o.takeTicket(ticket, q) // spent either way
		fail("access_denied", "the user declined")
		return
	}

	// Who: a password (and second factor), or the ticket of the first step.
	var uid int64
	if ticket != "" {
		t, ok := o.takeTicket(ticket, q)
		if !ok {
			q.Error = "this page has expired; sign in again"
			o.consent(w, http.StatusBadRequest, q)
			return
		}
		uid = t.user
	} else {
		ip := o.a.clientIP(r)
		u, err := o.a.attempt(ip, strings.TrimSpace(r.PostForm.Get("username")), r.PostForm.Get("password"))
		if err == nil && u.TOTP {
			if strings.TrimSpace(r.PostForm.Get("code")) == "" {
				q.Error = "Enter the code from your authenticator app, or a recovery code."
				o.consent(w, http.StatusUnauthorized, q)
				return
			}
			_, err = o.a.secondFactor(ip, u.ID, r.PostForm.Get("code"))
		}
		switch {
		case errors.Is(err, errLockedOut):
			q.Error = err.Error()
			o.consent(w, http.StatusTooManyRequests, q)
			return
		case errors.Is(err, errBadLogin), errors.Is(err, errBadCode):
			q.Error = err.Error()
			o.consent(w, http.StatusUnauthorized, q)
			return
		case errors.Is(err, errUnverified):
			q.Error = err.Error()
			o.consent(w, http.StatusForbidden, q)
			return
		case err != nil:
			q.Error = "sign-in failed"
			o.consent(w, http.StatusInternalServerError, q)
			return
		}
		o.a.signedInFrom(ip)
		if required, _ := o.a.acc.Requires2FA(u.ID); required && !u.TOTP {
			q.Error = "Your organization requires two-factor sign-in. Sign in to Dash and set it up first."
			o.consent(w, http.StatusForbidden, q)
			return
		}
		uid = u.ID
	}

	// Which org the client will act in.
	orgs, err := o.a.acc.Memberships(uid)
	if err != nil {
		q.Error = "sign-in failed"
		o.consent(w, http.StatusInternalServerError, q)
		return
	}
	if _, onOrg := HostSlug(r.Context()); onOrg {
		// An org's subdomain: that org, and no other to pick. Whether it
		// exists is only told to its members.
		host, ok := o.a.hostOrg(r)
		var only []store.Membership
		for _, m := range orgs {
			if ok && m.ID == host.ID {
				only = append(only, m)
			}
		}
		if len(only) == 0 {
			q.Error = "There is no organization here, or your account isn't a member of it."
			o.consent(w, http.StatusForbidden, q)
			return
		}
		orgs = only
	}
	orgID, _ := strconv.ParseInt(r.PostForm.Get("org_id"), 10, 64)
	org, err := pickOrg(orgs, orgID)
	switch {
	case errors.Is(err, errNoOrgs):
		q.Error = err.Error()
		o.consent(w, http.StatusForbidden, q)
		return
	case err != nil: // choose, or choose again
		q.Orgs, q.Ticket = orgs, o.newTicket(uid, q)
		code := http.StatusOK
		if errors.Is(err, errNotInOrgID) {
			q.Error, code = err.Error(), http.StatusForbidden
		}
		o.consent(w, code, q)
		return
	}
	// This sign-in is a password: an org that takes only Google sign-ins
	// from them gets no grant from it.
	user, err := o.a.acc.User(uid)
	if err != nil {
		q.Error = "sign-in failed"
		o.consent(w, http.StatusInternalServerError, q)
		return
	}
	if o.a.googleRequiredIn(user.Email, org) {
		q.Error = org.Name + " requires signing in with Google, so it can't be connected with a password."
		o.consent(w, http.StatusForbidden, q)
		return
	}

	code := randomString(32)
	o.mu.Lock()
	now := o.a.now()
	for k, c := range o.codes {
		if now.After(c.expires) {
			delete(o.codes, k)
		}
	}
	o.codes[sessionKey(code)] = authCode{client: q.ClientID, redirectURI: q.RedirectURI, challenge: q.Challenge,
		user: uid, org: org.ID, scopes: scopes, expires: now.Add(codeTTL)}
	o.mu.Unlock()
	reply(url.Values{"code": {code}})
}

// newTicket makes the ticket for uid's second step of q.
func (o *OAuth) newTicket(uid int64, q authRequest) string {
	t := randomString(32)
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.a.now()
	for k, c := range o.tickets {
		if now.After(c.expires) {
			delete(o.tickets, k)
		}
	}
	o.tickets[sessionKey(t)] = consentTicket{client: q.ClientID, redirectURI: q.RedirectURI, challenge: q.Challenge,
		user: uid, expires: now.Add(consentTicketTTL)}
	return t
}

// takeTicket spends a ticket made for q.
func (o *OAuth) takeTicket(t string, q authRequest) (consentTicket, bool) {
	if t == "" {
		return consentTicket{}, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	k := sessionKey(t)
	c, ok := o.tickets[k]
	delete(o.tickets, k)
	ok = ok && o.a.now().Before(c.expires) && c.client == q.ClientID && c.redirectURI == q.RedirectURI && c.challenge == q.Challenge
	return c, ok
}

var consentPage = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Connect to {{.Brand.Name}}</title>
<style>
body{font:16px system-ui,sans-serif;background:#111;color:#eee;display:flex;justify-content:center;padding:2rem 1rem;margin:0}
main{max-width:24rem;width:100%}h1{font-size:1.4rem}p{color:#bbb;line-height:1.4}
label{display:block;margin:.8rem 0 .3rem}input,select{width:100%;box-sizing:border-box;padding:.6rem;border-radius:6px;border:1px solid #444;background:#1c1c1c;color:#eee;font-size:1rem}
.err{color:#f77}.row{display:flex;gap:.6rem;margin-top:1.2rem}button{flex:1;padding:.7rem;border-radius:6px;border:0;font-size:1rem;cursor:pointer}
.allow{background:#d97757;color:#fff}.deny{background:#333;color:#eee}
.site-footer{margin-top:2rem;font-size:.85rem;color:#888;text-align:center}.site-footer a{color:#aaa}
</style></head><body><main>
<h1>Connect {{if .ClientName}}“{{.ClientName}}”{{else}}an app{{end}} to {{.Brand.Name}}?</h1>
{{if .ReadOnly}}<p>It will be able to see the projects, tasks and issues of the organization you connect it to, on every computer in its dashboard. It can't change anything.</p>
{{else}}<p>It will be able to see the projects and tasks of the organization you connect it to and create, run, approve, rework, cancel and delete tasks on every computer in its dashboard. Tasks run Claude Code on those computers.</p>{{end}}
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
<input type="hidden" name="scope" value="{{.Scope}}">
{{if .Ticket}}<input type="hidden" name="ticket" value="{{.Ticket}}">
<label for="org">Organization</label><select id="org" name="org_id" required>
<option value="">Choose…</option>
{{range .Orgs}}<option value="{{.ID}}">{{.Name}}</option>{{end}}
</select>
{{else}}<label for="u">{{.Brand.Name}} username</label><input id="u" name="username" autocomplete="username">
<label for="p">Password</label><input id="p" name="password" type="password" autocomplete="current-password">
<label for="c">Two-factor code <small>(if you've turned it on)</small></label><input id="c" name="code" inputmode="numeric" autocomplete="one-time-code">{{end}}
<div class="row"><button class="deny" name="action" value="deny" formnovalidate>Deny</button>
<button class="allow" name="action" value="allow">Allow</button></div>
</form>{{.Brand.Footer}}</main></body></html>
`))

func (o *OAuth) consent(w http.ResponseWriter, code int, q authRequest) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	q.Brand = o.a.brand()
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
	if c == nil || c.SecretHash != "" && subtle.ConstantTimeCompare([]byte(sessionKey(secret)), []byte(c.SecretHash)) != 1 {
		oauthErr(w, http.StatusUnauthorized, "invalid_client", "unknown client or wrong secret")
		return
	}
	if res := f.Get("resource"); res != "" && strings.TrimRight(res, "/") != o.ResourceFor(r) {
		oauthErr(w, http.StatusBadRequest, "invalid_target", "unknown resource "+res)
		return
	}

	now := o.a.now()
	var pair store.TokenPair
	var err error
	switch f.Get("grant_type") {
	case "authorization_code":
		k := sessionKey(f.Get("code"))
		o.mu.Lock()
		code, ok := o.codes[k]
		delete(o.codes, k) // single use, whatever happens next
		o.mu.Unlock()
		if !ok || code.client != id || now.After(code.expires) || code.redirectURI != f.Get("redirect_uri") {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "unknown or expired code, or wrong redirect_uri")
			return
		}
		if !verifierMatches(f.Get("code_verifier"), code.challenge) {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match")
			return
		}
		if _, onOrg := HostSlug(r.Context()); onOrg {
			if host, ok := o.a.hostOrg(r); !ok || host.ID != code.org {
				oauthErr(w, http.StatusBadRequest, "invalid_grant", "the code is for another organization")
				return
			}
		}
		_ = o.a.acc.PruneTokens(now)
		pair, err = o.a.acc.CreateGrant(store.Grant{ClientID: id, UserID: code.user, OrgID: code.org, Scopes: code.scopes},
			accessTTL, refreshTTL, now)
		if errors.Is(err, store.ErrNotMember) {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "you are no longer a member of that organization")
			return
		}
	case "refresh_token":
		pair, err = o.a.acc.RefreshGrant(f.Get("refresh_token"), id, accessTTL, refreshTTL, now)
		if errors.Is(err, store.ErrTokenInvalid) {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "unknown, expired or revoked refresh token")
			return
		}
	default:
		oauthErr(w, http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token")
		return
	}
	if err != nil {
		slog.Error("issue OAuth tokens", "err", err)
		oauthErr(w, http.StatusInternalServerError, "server_error", "could not issue tokens")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  pair.Access,
		"token_type":    "Bearer",
		"expires_in":    int(accessTTL.Seconds()),
		"refresh_token": pair.Refresh,
		"scope":         strings.Join(pair.Scopes, " "),
	})
}

func verifierMatches(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	h := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(h[:])), []byte(challenge)) == 1
}
