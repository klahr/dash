package auth

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/klahr/dash/internal/store"
)

// Managing someone else's account (see docs/users.md#admin):
//
//	GET    /api/admin/users?q=                         platform admins: every account, with its orgs
//	GET    /api/admin/users/{uid}                      one, with its sign-in log
//	POST   /api/admin/users/{uid}/password-reset {password, sign_out}
//	DELETE /api/admin/users/{uid}/2fa {password}       turn off two-factor sign-in
//	POST   /api/admin/users/{uid}/sign-out {password}  sessions, API tokens and connected apps
//	PUT    /api/admin/users/{uid}/disabled {disabled, password}
//	PUT    /api/admin/users/{uid}/platform-admin {platform_admin, password}
//	PUT    /api/admin/users/{uid}/orgs/{oid} {role, password}   add to an org, or change the role
//	DELETE /api/admin/users/{uid}/orgs/{oid} {password}         take out of an org
//	GET    /api/admin/orgs?q=                          every org, with member and project counts
//	GET    /api/admin/orgs/{oid}                       one, with its members
//	PATCH  /api/admin/orgs/{oid} {name, slug, password}
//	DELETE /api/admin/orgs/{oid} {name, password}      delete (name: typed to confirm)
//	POST   /api/orgs/{oid}/members/{uid}/password-reset {password, sign_out}   org owners and admins
//	DELETE /api/orgs/{oid}/members/{uid}/2fa {password}                       org owners and admins
//
// (DELETE /api/admin/users/{uid} is in erasure.go, and an org admin's
// sign-out of a member, POST /api/orgs/{oid}/members/{uid}/sign-out, in
// sso.go.) Every route needs a browser session, and every change the
// admin's password. An org admin acts only on accounts all of whose orgs
// they manage: see store.Accounts.MemberAccess. Each change goes to the
// sign-in log (audit) and the server log.

func (a *Auth) adminRoutes(mux Mux) {
	mux.HandleFunc("GET /api/admin/users", a.platformAdmin(a.adminUsers))
	mux.HandleFunc("GET /api/admin/users/{uid}", a.platformAdmin(a.adminUser))
	mux.HandleFunc("POST /api/admin/users/{uid}/password-reset", a.platformAdmin(a.adminResetPassword))
	mux.HandleFunc("DELETE /api/admin/users/{uid}/2fa", a.platformAdmin(a.adminDisableTOTP))
	mux.HandleFunc("POST /api/admin/users/{uid}/sign-out", a.platformAdmin(a.adminSignOut))
	mux.HandleFunc("PUT /api/admin/users/{uid}/disabled", a.platformAdmin(a.adminSetDisabled))
	mux.HandleFunc("PUT /api/admin/users/{uid}/platform-admin", a.platformAdmin(a.adminSetPlatformAdmin))
	mux.HandleFunc("PUT /api/admin/users/{uid}/orgs/{oid}", a.platformAdmin(a.adminSetMember))
	mux.HandleFunc("DELETE /api/admin/users/{uid}/orgs/{oid}", a.platformAdmin(a.adminRemoveMember))
	mux.HandleFunc("GET /api/admin/orgs", a.platformAdmin(a.adminOrgs))
	mux.HandleFunc("GET /api/admin/orgs/{oid}", a.platformAdmin(a.adminOrg))
	mux.HandleFunc("PATCH /api/admin/orgs/{oid}", a.platformAdmin(a.adminUpdateOrg))
	mux.HandleFunc("DELETE /api/admin/orgs/{oid}", a.platformAdmin(a.adminDeleteOrg))
	mux.HandleFunc("POST /api/orgs/{oid}/members/{uid}/password-reset", a.sessionOnly(a.memberResetPassword))
	mux.HandleFunc("DELETE /api/orgs/{oid}/members/{uid}/2fa", a.sessionOnly(a.memberDisableTOTP))
}

