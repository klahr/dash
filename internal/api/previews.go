package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/yeqown/go-qrcode/v2"
	"github.com/yeqown/go-qrcode/writer/standard"

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
	if _, ok := s.Previews.Get(tid); !ok && !s.Previews.IsRemote(tid) {
		if _, err := s.Store.GetTodo(tid); err != nil {
			http.NotFound(w, r)
			return
		}
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

// qrSize is the target width of a QR code image in pixels, and qrQuietZone
// the white margin around it in modules (the minimum the spec asks for).
const (
	qrSize      = 320
	qrQuietZone = 4
)

// qrCode renders text as a QR code (PNG), e.g. a preview's link for a phone.
func (s *Server) qrCode(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	if text == "" || len(text) > 1024 || !(strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://")) {
		writeErr(w, errBadRequest)
		return
	}
	png, err := encodeQR(text)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(png)
}

// encodeQR returns text as a PNG QR code at Medium error correction, about
// qrSize pixels wide including a qrQuietZone-module margin.
func encodeQR(text string) ([]byte, error) {
	qrc, err := qrcode.NewWith(text, qrcode.WithErrorCorrectionLevel(qrcode.ErrorCorrectionMedium))
	if err != nil {
		return nil, err
	}
	// Whole pixels per module, so the image is at most qrSize wide.
	block := max(1, min(255, qrSize/(qrc.Dimension()+2*qrQuietZone)))
	var buf bytes.Buffer
	wr := standard.NewWithWriter(nopCloser{&buf},
		standard.WithBuiltinImageEncoder(standard.PNG_FORMAT),
		standard.WithQRWidth(uint8(block)),
		standard.WithBorderWidth(qrQuietZone*block),
	)
	if err := qrc.Save(wr); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// nopCloser lets the QR writer, which closes its output, write to a buffer.
type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }
