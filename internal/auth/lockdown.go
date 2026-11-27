package auth

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/notify"
	"github.com/klahr/dash/internal/passhash"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/textutil"
)

// Incident response (see docs/breach.md):
//
//	GET    /api/admin/lockdown            platform admins: what it would touch, the notice, its emails
//	POST   /api/admin/lockdown {sessions, passwords, totp, secrets, notice, email, banner, confirm, password}
//	GET    /api/admin/lockdown/users.csv  the active accounts' names and email addresses
//	DELETE /api/admin/notice              stop showing the notice
//	GET    /api/notice                    anyone signed in: the notice they haven't dismissed
//	POST   /api/notice/{id}/dismiss       hide it
//	POST   /api/password/change {password}  a session asked for a new password chooses it
//
// The admin who runs it stays signed in in the browser they run it from,
// and their own account is left out of the new-password and two-factor
// steps (the checklist tells them to do those themselves). `dash lockdown`
// does the same with the server's database directly, leaving nobody out.

// LockdownConfirm is what the admin types to confirm a lockdown.
const LockdownConfirm = "LOCKDOWN"

var errLockdownConfirm = errors.New("type " + LockdownConfirm + " to confirm")

// codeNeedsPassword is the error code of a request refused until a new
// password is chosen.
const codeNeedsPassword = "password_change_required"

// noticeEvery is how long the notice's emails wait between one and the
// next, so the SMTP server isn't flooded.
const noticeEvery = 500 * time.Millisecond

func (a *Auth) lockdownRoutes(mux Mux) {
	mux.HandleFunc("GET /api/admin/lockdown", a.platformAdmin(a.lockdownStatus))
	mux.HandleFunc("POST /api/admin/lockdown", a.platformAdmin(a.lockdown))
	mux.HandleFunc("GET /api/admin/lockdown/users.csv", a.platformAdmin(a.lockdownUsersCSV))
	mux.HandleFunc("DELETE /api/admin/notice", a.platformAdmin(a.clearNotice))
	mux.HandleFunc("GET /api/notice", a.signedIn(a.myNotice))
	mux.HandleFunc("POST /api/notice/{id}/dismiss", a.signedIn(a.dismissNotice))
	mux.HandleFunc("POST /api/password/change", a.sessionOnly(a.changeRequiredPassword))
}

// passwordFree are the paths a session asked for a new password may use:
// who am I, sign out, and choosing it.
func passwordFree(path string) bool {
	switch path {
	case "/api/me", "/api/logout", "/api/password/change":
		return true
	}
	return public[path]
}

// needsPassword reports whether uid must choose a new password first.
func (a *Auth) needsPassword(uid int64) bool {
	need, err := a.acc.NeedsPassword(uid)
	if err != nil {
		slog.Error("needs password", "user", uid, "err", err)
	}
	return need
}

// ---- the forced password change ----

// changeRequiredPassword sets the new password of a session asked for one,
// with the rules of a reset, and lets it carry on. The account's other
// sessions, API tokens and connected apps end.
func (a *Auth) changeRequiredPassword(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	if !id.NeedsPassword {
		accountError(w, store.ErrNoPasswordChange)
		return
	}
	if len(req.Password) < store.MinPassword {
		accountError(w, store.ErrWeakPassword)
		return
	}
	if old, err := a.acc.PasswordHash(id.User.ID); err == nil {
		if same, _, _ := passhash.Verify(old, req.Password); same {
			accountError(w, store.ErrSamePassword)
			return
		}
	}
	hash, err := passhash.Hash(req.Password)
	if err != nil {
		accountError(w, err)
		return
	}
	if err := a.acc.ChangeRequiredPassword(id.User.ID, hash, id.key, a.now()); err != nil {
		accountError(w, err)
		return
	}
	a.audit(r, store.AuthEvent{UserID: id.User.ID, Actor: id.User.ID, Kind: "password.changed", Detail: "asked by incident response"})
	enrol, err := a.acc.Requires2FA(id.User.ID)
	if err != nil {
		slog.Error("change password", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "enrol_2fa": enrol && !id.User.TOTP})
}

// ---- the lockdown ----

