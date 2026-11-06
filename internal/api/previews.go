package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/skip2/go-qrcode"

	"github.com/klahr/dash/internal/preview"
)

// errNoPreviews answers preview calls on a Server built without previews (tests).
var errNoPreviews = errors.New("previews are not available")

// startPreview runs the todo's project run_command in its worktree, or
// returns the preview already running.
func (s *Server) startPreview(w http.ResponseWriter, r *http.Request) {
	if s.Previews == nil {
		writeErr(w, errNoPreviews)
		return
	}
	t, err := s.Store.GetTodo(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.Store.GetProject(t.ProjectID)
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
	if s.Previews == nil {
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
	if s.Previews == nil {
		writeErr(w, preview.ErrNotFound)
		return
	}
	pv, ok := s.Previews.Get(r.PathValue("tid"))
	if !ok {
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

func (s *Server) listPreviews(w http.ResponseWriter, r *http.Request) {
	if s.Previews == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	s.Previews.SetOrigin(r)
	writeJSON(w, http.StatusOK, s.Previews.List())
}

func (s *Server) stopPreviews(w http.ResponseWriter, r *http.Request) {
	n := 0
	if s.Previews != nil {
		n = s.Previews.StopAll("stopped")
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
	s.Previews.SetOrigin(r)
	if _, ok := s.Previews.Get(tid); !ok {
		if _, err := s.Store.GetTodo(tid); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, s.Previews.AuthURL(tid, r.URL.Query().Get("next")), http.StatusFound)
}

// qrCode renders text as a QR code (PNG), e.g. a preview's link for a phone.
func (s *Server) qrCode(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	if text == "" || len(text) > 1024 || !(strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://")) {
		writeErr(w, errBadRequest)
		return
	}
	png, err := qrcode.Encode(text, qrcode.Medium, 320)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(png)
}
