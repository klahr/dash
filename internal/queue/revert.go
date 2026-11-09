package queue

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// revert undoes a merged todo: a revert commit on the default branch (then
// pushed), or in "pr" mode a pull request with the revert. When later work
// makes `git revert` conflict, Claude is given a todo to undo it by hand.
func (m *MergeQueue) revert(ctx context.Context, job model.MergeJob) {
	t, err := m.Store.GetTodo(job.TodoID)
	if err != nil || t.Status != model.StatusMerged || t.Revert != nil {
		return // gone, or a duplicate request
	}
	p, err := m.Store.GetProject(t.ProjectID)
	if err != nil {
		return
	}
	p = p.For(t) // it landed on the branch it started from
	fail := func(err error) {
		m.Log.Line(t, "✖ revert: "+err.Error())
		_, _ = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error { x.Error = "Revert failed: " + err.Error(); return nil })
	}
	m.Log.Line(t, "↶ reverting")
	if len(t.RevertConflict) > 0 {
		_, _ = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error { x.RevertConflict = nil; return nil })
	}
	sync := func() bool {
		if !p.SyncsRemote() {
			return true
		}
		if err := m.syncRemote(ctx, p); errors.As(err, new(*gitops.ErrDirtyCheckout)) {
			m.wait(ctx, job, err)
			return false
		} else if err != nil {
			fail(err)
			return false
		}
		return true
	}
	if !sync() {
		return
	}
	sha := t.MergedCommit
	if sha == "" {
		if sha, err = m.Git.FindTodoCommit(ctx, p.ID, p.Path, p.Base(), t.ID); err != nil {
			fail(err)
			return
		}
	}
	msg := fmt.Sprintf("Revert \"%s\"\n\nThis reverts commit %s.\n\nDash-Revert: %s", subject(t), sha, t.ID)

	if p.LandsOnOrigin() {
		// Like a merge: pushed before it counts, redone on top of origin
		// when origin moved meanwhile.
		for attempt := 1; ; attempt++ {
			rsha, err := m.landPushed(ctx, p, func(dir string) (string, error) {
				return m.Git.RevertCommit(ctx, p.ID, dir, sha, msg)
			})
			var conflict *gitops.ErrConflict
			var moved *originMoved
			switch {
			case errors.As(err, &conflict):
				m.revertByClaude(t, p, sha, conflict.Files)
			case errors.As(err, new(*gitops.ErrDirtyCheckout)):
				m.wait(ctx, job, err)
			case errors.As(err, &moved) && attempt < maxLandAttempts:
				m.Log.Line(t, "↻ "+moved.Error()+"; reverting again on top of it")
				if sync() {
					continue
				}
			case err != nil:
				fail(err)
			default:
				m.reverted(t, &model.Revert{Commit: rsha, At: time.Now()}, "↶ reverted in "+short(rsha)+" and pushed")
			}
			return
		}
	}
	if p.UsesPR() {
		m.revertPR(ctx, t, p, sha, msg, fail)
		return
	}
	var rsha string
	_, err = m.land(ctx, p, func(dir string) (err error) {
		rsha, err = m.Git.RevertCommit(ctx, p.ID, dir, sha, msg)
		return err
	})
	var conflict *gitops.ErrConflict
	if errors.As(err, &conflict) {
		m.revertByClaude(t, p, sha, conflict.Files)
		return
	}
	if errors.As(err, new(*gitops.ErrDirtyCheckout)) {
		m.wait(ctx, job, err)
		return
	}
	if err != nil {
		fail(err)
		return
	}
	if p.AutoPush && p.RemoteURL != "" {
		m.pushDefault(ctx, p, t)
	}
	m.reverted(t, &model.Revert{Commit: rsha, At: time.Now()}, "↶ reverted in "+short(rsha))
}

