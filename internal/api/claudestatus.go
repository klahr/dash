package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/klahr/dash/internal/claudestatus"
)

// Claude's status page (status.claude.com, internal/claudestatus):
//
//	POST /api/claude-status/webhook/{token}  the status page's webhooks (public: the token is the credential)
//	GET  /api/claude-status                  everyone signed in: the status, active and recent incidents
//	GET  /api/admin/claude-status            platform admins: the settings, the webhook URL, recent deliveries
//	PUT  /api/admin/claude-status            platform admins: {enabled, page_url, poll}
//	POST /api/admin/claude-status/rotate     platform admins: a new webhook URL
//	POST /api/admin/claude-status/sync       platform admins: read the page's API now
func (s *Server) claudeStatusRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST "+claudestatus.WebhookPrefix+"{token}", s.claudeStatusWebhook)
	mux.HandleFunc("GET /api/claude-status", s.getClaudeStatus)
	mux.HandleFunc("GET /api/admin/claude-status", s.claudeStatusAdmin(s.getClaudeStatusAdmin))
	mux.HandleFunc("PUT /api/admin/claude-status", s.claudeStatusAdmin(s.putClaudeStatusAdmin))
	mux.HandleFunc("POST /api/admin/claude-status/rotate", s.claudeStatusAdmin(s.rotateClaudeStatus))
	mux.HandleFunc("POST /api/admin/claude-status/sync", s.claudeStatusAdmin(s.syncClaudeStatus))
}

var errClaudeStatusOff = errors.New("this Dash doesn't follow Claude's status")

func (s *Server) claudeStatusWebhook(w http.ResponseWriter, r *http.Request) {
	refused := func() {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": claudestatus.ErrRefused.Error()})
	}
	if s.ClaudeStatus == nil {
		refused()
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, claudestatus.MaxBody))
	if err != nil {
		writeErr(w, errors.Join(errBadRequest, err))
		return
	}
	switch err := s.ClaudeStatus.Webhook(r.PathValue("token"), body); {
	case errors.Is(err, claudestatus.ErrRefused):
		refused()
	case errors.Is(err, claudestatus.ErrBadPayload):
		writeErr(w, errors.Join(errBadRequest, err))
	case err != nil:
		writeErr(w, err)
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (s *Server) getClaudeStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.callerOr(w, r); !ok {
		return
	}
	if s.ClaudeStatus == nil {
		writeJSON(w, http.StatusOK, claudestatus.Status{Active: []claudestatus.Incident{}, Recent: []claudestatus.Incident{}, Components: []claudestatus.Component{}})
		return
	}
	st, err := s.ClaudeStatus.Status()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// claudeStatusAdmin lets only platform admins through to h, on a Dash that
// follows Claude's status.
func (s *Server) claudeStatusAdmin(h func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.adminOnly(w, r) {
			return
		}
		if s.ClaudeStatus == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": errClaudeStatusOff.Error()})
			return
		}
		h(w, r)
	}
}

// claudeStatusAdminView is the admin's settings, with the webhook's full URL.
type claudeStatusAdminView struct {
	claudestatus.Admin
	WebhookURL string `json:"webhook_url,omitempty"`
}

func (s *Server) adminView(r *http.Request, a claudestatus.Admin) claudeStatusAdminView {
	v := claudeStatusAdminView{Admin: a}
	if a.WebhookPath != "" {
		if base, err := s.hubURL(r); err == nil {
			v.WebhookURL = base + a.WebhookPath
		}
	}
	return v
}

func (s *Server) getClaudeStatusAdmin(w http.ResponseWriter, r *http.Request) {
	a, err := s.ClaudeStatus.Admin()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.adminView(r, a))
}

func (s *Server) putClaudeStatusAdmin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled *bool   `json:"enabled"`
		PageURL *string `json:"page_url"`
		Poll    *bool   `json:"poll"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, errors.Join(errBadRequest, err))
		return
	}
	a, err := s.ClaudeStatus.Configure(in.Enabled, in.PageURL, in.Poll)
	if errors.Is(err, claudestatus.ErrBadPage) {
		err = errors.Join(errBadRequest, err)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.adminView(r, a))
}

func (s *Server) rotateClaudeStatus(w http.ResponseWriter, r *http.Request) {
	a, err := s.ClaudeStatus.Rotate()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.adminView(r, a))
}

func (s *Server) syncClaudeStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if _, err := s.ClaudeStatus.Sync(ctx); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "couldn't read the status page: " + err.Error()})
		return
	}
	a, err := s.ClaudeStatus.Admin()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.adminView(r, a))
}
