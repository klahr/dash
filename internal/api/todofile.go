package api

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// Editing a task's changes before approving it: the review pane opens a
// file as the task's branch has it, and saving commits the new content on
// top of that branch (in its worktree), so approving lands the edit with
// Claude's work. A task a hub sent to a runner is edited on the runner,
// through the hub (hubTaskFile).

// todoFileEdit is a saved edit: name's new content, made from the file at
// Blob on the task's branch ("" for a file the branch hasn't got).
type todoFileEdit struct {
	Path    string `json:"path"`
	Blob    string `json:"blob"`
	Content string `json:"content"`
}

// todoFileSaved answers a saved edit: the file's new blob, the commit that
// holds it and the task's change size afterwards.
type todoFileSaved struct {
	Path      string `json:"path"`
	Blob      string `json:"blob"`
	Commit    string `json:"commit"`
	DiffFiles int    `json:"diff_files"`
	DiffAdd   int    `json:"diff_add"`
	DiffDel   int    `json:"diff_del"`
}

// editableInReview refuses a task whose changes can't be edited now: only
// work waiting for a human (in review, or a failed merge) can be.
func editableInReview(t model.Todo) error {
	switch {
	case t.Reviewing:
		return store.ErrReviewing
	case t.Status != model.StatusReview && t.Status != model.StatusFailed:
		return fmt.Errorf("%w: only tasks in review (or a failed merge) can be edited", errBadRequest)
	case t.Worktree == nil:
		return fmt.Errorf("%w: this task has no changes to edit", errBadRequest)
	}
	return nil
}

