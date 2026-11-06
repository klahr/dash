package api

import (
	"bytes"
	"fmt"
	"net/http"
	"path"
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

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	files, err := s.Git.Files(r.Context(), p.ID, p.Path, p.DefaultBranch)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"branch": p.DefaultBranch, "files": nonNil(files)})
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
	p, err := s.Store.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	name, err := cleanRepoPath(r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	blob, err := s.Git.Blob(r.Context(), p.ID, p.Path, p.DefaultBranch, name)
	if err != nil || blob == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": name + " is not on " + p.DefaultBranch})
		return
	}
	content, size, err := s.Git.ReadBlob(r.Context(), p.ID, p.Path, blob, maxEditBytes)
	if err != nil {
		writeErr(w, err)
		return
	}
	res := map[string]any{"path": name, "blob": blob, "size": size, "branch": p.DefaultBranch}
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
	p, err := s.Store.GetProject(r.PathValue("pid"))
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
	if cur, _ := s.Git.Blob(r.Context(), p.ID, p.Path, p.DefaultBranch, name); req.Blob == "" || cur != req.Blob {
		writeJSON(w, http.StatusConflict, map[string]string{"error": name + " changed on " + p.DefaultBranch + " since it was opened; reload it and edit again"})
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Edit " + name
	}
	t, err := s.addTodo(model.Todo{
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
