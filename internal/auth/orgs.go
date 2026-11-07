package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/klahr/dash/internal/store"
)

// Routes adds sign-in, the signed-in user, orgs, members and invitations:
//
//	POST   /api/login, /api/logout
//	GET    /api/me                               user, active org and all their orgs
//	POST   /api/auth/switch-org {org_id}
//	GET    /api/orgs                             the user's orgs
//	POST   /api/orgs {name}                      platform admins: a new org they own
//	GET    /api/orgs/{oid}/members               members (and, for admins, invitations)
//	PATCH  /api/orgs/{oid}/members/{uid} {role, password}
//	DELETE /api/orgs/{oid}/members/{uid} {password}
//	POST   /api/orgs/{oid}/invitations {email, role, password}
//	DELETE /api/orgs/{oid}/invitations/{iid}
//	POST   /api/invitations/lookup|accept|register|decline {token, …}
//
// Changing roles, removing members and inviting ask for the password again.
func (a *Auth) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/login", a.Login)
	mux.HandleFunc("POST /api/logout", a.Logout)
	mux.HandleFunc("GET /api/me", a.me)
	mux.HandleFunc("POST /api/auth/switch-org", a.signedIn(a.switchOrg))
	mux.HandleFunc("GET /api/orgs", a.signedIn(a.listOrgs))
	mux.HandleFunc("POST /api/orgs", a.signedIn(a.createOrg))
	mux.HandleFunc("GET /api/orgs/{oid}/members", a.signedIn(a.members))
	mux.HandleFunc("PATCH /api/orgs/{oid}/members/{uid}", a.signedIn(a.setRole))
	mux.HandleFunc("DELETE /api/orgs/{oid}/members/{uid}", a.signedIn(a.removeMember))
	mux.HandleFunc("POST /api/orgs/{oid}/invitations", a.signedIn(a.invite))
	mux.HandleFunc("DELETE /api/orgs/{oid}/invitations/{iid}", a.signedIn(a.revokeInvitation))
	mux.HandleFunc("POST /api/invitations/lookup", a.lookupInvitation)
	mux.HandleFunc("POST /api/invitations/accept", a.signedIn(a.acceptInvitation))
	mux.HandleFunc("POST /api/invitations/register", a.register)
	mux.HandleFunc("POST /api/invitations/decline", a.declineInvitation)
}

// signedIn passes the request's identity to h, and answers 401 without one
// (a request with the peer token, say).
func (a *Auth) signedIn(h func(http.ResponseWriter, *http.Request, *Identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := From(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "login required")
			return
		}
		h(w, r, id)
	}
}

// accountError answers with err's status and message.
func accountError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrForbidden):
		code = http.StatusForbidden
	case errors.Is(err, store.ErrNotMember), errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrInvitation):
		code = http.StatusNotFound
	case errors.Is(err, store.ErrLastOwner), errors.Is(err, store.ErrUsernameTaken):
		code = http.StatusConflict
	case errors.Is(err, store.ErrBadRole), errors.Is(err, store.ErrBadEmail), errors.Is(err, store.ErrBadUsername),
		errors.Is(err, store.ErrWeakPassword), errors.Is(err, store.ErrBadOrgName), errors.Is(err, errBadBody):
		code = http.StatusBadRequest
	case errors.Is(err, ErrBadPassword):
		code = http.StatusForbidden // not 401: that would sign the browser out
	case errors.Is(err, ErrLockedOut):
		code = http.StatusTooManyRequests
	}
	if code == http.StatusInternalServerError {
		slog.Error("accounts", "err", err)
		writeError(w, code, "something went wrong")
		return
	}
	writeError(w, code, err.Error())
}

var errBadBody = errors.New("bad request")

func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return errBadBody
	}
	return nil
}

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, store.ErrNotFound
	}
	return id, nil
}

// me handles GET /api/me.
func (a *Auth) me(w http.ResponseWriter, r *http.Request) {
	id, ok := From(r.Context())
	if !ok { // the peer token: a computer, not a person
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	orgs, err := a.acc.Memberships(id.User.ID)
	if err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"username": id.User.Username,
		"user":     id.User,
		"org":      id.Org,
		"orgs":     orgs,
	})
}

