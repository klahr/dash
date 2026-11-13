package auth

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/klahr/dash/internal/store"
)

// Sign in with Google (OpenID Connect), for organizations on Google
// Workspace. Dash is the relying party (oidc.go); Google proves who someone
// is, and the same rules as for everyone else decide which orgs they enter.
//
//	GET|POST /auth/google/start {org, next, purpose, invite}
//	                              to Google; purpose login (default), link (the
//	                              signed-in user links Google) or test (an org
//	                              admin checks the setup: never signs in)
//	GET      /auth/google/callback  back from Google
//	GET      /api/auth/google?org=  whether the sign-in page shows the button
//
// The client is the org's own when its owner set one (its consent screen
// can be "Internal" to their Workspace), else the instance's (GoogleConfig,
// or a platform admin's in settings). Without either, there is no button
// and these routes are 404. Google sends every browser back to one
// redirect URI, on the base domain (GoogleCallbackPath): the flow starts
// there too, so its cookie is the base domain's alone, and a browser
// signing in to an org's subdomain is handed there afterwards (/go/<slug>).
//
// Identities are matched on Google's subject (sub), never the address.
// Google's first sign-in with an address that already has a Dash account
// links to it only when the hosted domain (hd) is one an org of that
// account verified; otherwise its password confirms the link once. A new
// account joins an org by an invitation or its verified domain with
// auto-join on, or gets a personal workspace when signup is open, as signup
// would; signing in never makes anyone an owner.

const (
	GoogleStartPath    = "/auth/google/start"
	GoogleCallbackPath = "/auth/google/callback"
	googleStatusPath   = "/api/auth/google"

	googleCookiePrefix = "dash_google_" // + the flow's id: the browser that started it
	googleCookiePath   = "/auth/google/"

	// linkCookiePrefix + the pending link's id names the cookie that binds a
	// pending link to the browser that completed the Google flow. Its path
	// covers /api/login and /api/login/totp, where the link is redeemed.
	linkCookiePrefix = "dash_glink_"
	linkCookiePath   = "/api/login"

	// reauthWindow is how recent a Google sign-in confirms what a password
	// would, for an account without one.
	reauthWindow = 10 * time.Minute
)

// GoogleConfig is the instance's Google OAuth client, from the environment
// (DASH_GOOGLE_CLIENT_ID, DASH_GOOGLE_CLIENT_SECRET).
type GoogleConfig struct {
	ClientID, ClientSecret string
}

var googleProvider = oidcProvider{
	Name:   store.ProviderGoogle,
	Issuer: "https://accounts.google.com",
	Scopes: []string{oidc.ScopeOpenID, "email", "profile"},
}

// google is Google as this Auth talks to it (tests point it elsewhere).
func (a *Auth) google() oidcProvider {
	p := googleProvider
	if a.googleIssuer != "" {
		p.Issuer = a.googleIssuer
	}
	return p
}

func orgSecretContext(org int64) string {
	return "org_sso:" + strconv.FormatInt(org, 10) + ":google_client_secret"
}

const instanceSecretContext = "instance_sso:google_client_secret"

// instanceGoogle is the instance's client: the environment's, else the one
// a platform admin set.
func (a *Auth) instanceGoogle() (oidcClient, bool) {
	if a.Google.ClientID != "" && a.Google.ClientSecret != "" {
		return oidcClient{ID: a.Google.ClientID, Secret: a.Google.ClientSecret}, true
	}
	id, sealed, err := a.acc.InstanceGoogle()
	if err != nil || id == "" || sealed == "" {
		if err != nil {
			slog.Error("google sign-in: instance client", "err", err)
		}
		return oidcClient{}, false
	}
	secret, err := a.sealer.Open(sealed, instanceSecretContext)
	if err != nil {
		slog.Error("google sign-in: instance client secret", "err", err)
		return oidcClient{}, false
	}
	return oidcClient{ID: id, Secret: secret}, true
}

// orgGoogle is org's own client, if its owner set one.
func (a *Auth) orgGoogle(org int64) (oidcClient, bool) {
	if org == 0 {
		return oidcClient{}, false
	}
	s, err := a.acc.OrgSSO(org)
	if err != nil || s.GoogleClient == "" || s.GoogleSecret == "" {
		if err != nil {
			slog.Error("google sign-in: org client", "org", org, "err", err)
		}
		return oidcClient{}, false
	}
	secret, err := a.sealer.Open(s.GoogleSecret, orgSecretContext(org))
	if err != nil {
		slog.Error("google sign-in: org client secret", "org", org, "err", err)
		return oidcClient{}, false
	}
	return oidcClient{ID: s.GoogleClient, Secret: secret, Org: org}, true
}

