package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/klahr/dash/internal/store"
)

// Single sign-on settings (see google.go for the sign-in itself):
//
//	GET    /api/orgs/{oid}/sso                     admins: the org's Google sign-in settings
//	PATCH  /api/orgs/{oid}/sso {…, password}       client (owners), auto-join, role, require Google, 2FA after Google
//	POST   /api/orgs/{oid}/sso/domains {domain, password}
//	POST   /api/orgs/{oid}/sso/domains/{domain}/check  admins: verify a claim by its DNS TXT record
//	DELETE /api/orgs/{oid}/sso/domains/{domain} {password} (verified: owners)
//	POST   /api/orgs/{oid}/members/{uid}/sign-out {password}  admins: end a member's sessions in the org
//	GET    /api/account/google                     the user's linked Google account
//	DELETE /api/account/google {password}          unlink it (not if it's their only way in)
//	GET    /api/admin/sso                          platform admins: the instance's client, domains waiting
//	PUT    /api/admin/sso/google {client_id, client_secret, password}
//	POST   /api/admin/sso/domains/verify {org_id, domain, password}
//
// Client secrets are sealed before they reach the database and are never
// sent back: answers say whether one is set.

func (a *Auth) ssoRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/orgs/{oid}/sso", a.sessionOnly(a.orgSSO))
	mux.HandleFunc("PATCH /api/orgs/{oid}/sso", a.sessionOnly(a.updateOrgSSO))
	mux.HandleFunc("POST /api/orgs/{oid}/sso/domains", a.sessionOnly(a.addDomain))
	mux.HandleFunc("POST /api/orgs/{oid}/sso/domains/{domain}/check", a.sessionOnly(a.checkDomain))
	mux.HandleFunc("DELETE /api/orgs/{oid}/sso/domains/{domain}", a.sessionOnly(a.removeDomain))
	mux.HandleFunc("POST /api/orgs/{oid}/members/{uid}/sign-out", a.sessionOnly(a.signOutMember))
	mux.HandleFunc("GET /api/account/google", a.sessionOnly(a.accountGoogle))
	mux.HandleFunc("DELETE /api/account/google", a.sessionOnly(a.unlinkGoogle))
	mux.HandleFunc("GET /api/admin/sso", a.sessionOnly(a.adminSSO))
	mux.HandleFunc("PUT /api/admin/sso/google", a.sessionOnly(a.putInstanceGoogle))
	mux.HandleFunc("POST /api/admin/sso/domains/verify", a.sessionOnly(a.adminVerifyDomain))
}

// googleRequiredIn reports whether the org of m, the membership of the user
// with email, takes only Google sign-ins from them: it requires Google,
// their address is in one of its verified domains, and they aren't an owner
// (owners keep their password, so a broken Google setup can't lock everyone
// out). The address's domain is enough here: it only ever takes a way in
// away.
func (a *Auth) googleRequiredIn(email string, m store.Membership) bool {
	_, ok := a.requiringGoogle(email, m)
	return ok
}

func (a *Auth) requiringGoogle(email string, m store.Membership) (store.OrgSSO, bool) {
	if m.Role == store.RoleOwner {
		return store.OrgSSO{}, false
	}
	s, err := a.acc.OrgSSO(m.ID)
	if err != nil {
		return store.OrgSSO{}, false
	}
	return s, s.RequireGoogle && s.VerifiedDomain(emailDomain(email))
}

// googleRefuses reports whether the org of m, user's membership, refuses a
// session signed in by method: it requires Google of them (googleRequiredIn)
// and the session wasn't signed in with a Google account of one of the
// org's verified domains. Any Google account isn't enough: a member
// suspended in the org's Workspace could sign in with a personal one. The
// account checked is the one linked to theirs, which Google sign-in signs
// them in with; it changes only by linking another, which Google proves,
// and unlinking it leaves a Google session nothing to pass with.
func (a *Auth) googleRefuses(user store.User, method string, m store.Membership) bool {
	s, required := a.requiringGoogle(user.Email, m)
	if !required {
		return false
	}
	if method != store.MethodGoogle {
		return true
	}
	ident, err := a.acc.UserIdentity(user.ID, store.ProviderGoogle)
	return err != nil || !s.VerifiedDomain(ident.HD)
}

