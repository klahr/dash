package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/klahr/dash/internal/ci"
)

// maxHookBody is the largest webhook delivery Dash reads. GitHub's
// workflow_run and release payloads are tens of KB.
const maxHookBody = 1 << 20

// githubHook handles POST /api/github/hook (ci.HookPath), where GitHub
// delivers the webhooks the hub registered on its projects' repositories.
// It is public: the repository's secret signs each delivery
// (X-Hub-Signature-256), and one that it didn't sign is refused. Only ping,
// workflow_run and release mean something; other events are taken and
// ignored. What a delivery says of a run is never trusted: the run is read
// again from GitHub (ci.Watcher.HookRun).
func (s *Server) githubHook(w http.ResponseWriter, r *http.Request) {
	if s.GitHubHooks == nil {
		writeError(w, http.StatusNotFound, "this Dash takes no GitHub webhooks")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHookBody))
	if err != nil {
		if errors.As(err, new(*http.MaxBytesError)) {
			writeError(w, http.StatusRequestEntityTooLarge, "delivery too large")
			return
		}
		writeError(w, http.StatusBadRequest, "couldn't read the delivery")
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	if err := s.GitHubHooks.Deliver(event, r.Header.Get("X-Hub-Signature-256"), body); err != nil {
		slog.Debug("github webhook: refused a delivery", "event", event, "delivery", r.Header.Get("X-GitHub-Delivery"))
		writeError(w, http.StatusUnauthorized, ci.ErrHookRefused.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
