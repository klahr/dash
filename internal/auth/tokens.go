package auth

import (
	"errors"
	"net/http"
	"strings"

	"github.com/klahr/dash/internal/store"
)

// listTokens handles GET /api/tokens: the user's API tokens, the OAuth
// clients they authorised and the runners they registered.
func (a *Auth) listTokens(w http.ResponseWriter, r *http.Request, id *Identity) {
	ts, err := a.acc.Tokens(id.User.ID)
	if err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

// createToken handles POST /api/tokens {name, org_id, scopes, password}: a
// new API token, pinned to org_id, which is required (the UI sends the
// active org). The secret is in the answer only.
func (a *Auth) createToken(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req struct {
		Name     string   `json:"name"`
		OrgID    int64    `json:"org_id"`
		Scopes   []string `json:"scopes"`
		Password string   `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	if req.OrgID == 0 {
		accountError(w, store.ErrNoOrg)
		return
	}
	// Check what can be checked before the password, so a typo doesn't burn a try.
	m, err := a.acc.Membership(id.User.ID, req.OrgID)
	if err != nil {
		accountError(w, err)
		return
	}
	// The org may take only Google sign-ins from them: then only a session
	// signed in with a Google account of its domains makes tokens for it,
	// whichever org the session is working in, or a password (or a personal
	// Google account) would outlive their removal in Google.
	if a.googleRefuses(id.User, id.Method, m) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this organization requires signing in with Google to create tokens for it", "code": "google_required"})
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		accountError(w, store.ErrBadTokenName)
		return
	}
	if err := a.ConfirmPassword(r, req.Password); err != nil {
		accountError(w, err)
		return
	}
	secret, t, err := a.acc.CreateToken(store.NewToken{
		Kind: store.TokenAPI, Name: req.Name, UserID: id.User.ID, OrgID: req.OrgID, Scopes: req.Scopes,
	}, a.now())
	if err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": t, "secret": secret})
}

// revokeToken handles DELETE /api/tokens/{id}: one of the user's tokens of
// any kind. Revoking an OAuth grant disconnects its client; a runner's,
// the runner (see OnRevoke).
func (a *Auth) revokeToken(w http.ResponseWriter, r *http.Request, id *Identity) {
	tid, err := pathID(r, "id")
	if err != nil {
		accountError(w, err)
		return
	}
	t, err := a.acc.RevokeToken(id.User.ID, tid)
	if err != nil {
		accountError(w, err)
		return
	}
	if a.OnRevoke != nil {
		a.OnRevoke(t)
	}
	w.WriteHeader(http.StatusNoContent)
}

// orgTokens handles GET /api/orgs/{oid}/tokens (admins): the API tokens and
// connected apps of the org's members, so an admin can see what still
// signs in as someone, and revoke it.
func (a *Auth) orgTokens(w http.ResponseWriter, r *http.Request, id *Identity) {
	m, ok := a.adminOf(w, r, id, store.RoleAdmin)
	if !ok {
		return
	}
	ts, err := a.acc.OrgTokens(m.ID)
	if err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

// revokeOrgToken handles DELETE /api/orgs/{oid}/tokens/{id} {password}
// (admins): a member's API token or connected app, if the member's role
// isn't above the admin's. Runners are revoked in Runners.
func (a *Auth) revokeOrgToken(w http.ResponseWriter, r *http.Request, id *Identity) {
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
	tid, err := pathID(r, "id")
	if err != nil {
		accountError(w, err)
		return
	}
	t, err := a.acc.OrgToken(m.ID, tid)
	if err != nil {
		accountError(w, err)
		return
	}
	owner, err := a.acc.Membership(t.UserID, m.ID)
	if err != nil {
		accountError(w, store.ErrNotFound) // a former member's: it works no more
		return
	}
	if !m.Role.AtLeast(owner.Role) {
		writeError(w, http.StatusForbidden, errRevokeHigher.Error())
		return
	}
	if err := a.ConfirmPassword(r, req.Password); err != nil {
		accountError(w, err)
		return
	}
	if err := a.acc.DeleteToken(t.ID); err != nil {
		accountError(w, err)
		return
	}
	if a.OnRevoke != nil {
		a.OnRevoke(t)
	}
	a.audit(r, store.AuthEvent{OrgID: m.ID, UserID: t.UserID, Actor: id.User.ID, Kind: "token.revoked", Detail: t.Name})
	w.WriteHeader(http.StatusNoContent)
}

var errRevokeHigher = errors.New("you can't revoke the token of someone with a higher role than yours")
