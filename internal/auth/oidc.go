package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// A small OpenID Connect relying party: the authorization code flow with
// PKCE, state and nonce, and ID tokens checked against the provider's
// published keys (fetched by discovery, cached, refreshed when a token is
// signed with a key it doesn't know). Google is its only provider today;
// another (Microsoft Entra ID, Okta) is another oidcProvider, its own
// claim rules, and buttons.
//
// Dash is also an OAuth server (oauth.go) for MCP clients; that is separate.

// oidcProvider is an OpenID provider Dash signs people in with.
type oidcProvider struct {
	Name   string // its name in user_identities (store.ProviderGoogle)
	Issuer string // the iss of its ID tokens, where discovery starts
	Scopes []string
}

// oidcClient is the client Dash is registered as at a provider.
type oidcClient struct {
	ID, Secret string
	Org        int64 // the org whose own client it is (0: the instance's)
}

// oidcClaims is what Dash reads from a verified ID token.
type oidcClaims struct {
	Subject       string
	Email         string
	EmailVerified bool
	HD            string // Google Workspace hosted domain ("" for consumer accounts)
	Name          string
}

// oidcRP holds what the flows share: each provider's discovery and keys.
type oidcRP struct {
	mu        sync.Mutex
	providers map[string]*oidc.Provider // by issuer
	client    *http.Client              // nil: a default with a timeout
	now       func() time.Time
}

func (rp *oidcRP) httpClient() *http.Client {
	if rp.client != nil {
		return rp.client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// ctx is ctx with rp's HTTP client, for go-oidc and x/oauth2.
func (rp *oidcRP) ctx(ctx context.Context) context.Context {
	return oidc.ClientContext(ctx, rp.httpClient())
}

// provider is issuer's discovery, made once (a failure is tried again
// next time).
func (rp *oidcRP) provider(ctx context.Context, issuer string) (*oidc.Provider, error) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	if p, ok := rp.providers[issuer]; ok {
		return p, nil
	}
	// Its keys are fetched later, outside any one request: not with ctx's
	// deadline, but with the client.
	dctx, cancel := context.WithTimeout(rp.ctx(context.Background()), 15*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(dctx, issuer)
	if err != nil {
		return nil, &flowError{Code: "provider_unavailable", Err: err}
	}
	if rp.providers == nil {
		rp.providers = map[string]*oidc.Provider{}
	}
	rp.providers[issuer] = p
	return p, nil
}

func (rp *oidcRP) config(p *oidc.Provider, prov oidcProvider, c oidcClient, redirectURI string) *oauth2.Config {
	return &oauth2.Config{ClientID: c.ID, ClientSecret: c.Secret, Endpoint: p.Endpoint(), RedirectURL: redirectURI, Scopes: prov.Scopes}
}

// authURL is where the browser goes to sign in at prov.
func (rp *oidcRP) authURL(ctx context.Context, prov oidcProvider, c oidcClient, redirectURI string, f *oidcFlow, extra map[string]string) (string, error) {
	p, err := rp.provider(ctx, prov.Issuer)
	if err != nil {
		return "", err
	}
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(f.Verifier), oidc.Nonce(f.Nonce)}
	for k, v := range extra {
		opts = append(opts, oauth2.SetAuthURLParam(k, v))
	}
	return rp.config(p, prov, c, redirectURI).AuthCodeURL(f.State, opts...), nil
}

// finish exchanges code for an ID token and verifies it: its signature by
// the provider's keys, iss, aud (c's id), exp, and that its nonce is f's.
func (rp *oidcRP) finish(ctx context.Context, prov oidcProvider, c oidcClient, redirectURI, code string, f *oidcFlow) (oidcClaims, error) {
	p, err := rp.provider(ctx, prov.Issuer)
	if err != nil {
		return oidcClaims{}, err
	}
	ctx = rp.ctx(ctx)
	tok, err := rp.config(p, prov, c, redirectURI).Exchange(ctx, code, oauth2.VerifierOption(f.Verifier))
	if err != nil {
		return oidcClaims{}, &flowError{Code: classifyExchange(err), Err: err}
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return oidcClaims{}, &flowError{Code: "no_id_token", Err: errors.New("the token response had no id_token")}
	}
	now := time.Now
	if rp.now != nil {
		now = rp.now
	}
	idt, err := p.VerifierContext(ctx, &oidc.Config{ClientID: c.ID, Now: now}).Verify(ctx, raw)
	if err != nil {
		code := "token_invalid"
		var exp *oidc.TokenExpiredError
		if errors.As(err, &exp) {
			code = "token_expired"
		}
		return oidcClaims{}, &flowError{Code: code, Err: err}
	}
	if idt.Nonce == "" || !constantEqual(idt.Nonce, f.Nonce) {
		return oidcClaims{}, &flowError{Code: "nonce_mismatch", Err: errors.New("the ID token's nonce isn't this sign-in's")}
	}
	var all map[string]any
	if err := idt.Claims(&all); err != nil {
		return oidcClaims{}, &flowError{Code: "token_invalid", Err: err}
	}
	cl := oidcClaims{Subject: idt.Subject}
	cl.Email, _ = all["email"].(string)
	cl.Email = strings.TrimSpace(cl.Email)
	cl.EmailVerified = claimBool(all["email_verified"])
	cl.HD, _ = all["hd"].(string)
	cl.HD = strings.ToLower(strings.TrimSpace(cl.HD))
	cl.Name, _ = all["name"].(string)
	if cl.Subject == "" {
		return oidcClaims{}, &flowError{Code: "token_invalid", Err: errors.New("the ID token has no subject")}
	}
	return cl, nil
}

// claimBool reads a boolean claim, which some providers send as a string.
func claimBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true")
	}
	return false
}

