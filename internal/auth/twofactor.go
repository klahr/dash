package auth

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/klahr/dash/internal/qr"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/totp"
)

// Two-factor sign-in: a code from an authenticator app (TOTP) after the
// password, or one of ten single-use recovery codes instead.
//
//	POST  /api/login/totp {ticket, code}           the second step of /api/login
//	GET   /api/account/2fa                         on?, recovery codes left, required?
//	POST  /api/account/2fa/setup                   a new secret, its otpauth URI and QR code
//	POST  /api/account/2fa/enable {code}           confirm it; returns the recovery codes
//	POST  /api/account/2fa/disable {password}
//	POST  /api/account/2fa/recovery-codes {password}  new codes, the old ones stop working
//	PATCH /api/orgs/{oid} {require_2fa, password}  owners: require it of every member
//
// Wrong codes count towards the same per-address lockout as wrong passwords,
// and also towards one per account, so guessing from many addresses with a
// stolen password is locked out too.

const (
	ticketTTL     = 5 * time.Minute
	recoveryCount = 10
)

// A loginTicket stands for a password that was right, until the second
// factor comes (or it expires, or too many codes were wrong).
type loginTicket struct {
	user    int64
	expires time.Time
	tries   int
	method  string // how the first step was made (store.MethodPassword, store.MethodGoogle)
	org     int64  // the org to sign in to (0: their first)
	link    string // a Google identity to link once signed in (redeemLink)
}

var (
	errBadCode       = errors.New("that code isn't right")
	errTicketExpired = errors.New("sign-in timed out; enter your password again")
)

func (a *Auth) twoFactorRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/login/totp", a.loginTOTP)
	// The second factor is a credential and requiring it is managing the
	// org: a token may do neither.
	mux.HandleFunc("GET /api/account/2fa", a.sessionOnly(a.twoFactorStatus))
	mux.HandleFunc("POST /api/account/2fa/setup", a.sessionOnly(a.setupTOTP))
	mux.HandleFunc("POST /api/account/2fa/enable", a.sessionOnly(a.enableTOTP))
	mux.HandleFunc("POST /api/account/2fa/disable", a.sessionOnly(a.disableTOTP))
	mux.HandleFunc("POST /api/account/2fa/recovery-codes", a.sessionOnly(a.newRecoveryCodes))
	mux.HandleFunc("PATCH /api/orgs/{oid}", a.sessionOnly(a.updateOrg))
}

// twoFactorFree are the paths someone who must still set up two-factor
// sign-in may use: those of someone with no org, and setting it up.
func twoFactorFree(path string) bool {
	return orgFree(path) || strings.HasPrefix(path, "/api/account/2fa") || public[path]
}

// newTicketFor keeps t, waiting for its second factor, and returns its token.
func (a *Auth) newTicketFor(t *loginTicket) string {
	tok := randomToken()
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, t := range a.tickets {
		if now.After(t.expires) {
			delete(a.tickets, k)
		}
	}
	t.expires = now.Add(ticketTTL)
	a.tickets[sessionKey(tok)] = t
	return tok
}

func (a *Auth) ticket(tok string) (*loginTicket, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.tickets[sessionKey(tok)]
	if ok && a.now().After(t.expires) {
		delete(a.tickets, sessionKey(tok))
		return nil, false
	}
	return t, ok
}

// failedTicket counts a wrong code against tok, dropping it after as many
// as the lockout allows.
func (a *Auth) failedTicket(tok string, t *loginTicket) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t.tries++; t.tries >= a.lockout().PerIP {
		delete(a.tickets, sessionKey(tok))
	}
}

func (a *Auth) dropTicket(tok string) {
	a.mu.Lock()
	delete(a.tickets, sessionKey(tok))
	a.mu.Unlock()
}

func userKey(uid int64) string { return "2fa:" + strconv.FormatInt(uid, 10) }

