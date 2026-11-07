package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/klahr/dash/internal/mail"
	"github.com/klahr/dash/internal/store"
)

// Open signup and password reset by email. Both are off unless the hub is
// given a mailer (SetMail); signup also needs EnableSignup. Every answer that
// takes an email address is the same whether or not it has an account: what
// differs goes to that address, by email.
//
//	GET  /api/signup                     {open, password_reset}: what the login page offers
//	POST /api/signup {email, password}   an unverified account, and a link to confirm it
//	POST /api/signup/resend {email}      the confirmation link again
//	POST /api/signup/verify {token}      confirm, get a personal workspace, sign in
//	POST /api/password/forgot {email}    a link to choose a new password
//	POST /api/password/reset {token, password}

// Rate limits per client IP and per email address, for each kind of request
// that sends mail.
const (
	limitWindow   = time.Hour
	limitPerIP    = 10
	limitPerEmail = 3
)

// ErrSignupNoMail is why EnableSignup refuses: nothing would deliver the
// confirmation links.
var ErrSignupNoMail = errors.New("open signup needs email: configure SMTP (DASH_SMTP_HOST, DASH_SMTP_FROM, …) and a public URL (DASH_PUBLIC_URL)")

var (
	errSignupClosed = errors.New("signup isn't open on this Dash; ask an organization's owner for an invitation")
	errNoMail       = errors.New("this Dash can't send email; ask its operator to reset your password")
	errRateLimited  = errors.New("too many requests, try again later")
	errUnverified   = store.ErrUnverified
)

// SetMail lets Dash send email through sender, with links to baseURL (the
// public origin, e.g. https://dash.example.com; never taken from a request's
// Host header, which the client chooses). It turns on password reset.
func (a *Auth) SetMail(sender mail.Sender, baseURL string) {
	a.mail, a.baseURL = sender, strings.TrimRight(baseURL, "/")
}

// EnableSignup opens self-service registration. It needs SetMail first.
func (a *Auth) EnableSignup() error {
	if a.mail == nil || a.baseURL == "" {
		return ErrSignupNoMail
	}
	a.signupOpen = true
	return nil
}

// SignupOpen reports whether anyone may sign up. With it on, every user gets
// a personal workspace and may create orgs.
func (a *Auth) SignupOpen() bool { return a.signupOpen }

func (a *Auth) signupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/signup", a.signupStatus)
	mux.HandleFunc("POST /api/signup", a.signUp)
	mux.HandleFunc("POST /api/signup/resend", a.resendVerification)
	mux.HandleFunc("POST /api/signup/verify", a.verifyEmail)
	mux.HandleFunc("POST /api/password/forgot", a.forgotPassword)
	mux.HandleFunc("POST /api/password/reset", a.resetPassword)
}

// ---- rate limits ----

// limited counts a request towards key's limit of max per limitWindow and
// reports whether it's over.
func (a *Auth) limited(key string, max int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if len(a.limits) > 10000 { // forget windows that have passed
		for k, f := range a.limits {
			if now.Sub(f.first) > limitWindow {
				delete(a.limits, k)
			}
		}
	}
	f, ok := a.limits[key]
	if !ok || now.Sub(f.first) > limitWindow {
		f = &failures{first: now}
		a.limits[key] = f
	}
	if f.count >= max {
		return true
	}
	f.count++
	return false
}

// rateLimit checks kind's limits for r's address and for email (lower-cased,
// as addresses are matched).
func (a *Auth) rateLimit(r *http.Request, kind, email string) bool {
	return a.limited(kind+"-ip:"+a.clientIP(r), limitPerIP) ||
		a.limited(kind+"-email:"+strings.ToLower(email), limitPerEmail)
}

// ---- mail ----

// send delivers m in the background, so how long an answer takes doesn't
// tell whether an email went out.
func (a *Auth) send(m mail.Message) {
	sender := a.mail
	go func() {
		if err := sender.Send(context.Background(), m); err != nil {
			slog.Error("send email", "subject", m.Subject, "err", err) // not the body: it holds the link
		}
	}()
}

func (a *Auth) link(fragment string) string { return a.baseURL + "/login.html#" + fragment }

func (a *Auth) sendVerification(u store.User, tok string) {
	a.send(mail.Message{To: u.Email, Subject: "Confirm your email address for Dash", Body: "Hello,\n\n" +
		"Someone, hopefully you, signed up for Dash at " + a.baseURL + " with this address.\n" +
		"Open this link within 24 hours to confirm it and sign in:\n\n" +
		a.link("verify="+tok) + "\n\n" +
		"If it wasn't you, ignore this email: no account is made without the link.\n"})
}

func (a *Auth) sendAlreadyRegistered(u store.User) {
	body := "Hello,\n\n" +
		"Someone, hopefully you, tried to sign up for Dash at " + a.baseURL + " with this address, " +
		"but it has an account already (username " + u.Username + ").\n\n" +
		"Sign in here: " + a.baseURL + "/login.html\n"
	body += "Forgot your password? Choose a new one here: " + a.link("forgot") + "\n"
	body += "\nIf it wasn't you, ignore this email; nothing has changed.\n"
	a.send(mail.Message{To: u.Email, Subject: "You already have a Dash account", Body: body})
}

