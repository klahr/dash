package billing

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/httpjson"
	"github.com/klahr/dash/internal/store"
)

// Mux is where Routes registers its routes (see auth.Mux).
type Mux interface {
	Handle(pattern string, handler http.Handler)
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

// Routes adds billing's routes. With billing off (s nil), GET answers
// {"enabled": false} and the rest 404.
//
//	GET  /api/orgs/{oid}/billing            the org's billing state (members)
//	POST /api/orgs/{oid}/billing/checkout   {url}: Stripe Checkout (owners)
//	POST /api/orgs/{oid}/billing/portal     {url}: the customer portal (owners)
//	POST /api/billing/webhook               Stripe's events (signed; no session)
//
//	GET    /api/admin/orgs/{oid}/billing    platform admins: the org's free override and state
//	PUT    /api/admin/orgs/{oid}/billing    platform admins: {free_forever, free_until, note}
//	DELETE /api/admin/orgs/{oid}/billing    platform admins: clear the override
//
// They take a browser session, not a token: billing is for people.
func (s *Service) Routes(mux Mux) {
	mux.Handle("GET /api/orgs/{oid}/billing", auth.RequireSession(http.HandlerFunc(s.state)))
	mux.Handle("POST /api/orgs/{oid}/billing/checkout", auth.RequireSession(s.ownerOnly(s.checkout)))
	mux.Handle("POST /api/orgs/{oid}/billing/portal", auth.RequireSession(s.ownerOnly(s.portal)))
	mux.Handle("GET /api/admin/orgs/{oid}/billing", auth.RequireSession(s.adminOnly(s.getOverride)))
	mux.Handle("PUT /api/admin/orgs/{oid}/billing", auth.RequireSession(s.adminOnly(s.putOverride)))
	mux.Handle("DELETE /api/admin/orgs/{oid}/billing", auth.RequireSession(s.adminOnly(s.deleteOverride)))
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

// adminOnly lets only platform admins through to h, with the path's org
// (which they needn't be a member of).
func (s *Service) adminOnly(h func(http.ResponseWriter, *http.Request, int64, store.User)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s == nil {
			http.NotFound(w, r)
			return
		}
		id, ok := auth.From(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "login required")
			return
		}
		if !id.User.PlatformAdmin {
			writeError(w, http.StatusForbidden, "only platform admins can make an organization free")
			return
		}
		oid, err := strconv.ParseInt(r.PathValue("oid"), 10, 64)
		if err != nil || oid <= 0 {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		h(w, r, oid, id.User)
	})
}

// overrideView is what the admin routes answer: the override, and the
// org's billing state with it.
type overrideView struct {
	Override
	State State `json:"state"`
}

func (s *Service) answerOverride(w http.ResponseWriter, org int64, o Override, err error) {
	if err == nil {
		var st State
		if st, err = s.State(org); err == nil {
			writeJSON(w, http.StatusOK, overrideView{o, st})
			return
		}
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such organization")
	case errors.Is(err, ErrFreeOverride):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		slog.Error("billing override", "org", org, "err", err)
		writeError(w, http.StatusInternalServerError, "something went wrong")
	}
}

func (s *Service) getOverride(w http.ResponseWriter, r *http.Request, org int64, _ store.User) {
	o, err := s.Override(org)
	s.answerOverride(w, org, o, err)
}

func (s *Service) putOverride(w http.ResponseWriter, r *http.Request, org int64, u store.User) {
	var req struct {
		FreeForever bool       `json:"free_forever"`
		FreeUntil   *time.Time `json:"free_until"`
		Note        string     `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	var until time.Time
	if req.FreeUntil != nil {
		until = *req.FreeUntil
	}
	if _, err := s.acc.OrgName(org); err != nil {
		s.answerOverride(w, org, Override{}, err)
		return
	}
	o, err := s.SetFree(org, req.FreeForever, until, req.Note, u.Username)
	s.answerOverride(w, org, o, err)
}

func (s *Service) deleteOverride(w http.ResponseWriter, r *http.Request, org int64, u store.User) {
	o, err := s.ClearFree(org, u.Username)
	s.answerOverride(w, org, o, err)
}

var (
	writeJSON  = httpjson.Write
	writeError = httpjson.Error
)
