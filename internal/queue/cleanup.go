package queue

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
)

// CleanupReport says what a cleanup removed.
type CleanupReport struct {
	Worktrees int   `json:"worktrees"`
	Branches  int   `json:"branches"`
	Logs      int   `json:"logs"`
	Uploads   int   `json:"uploads"`
	Bytes     int64 `json:"bytes"`
}

func (r CleanupReport) Empty() bool { return r.Worktrees+r.Branches+r.Logs+r.Uploads == 0 }

func (r CleanupReport) String() string {
	if r.Empty() {
		return "nothing to clean up"
	}
	var parts []string
	for _, x := range []struct {
		n    int
		what string
	}{{r.Worktrees, "worktree"}, {r.Branches, "branch"}, {r.Logs, "log"}, {r.Uploads, "upload"}} {
		if x.n == 1 {
			parts = append(parts, "1 "+x.what)
		} else if x.n > 1 {
			plural := x.what + "s"
			if x.what == "branch" {
				plural = "branches"
			}
			parts = append(parts, fmt.Sprintf("%d %s", x.n, plural))
		}
	}
	return fmt.Sprintf("removed %s (%s)", strings.Join(parts, ", "), humanBytes(r.Bytes))
}

// Cleanup tidies one project on the merge worker (it touches the main
// checkout's worktree list and branches) and reports what it removed.
func (m *MergeQueue) Cleanup(ctx context.Context, projectID string) (CleanupReport, error) {
	var rep CleanupReport
	var err error
	if derr := m.Do(ctx, func(c context.Context) {
		var p model.Project
		if p, err = m.Store.GetProject(projectID); err == nil {
			rep, err = m.cleanup(c, p)
		}
	}); derr != nil {
		return rep, derr
	}
	return rep, err
}

// cleanup removes, for one project:
//   - worktrees of failed todos nobody touched for CleanupAfter (a retry
//     then starts from scratch)
//   - worktree directories and dash/ branches no todo uses any more
//
// and, for all projects, logs of deleted todos and uploads no todo uses.
// Directory and branch listings are read before the todos, so work that
// starts meanwhile is never mistaken for an orphan.
func (m *MergeQueue) cleanup(ctx context.Context, p model.Project) (CleanupReport, error) {
	var r CleanupReport
	m.Git.PruneWorktrees(ctx, p.ID, p.Path)
	root := filepath.Join(m.WorktreeRoot, p.ID)
	dirs, _ := os.ReadDir(root)
	branches, err := m.Git.Branches(ctx, p.ID, p.Path, "dash/")
	if err != nil {
		return r, err
	}
	todos := m.Store.ListTodos(p.ID)

	inUse := map[string]bool{m.landingDir(p): true} // worktree paths and branches still needed
	for _, t := range todos {
		if t.Worktree == nil || t.Status == model.StatusMerged {
			continue
		}
		stale := m.CleanupAfter > 0 && t.Status == model.StatusFailed && time.Since(t.UpdatedAt) > m.CleanupAfter &&
			(t.PR == nil || t.PR.State != "OPEN")
		if !stale {
			inUse[t.Worktree.Path], inUse[t.Worktree.Branch] = true, true
			continue
		}
		size := dirSize(t.Worktree.Path)
		if err := m.Git.RemoveWorktree(ctx, p.ID, p.Path, *t.Worktree); err != nil {
			inUse[t.Worktree.Path], inUse[t.Worktree.Branch] = true, true
			continue
		}
		r.Worktrees++
		r.Bytes += size
		inUse[t.Worktree.Path] = true // handled; not an orphan below
		m.Log.Line(t, fmt.Sprintf("🧹 worktree removed after %s unused; a retry starts from scratch", humanDuration(m.CleanupAfter)))
		_, _ = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
			x.Worktree, x.SessionID, x.FeedbackSent, x.MergeMain = nil, "", 0, false
			x.Checks, x.DiffFiles, x.DiffAdd, x.DiffDel = nil, 0, 0, 0
			return nil
		})
	}

	removedDir := false
	for _, d := range dirs {
		path := filepath.Join(root, d.Name())
		if !d.IsDir() || inUse[path] {
			continue
		}
		size := dirSize(path)
		if os.RemoveAll(path) == nil {
			r.Worktrees++
			r.Bytes += size
			removedDir = true
		}
	}
	if removedDir {
		m.Git.PruneWorktrees(ctx, p.ID, p.Path)
	}
	for _, b := range branches {
		if !inUse[b] && m.Git.DeleteBranch(ctx, p.ID, p.Path, b) == nil {
			r.Branches++
		}
	}

	g := m.cleanupFiles()
	r.Logs, r.Uploads, r.Bytes = g.Logs, g.Uploads, r.Bytes+g.Bytes
	return r, nil
}

// cleanupFiles removes logs of todos that no longer exist and uploads no
// todo, chat or project icon refers to (after a day, so a todo being written can still use them).
func (m *MergeQueue) cleanupFiles() CleanupReport {
	var r CleanupReport
	var logs, uploads []fs.DirEntry
	if m.Log != nil && m.Log.Root != "" {
		logs, _ = os.ReadDir(m.Log.Root)
	}
	if m.UploadRoot != "" {
		uploads, _ = os.ReadDir(m.UploadRoot)
	}
	todos, used := map[string]bool{}, map[string]bool{}
	useChat := func(msgs []model.ChatMessage) {
		for _, msg := range msgs {
			for _, a := range msg.Attachments {
				used[a.ID] = true
			}
		}
	}
	useChat(m.Store.Chat())
	for _, p := range m.Store.ListProjects() {
		useChat(m.Store.ProjectChat(p.ID))
		if id, _, ok := strings.Cut(p.Icon, "/"); ok {
			used[id] = true
		}
	}
	for _, t := range m.Store.ListTodos("") {
		todos[t.ID] = true
		for _, a := range t.Attachments {
			used[a.ID] = true
		}
		useChat(m.Store.TodoChat(t.ID))
	}
	for _, f := range logs {
		id, ok := strings.CutSuffix(f.Name(), ".log")
		if !ok || todos[id] {
			continue
		}
		path := filepath.Join(m.Log.Root, f.Name())
		size := dirSize(path)
		if os.Remove(path) == nil {
			r.Logs++
			r.Bytes += size
		}
	}
	for _, d := range uploads {
		info, err := d.Info()
		if err != nil || used[d.Name()] || time.Since(info.ModTime()) < 24*time.Hour {
			continue
		}
		path := filepath.Join(m.UploadRoot, d.Name())
		size := dirSize(path)
		if os.RemoveAll(path) == nil {
			r.Uploads++
			r.Bytes += size
		}
	}
	return r
}

func dirSize(path string) int64 {
	var n int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

func humanDuration(d time.Duration) string {
	if days := int(d.Hours() / 24); days >= 1 {
		return fmt.Sprintf("%d days", days)
	}
	return d.String()
}
