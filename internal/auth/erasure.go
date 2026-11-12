package auth

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/klahr/dash/internal/store"
)

// One's own data (see docs/privacy.md):
//
//	GET    /api/account/export                    everything Dash keeps about the user, as a JSON file
//	DELETE /api/account {password}                delete the user's own account
//	DELETE /api/admin/users/{uid} {password}      platform admins: delete someone else's
//
// Deleting refuses, saying why, while an org depends on the account (see
// store.Accounts.DeleteUser).

func (a *Auth) erasureRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/account/export", a.sessionOnly(a.exportAccount))
	mux.HandleFunc("DELETE /api/account", a.sessionOnly(a.deleteOwnAccount))
	mux.HandleFunc("DELETE /api/admin/users/{uid}", a.sessionOnly(a.adminDeleteUser))
}

func (a *Auth) exportAccount(w http.ResponseWriter, r *http.Request, id *Identity) {
	x, err := a.acc.ExportUser(id.User.ID, a.now())
	if err != nil {
		accountError(w, err)
		return
	}
	name := fmt.Sprintf("dash-account-%s-%s.json", store.FolderSlug(id.User.Username), x.ExportedAt.Format("2006-01-02"))
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, x)
}

func (a *Auth) deleteOwnAccount(w http.ResponseWriter, r *http.Request, id *Identity) {
	if !a.confirmDelete(w, r) {
		return
	}
	if !a.deleteUser(w, r, id.User.ID, 0) {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: a.secureCookie(r), SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (a *Auth) adminDeleteUser(w http.ResponseWriter, r *http.Request, id *Identity) {
	if !id.User.PlatformAdmin {
		writeError(w, http.StatusForbidden, errAdminsOnly.Error())
		return
	}
	uid, err := pathID(r, "uid")
	if err != nil {
		accountError(w, err)
		return
	}
	if uid == id.User.ID {
		writeError(w, http.StatusConflict, "to delete your own account, use Account → Your data")
		return
	}
	if !a.confirmDelete(w, r) {
		return
	}
	if a.deleteUser(w, r, uid, id.User.ID) {
		w.WriteHeader(http.StatusNoContent)
	}
}

// confirmDelete checks the request's password (or recent Google sign-in).
func (a *Auth) confirmDelete(w http.ResponseWriter, r *http.Request) bool {
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		accountError(w, err)
		return false
	}
	if err := a.ConfirmPassword(r, req.Password); err != nil {
		accountError(w, err)
		return false
	}
	return true
}

// deleteUser deletes uid, as actor (0: themselves), and tells what holds
// their tokens open and the orgs they left. It answers the error itself.
func (a *Auth) deleteUser(w http.ResponseWriter, r *http.Request, uid, actor int64) bool {
	d, err := a.acc.DeleteUser(uid)
	if err != nil {
		accountError(w, err)
		return false
	}
	slog.Info("account deleted", "user", uid, "by", actor, "orgs_deleted", len(d.DeletedOrgs))
	if a.OnRevoke != nil {
		for _, t := range d.Tokens {
			a.OnRevoke(t)
		}
	}
	for _, org := range d.Orgs {
		// Who it was goes with the account: the log says that it happened.
		a.audit(nil, store.AuthEvent{OrgID: org, Actor: actor, Kind: "account.deleted"})
		a.membersChanged(org)
	}
	return true
}

// auditPruneEvery is how often audit deletes entries older than
// AuditRetention.
const auditPruneEvery = time.Hour

// pruneAudit deletes old sign-in log entries, at most once an hour.
func (a *Auth) pruneAudit(now time.Time) {
	if a.AuditRetention <= 0 {
		return
	}
	a.mu.Lock()
	due := now.Sub(a.auditPruned) >= auditPruneEvery
	if due {
		a.auditPruned = now
	}
	a.mu.Unlock()
	if !due {
		return
	}
	if n, err := a.acc.PruneAuthEvents(now.Add(-a.AuditRetention)); err != nil {
		slog.Error("prune sign-in log", "err", err)
	} else if n > 0 {
		slog.Info("pruned sign-in log", "entries", n)
	}
}