// classifyExchange names why a code exchange failed, from the provider's
// OAuth error code (never shown raw).
func classifyExchange(err error) string {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		switch re.ErrorCode {
		case "invalid_client", "unauthorized_client":
			return "invalid_client"
		case "redirect_uri_mismatch":
			return "redirect_uri_mismatch"
		case "invalid_grant":
			return "invalid_grant"
		}
		if re.Response != nil && re.Response.StatusCode == http.StatusUnauthorized {
			return "invalid_client"
		}
	}
	return "exchange_failed"
}

// oidcFlow is a sign-in started on this hub, waiting for the provider to
// send the browser back (by the hash of its state).
type oidcFlow struct {
	State, Nonce, Verifier string
	Binding                string // also in the browser's cookie: the same browser must come back
	Purpose                string // flowLogin, flowLink, flowTest
	Org                    int64  // the org signing in to (0: none in particular)
	Client                 int64  // whose client: the org's own (its id) or the instance's (0)
	Invite                 string // an invitation's token to accept
	Next                   string // a path to go on to afterwards
	User                   int64  // flowLink, flowTest: who started it
	Expires                time.Time
}

const (
	flowLogin = "login"
	flowLink  = "link"
	flowTest  = "test"

	flowTTL = 10 * time.Minute
)

// flowError is a sign-in that failed for a reason with a name (Code), shown
// as a friendly message (flowMessages); Err is only logged.
type flowError struct {
	Code string
	Err  error
}

func (e *flowError) Error() string {
	if e.Err != nil {
		return e.Code + ": " + e.Err.Error()
	}
	return e.Code
}

func failFlow(code string, format string, args ...any) *flowError {
	return &flowError{Code: code, Err: fmt.Errorf(format, args...)}
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// newFlow keeps a new flow, and drops the expired ones.
func (a *Auth) newFlow(f *oidcFlow) *oidcFlow {
	f.State, f.Nonce, f.Verifier, f.Binding = randomString(24), randomString(24), oauth2.GenerateVerifier(), randomString(24)
	now := a.now()
	f.Expires = now.Add(flowTTL)
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, x := range a.flows {
		if now.After(x.Expires) {
			delete(a.flows, k)
		}
	}
	a.flows[sessionKey(f.State)] = f
	return f
}

// takeFlow spends the flow of state: each works once.
func (a *Auth) takeFlow(state string) (*oidcFlow, bool) {
	if state == "" {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	k := sessionKey(state)
	f, ok := a.flows[k]
	delete(a.flows, k)
	if !ok || a.now().After(f.Expires) {
		return nil, false
	}
	return f, true
}
