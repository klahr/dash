package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/klahr/dash/internal/model"
)

// The editor shows text files up to this size; requests are capped at 1 MiB
// (see decode), so saved content must stay well below it.
const maxEditBytes = 512 << 10

// ---------------------------------------------------------------- files
// The file browser reads the default branch as committed, not the user's
// checkout. Saving an edit creates a todo that commits it in a worktree and
// goes to review like any other.
//
// Files git doesn't track (untracked or ignored, like .env) can't go through a
// commit, so they are read from and saved straight to the project's checkout.

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	p, err := sv.data.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	files, err := s.Git.Files(r.Context(), p.ID, p.Path, p.Base())
	if err != nil {
		writeErr(w, err)
		return
	}
	local, err := s.Git.LocalFiles(r.Context(), p.ID, p.Path)
	if err != nil {
		writeErr(w, err)
		return
	}
	tracked := make(map[string]bool, len(files))
	for _, f := range files {
		tracked[f] = true
	}
	local = slices.DeleteFunc(local, func(f string) bool { return tracked[f] })
	writeJSON(w, http.StatusOK, map[string]any{"branch": p.Base(), "files": nonNil(files), "local": nonNil(local)})
}

// localPath returns where name lives in the checkout, if it is a file git
// doesn't track there or on the default branch, and stays inside the checkout
// after following symlinks.
func (s *Server) localPath(ctx context.Context, p model.Project, name string) (string, error) {
	local, err := s.Git.LocalFiles(ctx, p.ID, p.Path)
	if err != nil {
		return "", err
	}
	if !slices.Contains(local, name) {
		return "", fmt.Errorf("%w: %s is not a local file", errBadRequest, name)
	}
	if blob, _ := s.Git.Blob(ctx, p.ID, p.Path, p.Base(), name); blob != "" {
		return "", fmt.Errorf("%w: %s is on %s; edit it as a task", errBadRequest, name, p.Base())
	}
	root, err := filepath.EvalSymlinks(p.Path)
	if err != nil {
		return "", err
	}
	full, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(root, full); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s points outside the project", errBadRequest, name)
	}
	return full, nil
}

func fileVersion(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s *Server) readLocalFile(w http.ResponseWriter, r *http.Request, p model.Project, name string) {
	full, err := s.localPath(r.Context(), p, name)
	if err != nil {
		writeErr(w, err)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeErr(w, err)
		return
	}
	res := map[string]any{"path": name, "size": st.Size(), "local": true}
	if st.Size() > maxEditBytes {
		res["too_large"] = true
		writeJSON(w, http.StatusOK, res)
		return
	}
	content, err := io.ReadAll(io.LimitReader(f, maxEditBytes+1))
	if err != nil {
		writeErr(w, err)
		return
	}
	res["blob"] = fileVersion(content)
	switch {
	case len(content) > maxEditBytes:
		res["too_large"] = true
	case bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content):
		res["binary"] = true
	default:
		res["content"] = string(content)
	}
	writeJSON(w, http.StatusOK, res)
}

// saveLocalFile writes a local file in the checkout, unless it changed on
// disk since it was read (blob is the hash of what was read).
func (s *Server) saveLocalFile(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req struct {
		Path    string `json:"path"`
		Blob    string `json:"blob"`
		Content string `json:"content"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, err := sv.data.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	name, err := cleanRepoPath(req.Path)
	if err != nil {
		writeErr(w, err)
		return
	}
	if s.hub() {
		writeErr(w, fmt.Errorf("%w: files of a shared project can't be edited from the hub; describe the change in a task instead", errBadRequest))
		return
	}
	if len(req.Content) > maxEditBytes {
		writeErr(w, fmt.Errorf("%w: file is larger than %d KiB", errBadRequest, maxEditBytes>>10))
		return
	}
	full, err := s.localPath(r.Context(), p, name)
	if err != nil {
		writeErr(w, err)
		return
	}
	if cur, err := os.ReadFile(full); err != nil || fileVersion(cur) != req.Blob {
		writeJSON(w, http.StatusConflict, map[string]string{"error": name + " changed on disk since it was opened; reload it and edit again"})
		return
	}
	if err := os.WriteFile(full, []byte(req.Content), 0o644); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": name, "blob": fileVersion([]byte(req.Content))})
}

// cleanRepoPath rejects paths that leave the repository.
func cleanRepoPath(p string) (string, error) {
	c := path.Clean(strings.TrimPrefix(p, "./"))
	if p == "" || c == "." || path.IsAbs(c) || c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("%w: invalid path %q", errBadRequest, p)
	}
	return c, nil
}

func (s *Server) readFile(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	p, err := sv.data.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	name, err := cleanRepoPath(r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if r.URL.Query().Get("local") != "" {
		s.readLocalFile(w, r, p, name)
		return
	}
	blob, err := s.Git.Blob(r.Context(), p.ID, p.Path, p.Base(), name)
	if err != nil || blob == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": name + " is not on " + p.Base()})
		return
	}
	content, size, err := s.Git.ReadBlob(r.Context(), p.ID, p.Path, blob, maxEditBytes)
	if err != nil {
		writeErr(w, err)
		return
	}
	res := map[string]any{"path": name, "blob": blob, "size": size, "branch": p.Base()}
	switch {
	case size > maxEditBytes:
		res["too_large"] = true
	case bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content):
		res["binary"] = true
	default:
		res["content"] = string(content)
	}
	writeJSON(w, http.StatusOK, res)
}

// saveEdit queues a todo that commits the new content of a file.
func (s *Server) saveEdit(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req struct {
		Path    string `json:"path"`
		Blob    string `json:"blob"`
		Content string `json:"content"`
		Title   string `json:"title"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, err := sv.data.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	name, err := cleanRepoPath(req.Path)
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(req.Content) > maxEditBytes {
		writeErr(w, fmt.Errorf("%w: file is larger than %d KiB", errBadRequest, maxEditBytes>>10))
		return
	}
	// An empty blob creates a new file, one that isn't on the branch yet.
	cur, _ := s.Git.Blob(r.Context(), p.ID, p.Path, p.Base(), name)
	switch {
	case req.Blob == "" && cur != "":
		writeJSON(w, http.StatusConflict, map[string]string{"error": name + " already exists on " + p.Base() + "; open it and edit it instead"})
		return
	case cur != req.Blob:
		writeJSON(w, http.StatusConflict, map[string]string{"error": name + " changed on " + p.Base() + " since it was opened; reload it and edit again"})
		return
	}
	if req.Blob == "" {
		if local, err := s.Git.LocalFiles(r.Context(), p.ID, p.Path); err == nil && slices.Contains(local, name) {
			writeErr(w, fmt.Errorf("%w: %s is a local file git doesn't track; edit it in the file browser", errBadRequest, name))
			return
		}
	}
	title := strings.TrimSpace(req.Title)
	switch {
	case title != "":
	case req.Blob == "":
		title = "Create " + name
	default:
		title = "Edit " + name
	}
	t, err := sv.addTodo(model.Todo{
		ProjectID: p.ID, Title: title,
		Prompt: fmt.Sprintf("`%s` was edited by hand in Dash's file editor; the change is already committed in this working tree. "+
			"Keep it, and only change what the notes below ask for.", name),
		Edits: []model.FileEdit{{Path: name, Blob: req.Blob, Content: req.Content}},
	}, true)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}
