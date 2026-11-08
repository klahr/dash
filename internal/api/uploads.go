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
	"strconv"
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

// uploadOrgFile, in an upload's directory, names the org it was uploaded
// in; only that org can see or attach it. Uploads from before orgs have
// none and belong to the default org.
const uploadOrgFile = ".org"

// upload stores the request body (POST /api/uploads?name=shot.png) and
// returns its attachment record; todos refer to it by id.
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	name := cleanUploadName(r.URL.Query().Get("name"), r.Header.Get("Content-Type"))
	typ, ok := uploadTypes[strings.ToLower(filepath.Ext(name))]
	if !ok {
		writeErr(w, fmt.Errorf("%w: attach images (PNG, JPEG, GIF, WebP, SVG), PDFs or text files", errBadRequest))
		return
	}
	a, err := sv.saveUpload(name, typ, http.MaxBytesReader(w, r.Body, maxUpload))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			err = fmt.Errorf("%w: files can be at most %d MB", errBadRequest, maxUpload>>20)
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

// saveUpload stores body as a new upload of the caller's org, named name
// (already clean, of type typ).
func (sv *service) saveUpload(name, typ string, body io.Reader) (model.Attachment, error) {
	if sv.s.UploadRoot == "" {
		return model.Attachment{}, errors.New("this Dash has no uploads directory")
	}
	id := store.NewID()
	dir := filepath.Join(sv.s.UploadRoot, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return model.Attachment{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, uploadOrgFile), []byte(strconv.FormatInt(sv.c.Org, 10)), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return model.Attachment{}, err
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return model.Attachment{}, err
	}
	n, err := io.Copy(f, body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return model.Attachment{}, err
	}
	return model.Attachment{ID: id, Name: name, Type: typ, Size: n, Path: path}, nil
}

// getUpload serves an attachment back to the UI (thumbnails, downloads).
func (s *Server) getUpload(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	a, err := sv.findUpload(r.PathValue("id"))
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
func (sv *service) attachments(ids []string) ([]model.Attachment, error) {
	var out []model.Attachment
	for _, id := range ids {
		a, err := sv.findUpload(id)
		if err != nil {
			return nil, fmt.Errorf("%w: unknown attachment %q", errBadRequest, id)
		}
		out = append(out, a)
	}
	return out, nil
}

// findUpload is the caller's org's upload id.
func (sv *service) findUpload(id string) (model.Attachment, error) {
	s := sv.s
	if !uploadIDRe.MatchString(id) || s.UploadRoot == "" {
		return model.Attachment{}, store.ErrNotFound
	}
	dir := filepath.Join(s.UploadRoot, id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return model.Attachment{}, store.ErrNotFound
	}
	var files []os.DirEntry
	org := int64(0) // none recorded: from before orgs
	for _, e := range entries {
		if e.Name() == uploadOrgFile {
			b, err := os.ReadFile(filepath.Join(dir, uploadOrgFile))
			if org, err = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err != nil || org == 0 {
				return model.Attachment{}, store.ErrNotFound
			}
			continue
		}
		files = append(files, e)
	}
	if len(files) != 1 || files[0].IsDir() || !sv.uploadOrg(org) {
		return model.Attachment{}, store.ErrNotFound
	}
	info, err := files[0].Info()
	if err != nil {
		return model.Attachment{}, err
	}
	name := files[0].Name()
	typ, ok := uploadTypes[strings.ToLower(filepath.Ext(name))]
	if !ok {
		return model.Attachment{}, store.ErrNotFound
	}
	return model.Attachment{ID: id, Name: name, Type: typ, Size: info.Size(), Path: filepath.Join(dir, name)}, nil
}

// uploadOrg reports whether an upload of org (0: from before orgs, which
// are the default org's) is the caller's.
func (sv *service) uploadOrg(org int64) bool {
	if org != 0 {
		return org == sv.c.Org
	}
	def, err := sv.s.Store.Accounts().DefaultOrg()
	return err == nil && def.ID == sv.c.Org
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
