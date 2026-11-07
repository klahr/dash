package api

import (
	"net/http"
	"strings"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/billing"
)

// accountPaths are the API paths that still change things in an org whose
// board is read-only for want of a subscription: signing in and out, the
// account, the org's members and invitations (removing members down to one
// makes it free again), tokens, and billing itself.
var accountPaths = []string{
	"/api/login", "/api/logout", "/api/auth/", "/api/account/", "/api/orgs", "/api/invitations/",
	"/api/tokens", "/api/signup", "/api/password/", "/api/billing/",
	// Answering Claude's permission prompts only lets work already running end.
	"/api/permissions/",
}

func accountPath(p string) bool {
	for _, a := range accountPaths {
		if p == a || strings.HasPrefix(p, a) && (strings.HasSuffix(a, "/") || p[len(a)] == '/') {
			return true
		}
	}
	return false
}

// billingGate makes the board of an org that needs a subscription it
// doesn't have read-only: its API answers 402 to every change except the
// account paths. Reading works as always, and nothing is deleted. Another
// Dash's calls (a hub over a runner's tunnel) aren't an org's and pass.
func (s *Server) billingGate(next http.Handler) http.Handler {
	if s.Billing == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") || accountPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if id, ok := auth.From(r.Context()); ok && id.Org != nil && s.Billing.ReadOnly(id.Org.ID) {
			writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": billing.ErrReadOnly.Error(), "code": "billing_read_only"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writable is billing.ErrReadOnly while the caller's org's board is
// read-only, for the changes that don't come through the REST API: MCP
// tools and chat turns.
func (sv *service) writable() error {
	if sv.c.Machine || !sv.s.Billing.ReadOnly(sv.c.Org) {
		return nil
	}
	return billing.ErrReadOnly
}
