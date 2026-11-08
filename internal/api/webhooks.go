package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/notify"
)

// Outgoing notification webhooks, set up in a project's settings:
//
//	GET    /api/projects/{pid}/webhooks   list (never the secrets)
//	POST   /api/projects/{pid}/webhooks   create; answers the signing secret, once
//	PATCH  /api/webhooks/{wid}            edit
//	DELETE /api/webhooks/{wid}
//	POST   /api/webhooks/{wid}/rotate     new signing secret, answered once
//	POST   /api/webhooks/{wid}/test       send a test notification now
//
// and POST /api/notify/permission/{token}, the Approve/Deny buttons of ntfy
// notifications (public: the token is the credential).

var errNotifyOff = errors.New("notifications aren't sent from this Dash")

// webhookInput is a webhook's settings as the UI sends them. Token: nil
// keeps the stored one, "" removes it.
type webhookInput struct {
	Name    *string             `json:"name"`
	URL     *string             `json:"url"`
	Format  *string             `json:"format"`
	Events  []model.NotifyEvent `json:"events"`
	Enabled *bool               `json:"enabled"`
	Token   *string             `json:"token"`
}

func (in webhookInput) apply(w *model.Webhook) {
	if in.Name != nil {
		w.Name = *in.Name
	}
	if in.URL != nil {
		w.URL = *in.URL
	}
	if in.Format != nil {
		w.Format = *in.Format
	}
	if in.Events != nil {
		w.Events = in.Events
	}
	if in.Enabled != nil {
		w.Enabled = *in.Enabled
	}
	if in.Token != nil {
		w.Token = *in.Token
	}
}

// checkWebhook validates w before it is saved: its fields, and its URL
// against the private-address rule.
func (s *Server) checkWebhook(w *model.Webhook) error {
	if err := w.Normalize(); err != nil {
		return errors.Join(errBadRequest, err)
	}
	allow := s.Notify == nil || s.Notify.AllowPrivate
	if err := notify.CheckURL(w.URL, allow); err != nil {
		return errors.Join(errBadRequest, err)
	}
	return nil
}

func (s *Server) listWebhooks(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	hooks, err := sv.data.ListWebhooks(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"webhooks":      hooks,
		"events":        model.NotifyEvents,
		"formats":       model.WebhookFormats,
		"allow_private": s.Notify == nil || s.Notify.AllowPrivate,
		"enabled":       s.Notify != nil,
		"links":         s.Notify != nil && s.Notify.Origin != nil && s.Notify.Origin(r.PathValue("pid")) != "",
	})
}

func (s *Server) createWebhook(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var in webhookInput
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	hook := model.Webhook{ProjectID: r.PathValue("pid"), Format: model.WebhookJSON, Enabled: true, Events: model.NotifyEvents}
	in.apply(&hook)
	if err := s.checkWebhook(&hook); err != nil {
		writeErr(w, err)
		return
	}
	hook, err := sv.data.CreateWebhook(hook)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"webhook": hook, "secret": hook.Secret})
}

func (s *Server) updateWebhook(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var in webhookInput
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	hook, err := sv.data.UpdateWebhook(r.PathValue("wid"), func(h *model.Webhook) error {
		in.apply(h)
		return s.checkWebhook(h)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, hook)
}

func (s *Server) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := sv.data.DeleteWebhook(r.PathValue("wid")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) rotateWebhook(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	secret, err := sv.data.RotateWebhookSecret(r.PathValue("wid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret})
}

// testWebhook sends one test notification (enabled or not, whatever its
// events) and answers how it went, with the webhook's new status.
func (s *Server) testWebhook(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	hook, err := sv.data.GetWebhook(r.PathValue("wid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if s.Notify == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": errNotifyOff.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res := map[string]any{"ok": true}
	if err := s.Notify.Test(ctx, hook); err != nil {
		res["ok"], res["error"] = false, err.Error()
	}
	if hook, err = sv.data.GetWebhook(hook.ID); err == nil {
		res["webhook"] = hook
	}
	writeJSON(w, http.StatusOK, res)
}