func (a *Auth) sendReset(u store.User, tok string) {
	a.send(mail.Message{To: u.Email, Subject: "Reset your Dash password", Body: "Hello " + u.Username + ",\n\n" +
		"Someone, hopefully you, asked to reset the password of your Dash account at " + a.baseURL + ".\n" +
		"Open this link within an hour to choose a new one:\n\n" +
		a.link("reset="+tok) + "\n\n" +
		"Choosing a new password signs you out everywhere and stops your API tokens and connected apps.\n" +
		"If it wasn't you, ignore this email: your password stays as it is.\n"})
}

// ---- handlers ----

func (a *Auth) signupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"open": a.signupOpen, "password_reset": a.mail != nil && a.baseURL != ""})
}

// accepted is the one answer to every request that takes an email address.
func accepted(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": msg})
}

const (
	checkInbox = "If you can sign up with that address, we've sent it a link to confirm it. Check your inbox."
	resetSent  = "If that address belongs to an account, we've sent it a link to choose a new password. Check your inbox."
)

func (a *Auth) signUp(w http.ResponseWriter, r *http.Request) {
	if !a.signupOpen {
		writeError(w, http.StatusNotFound, errSignupClosed.Error())
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	// Checks that don't depend on any account first, then the limits.
	email, err := store.CleanEmail(req.Email)
	if err != nil {
		accountError(w, err)
		return
	}
	if len(req.Password) < store.MinPassword {
		accountError(w, store.ErrWeakPassword)
		return
	}
	if a.rateLimit(r, "signup", email) {
		writeError(w, http.StatusTooManyRequests, errRateLimited.Error())
		return
	}
	res, err := a.acc.SignUp(email, req.Password, a.now())
	if err != nil {
		accountError(w, err)
		return
	}
	switch {
	case res.Created, res.Pending:
		a.sendVerification(res.User, res.Token)
	case res.Existing:
		a.sendAlreadyRegistered(res.User)
	}
	accepted(w, checkInbox)
}

func (a *Auth) resendVerification(w http.ResponseWriter, r *http.Request) {
	if !a.signupOpen {
		writeError(w, http.StatusNotFound, errSignupClosed.Error())
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	email, err := store.CleanEmail(req.Email)
	if err != nil {
		accountError(w, err)
		return
	}
	if a.rateLimit(r, "signup", email) {
		writeError(w, http.StatusTooManyRequests, errRateLimited.Error())
		return
	}
	u, tok, ok, err := a.acc.ResendVerification(email, a.now())
	if err != nil {
		accountError(w, err)
		return
	}
	if ok {
		a.sendVerification(u, tok)
	}
	accepted(w, checkInbox)
}

// verifyEmail confirms an address and signs its new account in. It works
// after signup is closed again too: the account was made while it was open.
func (a *Auth) verifyEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	u, err := a.acc.VerifyEmail(strings.TrimSpace(req.Token), a.now())
	if errors.Is(err, store.ErrEmailLink) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		accountError(w, err)
		return
	}
	// A new account has no second factor yet, nor an org requiring one.
	if err := a.startSession(w, r, u.ID); err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": u})
}

func (a *Auth) forgotPassword(w http.ResponseWriter, r *http.Request) {
	if a.mail == nil || a.baseURL == "" {
		writeError(w, http.StatusNotFound, errNoMail.Error())
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	email, err := store.CleanEmail(req.Email)
	if err != nil {
		accountError(w, err)
		return
	}
	if a.rateLimit(r, "reset", email) {
		writeError(w, http.StatusTooManyRequests, errRateLimited.Error())
		return
	}
	u, tok, ok, err := a.acc.RequestPasswordReset(email, a.now())
	if err != nil {
		accountError(w, err)
		return
	}
	if ok {
		a.sendReset(u, tok)
	}
	accepted(w, resetSent)
}

// resetPassword sets a new password from a reset link. It doesn't sign in:
// the account may have two-factor sign-in, which the link doesn't replace.
// Tries count towards the address's lockout, since each costs a hash.
func (a *Auth) resetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	ip := a.clientIP(r)
	f, ok := a.reserve(ip)
	if !ok {
		accountError(w, ErrLockedOut)
		return
	}
	u, err := a.acc.ResetPassword(strings.TrimSpace(req.Token), req.Password, a.now())
	if errors.Is(err, store.ErrEmailLink) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		if errors.Is(err, store.ErrWeakPassword) {
			a.release(ip, f)
		}
		accountError(w, err)
		return
	}
	a.release(ip, f)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "username": u.Username})
}

// ensurePersonal gives uid their personal workspace, if signup is open and
// they have none (accounts from before signup, or from invitations).
func (a *Auth) ensurePersonal(uid int64) {
	if !a.signupOpen {
		return
	}
	if _, err := a.acc.EnsurePersonalOrg(uid); err != nil {
		slog.Error("personal workspace", "user", uid, "err", err)
	}
}