// googleRefusesIdentity is googleRefuses for id's session in its org.
func (a *Auth) googleRefusesIdentity(id *Identity) bool {
	return id.Org != nil && a.googleRefuses(id.User, id.Method, *id.Org)
}

// googleLinkRefused is the org that requires Google of uid and doesn't take
// hd, a Google account's hosted domain, as one of its verified domains, if
// any: linking that account would give uid Google sessions that org refuses.
func (a *Auth) googleLinkRefused(uid int64, hd string) (store.Membership, bool) {
	u, err := a.acc.User(uid)
	if err != nil {
		return store.Membership{}, true // fail closed
	}
	ms, err := a.acc.Memberships(uid)
	if err != nil {
		return store.Membership{}, true
	}
	for _, m := range ms {
		if s, required := a.requiringGoogle(u.Email, m); required && !s.VerifiedDomain(hd) {
			return m, true
		}
	}
	return store.Membership{}, false
}

func (a *Auth) answerGoogleRequired(w http.ResponseWriter, r *http.Request, id *Identity, api bool) {
	const msg = "this organization requires signing in with Google"
	if api {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": msg, "code": "google_required"})
		return
	}
	start := GoogleStartPath + "?org=" + url.QueryEscape(id.Org.Slug)
	if a.Domains != nil {
		start = a.origin(r, "") + start
	}
	a.page(w, http.StatusForbidden, hostPageData{
		Title: "Sign in with Google", Text: id.Org.Name + " requires signing in with your Google Workspace account.",
		Link: start, LinkText: "Sign in with Google", SignOut: true,
	})
}

// adminOf is the user's membership of the path's org, if at least role.
func (a *Auth) adminOf(w http.ResponseWriter, r *http.Request, id *Identity, role store.Role) (store.Membership, bool) {
	oid, err := pathID(r, "oid")
	if err != nil {
		accountError(w, err)
		return store.Membership{}, false
	}
	m, err := a.acc.Membership(id.User.ID, oid)
	if err != nil {
		accountError(w, err)
		return m, false
	}
	if !m.Role.AtLeast(role) {
		accountError(w, store.ErrForbidden)
		return m, false
	}
	return m, true
}

// startURL is where a browser starts Google sign-in for org with purpose.
func (a *Auth) startURL(r *http.Request, org store.Org, purpose string) string {
	q := url.Values{"org": {org.Slug}}
	if purpose != flowLogin {
		q.Set("purpose", purpose)
	}
	u := GoogleStartPath + "?" + q.Encode()
	if a.Domains != nil {
		u = a.origin(r, "") + u
	}
	return u
}

func (a *Auth) orgSSO(w http.ResponseWriter, r *http.Request, id *Identity) {
	m, ok := a.adminOf(w, r, id, store.RoleAdmin)
	if !ok {
		return
	}
	s, err := a.acc.OrgSSO(m.ID)
	if err != nil {
		accountError(w, err)
		return
	}
	_, instance := a.instanceGoogle()
	_, own := a.orgGoogle(m.ID)
	resp := map[string]any{
		"role":              m.Role,
		"available":         instance || own,
		"instance_client":   instance,
		"own_client":        own,
		"client_id":         s.GoogleClient,
		"client_secret_set": s.GoogleSecret != "",
		"can_store_secret":  a.sealer != nil,
		"auto_join":         s.AutoJoin,
		"join_role":         s.JoinRole,
		"require_google":    s.RequireGoogle,
		"google_2fa":        s.Google2FA,
		"domains":           s.Domains,
		"redirect_uri":      a.googleRedirectURI(r),
		"test_url":          a.startURL(r, m.Org, flowTest),
	}
	if ev, err := a.acc.AuthEvents(m.ID, 50); err == nil {
		resp["events"] = ev
	}
	writeJSON(w, http.StatusOK, resp)
}

