package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// fileContent is a file as the editor (and the checklist) reads it.
type fileContent struct {
	Path     string  `json:"path"`
	Blob     string  `json:"blob,omitempty"` // its blob on the branch, or the hash of a local file
	Size     int64   `json:"size"`
	Branch   string  `json:"branch,omitempty"`
	Local    bool    `json:"local,omitempty"`
	TooLarge bool    `json:"too_large,omitempty"`
	Binary   bool    `json:"binary,omitempty"`
	Content  *string `json:"content,omitempty"` // unset when too large or binary
}

// setContent fills in content, unless it is too large or binary.
func (f *fileContent) setContent(content []byte) {
	switch {
	case len(content) > maxEditBytes || f.Size > maxEditBytes:
		f.TooLarge = true
	case bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content):
		f.Binary = true
	default:
		c := string(content)
		f.Content = &c
	}
}

// localFile reads name, a file git doesn't track, from p's checkout.
func (s *Server) localFile(ctx context.Context, p model.Project, name string) (fileContent, error) {
	full, err := s.localPath(ctx, p, name)
	if err != nil {
		return fileContent{}, err
	}
	f, err := os.Open(full)
	if err != nil {
		return fileContent{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return fileContent{}, err
	}
	res := fileContent{Path: name, Size: st.Size(), Local: true}
	if st.Size() > maxEditBytes {
		res.TooLarge = true
		return res, nil
	}
	content, err := io.ReadAll(io.LimitReader(f, maxEditBytes+1))
	if err != nil {
		return fileContent{}, err
	}
	res.Blob = fileVersion(content)
	res.setContent(content)
	return res, nil
}

// branchFile reads name from p's default branch as committed; ok is false
// when it isn't there.
func (s *Server) branchFile(ctx context.Context, p model.Project, name string) (res fileContent, ok bool, err error) {
	blob, err := s.Git.Blob(ctx, p.ID, p.Path, p.Base(), name)
	if err != nil || blob == "" {
		return fileContent{}, false, nil
	}
	content, size, err := s.Git.ReadBlob(ctx, p.ID, p.Path, blob, maxEditBytes)
	if err != nil {
		return fileContent{}, false, err
	}
	res = fileContent{Path: name, Blob: blob, Size: size, Branch: p.Base()}
	res.setContent(content)
	return res, true, nil
}

func (s *Server) readLocalFile(w http.ResponseWriter, r *http.Request, p model.Project, name string) {
	res, err := s.localFile(r.Context(), p, name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// writeLocalFile writes name, a file git doesn't track, in p's checkout,
// unless it changed on disk since it was read (version is the hash of what
// was read), and returns its new version.
func (s *Server) writeLocalFile(ctx context.Context, p model.Project, name, version, content string) (string, error) {
	if s.hub() {
		return "", fmt.Errorf("%w: files of a shared project can't be edited from the hub; describe the change in a task instead", errBadRequest)
	}
	if len(content) > maxEditBytes {
		return "", fmt.Errorf("%w: file is larger than %d KiB", errBadRequest, maxEditBytes>>10)
	}
	full, err := s.localPath(ctx, p, name)
	if err != nil {
		return "", err
	}
	if cur, err := os.ReadFile(full); err != nil || fileVersion(cur) != version {
		return "", fmt.Errorf("%w: %s changed on disk since it was opened; reload it and edit again", errConflict, name)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fileVersion([]byte(content)), nil
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
	blob, err := s.writeLocalFile(r.Context(), p, name, req.Blob, req.Content)
	if errors.Is(err, errConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": strings.TrimPrefix(err.Error(), errConflict.Error()+": ")})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": name, "blob": blob})
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
	res, found, err := s.branchFile(r.Context(), p, name)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": name + " is not on " + p.Base()})
		return
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
	t, err := sv.queueEdit(r.Context(), p, name, req.Blob, req.Content, req.Title)
	if errors.Is(err, errConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": strings.TrimPrefix(err.Error(), errConflict.Error()+": ")})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// queueEdit queues a task that commits content as name, made from the file
// at blob on the default branch ("" for a new file), and goes to review.
func (sv *service) queueEdit(ctx context.Context, p model.Project, name, blob, content, title string) (model.Todo, error) {
	if len(content) > maxEditBytes {
		return model.Todo{}, fmt.Errorf("%w: file is larger than %d KiB", errBadRequest, maxEditBytes>>10)
	}
	s := sv.s
	// An empty blob creates a new file, one that isn't on the branch yet.
	cur, _ := s.Git.Blob(ctx, p.ID, p.Path, p.Base(), name)
	switch {
	case blob == "" && cur != "":
		return model.Todo{}, fmt.Errorf("%w: %s already exists on %s; open it and edit it instead", errConflict, name, p.Base())
	case cur != blob:
		return model.Todo{}, fmt.Errorf("%w: %s changed on %s since it was opened; reload it and edit again", errConflict, name, p.Base())
	}
	if blob == "" {
		if local, err := s.Git.LocalFiles(ctx, p.ID, p.Path); err == nil && slices.Contains(local, name) {
			return model.Todo{}, fmt.Errorf("%w: %s is a local file git doesn't track; edit it in the file browser", errBadRequest, name)
		}
	}
	title = strings.TrimSpace(title)
	switch {
	case title != "":
	case blob == "":
		title = "Create " + name
	default:
		title = "Edit " + name
	}
	return sv.addTodo(model.Todo{
		ProjectID: p.ID, Title: title,
		Prompt: fmt.Sprintf("`%s` was edited by hand in Dash's file editor; the change is already committed in this working tree. "+
			"Keep it, and only change what the notes below ask for.", name),
		Edits: []model.FileEdit{{Path: name, Blob: blob, Content: content}},
	}, true)
}