func (a *Auth) switchOrg(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req struct {
		OrgID int64 `json:"org_id"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	m, err := a.acc.SwitchOrg(id.key, id.User.ID, req.OrgID)
	if errors.Is(err, store.ErrNotMember) {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	if err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (a *Auth) listOrgs(w http.ResponseWriter, r *http.Request, id *Identity) {
	orgs, err := a.acc.Memberships(id.User.ID)
	if err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, orgs)
}

func (a *Auth) createOrg(w http.ResponseWriter, r *http.Request, id *Identity) {
	if !id.User.PlatformAdmin {
		writeError(w, http.StatusForbidden, "only platform admins create organizations")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	o, err := a.acc.CreateOrg(id.User.ID, req.Name)
	if err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, store.Membership{Org: o, Role: store.RoleOwner})
}

func (a *Auth) members(w http.ResponseWriter, r *http.Request, id *Identity) {
	oid, err := pathID(r, "oid")
	if err != nil {
		accountError(w, err)
		return
	}
	mine, err := a.acc.Membership(id.User.ID, oid)
	if err != nil {
		accountError(w, err)
		return
	}
	members, err := a.acc.Members(oid)
	if err != nil {
		accountError(w, err)
		return
	}
	resp := map[string]any{"org": mine.Org, "role": mine.Role, "members": members}
	if mine.Role.AtLeast(store.RoleAdmin) {
		if resp["invitations"], err = a.acc.Invitations(oid); err != nil {
			accountError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *Auth) setRole(w http.ResponseWriter, r *http.Request, id *Identity) {
	oid, err := pathID(r, "oid")
	if err != nil {
		accountError(w, err)
		return
	}
	uid, err := pathID(r, "uid")
	if err != nil {
		accountError(w, err)
		return
	}
	var req struct {
		Role     store.Role `json:"role"`
		Password string     `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	if err := a.ConfirmPassword(r, req.Password); err != nil {
		accountError(w, err)
		return
	}
	if err := a.acc.SetRole(id.User.ID, oid, uid, req.Role); err != nil {
		accountError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Auth) removeMember(w http.ResponseWriter, r *http.Request, id *Identity) {
	oid, err := pathID(r, "oid")
	if err != nil {
		accountError(w, err)
		return
	}
	uid, err := pathID(r, "uid")
	if err != nil {
		accountError(w, err)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	if err := a.ConfirmPassword(r, req.Password); err != nil {
		accountError(w, err)
		return
	}
	if err := a.acc.RemoveMember(id.User.ID, oid, uid); err != nil {
		accountError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// invite answers the same whether or not the address has an account: the
// link (path + token) is for the inviter to pass on.
func (a *Auth) invite(w http.ResponseWriter, r *http.Request, id *Identity) {
	oid, err := pathID(r, "oid")
	if err != nil {
		accountError(w, err)
		return
	}
	var req struct {
		Email    string     `json:"email"`
		Role     store.Role `json:"role"`
		Password string     `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	if req.Role == "" {
		req.Role = store.RoleMember
	}
	// Check the role before the password, so a member doesn't burn a try.
	mine, err := a.acc.Membership(id.User.ID, oid)
	if err != nil {
		accountError(w, err)
		return
	}
	if !mine.Role.AtLeast(store.RoleAdmin) {
		accountError(w, store.ErrForbidden)
		return
	}
	if err := a.ConfirmPassword(r, req.Password); err != nil {
		accountError(w, err)
		return
	}
	tok, inv, err := a.acc.Invite(id.User.ID, oid, req.Email, req.Role)
	if err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"invitation": inv, "token": tok, "path": "/invite.html#" + tok})
}

func (a *Auth) revokeInvitation(w http.ResponseWriter, r *http.Request, id *Identity) {
	oid, err := pathID(r, "oid")
	if err != nil {
		accountError(w, err)
		return
	}
	iid, err := pathID(r, "iid")
	if err != nil {
		accountError(w, err)
		return
	}
	if err := a.acc.RevokeInvitation(id.User.ID, oid, iid); err != nil {
		accountError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type tokenRequest struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func decodeToken(w http.ResponseWriter, r *http.Request) (tokenRequest, error) {
	var req tokenRequest
	if err := decodeBody(w, r, &req); err != nil {
		return req, err
	}
	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" {
		return req, store.ErrInvitation
	}
	return req, nil
}

// lookupInvitation shows the invitation page what it's for, and who is
// signed in on this browser (if anyone), to accept it as.
func (a *Auth) lookupInvitation(w http.ResponseWriter, r *http.Request) {
	req, err := decodeToken(w, r)
	if err != nil {
		accountError(w, err)
		return
	}
	inv, err := a.acc.InvitationByToken(req.Token)
	if err != nil {
		accountError(w, err)
		return
	}
	resp := map[string]any{"org": inv.Org, "role": inv.Role, "email": inv.Email, "invited_by": inv.InvitedBy, "expires": inv.Expires}
	if id, ok := a.session(r); ok {
		resp["signed_in_as"] = id.User.Username
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *Auth) acceptInvitation(w http.ResponseWriter, r *http.Request, id *Identity) {
	req, err := decodeToken(w, r)
	if err != nil {
		accountError(w, err)
		return
	}
	m, err := a.acc.AcceptInvitation(req.Token, id.User.ID)
	if err != nil {
		accountError(w, err)
		return
	}
	if _, err := a.acc.SwitchOrg(id.key, id.User.ID, m.ID); err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// register creates an account from an invitation link and signs it in.
// Registrations count towards the IP's lockout like failed logins, since
// each costs a password hash.
func (a *Auth) register(w http.ResponseWriter, r *http.Request) {
	req, err := decodeToken(w, r)
	if err != nil {
		accountError(w, err)
		return
	}
	ip := a.clientIP(r)
	f, ok := a.reserve(ip)
	if !ok {
		accountError(w, ErrLockedOut)
		return
	}
	u, m, err := a.acc.RegisterWithInvitation(req.Token, req.Username, req.Password)
	if err != nil {
		accountError(w, err)
		return
	}
	a.release(ip, f)
	if err := a.startSession(w, r, u.ID); err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": u, "org": m})
}

func (a *Auth) declineInvitation(w http.ResponseWriter, r *http.Request) {
	req, err := decodeToken(w, r)
	if err != nil {
		accountError(w, err)
		return
	}
	if err := a.acc.DeclineInvitation(req.Token); err != nil {
		accountError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
