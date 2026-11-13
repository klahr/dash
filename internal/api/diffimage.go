package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// Images in diffs: git shows a changed image as "Binary files … differ",
// so the diff views ask for the image itself, as it was before the change
// (side=old) and after it (side=new), and show the two.

// maxDiffImage caps an image shown in a diff: below what the hub reads of a
// runner's answer.
const maxDiffImage = 16 << 20

// diffImageTypes are the images a diff shows, by extension: those a
// browser draws in an <img> and the hub passes on from a runner
// (tunnel.SafeRunnerHeader).
var diffImageTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
}

// diffImageType is name's content type, or an error for a file that isn't
// an image a diff shows.
func diffImageType(name string) (string, error) {
	if ct, ok := diffImageTypes[strings.ToLower(path.Ext(name))]; ok {
		return ct, nil
	}
	return "", fmt.Errorf("%w: %s is not an image", errBadRequest, name)
}

// diffImageQuery reads ?path= and ?side= (old or new).
func diffImageQuery(r *http.Request) (name string, old bool, err error) {
	q := r.URL.Query()
	name = q.Get("path")
	if err := gitops.CheckRepoPath(name); err != nil {
		return "", false, fmt.Errorf("%w: invalid path %q", errBadRequest, name)
	}
	if _, err := diffImageType(name); err != nil {
		return "", false, err
	}
	switch q.Get("side") {
	case "old":
		return name, true, nil
	case "new", "":
		return name, false, nil
	}
	return "", false, fmt.Errorf("%w: side is old or new", errBadRequest)
}

// writeDiffImage answers with b, the image name, made safe to open
// directly: no sniffing, and no script in an SVG.
func writeDiffImage(w http.ResponseWriter, name string, b []byte) {
	ct, _ := diffImageType(name)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=60")
	_, _ = w.Write(b)
}

// fileAt reads name as rev has it, in repo.
func (s *Server) fileAt(ctx context.Context, projectID, repo, rev, name string) ([]byte, error) {
	blob, _ := s.Git.Blob(ctx, projectID, repo, rev, name)
	if blob == "" {
		return nil, fmt.Errorf("%w: %s is not in %s", store.ErrNotFound, name, rev)
	}
	b, size, err := s.Git.ReadBlob(ctx, projectID, repo, blob, maxDiffImage)
	if err != nil {
		return nil, err
	}
	if size > maxDiffImage {
		return nil, fmt.Errorf("%w: %s is larger than %d MiB", errBadRequest, name, maxDiffImage>>20)
	}
	return b, nil
}

// commitImage handles GET /api/projects/{pid}/commits/{sha}/image: an
// image the commit changed, as the commit has it (side=new) or its first
// parent (side=old).
func (s *Server) commitImage(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.project(w, r)
	if !ok {
		return
	}
	name, old, err := diffImageQuery(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	rev, err := s.Git.ResolveCommit(r.Context(), p.ID, p.Path, r.PathValue("sha"))
	if err != nil {
		writeErr(w, commitErr(err))
		return
	}
	if old {
		rev += "^"
	}
	b, err := s.fileAt(r.Context(), p.ID, p.Path, rev, name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeDiffImage(w, name, b)
}

// todoImage handles GET /api/todos/{tid}/image: an image the task changed,
// as its changes have it (side=new) or as they started from (side=old).
func (s *Server) todoImage(w http.ResponseWriter, r *http.Request) {
	sv, t, ok := s.todo(w, r)
	if !ok {
		return
	}
	name, old, err := diffImageQuery(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var b []byte
	if t.OnRunner() {
		side := "new"
		if old {
			side = "old"
		}
		b, err = sv.fetchFromRunner(r.Context(), t, "image?"+url.Values{"path": {name}, "side": {side}}.Encode())
	} else {
		b, err = sv.taskImage(r.Context(), t, name, old)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeDiffImage(w, name, b)
}

// hubTaskImage handles GET /api/hub/tasks/{htid}/image on a runner: the
// hub's todoImage for a task it sent here.
func (s *Server) hubTaskImage(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.fromHub(w, r)
	if !ok {
		return
	}
	t, ok := sv.hubTodo(r.PathValue("htid"))
	if !ok {
		writeErr(w, fmt.Errorf("%w: no such task from the hub", store.ErrNotFound))
		return
	}
	name, old, err := diffImageQuery(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	b, err := sv.taskImage(r.Context(), t, name, old)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeDiffImage(w, name, b)
}

// taskImage reads name from either side of t's changes, here: the same
// two commits todoChanges compares.
func (sv *service) taskImage(ctx context.Context, t model.Todo, name string, old bool) ([]byte, error) {
	if t.Worktree == nil {
		return nil, fmt.Errorf("%w: task has no changes yet", errBadRequest)
	}
	p, err := sv.data.GetProject(t.ProjectID)
	if err != nil {
		return nil, err
	}
	g := sv.s.Git
	var rev string
	switch {
	case t.Status == model.StatusMerged:
		if t.MergedCommit == "" {
			return nil, fmt.Errorf("%w: the task's merge commit is unknown", store.ErrNotFound)
		}
		rev = t.MergedCommit
		if old {
			rev += "^"
		}
	case old:
		if rev, err = g.MergeBase(ctx, p.ID, p.Path, p.For(t).Base(), t.Worktree.Branch); err != nil {
			return nil, err
		}
	default:
		rev = "refs/heads/" + t.Worktree.Branch
	}
	return sv.s.fileAt(ctx, p.ID, p.Path, rev, name)
}
