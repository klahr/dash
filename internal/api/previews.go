package api

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/preview"
	"github.com/klahr/dash/internal/qr"
)

// errNoPreviews answers preview calls on a Server built without previews (tests).
var errNoPreviews = errors.New("previews are not available")

// startPreview runs the todo's project run_command in its worktree, or
// returns the preview already running.
func (s *Server) startPreview(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if s.Previews == nil {
		writeErr(w, errNoPreviews)
		return
	}
	t, err := sv.data.GetTodo(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	p, err := sv.data.GetProject(t.ProjectID)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Previews.SetOrigin(r)
	pv, err := s.Previews.Start(t, p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pv)
}

func (s *Server) stopPreview(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if s.Previews == nil {
		writeErr(w, preview.ErrNotFound)
		return
	}
	if _, err := sv.data.GetTodo(r.PathValue("tid")); err != nil {
		writeErr(w, preview.ErrNotFound)
		return
	}
	if err := s.Previews.Stop(r.PathValue("tid"), "stopped"); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// previewLog returns the end of the preview server's output, e.g. to send
// it back to Claude when it crashed.
func (s *Server) previewLog(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if s.Previews == nil {
		writeErr(w, preview.ErrNotFound)
		return
	}
	pv, ok := s.Previews.Get(r.PathValue("tid"))
	if !ok || !sv.data.OwnsProject(pv.ProjectID) {
		writeErr(w, preview.ErrNotFound)
		return
	}
	b, err := s.Procs.Tail(pv.ProcessID, 16*1024)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(b)
}

// listPreviews is the previews of the caller's org's tasks.
func (s *Server) listPreviews(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if s.Previews == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	s.Previews.SetOrigin(r)
	writeJSON(w, http.StatusOK, sv.previews())
}

// previews are the running previews of the caller's org's tasks.
func (sv *service) previews() []model.Preview {
	out := []model.Preview{}
	for _, pv := range sv.s.Previews.List() {
		if sv.data.OwnsProject(pv.ProjectID) {
			out = append(out, pv)
		}
	}
	return out
}

// stopPreviews stops the previews of the caller's org's tasks.
func (s *Server) stopPreviews(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	n := 0
	if s.Previews != nil {
		for _, pv := range sv.previews() {
			if s.Previews.Stop(pv.TodoID, "stopped") == nil {
				n++
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"stopped": n})
}

// openPreview signs this browser in to the todo's preview host and sends it
// there (see package preview). It needs a Dash session like any page.
func (s *Server) openPreview(w http.ResponseWriter, r *http.Request) {
	tid := r.PathValue("tid")
	if s.Previews == nil || s.Previews.Domain == "" {
		http.Error(w, "No preview domain is configured (-preview-domain).", http.StatusNotFound)
		return
	}
	c, err := s.caller(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	s.Previews.SetOrigin(r)
	// Only the task's own org gets signed in to its preview; a runner's
	// previews (<task>-<runner>) belong to the whole Dash, like the runner.
	if s.Previews.IsRemote(tid) {
		if !c.PlatformAdmin {
			http.NotFound(w, r)
			return
		}
	} else if _, err := s.as(c).data.GetTodo(tid); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, s.Previews.AuthURL(tid, r.URL.Query().Get("next")), http.StatusFound)
}

// openRunnerPreview handles GET /peer/{name}/preview/{tid}, the open_url of
// a preview on a runner as the UI asks for it: the hub serves those previews
// (as <tid>-<name>), so it signs the browser in itself. Other computers
// (-peer) serve their own previews.
func (s *Server) openRunnerPreview(w http.ResponseWriter, r *http.Request) {
	name, tid := r.PathValue("name"), r.PathValue("tid")
	if s.Previews == nil || s.Previews.Domain == "" || !s.Previews.IsRemote(tid+"-"+name) {
		s.forwardToPeer(w, r)
		return
	}
	u := "/preview/" + url.PathEscape(tid+"-"+name)
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, u, http.StatusFound)
}

// qrCode renders text as a QR code (PNG), e.g. a preview's link for a phone.
func (s *Server) qrCode(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	if text == "" || len(text) > 1024 || !(strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://")) {
		writeErr(w, errBadRequest)
		return
	}
	png, err := qr.PNG(text)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(png)
}