func (a *Auth) lockdownStatus(w http.ResponseWriter, r *http.Request, id *Identity) {
	p, err := a.acc.LockdownPreview(id.key)
	if err != nil {
		accountError(w, err)
		return
	}
	events, err := a.acc.LockdownEvents(20)
	if err != nil {
		accountError(w, err)
		return
	}
	resp := map[string]any{"preview": p, "mail": a.MailOn(), "confirm": LockdownConfirm, "notice": nil, "emails": a.notices.status(), "events": events}
	if n, err := a.acc.CurrentNotice(); err == nil {
		resp["notice"] = n
	} else if !errors.Is(err, store.ErrNotFound) {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type lockdownBody struct {
	store.LockdownSteps
	Notice   string `json:"notice"`
	Email    bool   `json:"email"`  // email the notice to every active account
	Banner   bool   `json:"banner"` // show it after sign-in
	Confirm  string `json:"confirm"`
	Password string `json:"password"`
}

func (a *Auth) lockdown(w http.ResponseWriter, r *http.Request, id *Identity) {
	var req lockdownBody
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return
	}
	notice := strings.TrimSpace(req.Notice)
	switch {
	case strings.TrimSpace(req.Confirm) != LockdownConfirm:
		accountError(w, errLockdownConfirm)
		return
	case (req.Email || req.Banner) && (notice == "" || len(notice) > store.MaxNotice):
		accountError(w, store.ErrBadNotice)
		return
	case !req.Any() && !req.Email && !req.Banner:
		writeError(w, http.StatusBadRequest, "choose at least one step")
		return
	}
	if err := a.ConfirmPassword(r, req.Password); err != nil {
		accountError(w, err)
		return
	}
	resp := map[string]any{}
	res, err := a.RunLockdown(r, id.User.ID, store.Lockdown{LockdownSteps: req.LockdownSteps, Keep: id.key, Now: a.now()})
	if err != nil {
		accountError(w, err)
		return
	}
	resp["result"], resp["checklist"] = res, res.Checklist()
	if req.Banner {
		n, err := a.acc.SetNotice(notice, id.User.ID, a.now())
		if err != nil {
			accountError(w, err)
			return
		}
		a.audit(r, store.AuthEvent{Actor: id.User.ID, Kind: "admin.lockdown.banner"})
		resp["notice"] = n
	}
	if req.Email {
		if !a.MailOn() {
			resp["emails"] = map[string]any{"unavailable": true}
		} else {
			users, err := a.acc.LockdownUsers()
			if err != nil {
				accountError(w, err)
				return
			}
			resp["emails"] = a.startNoticeMailing(notice, users, id.User.ID, a.clientIP(r))
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// RunLockdown takes l's steps as actor (0: the server's operator, on the
// command line; r nil there), tells what holds revoked tokens open and
// OnLockdown, and writes each step to the audit log.
func (a *Auth) RunLockdown(r *http.Request, actor int64, l store.Lockdown) (store.LockdownResult, error) {
	if l.Secrets && l.NewVAPIDKey == nil {
		l.NewVAPIDKey = notify.NewVAPIDPrivateKey
	}
	if l.Secrets && l.NewNotifyKey == nil {
		l.NewNotifyKey = notify.NewSummaryKey
	}
	if !l.Any() {
		return store.LockdownResult{Steps: l.LockdownSteps}, nil
	}
	res, err := a.acc.Lockdown(l)
	if err != nil {
		return res, err
	}
	a.revoked(res.Revoked)
	if a.OnLockdown != nil {
		a.OnLockdown(res)
	}
	ev := func(kind, detail string) {
		if r == nil && actor == 0 {
			detail = "dash lockdown on the server: " + detail
		}
		a.audit(r, store.AuthEvent{UserID: actor, Actor: actor, Kind: "admin.lockdown." + kind, Detail: detail})
	}
	ev("run", "steps: "+l.String())
	if l.Sessions {
		ev("sessions", strings.Join([]string{
			textutil.Count(res.Sessions, "session", "sessions"),
			textutil.Count(res.APITokens, "token", "tokens"),
			textutil.Count(len(res.Runners), "runner", "runners"),
			textutil.Count(res.Invitations, "invitation", "invitations"),
			textutil.Count(res.EmailLinks, "email link", "email links"),
			textutil.Count(res.WallLinks, "wall link", "wall links"),
		}, ", "))
	}
	if l.Passwords {
		ev("passwords", textutil.Count(res.Passwords, "account", "accounts")+" asked for a new password")
	}
	if l.TOTP {
		ev("2fa", textutil.Count(res.TOTP, "account", "accounts")+" to set up two-factor sign-in again")
	}
	if l.Secrets {
		ev("secrets", strings.Join([]string{
			textutil.Count(len(res.Webhooks), "webhook", "webhooks"),
			textutil.Count(len(res.GitHubHooks), "GitHub webhook", "GitHub webhooks"),
			textutil.Count(len(res.Trackers), "board backend", "board backends"),
			textutil.Count(res.PushSubscriptions, "push subscription", "push subscriptions") + " cleared",
			"new VAPID key",
		}, ", "))
	}
	return res, nil
}

func (a *Auth) lockdownUsersCSV(w http.ResponseWriter, r *http.Request, id *Identity) {
	users, err := a.acc.LockdownUsers()
	if err != nil {
		accountError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="dash-users.csv"`)
	_ = WriteUsersCSV(w, users)
}

// WriteUsersCSV writes users' names and email addresses as CSV, for a
// notice sent another way than Dash's email.
func WriteUsersCSV(w interface{ Write([]byte) (int, error) }, users []store.User) error {
	c := csv.NewWriter(w)
	_ = c.Write([]string{"username", "email"})
	for _, u := range users {
		_ = c.Write([]string{csvCell(u.Username), csvCell(u.Email)})
	}
	c.Flush()
	return c.Error()
}

// csvCell keeps a spreadsheet from reading s as a formula.
func csvCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// ---- the notice ----

func (a *Auth) myNotice(w http.ResponseWriter, r *http.Request, id *Identity) {
	n, err := a.acc.NoticeFor(id.User.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"notice": nil})
		return
	}
	if err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notice": n})
}

func (a *Auth) dismissNotice(w http.ResponseWriter, r *http.Request, id *Identity) {
	nid, err := pathID(r, "id")
	if err == nil {
		err = a.acc.DismissNotice(nid, id.User.ID, a.now())
	}
	if err != nil {
		accountError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Auth) clearNotice(w http.ResponseWriter, r *http.Request, id *Identity) {
	if err := a.acc.ClearNotice(a.now()); err != nil {
		accountError(w, err)
		return
	}
	a.audit(r, store.AuthEvent{Actor: id.User.ID, Kind: "admin.notice.cleared"})
	w.WriteHeader(http.StatusNoContent)
}

// noticeMailing is the notice's emails going out (or the last that did).
type noticeMailing struct {
	mu  sync.Mutex
	cur *NoticeEmails
}

// NoticeEmails is how the notice's emails are going.
type NoticeEmails struct {
	Total    int       `json:"total"`
	Sent     int       `json:"sent"`
	Failed   int       `json:"failed"`
	Done     bool      `json:"done"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitzero"`
}

func (m *noticeMailing) status() *NoticeEmails {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return nil
	}
	c := *m.cur
	return &c
}

func (m *noticeMailing) update(f func(*NoticeEmails)) {
	m.mu.Lock()
	f(m.cur)
	m.mu.Unlock()
}

// startNoticeMailing emails body to users in the background, one at a
// time, and returns how it starts.
func (a *Auth) startNoticeMailing(body string, users []store.User, actor int64, ip string) *NoticeEmails {
	a.notices.mu.Lock()
	a.notices.cur = &NoticeEmails{Total: len(users), Started: a.now()}
	start := *a.notices.cur
	a.notices.mu.Unlock()
	go func() {
		sent, failed := a.MailNotice(context.Background(), body, users, func(sent, failed int) {
			a.notices.update(func(e *NoticeEmails) { e.Sent, e.Failed = sent, failed })
		})
		a.notices.update(func(e *NoticeEmails) { e.Done, e.Finished = true, a.now() })
		a.audit(nil, store.AuthEvent{Actor: actor, Kind: "admin.lockdown.emailed", IP: ip,
			Detail: fmt.Sprintf("notice emailed: %d sent, %d failed of %d", sent, failed, len(users))})
	}()
	return &start
}

// MailNotice emails body to each of users, one every NoticeEvery, telling
// progress after each, and reports how many were sent and failed. It
// stops early when ctx ends.
func (a *Auth) MailNotice(ctx context.Context, body string, users []store.User, progress func(sent, failed int)) (sent, failed int) {
	every := a.NoticeEvery
	if every <= 0 {
		every = noticeEvery
	}
	name := a.brand().Name
	var paras []string
	for _, p := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			paras = append(paras, p)
		}
	}
	for i, u := range users {
		if i > 0 {
			select {
			case <-ctx.Done():
				return sent, failed
			case <-time.After(every):
			}
		}
		n := a.notice("A message from the administrators of "+name, "Hello "+u.Username+",")
		n.Before = paras
		where := name
		if a.baseURL != "" {
			where += " at " + a.baseURL
		}
		n.After = []string{"You'll see this message when you next sign in to " + where + " too."}
		if err := a.mail.Send(ctx, n.Message(u.Email, "A message from the administrators of "+name)); err != nil {
			failed++
			slog.Warn("email the notice", "user", u.Username, "err", err)
		} else {
			sent++
		}
		if progress != nil {
			progress(sent, failed)
		}
	}
	return sent, failed
}