func (m *MergeQueue) revertPR(ctx context.Context, t model.Todo, p model.Project, sha, msg string, fail func(error)) {
	wt := model.Worktree{
		Name: "revert-" + t.ID, Path: filepath.Join(m.WorktreeRoot, p.ID, "revert-"+t.ID), Branch: "dash/revert-" + t.ID,
	}
	if err := m.Git.AddWorktreeAt(ctx, p.ID, p.Path, wt.Path, wt.Branch, p.Base()); err != nil {
		fail(err)
		return
	}
	defer func() { _ = m.Git.RemoveWorktree(ctx, p.ID, p.Path, wt) }() // the pushed branch lives on in the PR
	_, err := m.Git.RevertCommit(ctx, p.ID, wt.Path, sha, msg)
	var conflict *gitops.ErrConflict
	if errors.As(err, &conflict) {
		m.revertByClaude(t, p, sha, conflict.Files)
		return
	}
	if err != nil {
		fail(err)
		return
	}
	if err := m.Git.PushBranch(ctx, p.ID, wt.Path, wt.Branch); err != nil {
		fail(err)
		return
	}
	n, url, err := m.Git.OpenPR(ctx, p.ID, wt.Path, p.Base(), wt.Branch, "Revert: "+subject(t),
		fmt.Sprintf("Reverts %s, which landed \"%s\".\n\n---\nOpened by Dash.", sha, subject(t)))
	if err != nil {
		fail(err)
		return
	}
	m.reverted(t, &model.Revert{PRURL: url, At: time.Now()}, fmt.Sprintf("↶ revert pull request #%d is open: %s", n, url))
}

func (m *MergeQueue) reverted(t model.Todo, r *model.Revert, line string) {
	m.Log.Line(t, line)
	_, _ = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error { x.Revert, x.Error = r, ""; return nil })
}

// revertByClaude queues a todo asking Claude to undo the change by hand. A
// hub's task on a runner only records the conflict: its hub, which the
// people reviewing the work look at, queues that todo (see UndoTodo).
func (m *MergeQueue) revertByClaude(t model.Todo, p model.Project, sha string, files []string) {
	if t.HubTodoID != "" {
		m.Log.Line(t, "⚠ git revert conflicts with later work in "+strings.Join(files, ", ")+"; the hub queues a task for Claude to undo it by hand")
		_, _ = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
			x.RevertConflict = files
			x.Error = "Reverting conflicts with later work in " + strings.Join(files, ", ")
			return nil
		})
		return
	}
	nt, err := UndoTodo(m.Store, t, sha, files)
	if err != nil {
		m.Log.Line(t, "✖ revert: "+err.Error())
		return
	}
	m.Log.Line(t, "⚠ git revert conflicts with later work in "+strings.Join(files, ", ")+"; queued \""+nt.Title+"\" for Claude")
	if m.Kick != nil {
		m.Kick()
	}
}

// UndoTodo queues a todo in t's project asking Claude to undo t, which
// landed as commit sha, by hand, since reverting it conflicts in files, and
// notes it on t.
func UndoTodo(st *store.Store, t model.Todo, sha string, files []string) (model.Todo, error) {
	prompt := fmt.Sprintf("Undo the change an earlier task made, which landed as commit %s (\"%s\"). "+
		"`git revert` conflicts because later work changed the same code (%s), so do it by hand: remove that "+
		"change's behaviour while keeping everything added since working. Run `git show %s` to see the "+
		"change.\n\nThe earlier task was:\n\n%s", short(sha), subject(t), strings.Join(files, ", "), sha, quote(t.Prompt))
	nt, err := st.CreateTodo(model.Todo{ProjectID: t.ProjectID, CreatedBy: t.CreatedBy, Title: "Undo: " + subject(t), Prompt: prompt})
	if err == nil {
		nt, err = st.SetStatus(nt.ID, model.StatusQueued, "")
	}
	if err != nil {
		return nt, err
	}
	_, _ = st.UpdateTodo(t.ID, func(x *model.Todo) error {
		x.Error = "Reverting conflicts with later work, so Claude got a task to undo it by hand: " + nt.Title
		return nil
	})
	return nt, nil
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func quote(s string) string {
	return "> " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n> ")
}