// secondFactor checks code, a TOTP code or a recovery code, for uid from ip.
// It reports whether a recovery code was used. Failures count towards ip's
// and uid's lockouts.
func (a *Auth) secondFactor(ip string, uid int64, code string) (recovery bool, err error) {
	fip, ok := a.reserve(ip)
	if !ok {
		return false, errLockedOut
	}
	fu, ok := a.reserveLocal(userKey(uid))
	if !ok {
		a.release(fip)
		return false, errLockedOut
	}
	ok, recovery, err = a.checkCode(uid, code)
	if err != nil {
		a.release(fip)
		a.release(fu)
		return false, err
	}
	if !ok {
		a.failedAttemptWait()
		return false, errBadCode
	}
	a.release(fip)
	a.mu.Lock()
	delete(a.failed, userKey(uid))
	a.mu.Unlock()
	return recovery, nil
}

func (a *Auth) checkCode(uid int64, code string) (ok, recovery bool, err error) {
	c := totp.Clean(code)
	if totp.LooksLikeCode(c) {
		secret, err := a.acc.TOTPSecret(uid)
		if errors.Is(err, store.ErrTOTPNotSetUp) {
			return false, false, nil
		}
		if err != nil {
			return false, false, err
		}
		step, ok := totp.Verify(secret, c, a.now())
		if !ok {
			return false, false, nil
		}
		// Each code works once, even within its 30 seconds.
		ok, err = a.acc.UseTOTPStep(uid, step)
		return ok, false, err
	}
	c = strings.ToLower(c)
	if len(c) != recoveryLen {
		return false, false, nil
	}
	ok, err = a.acc.UseRecoveryCode(uid, c)
	return ok, ok, err
}

const recoveryLen = 16 // base32 characters: 80 bits

var recoveryEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// newRecoveryCodes returns recoveryCount codes as stored (16 lower-case
// base32 characters) and as shown (in groups of four).
func newRecoveryCodes() (stored, shown []string) {
	for range recoveryCount {
		b := make([]byte, 10)
		_, _ = rand.Read(b)
		c := strings.ToLower(recoveryEnc.EncodeToString(b))
		stored = append(stored, c)
		shown = append(shown, c[0:4]+"-"+c[4:8]+"-"+c[8:12]+"-"+c[12:16])
	}
	return stored, shown
}