// googleClient is the client signing in to org uses (0: none in
// particular): its own, else the instance's.
func (a *Auth) googleClient(org int64) (oidcClient, bool) {
	if c, ok := a.orgGoogle(org); ok {
		return c, true
	}
	return a.instanceGoogle()
}

// googleRedirectURI is the one redirect URI registered with Google: on the
// public URL, else the base domain, else the host r came to.
func (a *Auth) googleRedirectURI(r *http.Request) string {
	if a.PublicURL != "" {
		return strings.TrimRight(a.PublicURL, "/") + GoogleCallbackPath
	}
	if a.Domains != nil {
		return a.origin(r, "") + GoogleCallbackPath
	}
	scheme := "http"
	if a.Secure(r) {
		scheme = "https"
	}
	return scheme + "://" + a.reqHost(r) + GoogleCallbackPath
}

// GoogleRedirectURI is the redirect URI to register with Google.
func (a *Auth) GoogleRedirectURI(r *http.Request) string { return a.googleRedirectURI(r) }

func (a *Auth) googleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+GoogleStartPath, a.googleStart)
	mux.HandleFunc("POST "+GoogleStartPath, a.googleStart)
	mux.HandleFunc("GET "+GoogleCallbackPath, a.googleCallback)
	mux.HandleFunc("GET "+googleStatusPath, a.googleStatus)
}

// googleStatus tells the sign-in page whether to show the button, for an
// org (by slug) or none.
func (a *Auth) googleStatus(w http.ResponseWriter, r *http.Request) {
	var org int64
	if slug := r.URL.Query().Get("org"); slug != "" {
		if o, _, err := a.acc.OrgBySlug(slug, a.now()); err == nil {
			org = o.ID
		}
	}
	_, ok := a.googleClient(org)
	writeJSON(w, http.StatusOK, map[string]any{"enabled": ok, "start": GoogleStartPath})
}

// ---- start ----