// platformAdmin is sessionOnly for platform admins: anyone else gets 403.
func (a *Auth) platformAdmin(h func(http.ResponseWriter, *http.Request, *Identity)) http.HandlerFunc {
	return a.sessionOnly(func(w http.ResponseWriter, r *http.Request, id *Identity) {
		if !id.User.PlatformAdmin {
			writeError(w, http.StatusForbidden, errAdminsOnly.Error())
			return
		}
		h(w, r, id)
	})
}

var errAdminSelf = errors.New("that's you: use Account for your own account")

// adminChange reads a change's body into req (which has a Password field
// read by pw), checks the target uid (not the caller, unless self is
// allowed) and the password. It answers the error itself.
func (a *Auth) adminChange(w http.ResponseWriter, r *http.Request, id *Identity, req any, pw *string, self bool) (int64, bool) {
	uid, err := pathID(r, "uid")
	if err != nil {
		accountError(w, err)
		return 0, false
	}
	if err := decodeBody(w, r, req); err != nil {
		accountError(w, err)
		return 0, false
	}
	if !self && uid == id.User.ID {
		writeError(w, http.StatusConflict, errAdminSelf.Error())
		return 0, false
	}
	if _, err := a.acc.User(uid); err != nil {
		accountError(w, err)
		return 0, false
	}
	if err := a.ConfirmPassword(r, *pw); err != nil {
		accountError(w, err)
		return 0, false
	}
	return uid, true
}

type passwordBody struct {
	Password string `json:"password"`
}

// ---- platform admins: users ----

func (a *Auth) adminUsers(w http.ResponseWriter, r *http.Request, id *Identity) {
	users, total, matched, err := a.acc.AdminUsers(r.URL.Query().Get("q"))
	if err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users, "total": total, "matched": matched, "limit": store.AdminUserLimit,
		"mail": a.MailOn()})
}

func (a *Auth) adminUser(w http.ResponseWriter, r *http.Request, id *Identity) {
	uid, err := pathID(r, "uid")
	if err != nil {
		accountError(w, err)
		return
	}
	u, err := a.acc.AdminUser(uid)
	if err != nil {
		accountError(w, err)
		return
	}
	events, err := a.acc.UserAuthEvents(uid, 30)
	if err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u, "events": events, "mail": a.MailOn()})
}

type resetBody struct {
	Password string `json:"password"`
	SignOut  bool   `json:"sign_out"` // also end their sessions now
}

func (a *Auth) adminResetPassword(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req resetBody
	uid, ok := a.adminChange(w, r, id, &req, &req.Password, false)
	if !ok {
		return
	}
	a.resetFor(w, r, id, uid, 0, req.SignOut)
}

func (a *Auth) adminDisableTOTP(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req passwordBody
	uid, ok := a.adminChange(w, r, id, &req, &req.Password, false)
	if !ok {
		return
	}
	a.disableTOTPFor(w, r, id, uid, 0)
}

func (a *Auth) adminSignOut(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req passwordBody
	uid, ok := a.adminChange(w, r, id, &req, &req.Password, false)
	if !ok {
		return
	}
	a.signOutEverywhere(w, r, id, uid, 0)
}

func (a *Auth) adminSetDisabled(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req struct {
		Disabled bool   `json:"disabled"`
		Password string `json:"password"`
	}
	uid, ok := a.adminChange(w, r, id, &req, &req.Password, false)
	if !ok {
		return
	}
	n, err := a.acc.SetDisabled(uid, req.Disabled, a.now())
	if err != nil {
		accountError(w, err)
		return
	}
	kind := "admin.enabled"
	if req.Disabled {
		kind = "admin.disabled"
	}
	a.audit(r, store.AuthEvent{UserID: uid, Actor: id.User.ID, Kind: kind})
	writeJSON(w, http.StatusOK, map[string]any{"disabled": req.Disabled, "sessions": n})
}

