package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/klahr/dash/internal/gitops"
)

// maxCommitDiff caps one file's patch in a commit's detail: a generated or
// vendored file can be megabytes the browser shouldn't have to draw.
const maxCommitDiff = 1 << 20

// getCommit is one commit of the project's repository for the History
// view's detail pane: message, author, committer and changed files.
func (s *Server) getCommit(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	p, err := sv.data.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	d, err := s.Git.CommitDetail(r.Context(), p.ID, p.Path, r.PathValue("sha"))
	if err != nil {
		writeErr(w, commitErr(err))
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// commitDiff is the patch a commit made to one file (?path=), compared
// with its first parent (the empty tree for a root commit), as text.
func (s *Server) commitDiff(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	p, err := sv.data.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	diff, err := s.Git.CommitFileDiff(r.Context(), p.ID, p.Path, r.PathValue("sha"), r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, commitErr(err))
		return
	}
	if len(diff) > maxCommitDiff {
		diff = diff[:maxCommitDiff] + "\n[diff truncated]"
	}
	writeText(w, diff)
}

// commitErr makes gitops' refusals of a hash or path a 400 (writeErr
// makes an unknown commit or file a 404).
func commitErr(err error) error {
	switch {
	case errors.Is(err, gitops.ErrBadRev), errors.Is(err, gitops.ErrBadPath):
		return fmt.Errorf("%w: %v", errBadRequest, err)
	}
	return err
}