// todoFile handles GET /api/todos/{tid}/file?path=: the file as the task's
// branch has it.
func (s *Server) todoFile(w http.ResponseWriter, r *http.Request) {
	sv, t, ok := s.todo(w, r)
	if !ok {
		return
	}
	name, err := cleanRepoPath(r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := editableInReview(t); err != nil {
		writeErr(w, err)
		return
	}
	var res fileContent
	if t.OnRunner() {
		err = sv.onRunner(r.Context(), t, func(ctx context.Context) error {
			return sv.s.Dispatch.TaskFile(ctx, t, name, &res)
		})
	} else {
		res, err = sv.worktreeFile(r.Context(), t, name)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// saveTodoFile handles PUT /api/todos/{tid}/file: commits an edit on the
// task's branch.
func (s *Server) saveTodoFile(w http.ResponseWriter, r *http.Request) {
	sv, t, ok := s.todo(w, r)
	if !ok {
		return
	}
	var req todoFileEdit
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := editableInReview(t); err != nil {
		writeErr(w, err)
		return
	}
	var (
		res todoFileSaved
		err error
	)
	if t.OnRunner() {
		if req.Path, err = cleanRepoPath(req.Path); err != nil {
			writeErr(w, err)
			return
		}
		err = sv.onRunner(r.Context(), t, func(ctx context.Context) error {
			return sv.s.Dispatch.SaveTaskFile(ctx, t, req, &res)
		})
		if err == nil {
			// The runner reports the task's new size too; show it right away.
			t, err = sv.data.UpdateTodo(t.ID, func(x *model.Todo) error {
				x.DiffFiles, x.DiffAdd, x.DiffDel = res.DiffFiles, res.DiffAdd, res.DiffDel
				return nil
			})
		}
	} else {
		res, err = sv.commitTodoFile(r.Context(), t, req)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// hubTaskFile handles GET and PUT /api/hub/tasks/{htid}/file on a runner:
// the hub's todoFile and saveTodoFile for a task it sent here.
func (s *Server) hubTaskFile(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.fromHub(w, r)
	if !ok {
		return
	}
	t, ok := sv.hubTodo(r.PathValue("htid"))
	if !ok {
		writeErr(w, fmt.Errorf("%w: no such task from the hub", store.ErrNotFound))
		return
	}
	if err := editableInReview(t); err != nil {
		writeErr(w, err)
		return
	}
	if r.Method == http.MethodGet {
		name, err := cleanRepoPath(r.URL.Query().Get("path"))
		if err != nil {
			writeErr(w, err)
			return
		}
		res, err := sv.worktreeFile(r.Context(), t, name)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	var req todoFileEdit
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	res, err := sv.commitTodoFile(r.Context(), t, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// worktreeFile reads name as t's branch has it, here.
func (sv *service) worktreeFile(ctx context.Context, t model.Todo, name string) (fileContent, error) {
	p, err := sv.data.GetProject(t.ProjectID)
	if err != nil {
		return fileContent{}, err
	}
	g, wt := sv.s.Git, t.Worktree
	blob, _ := g.Blob(ctx, p.ID, p.Path, "refs/heads/"+wt.Branch, name)
	if blob == "" {
		return fileContent{}, fmt.Errorf("%w: %s is not on %s", store.ErrNotFound, name, wt.Branch)
	}
	content, size, err := g.ReadBlob(ctx, p.ID, p.Path, blob, maxEditBytes)
	if err != nil {
		return fileContent{}, err
	}
	res := fileContent{Path: name, Blob: blob, Size: size, Branch: wt.Branch}
	res.setContent(content)
	return res, nil
}

// commitTodoFile commits e on t's branch, here, and updates t's worktree
// and change size to match. It refuses when the file changed on the branch
// since it was opened, or has uncommitted changes in the worktree.
func (sv *service) commitTodoFile(ctx context.Context, t model.Todo, e todoFileEdit) (todoFileSaved, error) {
	name, err := cleanRepoPath(e.Path)
	if err != nil {
		return todoFileSaved{}, err
	}
	if len(e.Content) > maxEditBytes {
		return todoFileSaved{}, fmt.Errorf("%w: file is larger than %d KiB", errBadRequest, maxEditBytes>>10)
	}
	p, err := sv.data.GetProject(t.ProjectID)
	if err != nil {
		return todoFileSaved{}, err
	}
	g, wt := sv.s.Git, t.Worktree
	if st, err := os.Stat(wt.Path); err != nil || !st.IsDir() {
		return todoFileSaved{}, fmt.Errorf("%w: this task's worktree is gone; retry it instead", errBadRequest)
	}
	if b, err := g.CurrentBranch(ctx, p.ID, wt.Path); err != nil || b != wt.Branch {
		return todoFileSaved{}, fmt.Errorf("%w: the task's worktree isn't on %s", errConflict, wt.Branch)
	}
	head, err := g.Head(ctx, p.ID, wt.Path, "HEAD")
	if err != nil {
		return todoFileSaved{}, err
	}
	if cur, _ := g.Blob(ctx, p.ID, wt.Path, head, name); cur != e.Blob {
		return todoFileSaved{}, fmt.Errorf("%w: %s changed on %s since it was opened; reload it and edit again", errConflict, name, wt.Branch)
	}
	if dirty, err := g.Dirty(ctx, p.ID, wt.Path, []string{name}); err != nil {
		return todoFileSaved{}, err
	} else if len(dirty) > 0 {
		return todoFileSaved{}, fmt.Errorf("%w: %s has uncommitted changes in the task's worktree", errConflict, name)
	}
	// Check again just before committing: the task may have been approved
	// or sent back meanwhile.
	if cur, err := sv.data.GetTodo(t.ID); err != nil {
		return todoFileSaved{}, err
	} else if err := editableInReview(cur); err != nil {
		return todoFileSaved{}, err
	}
	commit, blob, err := g.CommitFile(ctx, p.ID, wt.Path, head, name, e.Content, "Edit "+name+" in review")
	if err != nil {
		return todoFileSaved{}, err
	}
	if err := g.FastForward(ctx, p.ID, wt.Path, commit); err != nil {
		return todoFileSaved{}, err
	}
	res := todoFileSaved{Path: name, Blob: blob, Commit: commit}
	if files, add, del, err := g.DiffStat(ctx, p.ID, wt.Path, p.For(t).Base()); err == nil {
		res.DiffFiles, res.DiffAdd, res.DiffDel = files, add, del
		_, _ = sv.data.UpdateTodo(t.ID, func(x *model.Todo) error {
			x.DiffFiles, x.DiffAdd, x.DiffDel = files, add, del
			return nil
		})
	}
	return res, nil
}
