package billing

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/store"
)

// Routes adds billing's routes. With billing off (s nil), GET answers
// {"enabled": false} and the rest 404.
//
//	GET  /api/orgs/{oid}/billing            the org's billing state (members)
//	POST /api/orgs/{oid}/billing/checkout   {url}: Stripe Checkout (owners)
//	POST /api/orgs/{oid}/billing/portal     {url}: the customer portal (owners)
//	POST /api/billing/webhook               Stripe's events (signed; no session)
//
// They take a browser session, not a token: billing is for people.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/orgs/{oid}/billing", auth.RequireSession(http.HandlerFunc(s.state)))
	mux.Handle("POST /api/orgs/{oid}/billing/checkout", auth.RequireSession(s.ownerOnly(s.checkout)))
	mux.Handle("POST /api/orgs/{oid}/billing/portal", auth.RequireSession(s.ownerOnly(s.portal)))
	mux.HandleFunc("POST /api/billing/webhook", func(w http.ResponseWriter, r *http.Request) {
		if s == nil {
			http.NotFound(w, r)
			return
		}
		s.webhook(w, r)
	})
}

// member is the caller's membership of the path's org, or it answers.
func (s *Service) member(w http.ResponseWriter, r *http.Request) (store.Membership, bool) {
	id, ok := auth.From(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "login required")
		return store.Membership{}, false
	}
	oid, err := strconv.ParseInt(r.PathValue("oid"), 10, 64)
	if err != nil || oid <= 0 {
		writeError(w, http.StatusNotFound, "not found")
		return store.Membership{}, false
	}
	m, err := s.acc.Membership(id.User.ID, oid)
	if err != nil {
		writeError(w, http.StatusNotFound, store.ErrNotMember.Error())
		return store.Membership{}, false
	}
	return m, true
}

func (s *Service) state(w http.ResponseWriter, r *http.Request) {
	if s == nil {
		if _, ok := auth.From(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "login required")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	m, ok := s.member(w, r)
	if !ok {
		return
	}
	st, err := s.State(m.ID)
	if err != nil {
		slog.Error("billing state", "org", m.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "something went wrong")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		State
		CanManage bool `json:"can_manage"` // owners subscribe and manage billing
	}{st, m.Role == store.RoleOwner})
}

func (s *Service) ownerOnly(h func(http.ResponseWriter, *http.Request, store.Membership)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s == nil {
			http.NotFound(w, r)
			return
		}
		m, ok := s.member(w, r)
		if !ok {
			return
		}
		if m.Role != store.RoleOwner {
			writeError(w, http.StatusForbidden, "only the organization's owners manage its billing")
			return
		}
		h(w, r, m)
	})
}

func (s *Service) checkout(w http.ResponseWriter, r *http.Request, m store.Membership) {
	url, err := s.Checkout(r.Context(), m.ID)
	s.answerURL(w, m, url, err)
}

func (s *Service) portal(w http.ResponseWriter, r *http.Request, m store.Membership) {
	url, err := s.Portal(r.Context(), m.ID)
	s.answerURL(w, m, url, err)
}

func (s *Service) answerURL(w http.ResponseWriter, m store.Membership, url string, err error) {
	switch {
	case errors.Is(err, ErrSubscribed), errors.Is(err, ErrNoCustomer):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		slog.Error("billing: stripe", "org", m.ID, "err", err)
		writeError(w, http.StatusBadGateway, "Stripe didn't answer as expected; try again in a moment")
	default:
		writeJSON(w, http.StatusOK, map[string]string{"url": url})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