var (
	errNeedSecret   = errors.New("enter the client secret with the client ID")
	errNeedDomain   = errors.New("verify a Google Workspace domain first: requiring Google applies to the people of the organization's domains")
	errNeedGoogle   = errors.New("set up Google sign-in first (this Dash's client or your own)")
	errOwnersOnly   = errors.New("only owners change the Google client, require Google or a second factor after it")
	errOwnersDomain = errors.New("only owners remove a verified domain")
	errBadClientID  = errors.New("that doesn't look like a Google OAuth client ID")
	errAdminsOnly   = errors.New("only platform admins do that")
	errEnvClient    = errors.New("this Dash's Google client is set by DASH_GOOGLE_CLIENT_ID and DASH_GOOGLE_CLIENT_SECRET: change it there")
	errNotPersonal  = errors.New("a personal workspace has no single sign-on: create an organization")
	errNeedRoleHigh = errors.New("you can't sign out someone with a higher role than yours")
)

func (a *Auth) updateOrgSSO(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req struct {
		ClientID      *string     `json:"client_id"`
		ClientSecret  *string     `json:"client_secret"`
		AutoJoin      *bool       `json:"auto_join"`
		JoinRole      *store.Role `json:"join_role"`
		RequireGoogle *bool       `json:"require_google"`
		Google2FA     *bool       `json:"google_2fa"`
		Password      string      `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	m, ok := a.adminOf(w, r, id, store.RoleAdmin)
	if !ok {
		return
	}
	if m.Personal {
		writeError(w, http.StatusBadRequest, errNotPersonal.Error())
		return
	}
	if (req.ClientID != nil || req.ClientSecret != nil || req.RequireGoogle != nil || req.Google2FA != nil) && m.Role != store.RoleOwner {
		writeError(w, http.StatusForbidden, errOwnersOnly.Error())
		return
	}
	s, err := a.acc.OrgSSO(m.ID)
	if err != nil {
		accountError(w, err)
		return
	}
	var changed []string
	if req.ClientID != nil {
		cid := strings.TrimSpace(*req.ClientID)
		if cid != "" && (len(cid) > 200 || strings.ContainsAny(cid, " \t\r\n/")) {
			writeError(w, http.StatusBadRequest, errBadClientID.Error())
			return
		}
		if cid != s.GoogleClient {
			changed = append(changed, "client id")
		}
		s.GoogleClient = cid
		if cid == "" {
			s.GoogleSecret = "" // no client, no secret
		}
	}
	if req.ClientSecret != nil && strings.TrimSpace(*req.ClientSecret) != "" {
		if s.GoogleClient == "" {
			writeError(w, http.StatusBadRequest, "enter the client ID too")
			return
		}
		sealed, err := a.sealer.Seal(strings.TrimSpace(*req.ClientSecret), orgSecretContext(m.ID))
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		s.GoogleSecret = sealed
		changed = append(changed, "client secret")
	}
	if s.GoogleClient != "" && s.GoogleSecret == "" {
		writeError(w, http.StatusBadRequest, errNeedSecret.Error())
		return
	}
	if req.AutoJoin != nil && *req.AutoJoin != s.AutoJoin {
		s.AutoJoin = *req.AutoJoin
		changed = append(changed, "auto-join "+onOff(s.AutoJoin))
	}
	if req.JoinRole != nil && *req.JoinRole != s.JoinRole {
		if *req.JoinRole != store.RoleMember && *req.JoinRole != store.RoleAdmin {
			writeError(w, http.StatusBadRequest, store.ErrBadJoinRole.Error())
			return
		}
		s.JoinRole = *req.JoinRole
		changed = append(changed, "join role "+string(s.JoinRole))
	}
	if req.RequireGoogle != nil && *req.RequireGoogle != s.RequireGoogle {
		s.RequireGoogle = *req.RequireGoogle
		changed = append(changed, "require Google "+onOff(s.RequireGoogle))
	}
	if req.Google2FA != nil && *req.Google2FA != s.Google2FA {
		s.Google2FA = *req.Google2FA
		changed = append(changed, "2FA after Google "+onOff(s.Google2FA))
	}
	if s.RequireGoogle {
		if !hasVerified(s) {
			writeError(w, http.StatusBadRequest, errNeedDomain.Error())
			return
		}
		if _, instance := a.instanceGoogle(); !instance && s.GoogleClient == "" {
			writeError(w, http.StatusBadRequest, errNeedGoogle.Error())
			return
		}
	}
	if err := a.ConfirmPassword(r, req.Password); err != nil {
		accountError(w, err)
		return
	}
	if err := a.acc.PutOrgSSO(s, a.now()); err != nil {
		accountError(w, err)
		return
	}
	if len(changed) > 0 {
		a.audit(r, store.AuthEvent{OrgID: m.ID, Actor: id.User.ID, Kind: "sso.settings", Detail: strings.Join(changed, ", ")})
	}
	a.orgSSO(w, r, id)
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// addDomain claims a domain for the org. It is verified at once when a
// platform admin adds it; otherwise it waits for its DNS TXT record (see
// checkDomain) or a platform admin's approval. A Google account with that
// hosted domain is no proof: any employee has one, not only the people who
// run the domain.
func (a *Auth) addDomain(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req struct {
		Domain   string `json:"domain"`
		Password string `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	m, ok := a.adminOf(w, r, id, store.RoleAdmin)
	if !ok {
		return
	}
	if m.Personal {
		writeError(w, http.StatusBadRequest, errNotPersonal.Error())
		return
	}
	domain, err := store.CleanDomain(req.Domain)
	if err != nil {
		accountError(w, err)
		return
	}
	if err := a.ConfirmPassword(r, req.Password); err != nil {
		accountError(w, err)
		return
	}
	how := ""
	if id.User.PlatformAdmin {
		how = "admin"
	}
	d, err := a.acc.AddSSODomain(m.ID, domain, how, id.User.ID, a.now())
	if err != nil {
		accountError(w, err)
		return
	}
	detail := domain
	if d.Verified {
		detail += " (verified: " + d.How + ")"
	}
	a.audit(r, store.AuthEvent{OrgID: m.ID, Actor: id.User.ID, Kind: "sso.domain.added", Detail: detail})
	writeJSON(w, http.StatusCreated, d)
}

// errNoTXT says the domain doesn't publish the claim's TXT record (yet).
type errNoTXT struct{ domain, record string }

func (e errNoTXT) Error() string {
	return "no TXT record " + e.record + " on " + e.domain + " yet: add it at your DNS provider, wait for it to show, then check again"
}

// checkDomain verifies the org's claim of a domain when the domain's DNS
// has a TXT record with the claim's token: only who runs the domain can
// put it there.
func (a *Auth) checkDomain(w http.ResponseWriter, r *http.Request, id *Identity) {
	m, ok := a.adminOf(w, r, id, store.RoleAdmin)
	if !ok {
		return
	}
	domain := strings.ToLower(strings.TrimSpace(r.PathValue("domain")))
	s, err := a.acc.OrgSSO(m.ID)
	if err != nil {
		accountError(w, err)
		return
	}
	var claim *store.SSODomain
	for i := range s.Domains {
		if s.Domains[i].Domain == domain {
			claim = &s.Domains[i]
		}
	}
	if claim == nil {
		accountError(w, store.ErrNotFound)
		return
	}
	if claim.Verified {
		writeJSON(w, http.StatusOK, claim)
		return
	}
	if claim.Token == "" || !a.hasTXT(r.Context(), domain, claim.TXTRecord()) {
		writeError(w, http.StatusUnprocessableEntity, errNoTXT{domain, claim.TXTRecord()}.Error())
		return
	}
	if err := a.acc.VerifySSODomain(m.ID, domain, "dns", id.User.ID, a.now()); err != nil {
		accountError(w, err)
		return
	}
	a.audit(r, store.AuthEvent{OrgID: m.ID, Actor: id.User.ID, Kind: "sso.domain.verified", Detail: domain + " by its DNS TXT record"})
	claim.Verified, claim.How, claim.VerifiedAt = true, "dns", a.now().UTC()
	writeJSON(w, http.StatusOK, claim)
}

// hasTXT reports whether domain publishes the TXT record want.
func (a *Auth) hasTXT(ctx context.Context, domain, want string) bool {
	lookup := a.lookupTXT
	if lookup == nil {
		lookup = net.DefaultResolver.LookupTXT
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	recs, err := lookup(ctx, domain)
	if err != nil {
		return false
	}
	for _, rec := range recs {
		if strings.TrimSpace(rec) == want {
			return true
		}
	}
	return false
}

// removeDomain drops a domain claim. A verified domain is what makes
// "require Google" (and the own client's hd restriction) apply, so only
// owners remove one, like they alone switch those off; admins can still
// withdraw claims that wait for verification. Either needs the password.
func (a *Auth) removeDomain(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	m, ok := a.adminOf(w, r, id, store.RoleAdmin)
	if !ok {
		return
	}
	domain := strings.ToLower(strings.TrimSpace(r.PathValue("domain")))
	s, err := a.acc.OrgSSO(m.ID)
	if err != nil {
		accountError(w, err)
		return
	}
	found := false
	for _, d := range s.Domains {
		if d.Domain != domain {
			continue
		}
		found = true
		if d.Verified && m.Role != store.RoleOwner {
			writeError(w, http.StatusForbidden, errOwnersDomain.Error())
			return
		}
	}
	if !found {
		accountError(w, store.ErrNotFound)
		return
	}
	if err := a.ConfirmPassword(r, req.Password); err != nil {
		accountError(w, err)
		return
	}
	if err := a.acc.RemoveSSODomain(m.ID, domain, m.Role == store.RoleOwner); err != nil {
		accountError(w, err)
		return
	}
	a.audit(r, store.AuthEvent{OrgID: m.ID, Actor: id.User.ID, Kind: "sso.domain.removed", Detail: domain})
	w.WriteHeader(http.StatusNoContent)
}

// signOutMember ends a member's sessions in the org (and those sessions'
// siblings on the base domain): removing someone in Google stops new
// sign-ins, and this ends the ones they have.
func (a *Auth) signOutMember(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	m, ok := a.adminOf(w, r, id, store.RoleAdmin)
	if !ok {
		return
	}
	uid, err := pathID(r, "uid")
	if err != nil {
		accountError(w, err)
		return
	}
	target, err := a.acc.Membership(uid, m.ID)
	if err != nil {
		accountError(w, err)
		return
	}
	if !m.Role.AtLeast(target.Role) {
		writeError(w, http.StatusForbidden, errNeedRoleHigh.Error())
		return
	}
	if err := a.ConfirmPassword(r, req.Password); err != nil {
		accountError(w, err)
		return
	}
	n, err := a.acc.SignOutMember(uid, m.ID)
	if err != nil {
		accountError(w, err)
		return
	}
	a.audit(r, store.AuthEvent{OrgID: m.ID, UserID: uid, Actor: id.User.ID, Kind: "member.signed_out", Detail: plural(n, "session")})
	writeJSON(w, http.StatusOK, map[string]int{"sessions": n})
}

func plural(n int, what string) string {
	s := strconv.Itoa(n) + " " + what
	if n != 1 {
		s += "s"
	}
	return s
}

// googleAvailable reports whether id may sign in with Google: the
// instance's client or their org's own.
func (a *Auth) googleAvailable(id *Identity) bool {
	var org int64
	if id.Org != nil {
		org = id.Org.ID
	}
	_, ok := a.googleClient(org)
	return ok
}

func (a *Auth) accountGoogle(w http.ResponseWriter, r *http.Request, id *Identity) {
	has, err := a.acc.HasPassword(id.User.ID)
	if err != nil {
		accountError(w, err)
		return
	}
	resp := map[string]any{"available": a.googleAvailable(id), "has_password": has}
	if ident, err := a.acc.UserIdentity(id.User.ID, store.ProviderGoogle); err == nil {
		resp["linked"], resp["identity"] = true, ident
	} else if !errors.Is(err, store.ErrNotFound) {
		accountError(w, err)
		return
	} else {
		resp["linked"] = false
	}
	if id.Org != nil {
		resp["link_url"] = a.startURL(r, id.Org.Org, flowLink)
	} else {
		resp["link_url"] = GoogleStartPath + "?purpose=link"
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *Auth) unlinkGoogle(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	// Refuse before the password if it's their only way in, so it costs no try.
	if has, err := a.acc.HasPassword(id.User.ID); err != nil || !has {
		if err == nil {
			err = store.ErrNoPassword
		}
		accountError(w, err)
		return
	}
	if err := a.ConfirmPassword(r, req.Password); err != nil {
		accountError(w, err)
		return
	}
	ident, err := a.acc.UnlinkIdentity(id.User.ID, store.ProviderGoogle)
	if err != nil {
		accountError(w, err)
		return
	}
	var org int64
	if id.Org != nil {
		org = id.Org.ID
	}
	a.audit(r, store.AuthEvent{OrgID: org, UserID: id.User.ID, Actor: id.User.ID, Kind: "google.unlinked", Detail: ident.Email})
	w.WriteHeader(http.StatusNoContent)
}

// ---- platform admins ----

func (a *Auth) adminSSO(w http.ResponseWriter, r *http.Request, id *Identity) {
	if !id.User.PlatformAdmin {
		writeError(w, http.StatusForbidden, errAdminsOnly.Error())
		return
	}
	cid, sealed, err := a.acc.InstanceGoogle()
	if err != nil {
		accountError(w, err)
		return
	}
	fromEnv := a.Google.ClientID != "" && a.Google.ClientSecret != ""
	if fromEnv {
		cid, sealed = a.Google.ClientID, "env"
	}
	pending, err := a.acc.PendingSSODomains()
	if err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"client_id": cid, "client_secret_set": sealed != "", "from_env": fromEnv,
		"can_store_secret": a.sealer != nil, "redirect_uri": a.googleRedirectURI(r), "pending_domains": pending,
	})
}

func (a *Auth) putInstanceGoogle(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		Password     string `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	if !id.User.PlatformAdmin {
		writeError(w, http.StatusForbidden, errAdminsOnly.Error())
		return
	}
	if a.Google.ClientID != "" && a.Google.ClientSecret != "" {
		writeError(w, http.StatusConflict, errEnvClient.Error())
		return
	}
	cid := strings.TrimSpace(req.ClientID)
	if cid != "" && (len(cid) > 200 || strings.ContainsAny(cid, " \t\r\n/")) {
		writeError(w, http.StatusBadRequest, errBadClientID.Error())
		return
	}
	_, sealed, err := a.acc.InstanceGoogle()
	if err != nil {
		accountError(w, err)
		return
	}
	if cid == "" {
		sealed = ""
	} else if s := strings.TrimSpace(req.ClientSecret); s != "" {
		if sealed, err = a.sealer.Seal(s, instanceSecretContext); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
	}
	if cid != "" && sealed == "" {
		writeError(w, http.StatusBadRequest, errNeedSecret.Error())
		return
	}
	if err := a.ConfirmPassword(r, req.Password); err != nil {
		accountError(w, err)
		return
	}
	if err := a.acc.PutInstanceGoogle(cid, sealed, a.now()); err != nil {
		accountError(w, err)
		return
	}
	a.audit(r, store.AuthEvent{Actor: id.User.ID, Kind: "sso.instance_client", Detail: "client id " + cid})
	a.adminSSO(w, r, id)
}

func (a *Auth) adminVerifyDomain(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req struct {
		OrgID    int64  `json:"org_id"`
		Domain   string `json:"domain"`
		Password string `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	if !id.User.PlatformAdmin {
		writeError(w, http.StatusForbidden, errAdminsOnly.Error())
		return
	}
	if err := a.ConfirmPassword(r, req.Password); err != nil {
		accountError(w, err)
		return
	}
	if err := a.acc.VerifySSODomain(req.OrgID, strings.ToLower(strings.TrimSpace(req.Domain)), "admin", id.User.ID, a.now()); err != nil {
		accountError(w, err)
		return
	}
	a.audit(r, store.AuthEvent{OrgID: req.OrgID, Actor: id.User.ID, Kind: "sso.domain.verified", Detail: req.Domain + " by a platform admin"})
	w.WriteHeader(http.StatusNoContent)
}