func (a *Auth) adminSetPlatformAdmin(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req struct {
		PlatformAdmin bool   `json:"platform_admin"`
		Password      string `json:"password"`
	}
	// Demoting yourself is allowed while someone else stays one.
	uid, ok := a.adminChange(w, r, id, &req, &req.Password, true)
	if !ok {
		return
	}
	if err := a.acc.SetPlatformAdmin(uid, req.PlatformAdmin); err != nil {
		accountError(w, err)
		return
	}
	kind := "admin.platform_admin.removed"
	if req.PlatformAdmin {
		kind = "admin.platform_admin.granted"
	}
	a.audit(r, store.AuthEvent{UserID: uid, Actor: id.User.ID, Kind: kind})
	writeJSON(w, http.StatusOK, map[string]bool{"platform_admin": req.PlatformAdmin})
}

func (a *Auth) adminSetMember(w http.ResponseWriter, r *http.Request, id *Identity) {
	oid, err := pathID(r, "oid")
	if err != nil {
		accountError(w, err)
		return
	}
	var req struct {
		Role     store.Role `json:"role"`
		Password string     `json:"password"`
	}
	uid, ok := a.adminChange(w, r, id, &req, &req.Password, true)
	if !ok {
		return
	}
	added, err := a.acc.AdminSetMember(oid, uid, req.Role)
	if err != nil {
		accountError(w, err)
		return
	}
	kind := "admin.member.role"
	if added {
		kind = "admin.member.added"
		a.membersChanged(oid)
	}
	a.audit(r, store.AuthEvent{OrgID: oid, UserID: uid, Actor: id.User.ID, Kind: kind, Detail: string(req.Role)})
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "role": req.Role})
}

func (a *Auth) adminRemoveMember(w http.ResponseWriter, r *http.Request, id *Identity) {
	oid, err := pathID(r, "oid")
	if err != nil {
		accountError(w, err)
		return
	}
	var req passwordBody
	uid, ok := a.adminChange(w, r, id, &req, &req.Password, true)
	if !ok {
		return
	}
	if err := a.acc.AdminRemoveMember(oid, uid); err != nil {
		accountError(w, err)
		return
	}
	a.membersChanged(oid)
	a.audit(r, store.AuthEvent{OrgID: oid, UserID: uid, Actor: id.User.ID, Kind: "admin.member.removed"})
	w.WriteHeader(http.StatusNoContent)
}

// ---- platform admins: orgs ----

func (a *Auth) adminOrgs(w http.ResponseWriter, r *http.Request, id *Identity) {
	orgs, err := a.acc.AdminOrgs(r.URL.Query().Get("q"))
	if err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"orgs": orgs, "total": len(orgs)})
}

func (a *Auth) adminOrg(w http.ResponseWriter, r *http.Request, id *Identity) {
	oid, err := pathID(r, "oid")
	if err != nil {
		accountError(w, err)
		return
	}
	o, err := a.acc.AdminOrg(oid)
	if err != nil {
		accountError(w, err)
		return
	}
	members, err := a.acc.Members(oid)
	if err != nil {
		accountError(w, err)
		return
	}
	resp := map[string]any{"org": o, "members": members, "slugs": a.Domains != nil}
	writeJSON(w, http.StatusOK, resp)
}

