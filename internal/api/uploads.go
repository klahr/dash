package api

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

const maxUpload = 20 << 20

// Kinds of files Claude can make use of; images are shown to it by the Read tool.
var uploadTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".svg": "image/svg+xml",
	".pdf": "application/pdf", ".txt": "text/plain", ".md": "text/markdown", ".log": "text/plain",
	".json": "application/json", ".csv": "text/csv",
}

var (
	uploadIDRe   = regexp.MustCompile(`^[0-9a-f]{12}$`)
	unsafeNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

// upload stores the request body (POST /api/uploads?name=shot.png) and
// returns its attachment record; todos refer to it by id.
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	name := cleanUploadName(r.URL.Query().Get("name"), r.Header.Get("Content-Type"))
	typ, ok := uploadTypes[strings.ToLower(filepath.Ext(name))]
	if !ok {
		writeErr(w, fmt.Errorf("%w: attach images (PNG, JPEG, GIF, WebP, SVG), PDFs or text files", errBadRequest))
		return
	}
	id := store.NewID()
	dir := filepath.Join(s.UploadRoot, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeErr(w, err)
		return
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		writeErr(w, err)
		return
	}
	n, err := io.Copy(f, http.MaxBytesReader(w, r.Body, maxUpload))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			err = fmt.Errorf("%w: files can be at most %d MB", errBadRequest, maxUpload>>20)
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, model.Attachment{ID: id, Name: name, Type: typ, Size: n, Path: path})
}

// getUpload serves an attachment back to the UI (thumbnails, downloads).
func (s *Server) getUpload(w http.ResponseWriter, r *http.Request) {
	a, err := s.findUpload(r.PathValue("id"))
	if err != nil || a.Name != r.PathValue("name") {
		writeErr(w, store.ErrNotFound)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", a.Type)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if !strings.HasPrefix(a.Type, "image/") {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": a.Name}))
	}
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	http.ServeFile(w, r, a.Path)
}

// attachments resolves upload ids from a request.
func (s *Server) attachments(ids []string) ([]model.Attachment, error) {
	var out []model.Attachment
	for _, id := range ids {
		a, err := s.findUpload(id)
		if err != nil {
			return nil, fmt.Errorf("%w: unknown attachment %q", errBadRequest, id)
		}
		out = append(out, a)
	}
	return out, nil
}

func (s *Server) findUpload(id string) (model.Attachment, error) {
	if !uploadIDRe.MatchString(id) || s.UploadRoot == "" {
		return model.Attachment{}, store.ErrNotFound
	}
	dir := filepath.Join(s.UploadRoot, id)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].IsDir() {
		return model.Attachment{}, store.ErrNotFound
	}
	info, err := entries[0].Info()
	if err != nil {
		return model.Attachment{}, err
	}
	name := entries[0].Name()
	typ, ok := uploadTypes[strings.ToLower(filepath.Ext(name))]
	if !ok {
		return model.Attachment{}, store.ErrNotFound
	}
	return model.Attachment{ID: id, Name: name, Type: typ, Size: info.Size(), Path: filepath.Join(dir, name)}, nil
}

// cleanUploadName makes a safe file name, adding an extension from the
// content type for pasted images that arrive without a name.
func cleanUploadName(name, contentType string) string {
	name = strings.Trim(unsafeNameRe.ReplaceAllString(filepath.Base(strings.ReplaceAll(name, "\\", "/")), "-"), ".-")
	if len(name) > 80 {
		// Keep a real extension when cutting the base name. An "extension"
		// this long is not one we accept, so cut the whole name instead and
		// let the type check reject it.
		if ext := filepath.Ext(name); len(ext) <= 16 {
			name = name[:80-len(ext)] + ext
		} else {
			name = name[:80]
		}
	}
	if filepath.Ext(name) == "" {
		ct, _, _ := mime.ParseMediaType(contentType)
		for ext, typ := range uploadTypes {
			if typ == ct && ext != ".jpeg" && ext != ".log" {
				if name == "" {
					name = "pasted"
				}
				return name + ext
			}
		}
	}
	if name == "" {
		return "file"
	}
	return name
}
