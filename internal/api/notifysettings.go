package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/notify"
	"github.com/klahr/dash/internal/store"
)

// The caller's notification settings in their active org (Account →
// Notifications): their daily or weekly summary and which events push to
// their browsers.
//
//	GET  /api/me/notifications                the settings, with what the page needs to show them
//	PUT  /api/me/notifications                save them (fields left out keep their value)
//	POST /api/me/notifications/test-summary   send the summary now, to the chosen destinations
//
// and GET/POST /api/notify/unsubscribe/{token}, a summary email's
// unsubscribe link (public: the signed token is the credential).
//
// They need a person: a signed-in session or one of their API tokens.

var errSummariesOff = errors.New("summaries aren't sent from this Dash (a runner: its hub sends them)")

// emailNote says why summaries can't go by email ("" = they can).
func (s *Server) emailNote(email string) string {
	switch {
	case !s.Summaries.EmailOn():
		return "Email summaries are unavailable: this Dash has no email (SMTP) set up."
	case email == "":
		return "Email summaries are unavailable: your account has no email address."
	}
	return ""
}

// notifyView is the settings page's data. A planner's is what they may
// choose: card news (all that is pushed to them).
func (s *Server) notifyView(sv *service, set model.NotifySettings, st model.NotifyState) (map[string]any, error) {
	u, err := s.Store.Accounts().User(sv.c.User)
	if err != nil {
		return nil, err
	}
	if sv.c.Planner {
		return map[string]any{
			"settings":     set,
			"state":        model.NotifyState{},
			"planner":      true,
			"push_choices": []model.NotifyEvent{},
			"push_enabled": s.Notify.PushEnabled(),
			"enabled":      false,
		}, nil
	}
	hooks, err := sv.data.ListPersonalWebhooks()
	if err != nil {
		return nil, err
	}
	type proj struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	projects := []proj{}
	for _, p := range sv.data.ListProjects() {
		projects = append(projects, proj{p.ID, p.Name})
	}
	note := s.emailNote(u.Email)
	return map[string]any{
		"settings":        set,
		"state":           st,
		"email":           u.Email,
		"email_available": note == "",
		"email_note":      note,
		"webhooks":        hooks,
		"projects":        projects,
		"push_choices":    model.PushChoices,
		"push_enabled":    s.Notify.PushEnabled(),
		"enabled":         s.Summaries != nil,
	}, nil
}

func (s *Server) getNotifySettings(w http.ResponseWriter, r *http.Request) {
	sv := s.personCaller(w, r)
	if sv == nil {
		return
	}
	set, st, err := sv.data.NotifySettings()
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.notifyView(sv, set, st)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) putNotifySettings(w http.ResponseWriter, r *http.Request) {
	sv := s.personCaller(w, r)
	if sv == nil {
		return
	}
	// Fields left out keep their value.
	set, _, err := sv.data.NotifySettings()
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := decode(r, &set); err != nil {
		writeErr(w, err)
		return
	}
	if sv.c.Planner {
		set.Summary = model.SummaryOff // a summary tells of tasks, which planners don't see
	}
	if err := set.Normalize(); err != nil {
		writeErr(w, errors.Join(errBadRequest, err))
		return
	}
	if set.On() && s.Summaries == nil {
		writeErr(w, errSummariesOff)
		return
	}
	if set.Email && set.On() {
		u, err := s.Store.Accounts().User(sv.c.User)
		if err != nil {
			writeErr(w, err)
			return
		}
		if note := s.emailNote(u.Email); note != "" {
			writeErr(w, errors.Join(errBadRequest, errors.New(note)))
			return
		}
	}
	set, st, err := sv.data.SaveNotifySettings(set, time.Now())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		err = errors.Join(errBadRequest, err)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.notifyView(sv, set, st)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// testSummary sends the caller's summary now and answers how each
// destination took it.
func (s *Server) testSummary(w http.ResponseWriter, r *http.Request) {
	sv := s.personCaller(w, r)
	if sv == nil {
		return
	}
	if s.Summaries == nil {
		writeErr(w, errSummariesOff)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res, err := s.Summaries.Test(ctx, sv.c.User, sv.c.Org)
	if errors.Is(err, notify.ErrNoDestination) {
		err = errors.Join(errBadRequest, err)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	ok := true
	for _, d := range res {
		ok = ok && d.OK
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "results": res})
}

// unsubscribeSummary serves a summary email's unsubscribe link.
func (s *Server) unsubscribeSummary(w http.ResponseWriter, r *http.Request) {
	if s.Summaries == nil {
		http.NotFound(w, r)
		return
	}
	s.Summaries.ServeHTTP(w, r)
}
