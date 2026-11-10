package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tracker"
)

// A project's board backend (internal/tracker), set up in its settings:
//
//	GET  /api/projects/{pid}/tracker                 the backend, its sync status and the backends to pick from
//	PUT  /api/projects/{pid}/tracker                 save it; switching boards may answer 409 asking for a decision
//	POST /api/projects/{pid}/tracker/test            try settings (or the saved ones) without saving
//	POST /api/projects/{pid}/tracker/sync            sync now
//	POST /api/projects/{pid}/tracker/webhook-secret  new webhook secret, answered once
//	POST /api/projects/{pid}/tracker/webhook         the tracker's webhook (public: the project's secret is the credential)
//
// Credentials are write-only: no answer ever carries them (nor the webhook
// secret, but once when it is made).

var errTrackersOff = errors.New("board backends aren't available on this Dash")

// trackerInput is a backend's settings as the UI sends them. Credentials
// are merged into the saved ones of the same backend: a key with an empty
// value removes it, a missing key keeps it; ClearCredentials drops them all.
type trackerInput struct {
	Backend          string                      `json:"backend"`
	Config           json.RawMessage             `json:"config"`
	StatusMap        map[model.TodoStatus]string `json:"status_map"`
	Credentials      map[string]string           `json:"credentials"`
	ClearCredentials bool                        `json:"clear_credentials"`
	// The switch's decisions (see tracker.SwitchOptions).
	Export  *bool `json:"export"`
	Confirm bool  `json:"confirm"`
}

// config is in applied to saved, the project's current backend.
func (in trackerInput) config(saved model.TrackerConfig) (model.TrackerConfig, error) {
	next := saved
	next.Backend = strings.TrimSpace(in.Backend)
	if next.Backend == "" {
		next.Backend = tracker.Native
	}
	if _, ok := tracker.Lookup(next.Backend); !ok {
		return next, fmt.Errorf("%w: unknown board backend %q", errBadRequest, next.Backend)
	}
	if in.Config != nil {
		if !json.Valid(in.Config) {
			return next, fmt.Errorf("%w: config isn't JSON", errBadRequest)
		}
		next.Config = in.Config
	}
	if in.StatusMap != nil {
		m := map[model.TodoStatus]string{}
		for s, col := range in.StatusMap {
			if !slices.Contains(model.Statuses, s) {
				return next, fmt.Errorf("%w: no status %q", errBadRequest, s)
			}
			if col = strings.TrimSpace(col); col != "" {
				m[s] = col
			}
		}
		next.StatusMap = m
	}
	creds := map[string]string{}
	if next.Backend == saved.Backend && !in.ClearCredentials {
		for k, v := range saved.Credentials {
			creds[k] = v
		}
	}
	for k, v := range in.Credentials {
		if v == "" {
			delete(creds, k)
		} else {
			creds[k] = v
		}
	}
	next.Credentials = creds
	next.HasCredentials = len(creds) > 0
	if next.Backend != saved.Backend { // nothing of the old backend's carries over
		if in.StatusMap == nil {
			next.StatusMap = nil
		}
		if in.Config == nil {
			next.Config = nil
		}
	}
	return next, nil
}

// trackerState is what the settings show of a project's backend.
func (s *Server) trackerState(sv *service, pid string) (map[string]any, error) {
	cfg, err := sv.data.TrackerConfig(pid)
	if err != nil {
		return nil, err
	}
	events, err := sv.data.ProjectEvents(pid, 20)
	if err != nil {
		return nil, err
	}
	linked, unlinked := 0, 0
	for _, t := range sv.data.ListTodos(pid) {
		if t.ExternalRef != nil {
			linked++
		} else if t.Status != model.StatusMerged && t.Status != model.StatusMerging {
			unlinked++
		}
	}
	return map[string]any{
		"enabled":      s.Trackers != nil,
		"config":       cfg,
		"backends":     tracker.Backends(),
		"statuses":     model.Statuses,
		"running":      s.Trackers != nil && s.Trackers.Running(pid),
		"linked":       linked,
		"unlinked":     unlinked,
		"webhook_path": "/api/projects/" + pid + "/tracker/webhook",
		"events":       events,
	}, nil
}