// loginTOTP handles POST /api/login/totp {ticket, code}, the second step of
// signing in to an account with two-factor sign-in.
func (a *Auth) loginTOTP(w http.ResponseWriter, r *http.Request) {
	ip := a.clientIP(r)
	if a.lockedOut(ip) {
		writeError(w, http.StatusTooManyRequests, errLockedOut.Error())
		return
	}
	var req struct {
		Ticket string `json:"ticket"`
		Code   string `json:"code"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	t, ok := a.ticket(req.Ticket)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": errTicketExpired.Error(), "code": "ticket_expired"})
		return
	}
	recovery, err := a.secondFactor(ip, t.user, req.Code)
	switch {
	case errors.Is(err, errLockedOut):
		a.dropTicket(req.Ticket)
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	case errors.Is(err, errBadCode):
		a.failedTicket(req.Ticket, t)
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	case err != nil:
		slog.Error("login second factor", "err", err)
		writeError(w, http.StatusInternalServerError, "sign-in failed")
		return
	}
	a.dropTicket(req.Ticket)
	a.signedInFrom(ip)
	method := t.method
	if method == "" {
		method = store.MethodPassword
	}
	if err := a.startSessionVia(w, r, t.user, t.org, newFamily(), method); err != nil {
		slog.Error("create session", "err", err)
		writeError(w, http.StatusInternalServerError, "sign-in failed")
		return
	}
	resp := map[string]any{"ok": true}
	if method == store.MethodGoogle {
		a.audit(r, store.AuthEvent{OrgID: t.org, UserID: t.user, Kind: "signin.google", Detail: "with two-factor code"})
	}
	if a.redeemLink(r, t.link, t.user) {
		resp["google_linked"] = true
	}
	if recovery {
		left, _ := a.acc.RecoveryCodesLeft(t.user)
		resp["recovery_used"], resp["recovery_codes_left"] = true, left
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *Auth) twoFactorStatus(w http.ResponseWriter, r *http.Request, id *Identity) {
	left, err := a.acc.RecoveryCodesLeft(id.User.ID)
	if err != nil {
		accountError(w, err)
		return
	}
	required, err := a.acc.Requires2FA(id.User.ID)
	if err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": id.User.TOTP, "recovery_codes_left": left, "required": required})
}

// setupTOTP starts enrolment: a new secret, shown as a QR code (and as text
// for typing in), which works once confirmed with a code from it.
func (a *Auth) setupTOTP(w http.ResponseWriter, r *http.Request, id *Identity) {
	secret := totp.NewSecret()
	if err := a.acc.BeginTOTP(id.User.ID, secret); err != nil {
		accountError(w, err)
		return
	}
	account := id.User.Username
	if id.User.Email != "" {
		account = id.User.Email
	}
	uri := totp.URI(a.brand().Name, account, secret) // the issuer authenticator apps show
	png, err := qr.PNG(uri)
	if err != nil {
		accountError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{
		"secret": secret,
		"uri":    uri,
		"qr":     "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
	})
}

// enableTOTP confirms enrolment with a code from the app and hands out the
// recovery codes, the only time they are shown.
func (a *Auth) enableTOTP(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	f, ok := a.reserveLocal(a.clientIP(r)) // signed in already: per address only
	if !ok {
		accountError(w, ErrLockedOut)
		return
	}
	secret, err := a.acc.PendingTOTP(id.User.ID)
	if err != nil {
		a.release(f)
		accountError(w, err)
		return
	}
	step, ok := totp.Verify(secret, req.Code, a.now())
	if !ok {
		a.failedAttemptWait()
		writeError(w, http.StatusBadRequest, errBadCode.Error()+"; check the time on your phone and try the next one")
		return
	}
	a.release(f)
	stored, shown := newRecoveryCodes()
	if err := a.acc.ConfirmTOTP(id.User.ID, secret, step, stored); err != nil {
		accountError(w, err)
		return
	}
	slog.Info("two-factor sign-in turned on", "user", id.User.Username)
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": shown})
}

func (a *Auth) disableTOTP(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	// Refuse before the password if an org requires it, so it costs no try.
	if required, err := a.acc.Requires2FA(id.User.ID); err != nil || required {
		if err == nil {
			err = store.ErrTOTPRequired
		}
		accountError(w, err)
		return
	}
	if err := a.ConfirmPassword(r, req.Password); err != nil {
		accountError(w, err)
		return
	}
	if err := a.acc.DisableTOTP(id.User.ID); err != nil {
		accountError(w, err)
		return
	}
	slog.Info("two-factor sign-in turned off", "user", id.User.Username)
	w.WriteHeader(http.StatusNoContent)
}

func (a *Auth) newRecoveryCodes(w http.ResponseWriter, r *http.Request, id *Identity) {
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
	stored, shown := newRecoveryCodes()
	if err := a.acc.ReplaceRecoveryCodes(id.User.ID, stored); err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": shown})
}

// updateOrg handles PATCH /api/orgs/{oid} {require_2fa, slug, password}:
// owners require two-factor sign-in of every member (members without it are
// asked to set it up at their next request), and rename the org's slug, its
// subdomain with org subdomains on (the old one redirects for a while; see
// store.SetOrgSlug).
func (a *Auth) updateOrg(w http.ResponseWriter, r *http.Request, id *Identity) {
	oid, err := pathID(r, "oid")
	if err != nil {
		accountError(w, err)
		return
	}
	var req struct {
		Require2FA *bool   `json:"require_2fa"`
		Slug       *string `json:"slug"`
		Password   string  `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	if req.Require2FA == nil && req.Slug == nil {
		accountError(w, errBadBody)
		return
	}
	// Check the role (and the owner's own 2FA) before the password, so it
	// costs no try.
	mine, err := a.acc.Membership(id.User.ID, oid)
	if err != nil {
		accountError(w, err)
		return
	}
	if mine.Role != store.RoleOwner {
		accountError(w, store.ErrForbidden)
		return
	}
	if req.Require2FA != nil && *req.Require2FA && !id.User.TOTP {
		accountError(w, store.ErrTOTPSelf)
		return
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
	o := mine.Org
	if req.Slug != nil {
		if o, err = a.acc.SetOrgSlug(id.User.ID, oid, *req.Slug, a.now()); err != nil {
			accountError(w, err)
			return
		}
	}
	if req.Require2FA != nil {
		if o, err = a.acc.SetRequire2FA(id.User.ID, oid, *req.Require2FA); err != nil {
			accountError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, o)
}