func (a *Auth) adminUpdateOrg(w http.ResponseWriter, r *http.Request, id *Identity) {
	oid, err := pathID(r, "oid")
	if err != nil {
		accountError(w, err)
		return
	}
	var req struct {
		Name     *string `json:"name"`
		Slug     *string `json:"slug"`
		Password string  `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	if req.Name == nil && req.Slug == nil {
		accountError(w, errBadBody)
		return
	}
	cur, err := a.acc.Org(oid)
	if err != nil {
		accountError(w, err)
		return
	}
	if req.Name != nil {
		if n := strings.TrimSpace(*req.Name); n == "" || len(n) > 100 {
			accountError(w, store.ErrBadOrgName)
			return
		}
	}
	if req.Slug != nil {
		if err := a.acc.CheckSlug(*req.Slug); err != nil {
			accountError(w, err)
			return
		}
	}
	if err := a.ConfirmPassword(r, req.Password); err != nil {
		accountError(w, err)
		return
	}
	o := cur
	if req.Name != nil && strings.TrimSpace(*req.Name) != cur.Name {
		if o, err = a.acc.AdminSetOrgName(oid, *req.Name); err != nil {
			accountError(w, err)
			return
		}
		a.audit(r, store.AuthEvent{OrgID: oid, Actor: id.User.ID, Kind: "admin.org.renamed", Detail: cur.Name + " → " + o.Name})
	}
	if req.Slug != nil && *req.Slug != cur.Slug {
		if o, err = a.acc.AdminSetOrgSlug(oid, *req.Slug, a.now()); err != nil {
			accountError(w, err)
			return
		}
		a.audit(r, store.AuthEvent{OrgID: oid, Actor: id.User.ID, Kind: "admin.org.slug", Detail: cur.Slug + " → " + o.Slug})
	}
	writeJSON(w, http.StatusOK, o)
}

var errConfirmName = errors.New("type the organization's name to confirm")

func (a *Auth) adminDeleteOrg(w http.ResponseWriter, r *http.Request, id *Identity) {
	oid, err := pathID(r, "oid")
	if err != nil {
		accountError(w, err)
		return
	}
	var req struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	o, err := a.acc.Org(oid)
	if err != nil {
		accountError(w, err)
		return
	}
	if strings.TrimSpace(req.Name) != o.Name {
		writeError(w, http.StatusBadRequest, errConfirmName.Error())
		return
	}
	if err := a.ConfirmPassword(r, req.Password); err != nil {
		accountError(w, err)
		return
	}
	d, err := a.acc.DeleteOrg(oid)
	if err != nil {
		accountError(w, err)
		return
	}
	if a.OnRevoke != nil {
		for _, t := range d.Tokens {
			a.OnRevoke(t)
		}
	}
	// Its own log went with it: the entry is the instance's.
	a.audit(r, store.AuthEvent{Actor: id.User.ID, Kind: "admin.org.deleted", Detail: o.Name + " (" + o.Slug + ")"})
	writeJSON(w, http.StatusOK, map[string]any{"members": len(d.Members)})
}

// ---- org owners and admins: their members' accounts ----

// memberTarget checks that the caller, an owner or admin of the path's org,
// may manage the path's member's account (store.Accounts.MemberAccess),
// before the password so a refusal costs no try. It answers the error
// itself.
func (a *Auth) memberTarget(w http.ResponseWriter, r *http.Request, id *Identity, pw string) (store.Membership, int64, bool) {
	m, ok := a.adminOf(w, r, id, store.RoleAdmin)
	if !ok {
		return m, 0, false
	}
	uid, err := pathID(r, "uid")
	if err != nil {
		accountError(w, err)
		return m, 0, false
	}
	if err := a.acc.MemberAccess(id.User.ID, id.User.PlatformAdmin, m.ID, uid); err != nil {
		accountError(w, err)
		return m, 0, false
	}
	if err := a.ConfirmPassword(r, pw); err != nil {
		accountError(w, err)
		return m, 0, false
	}
	return m, uid, true
}

func (a *Auth) memberResetPassword(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req resetBody
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	m, uid, ok := a.memberTarget(w, r, id, req.Password)
	if !ok {
		return
	}
	a.resetFor(w, r, id, uid, m.ID, req.SignOut)
}

func (a *Auth) memberDisableTOTP(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req passwordBody
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	m, uid, ok := a.memberTarget(w, r, id, req.Password)
	if !ok {
		return
	}
	a.disableTOTPFor(w, r, id, uid, m.ID)
}

// ---- the actions, for either ----

// resetFor makes a one-time reset link for uid, as id (in org, 0: as a
// platform admin): emailed to them when Dash sends email and they have an
// address, and shown to the admin once either way. With signOut their
// sessions, API tokens and connected apps end now; otherwise when the link
// is used, as with "Forgot password?".
func (a *Auth) resetFor(w http.ResponseWriter, r *http.Request, id *Identity, uid, org int64, signOut bool) {
	u, tok, expires, err := a.acc.AdminResetLink(uid, a.now())
	if err != nil {
		accountError(w, err)
		return
	}
	resp := map[string]any{"path": "/login.html#reset=" + tok, "expires": expires, "emailed": false}
	if a.baseURL != "" {
		resp["url"] = a.link("reset=" + tok)
	}
	if a.MailOn() && u.Email != "" {
		a.sendAdminReset(u, tok, id.User.Username)
		resp["emailed"] = true
	}
	detail := "link made"
	if resp["emailed"] == true {
		detail = "link emailed and shown"
	}
	if signOut {
		n, toks, err := a.acc.SignOutEverywhere(uid)
		if err != nil {
			accountError(w, err)
			return
		}
		a.revoked(toks)
		resp["sessions"] = n
		detail += ", signed out"
	}
	a.audit(r, store.AuthEvent{OrgID: org, UserID: uid, Actor: id.User.ID, Kind: "admin.password_reset", Detail: detail})
	writeJSON(w, http.StatusOK, resp)
}

// disableTOTPFor turns off uid's two-factor sign-in, as id (in org, 0: as a
// platform admin), and tells them by email.
func (a *Auth) disableTOTPFor(w http.ResponseWriter, r *http.Request, id *Identity, uid, org int64) {
	u, err := a.acc.AdminDisableTOTP(uid)
	if err != nil {
		accountError(w, err)
		return
	}
	a.audit(r, store.AuthEvent{OrgID: org, UserID: uid, Actor: id.User.ID, Kind: "admin.2fa_off"})
	emailed := false
	if a.MailOn() && u.Email != "" {
		a.sendTOTPOff(u, id.User.Username)
		emailed = true
	}
	writeJSON(w, http.StatusOK, map[string]any{"emailed": emailed})
}

// signOutEverywhere ends uid's sessions and deletes their API tokens and
// connected apps, as id (in org, 0: as a platform admin).
func (a *Auth) signOutEverywhere(w http.ResponseWriter, r *http.Request, id *Identity, uid, org int64) {
	n, toks, err := a.acc.SignOutEverywhere(uid)
	if err != nil {
		accountError(w, err)
		return
	}
	a.revoked(toks)
	a.audit(r, store.AuthEvent{OrgID: org, UserID: uid, Actor: id.User.ID, Kind: "admin.signed_out",
		Detail: plural(n, "session") + ", " + plural(len(toks), "token")})
	writeJSON(w, http.StatusOK, map[string]any{"sessions": n, "tokens": len(toks), "everywhere": true})
}

// revoked tells OnRevoke about tokens that were deleted.
func (a *Auth) revoked(toks []store.Token) {
	if a.OnRevoke == nil {
		return
	}
	for _, t := range toks {
		a.OnRevoke(t)
	}
}

// ---- emails ----

func (a *Auth) sendAdminReset(u store.User, tok, by string) {
	name := a.brand().Name
	n := a.notice("Choose a new password", "Hello "+u.Username+",")
	n.Before = []string{
		"An administrator (" + by + ") made a link for you to choose a new password for your " + name + " account at " + a.baseURL + ". " +
			"Open it within " + durationText(store.ResetTTL) + " to choose one:",
	}
	n.Action, n.URL = "Choose a new password", a.link("reset="+tok)
	n.After = []string{
		"Choosing a new password signs you out everywhere and stops your API tokens and connected apps.",
		"If you didn't ask for this, tell your administrator: your password stays as it is until the link is used.",
	}
	a.send(n.Message(u.Email, "Reset your "+name+" password"))
}

func (a *Auth) sendTOTPOff(u store.User, by string) {
	name := a.brand().Name
	n := a.notice("Two-factor sign-in was turned off", "Hello "+u.Username+",")
	n.Before = []string{
		"An administrator (" + by + ") turned off two-factor sign-in for your " + name + " account at " + a.baseURL + ".",
		"You sign in with your password alone until you turn it on again under Account → Two-factor sign-in " +
			"(if an organization of yours requires it, you're asked to at your next sign-in).",
	}
	n.After = []string{"If you didn't ask for this, tell your administrator and change your password."}
	a.send(n.Message(u.Email, "Two-factor sign-in was turned off for your "+name+" account"))
}

func durationText(d time.Duration) string {
	if d == time.Hour {
		return "an hour"
	}
	return d.String()
}