func (s *Server) getTracker(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	st, err := s.trackerState(sv, r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) putTracker(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	pid := r.PathValue("pid")
	var in trackerInput
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	saved, err := sv.data.TrackerConfig(pid)
	if err != nil {
		writeErr(w, err)
		return
	}
	if s.Trackers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": errTrackersOff.Error()})
		return
	}
	next, err := in.config(saved)
	if err != nil {
		writeErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	_, err = s.Trackers.Apply(ctx, pid, next, tracker.SwitchOptions{Export: in.Export, Confirm: in.Confirm, UserID: sv.c.User})
	var need tracker.ErrNeedsDecision
	switch {
	case errors.As(err, &need):
		writeJSON(w, http.StatusConflict, map[string]any{"error": need.Error(), "needs": need.Need, "open_todos": need.OpenTodos, "from": need.From, "to": need.To})
		return
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, err)
		return
	case err != nil:
		writeErr(w, errors.Join(errBadRequest, err))
		return
	}
	st, err := s.trackerState(sv, pid)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) testTracker(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	pid := r.PathValue("pid")
	saved, err := sv.data.TrackerConfig(pid)
	if err != nil {
		writeErr(w, err)
		return
	}
	in := trackerInput{Backend: saved.Backend}
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			writeErr(w, err)
			return
		}
	}
	cfg, err := in.config(saved)
	if err != nil {
		writeErr(w, err)
		return
	}
	if s.Trackers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": errTrackersOff.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res := map[string]any{"ok": true}
	cols, err := s.Trackers.Test(ctx, cfg)
	if err != nil {
		res["ok"], res["error"] = false, err.Error()
	} else if cols != nil {
		res["columns"] = cols
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) syncTracker(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	pid := r.PathValue("pid")
	if _, err := sv.data.TrackerConfig(pid); err != nil {
		writeErr(w, err)
		return
	}
	if s.Trackers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": errTrackersOff.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	res := map[string]any{"ok": true}
	if err := s.Trackers.SyncNow(ctx, pid); err != nil {
		if errors.Is(err, tracker.ErrNoSync) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		res["ok"], res["error"] = false, err.Error()
	}
	if st, err := s.trackerState(sv, pid); err == nil {
		res["state"] = st
	}
	writeJSON(w, http.StatusOK, res)
}

// rotateTrackerSecret gives the project's tracker webhook a new secret and
// answers it, once; the old one stops working at once.
func (s *Server) rotateTrackerSecret(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	pid := r.PathValue("pid")
	cfg, err := sv.data.TrackerConfig(pid)
	if err != nil {
		writeErr(w, err)
		return
	}
	if s.Trackers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": errTrackersOff.Error()})
		return
	}
	if cfg.Native() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this project's board is Dash's own: it has no tracker webhook"})
		return
	}
	cfg.WebhookSecret = store.NewTrackerSecret()
	if err := s.Trackers.Store.PutTrackerConfig(cfg); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.Trackers.Reconfigure(pid); err != nil {
		writeErr(w, errors.Join(errBadRequest, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": cfg.WebhookSecret, "path": "/api/projects/" + pid + "/tracker/webhook"})
}

// trackerWebhook is a tracker's call to a project's webhook. Whatever is
// wrong (no such project, no tracker, a bad secret) it answers 401 alike,
// so it can't be used to probe for projects.
func (s *Server) trackerWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK) // some trackers check the URL answers before they save it
		return
	}
	if s.Trackers == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": tracker.ErrWebhookRefused.Error()})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, errors.Join(errBadRequest, err))
		return
	}
	if err := s.Trackers.Webhook(r.PathValue("pid"), r, body); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": tracker.ErrWebhookRefused.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