func (a *Auth) googleStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		a.flowPage(w, r, nil, failFlow("failed", "parse form: %v", err))
		return
	}
	form := r.Form
	if r.Method == http.MethodGet {
		form = r.URL.Query() // an invitation's token comes in a POST's body only
		form.Del("invite")
	}
	purpose := form.Get("purpose")
	if purpose == "" {
		purpose = flowLogin
	}
	f := &oidcFlow{Purpose: purpose, Next: safeNext(form.Get("next")), Invite: strings.TrimSpace(form.Get("invite"))}
	if purpose != flowLogin && purpose != flowLink && purpose != flowTest {
		writeError(w, http.StatusBadRequest, "unknown purpose")
		return
	}
	if slug := form.Get("org"); slug != "" {
		o, _, err := a.acc.OrgBySlug(slug, a.now())
		if err != nil {
			a.googleNotFound(w, r)
			return
		}
		f.Org = o.ID
	}
	if f.Invite != "" {
		inv, err := a.acc.InvitationByToken(f.Invite)
		if err != nil {
			a.flowPage(w, r, f, &flowError{Code: "invite_invalid", Err: err})
			return
		}
		f.Org = inv.Org.ID
	}
	c, ok := a.googleClient(f.Org)
	if !ok {
		a.googleNotFound(w, r)
		return
	}
	f.Client = c.Org
	if a.lockedOut(a.clientIP(r)) {
		a.flowPage(w, r, f, &flowError{Code: "locked_out"})
		return
	}
	// The flow's cookie is for the redirect URI's host: start there.
	if cb, err := url.Parse(a.googleRedirectURI(r)); err == nil && cb.Host != "" && !strings.EqualFold(cb.Host, a.reqHost(r)) {
		if r.Method != http.MethodGet {
			a.flowPage(w, r, f, failFlow("failed", "start on %s, not %s", cb.Host, a.reqHost(r)))
			return
		}
		http.Redirect(w, r, cb.Scheme+"://"+cb.Host+r.URL.RequestURI(), http.StatusSeeOther)
		return
	}
	if purpose != flowLogin {
		// Linking and testing are for someone signed in on this browser.
		id, ok := a.session(r)
		if !ok {
			http.Redirect(w, r, "/login.html", http.StatusSeeOther)
			return
		}
		f.User = id.User.ID
		if purpose == flowTest {
			m, err := a.acc.Membership(id.User.ID, f.Org)
			if f.Org == 0 || err != nil || !m.Role.AtLeast(store.RoleAdmin) {
				a.googleNotFound(w, r)
				return
			}
		}
	}
	extra := map[string]string{"prompt": "select_account"}
	if hd := a.hdHint(f.Org); hd != "" {
		extra["hd"] = hd // only a hint: the callback checks the hd claim
	}
	f = a.newFlow(f)
	to, err := a.rp.authURL(r.Context(), a.google(), c, a.googleRedirectURI(r), f, extra)
	if err != nil {
		a.flowPage(w, r, f, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: flowCookie(f), Value: f.Binding, Path: googleCookiePath, MaxAge: int(flowTTL.Seconds()),
		// Lax: Google sends the browser back with a cross-site GET.
		HttpOnly: true, Secure: a.secureCookie(r), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// flowCookie is the name of f's browser-binding cookie: one per flow, so
// two tabs signing in at once don't undo each other.
func flowCookie(f *oidcFlow) string { return googleCookiePrefix + sessionKey(f.State)[:16] }

// hdHint is the hosted domain Google's account chooser is limited to: the
// org's, when it verified exactly one.
func (a *Auth) hdHint(org int64) string {
	if org == 0 {
		return ""
	}
	s, err := a.acc.OrgSSO(org)
	if err != nil {
		return ""
	}
	hd := ""
	for _, d := range s.Domains {
		if d.Verified {
			if hd != "" {
				return ""
			}
			hd = d.Domain
		}
	}
	return hd
}

func (a *Auth) googleNotFound(w http.ResponseWriter, r *http.Request) {
	a.page(w, http.StatusNotFound, hostPageData{Title: "Not found", Text: "Google sign-in isn't set up here.", Link: "/login.html", LinkText: "Sign in"})
}

// ---- callback ----

func (a *Auth) googleCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	q := r.URL.Query()
	f, ok := a.takeFlow(q.Get("state"))
	if !ok && !a.googleAnywhere() {
		a.googleNotFound(w, r)
		return
	}
	ip := a.clientIP(r)
	if a.lockedOut(ip) {
		a.flowPage(w, r, f, &flowError{Code: "locked_out"})
		return
	}
	if !ok {
		a.countFailure(ip)
		a.flowPage(w, r, nil, &flowError{Code: "expired"})
		return
	}
	c, err := r.Cookie(flowCookie(f))
	http.SetCookie(w, &http.Cookie{Name: flowCookie(f), Value: "", Path: googleCookiePath, MaxAge: -1, HttpOnly: true,
		Secure: a.secureCookie(r), SameSite: http.SameSiteLaxMode})
	if err != nil || !constantEqual(c.Value, f.Binding) {
		// The state proves some browser started a sign-in, not this one.
		a.countFailure(ip)
		a.flowFailed(w, r, f, nil, &flowError{Code: "binding"})
		return
	}
	if e := q.Get("error"); e != "" {
		a.flowFailed(w, r, f, nil, failFlow("denied", "google: %.100s", e))
		return
	}
	hold, ok := a.reserve(ip)
	if !ok {
		a.flowPage(w, r, f, &flowError{Code: "locked_out"})
		return
	}
	client, ok := a.flowClient(f)
	if !ok {
		a.release(hold)
		a.flowFailed(w, r, f, nil, &flowError{Code: "not_configured"})
		return
	}
	cl, err := a.rp.finish(r.Context(), a.google(), client, a.googleRedirectURI(r), q.Get("code"), f)
	if err != nil {
		a.flowFailed(w, r, f, nil, err) // counts: forged or replayed answers are guessing
		return
	}
	a.release(hold)
	if !cl.EmailVerified || cl.Email == "" {
		a.flowFailed(w, r, f, &cl, &flowError{Code: "email_unverified"})
		return
	}
	// The org's own client signs in to that org's domains only.
	if f.Client != 0 {
		s, err := a.acc.OrgSSO(f.Client)
		if err != nil {
			a.flowFailed(w, r, f, &cl, &flowError{Code: "failed", Err: err})
			return
		}
		if hasVerified(s) && !s.VerifiedDomain(cl.HD) {
			a.flowFailed(w, r, f, &cl, failFlow("domain_mismatch", "hd %q", cl.HD))
			return
		}
	}
	switch f.Purpose {
	case flowTest:
		a.googleTested(w, r, f, cl)
	case flowLink:
		a.googleLinked(w, r, f, cl)
	default:
		a.googleSignIn(w, r, f, cl)
	}
}

// googleAnywhere reports whether Google sign-in is set up at all: the
// instance's client, or any org's own.
func (a *Auth) googleAnywhere() bool {
	if _, ok := a.instanceGoogle(); ok {
		return true
	}
	ok, err := a.acc.AnyOrgGoogleClient()
	return err == nil && ok
}

// flowClient is the client f started with, if it is still set up.
func (a *Auth) flowClient(f *oidcFlow) (oidcClient, bool) {
	if f.Client != 0 {
		return a.orgGoogle(f.Client)
	}
	return a.instanceGoogle()
}

func hasVerified(s store.OrgSSO) bool {
	for _, d := range s.Domains {
		if d.Verified {
			return true
		}
	}
	return false
}

func constantEqual(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// countFailure counts a failed sign-in from ip.
func (a *Auth) countFailure(ip string) { a.reserve(ip) }

func emailDomain(email string) string {
	_, d, ok := strings.Cut(strings.ToLower(strings.TrimSpace(email)), "@")
	if !ok {
		return ""
	}
	return d
}

// ---- signing in ----

func (a *Auth) googleSignIn(w http.ResponseWriter, r *http.Request, f *oidcFlow, cl oidcClaims) {
	now := a.now()
	u, joined, isNew, ferr := a.googleUser(w, r, f, cl, now)
	if ferr != nil {
		a.flowFailed(w, r, f, &cl, ferr)
		return
	}
	if u.ID == 0 {
		return // answered: the link needs the password
	}
	// The org it signs in to: the one asked for, else the one just joined.
	org := f.Org
	if org == 0 && joined != nil {
		org = joined.ID
	}
	var target *store.Membership
	if org != 0 {
		if m, err := a.acc.Membership(u.ID, org); err == nil {
			target = &m
		}
	}
	if f.Client != 0 && (target == nil || target.ID != f.Client) {
		// A Google account isn't enough: the account must be a member of the
		// org whose client signed it in.
		a.flowFailed(w, r, f, &cl, failFlow("not_member", "user %d, org %d", u.ID, f.Client))
		return
	}
	if target == nil && isNew && !a.signupOpen {
		a.flowFailed(w, r, f, &cl, &flowError{Code: "org_not_enabled"})
		return
	}
	if target != nil {
		// Deprovisioning: an org that requires Google takes its domains'
		// people only from those domains, checked again each time.
		s, err := a.acc.OrgSSO(target.ID)
		if err == nil && s.RequireGoogle && s.VerifiedDomain(emailDomain(u.Email)) && !s.VerifiedDomain(cl.HD) {
			a.flowFailed(w, r, f, &cl, failFlow("domain_mismatch", "hd %q for %s", cl.HD, target.Slug))
			return
		}
	}
	var orgID int64
	if target != nil {
		orgID = target.ID
	}
	if u.TOTP && a.totpAfterGoogle(u.ID) {
		tok := a.newTicketFor(&loginTicket{user: u.ID, method: store.MethodGoogle, org: orgID})
		a.audit(r, store.AuthEvent{OrgID: orgID, UserID: u.ID, Kind: "signin.google.2fa", Detail: cl.Email})
		a.refresh(w, "Two-factor sign-in", "/login.html?next="+url.QueryEscape(a.googleNext(r, target, f.Next))+"#google_totp="+tok)
		return
	}
	a.signedInFrom(a.clientIP(r))
	if err := a.startSessionVia(w, r, u.ID, orgID, newFamily(), store.MethodGoogle); err != nil {
		a.flowFailed(w, r, f, &cl, &flowError{Code: "failed", Err: err})
		return
	}
	a.audit(r, store.AuthEvent{OrgID: orgID, UserID: u.ID, Kind: "signin.google", Detail: identityDetail(cl)})
	a.refresh(w, "Signed in", a.googleNext(r, target, f.Next))
}

// googleNext is where a browser goes once signed in: next, by way of the
// org's subdomain with org subdomains.
func (a *Auth) googleNext(r *http.Request, target *store.Membership, next string) string {
	next = safeNext(next)
	if a.Domains == nil || target == nil || strings.HasPrefix(next, "/go/") || !a.Domains.Usable(target.Slug) {
		return next
	}
	u := "/go/" + target.Slug
	if next != "/" {
		u += "?next=" + url.QueryEscape(next)
	}
	return u
}

// totpAfterGoogle reports whether uid's Google sign-in asks for their
// authenticator code too: an org of theirs wants Dash's second factor after
// Google, or requires two-factor sign-in.
func (a *Auth) totpAfterGoogle(uid int64) bool {
	ms, err := a.acc.Memberships(uid)
	if err != nil {
		return true // fail closed
	}
	for _, m := range ms {
		if m.Require2FA {
			return true
		}
		if s, err := a.acc.OrgSSO(m.ID); err != nil || s.Google2FA {
			return true
		}
	}
	return false
}

// googleUser finds or makes the account cl signs in, and joins what the
// flow and cl's domain allow. A zero user with no error means it answered
// (a link to confirm with the password).
func (a *Auth) googleUser(w http.ResponseWriter, r *http.Request, f *oidcFlow, cl oidcClaims, now time.Time) (store.User, *store.Membership, bool, *flowError) {
	ident := store.Identity{Provider: store.ProviderGoogle, Subject: cl.Subject, Email: cl.Email, HD: cl.HD}
	known, err := a.acc.IdentityBySubject(store.ProviderGoogle, cl.Subject)
	if err == nil {
		u, err := a.acc.User(known.UserID)
		if err != nil {
			return store.User{}, nil, false, &flowError{Code: "failed", Err: err}
		}
		if err := a.acc.TouchIdentity(store.ProviderGoogle, cl.Subject, cl.Email, cl.HD, now); err != nil {
			slog.Error("google sign-in: touch identity", "err", err)
		}
		// Only an invitation: joining by domain is for the first sign-in, so
		// a member an admin removed doesn't come back by signing in.
		j, ferr := a.googleJoin(f, cl, u.ID, false)
		if ferr != nil {
			return store.User{}, nil, false, ferr
		}
		m, ferr := a.join(r, u.ID, j, cl, now)
		return u, m, false, ferr
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.User{}, nil, false, &flowError{Code: "failed", Err: err}
	}
	existing, unverified, err := a.acc.UserByEmail(cl.Email)
	switch {
	case err == nil && !unverified:
		// An account has the address. Its owner may not be whoever holds the
		// Google account now: whoever set its password may never have proven
		// the address (an inviter can register an invitation to a colleague's
		// address with a password of their own), so an account with a
		// password links only after that password, whatever org vouches for
		// the domain. Without one (a provider made it, from an address the
		// provider proved), it links at once where an org of theirs vouches
		// for the domain.
		has, err := a.acc.HasPassword(existing.ID)
		if err != nil {
			return store.User{}, nil, false, &flowError{Code: "failed", Err: err}
		}
		if has || !a.vouches(existing.ID, cl.HD) {
			tok, nonce := a.newPendingLink(existing.ID, ident, f)
			// Only this browser may redeem it: the token alone, handed to
			// the account's owner, mustn't link someone else's Google.
			http.SetCookie(w, &http.Cookie{
				Name: linkCookie(tok), Value: nonce, Path: linkCookiePath, MaxAge: int(flowTTL.Seconds()),
				HttpOnly: true, Secure: a.secureCookie(r), SameSite: http.SameSiteLaxMode,
			})
			a.audit(r, store.AuthEvent{OrgID: f.Org, UserID: existing.ID, Kind: "google.link_pending", Detail: identityDetail(cl)})
			a.page(w, http.StatusOK, hostPageData{
				Title: "Link your Google account",
				Text: "There's a " + a.brand().Name + " account with " + cl.Email + " already. Sign in to it with its password once " +
					"to link it to this Google account; after that, Google signs you in.",
				Link: "/login.html?next=" + url.QueryEscape(safeNext(f.Next)) + "#google_link=" + tok, LinkText: "Sign in with password",
			})
			return store.User{}, nil, false, nil
		}
		ident.UserID = existing.ID
		if err := a.acc.LinkIdentity(ident, now); err != nil {
			return store.User{}, nil, false, linkError(err)
		}
		a.audit(r, store.AuthEvent{OrgID: f.Org, UserID: existing.ID, Kind: "google.linked", Detail: identityDetail(cl) + " (verified domain)"})
		j, ferr := a.googleJoin(f, cl, existing.ID, true)
		if ferr != nil {
			return store.User{}, nil, false, ferr
		}
		m, ferr := a.join(r, existing.ID, j, cl, now)
		return existing, m, false, ferr
	case err == nil, errors.Is(err, store.ErrNotFound):
		// No account (or one that never confirmed its address): a new one, by
		// the same rules as signing up.
		j, ferr := a.googleJoin(f, cl, 0, true)
		if ferr != nil {
			return store.User{}, nil, false, ferr
		}
		if f.Client != 0 && (j == (store.Join{}) || j.Org != 0 && j.Org != f.Client) {
			// The org's own client makes accounts for that org only.
			return store.User{}, nil, false, failFlow("not_member", "new account, org %d", f.Client)
		}
		if j == (store.Join{}) && !a.signupOpen {
			return store.User{}, nil, false, &flowError{Code: "org_not_enabled"}
		}
		u, m, err := a.acc.CreateIdentityUser(cl.Email, ident, j, now)
		if err != nil {
			if errors.Is(err, store.ErrInvitation) {
				return store.User{}, nil, false, &flowError{Code: "invite_invalid", Err: err}
			}
			return store.User{}, nil, false, &flowError{Code: "failed", Err: err}
		}
		a.audit(r, store.AuthEvent{UserID: u.ID, Kind: "account.created.google", Detail: identityDetail(cl)})
		if m != nil {
			a.membersChanged(m.ID)
			a.audit(r, store.AuthEvent{OrgID: m.ID, UserID: u.ID, Kind: joinKind(j), Detail: string(m.Role)})
		}
		return u, m, true, nil
	default:
		return store.User{}, nil, false, &flowError{Code: "failed", Err: err}
	}
}

func joinKind(j store.Join) string {
	if j.InviteToken != "" {
		return "member.joined.invitation"
	}
	return "member.joined.domain"
}

// googleJoin is what signing in as cl joins: the flow's invitation (for
// cl's address, unless it allows any), else, on an identity's first sign-in
// (first), the org that verified cl's hosted domain if it lets people join
// by it. Only the hd claim counts, never the address's domain: Google
// vouches for hd, anyone can have an address that looks like it.
func (a *Auth) googleJoin(f *oidcFlow, cl oidcClaims, uid int64, first bool) (store.Join, *flowError) {
	if f.Invite != "" {
		inv, err := a.acc.InvitationByToken(f.Invite)
		if err != nil {
			return store.Join{}, &flowError{Code: "invite_invalid", Err: err}
		}
		if !inv.AnyEmail && !strings.EqualFold(strings.TrimSpace(inv.Email), cl.Email) {
			return store.Join{}, failFlow("invite_email_mismatch", "invited %s, signed in as %s", inv.Email, cl.Email)
		}
		return store.Join{InviteToken: f.Invite}, nil
	}
	if !first || cl.HD == "" {
		return store.Join{}, nil
	}
	s, err := a.acc.DomainOrg(cl.HD)
	if err != nil || !s.AutoJoin || (f.Org != 0 && f.Org != s.OrgID) {
		return store.Join{}, nil
	}
	if uid != 0 {
		if _, err := a.acc.Membership(uid, s.OrgID); err == nil {
			return store.Join{}, nil
		}
	}
	return store.Join{Org: s.OrgID, Role: s.JoinRole}, nil
}

// join makes uid a member as j says, for an account that exists already.
func (a *Auth) join(r *http.Request, uid int64, j store.Join, cl oidcClaims, now time.Time) (*store.Membership, *flowError) {
	if j == (store.Join{}) {
		return nil, nil
	}
	m, err := a.acc.JoinOrg(uid, j, now)
	if err != nil {
		if errors.Is(err, store.ErrInvitation) {
			return nil, &flowError{Code: "invite_invalid", Err: err}
		}
		return nil, &flowError{Code: "failed", Err: err}
	}
	a.membersChanged(m.ID)
	a.audit(r, store.AuthEvent{OrgID: m.ID, UserID: uid, Kind: joinKind(j), Detail: string(m.Role) + " · " + cl.Email})
	return m, nil
}

// vouches reports whether hd is a verified domain of an org uid belongs to.
func (a *Auth) vouches(uid int64, hd string) bool {
	if hd == "" {
		return false
	}
	ms, err := a.acc.Memberships(uid)
	if err != nil {
		return false
	}
	for _, m := range ms {
		if s, err := a.acc.OrgSSO(m.ID); err == nil && s.VerifiedDomain(hd) {
			return true
		}
	}
	return false
}

func linkError(err error) *flowError {
	switch {
	case errors.Is(err, store.ErrIdentityTaken):
		return &flowError{Code: "identity_taken", Err: err}
	case errors.Is(err, store.ErrIdentityLinked):
		return &flowError{Code: "already_linked", Err: err}
	}
	return &flowError{Code: "failed", Err: err}
}

func identityDetail(cl oidcClaims) string {
	if cl.HD != "" {
		return cl.Email + " (" + cl.HD + ")"
	}
	return cl.Email
}

// ---- linking ----

// pendingLink is a Google identity waiting for its account's password.
type pendingLink struct {
	user    int64
	binding string // sessionKey of the nonce in the originating browser's cookie
	ident   store.Identity
	org     int64
	expires time.Time
}

// linkCookie is the name of the cookie binding the pending link tok to the
// browser that completed its Google flow.
func linkCookie(tok string) string { return linkCookiePrefix + sessionKey(tok)[:16] }

// newPendingLink returns the link's token and the nonce the originating
// browser must present (in its linkCookie) to redeem it.
func (a *Auth) newPendingLink(uid int64, ident store.Identity, f *oidcFlow) (tok, nonce string) {
	tok, nonce = randomToken(), randomToken()
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, l := range a.links {
		if now.After(l.expires) {
			delete(a.links, k)
		}
	}
	a.links[sessionKey(tok)] = &pendingLink{user: uid, binding: sessionKey(nonce), ident: ident, org: f.Org, expires: now.Add(flowTTL)}
	return tok, nonce
}

// redeemLink links the identity waiting under tok to uid, who just signed
// in with their password (and second factor), in the browser that completed
// the Google flow. Another account's, another browser's or an expired link
// does nothing.
func (a *Auth) redeemLink(r *http.Request, tok string, uid int64) bool {
	if tok == "" {
		return false
	}
	c, err := r.Cookie(linkCookie(tok))
	if err != nil || c.Value == "" {
		return false
	}
	bound := sessionKey(c.Value)
	a.mu.Lock()
	l, ok := a.links[sessionKey(tok)]
	if ok && (l.user != uid || !constantEqual(l.binding, bound)) {
		ok = false
	}
	if ok {
		delete(a.links, sessionKey(tok))
	}
	a.mu.Unlock()
	if !ok || a.now().After(l.expires) {
		return false
	}
	l.ident.UserID = uid
	if err := a.acc.LinkIdentity(l.ident, a.now()); err != nil {
		slog.Warn("google sign-in: link after password", "user", uid, "err", err)
		return false
	}
	a.audit(r, store.AuthEvent{OrgID: l.org, UserID: uid, Kind: "google.linked", Detail: l.ident.Email + " (confirmed with password)"})
	return true
}

// googleLinked links cl to the signed-in user who started f.
func (a *Auth) googleLinked(w http.ResponseWriter, r *http.Request, f *oidcFlow, cl oidcClaims) {
	ident := store.Identity{UserID: f.User, Provider: store.ProviderGoogle, Subject: cl.Subject, Email: cl.Email, HD: cl.HD}
	if err := a.acc.LinkIdentity(ident, a.now()); err != nil {
		a.flowFailed(w, r, f, &cl, linkError(err))
		return
	}
	a.audit(r, store.AuthEvent{OrgID: f.Org, UserID: f.User, Actor: f.User, Kind: "google.linked", Detail: identityDetail(cl)})
	a.page(w, http.StatusOK, hostPageData{Title: "Google account linked",
		Text: "You can sign in with Google as " + cl.Email + " from now on.", Link: safeNext(f.Next), LinkText: "Continue"})
}

// ---- testing ----

// googleTested shows an org admin what Google said: it never signs in.
func (a *Auth) googleTested(w http.ResponseWriter, r *http.Request, f *oidcFlow, cl oidcClaims) {
	o, err := a.acc.Org(f.Org)
	if err != nil {
		a.flowFailed(w, r, f, &cl, &flowError{Code: "not_configured", Err: err})
		return
	}
	s, _ := a.acc.OrgSSO(f.Org)
	client := "this Dash's Google client"
	if f.Client != 0 {
		client = o.Name + "'s own Google client"
	}
	text := "Google signed in " + cl.Email + " with " + client + "."
	switch {
	case cl.HD == "":
		text += " It's a personal Google account (no Workspace domain), so it can't join by domain."
	case s.VerifiedDomain(cl.HD):
		text += " Its Workspace domain " + cl.HD + " is verified for " + o.Name + "."
	default:
		text += " Its Workspace domain " + cl.HD + " isn't one of " + o.Name + "'s verified domains."
	}
	a.audit(r, store.AuthEvent{OrgID: f.Org, UserID: f.User, Actor: f.User, Kind: "sso.test.ok", Detail: identityDetail(cl)})
	a.page(w, http.StatusOK, hostPageData{Title: "Google sign-in works", Text: text + " Nobody was signed in by this test.",
		Link: a.orgHome(r, o), LinkText: "Back to " + o.Name})
}

// orgHome is where o's board is.
func (a *Auth) orgHome(r *http.Request, o store.Org) string {
	if a.Domains != nil && a.Domains.Usable(o.Slug) {
		return a.origin(r, o.Slug) + "/"
	}
	return "/"
}

// ---- answers ----

var flowMessages = map[string]string{
	"expired":               "This sign-in has expired or was used already. Start again.",
	"binding":               "This sign-in was started in another browser, or its cookie is gone. Start again in this browser.",
	"denied":                "Signing in with Google was cancelled.",
	"locked_out":            "Too many failed sign-ins. Try again later.",
	"not_configured":        "Google sign-in isn't set up for this organization any more.",
	"provider_unavailable":  "Google can't be reached right now. Try again in a minute.",
	"invalid_client":        "Google didn't accept this Dash's client ID or secret. Ask the organization's owner to check its Google sign-in settings.",
	"redirect_uri_mismatch": "Google doesn't know this Dash's redirect URI. Ask the owner to add it to the OAuth client in the Google Cloud console.",
	"invalid_grant":         "Google's sign-in code had expired or was used already. Start again.",
	"exchange_failed":       "Signing in with Google didn't complete. Try again.",
	"no_id_token":           "Google's answer couldn't be verified. Try again.",
	"token_invalid":         "Google's answer couldn't be verified. Try again.",
	"token_expired":         "Google's answer had expired. Start again.",
	"nonce_mismatch":        "Google's answer wasn't for this sign-in. Start again.",
	"email_unverified":      "Google hasn't verified this account's email address.",
	"domain_mismatch":       "This Google account isn't in the organization's Google Workspace domain. Choose your work account.",
	"not_member":            "Your account isn't a member of this organization. Ask an owner or admin for an invitation.",
	"org_not_enabled":       "Your organization hasn't enabled this Dash yet. Ask its owner for an invitation, or to let your Google Workspace domain join.",
	"invite_invalid":        "This invitation link is not valid any more; ask for a new one.",
	"invite_email_mismatch": "This invitation is for another email address. Sign in with the Google account of the address it was sent to, or ask for a new one.",
	"identity_taken":        "That Google account is linked to another account here.",
	"already_linked":        "Your account is linked to a Google account already. Unlink it first.",
	"failed":                "Signing in with Google failed. Try again.",
}

// flowFailed logs and audits why f failed, and answers.
func (a *Auth) flowFailed(w http.ResponseWriter, r *http.Request, f *oidcFlow, cl *oidcClaims, err error) {
	var fe *flowError
	if !errors.As(err, &fe) {
		fe = &flowError{Code: "failed", Err: err}
	}
	detail := fe.Code
	if cl != nil {
		detail += " · " + identityDetail(*cl)
	}
	slog.Warn("google sign-in failed", "reason", fe.Code, "err", fe.Err, "ip", a.clientIP(r))
	if f != nil {
		kind := "signin.google.failed"
		if f.Purpose == flowTest {
			kind = "sso.test.failed"
		}
		a.audit(r, store.AuthEvent{OrgID: f.Org, UserID: f.User, Kind: kind, Detail: detail})
	}
	a.flowPage(w, r, f, fe)
}

// flowPage answers a failed sign-in with a friendly page and its reason.
func (a *Auth) flowPage(w http.ResponseWriter, r *http.Request, f *oidcFlow, err error) {
	var fe *flowError
	if !errors.As(err, &fe) {
		fe = &flowError{Code: "failed", Err: err}
	}
	msg, ok := flowMessages[fe.Code]
	if !ok {
		msg = flowMessages["failed"]
	}
	code := http.StatusBadRequest
	switch fe.Code {
	case "locked_out":
		code = http.StatusTooManyRequests
	case "provider_unavailable", "exchange_failed", "invalid_client", "redirect_uri_mismatch":
		code = http.StatusBadGateway
	case "domain_mismatch", "not_member", "org_not_enabled", "invite_email_mismatch", "identity_taken", "email_unverified":
		code = http.StatusForbidden
	}
	d := hostPageData{Title: "Couldn't sign in with Google", Text: msg, Reason: fe.Code, Link: "/login.html", LinkText: "Back to sign in"}
	if f != nil && f.Purpose == flowTest {
		d.Title = "Google sign-in test failed"
		if o, err := a.acc.Org(f.Org); err == nil {
			d.Link, d.LinkText = a.orgHome(r, o), "Back to "+o.Name
		}
	}
	if f != nil && f.Purpose == flowLink {
		d.Title, d.Link, d.LinkText = "Couldn't link Google", safeNext(f.Next), "Back"
	}
	a.page(w, code, d)
}

// refresh answers with a page that goes on to to. Not a redirect: the
// browser came from Google, cross-site, and a SameSite=Strict session
// cookie isn't sent along a redirect chain that started there. A page of
// this host's that navigates on is same-site.
func (a *Auth) refresh(w http.ResponseWriter, title, to string) {
	a.page(w, http.StatusOK, hostPageData{Title: title, Text: "One moment…", Refresh: to, Link: to, LinkText: "Continue"})
}

// audit adds e to the audit log, with r's address, and logs it.
func (a *Auth) audit(r *http.Request, e store.AuthEvent) {
	if r != nil {
		e.IP = a.clientIP(r)
	}
	e.At = a.now()
	a.pruneAudit(e.At)
	slog.Info("auth event", "kind", e.Kind, "org", e.OrgID, "user", e.UserID, "detail", e.Detail)
	if err := a.acc.Audit(e); err != nil {
		slog.Error("audit log", "err", err)
	}
}

func (f *oidcFlow) String() string { return fmt.Sprintf("%s flow for org %d", f.Purpose, f.Org) }
