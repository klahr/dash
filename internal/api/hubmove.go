package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/model"
)

// A runner keeps its clones of a hub's projects in the org's folder,
// <projects root>/<org folder>/<repo> (see hubPlace). Clones made before
// that, straight in the projects root, move there once: at the runner's
// start (MoveHubClones) and at the hub's next link of the project, which
// comes before every task it sends and with every look at the project's
// files, branches or commands (hubLink). Only clones Dash made
// (model.HubCloned) move: an adopted checkout, or one under the old hub/
// folder, may be its owner's own and stays where it is. A busy clone (a
// task running or merging, a preview, a try session or a command running
// in it) waits for the next start or link. The org's folder is known once
// the hub has linked the project since the upgrade (model.Project's
// HubOrgFolder); until then the clone stays where it is.

// hubMoveTimeout bounds one move: a rename, and git worktree repair.
const hubMoveTimeout = 2 * time.Minute

// MoveHubClones moves each of the hub's clones on this runner that Dash
// made, and that isn't in its org's folder yet, into it. It runs at the
// runner's start, before tasks and merges resume; running it again
// changes nothing.
func (s *Server) MoveHubClones(ctx context.Context) {
	if s.ProjectRoot == "" || s.Store == nil {
		return
	}
	org, err := s.Store.Accounts().DefaultOrg()
	if err != nil {
		slog.Warn("runner: moving the hub's clones into their org's folder", "err", err)
		return
	}
	sv := s.as(Caller{Org: org.ID, OrgFolder: folderOf(org), PlatformAdmin: true, Machine: true})
	for _, p := range sv.data.ListProjects() {
		if p.HubProjectID != "" {
			s.moveHubClone(ctx, sv, p)
		}
	}
}

// hubMoveNoted are the projects whose clone stays put for good (adopted,
// legacy) that were logged already, so each is logged once per run.
var hubMoveNoted sync.Map

// afterHubMove, when set (tests), runs after a clone is moved and its
// worktrees repaired, before the store has its new path; an error undoes
// the move.
var afterHubMove func(from, to string) error

// moveHubClone moves p, a clone of one of the hub's projects, into its
// org's folder when it should go there and can now, and returns p as it is
// then: with its new Path when it moved, else as it was. A move that fails
// part way is undone, and p keeps working from where it was.
func (s *Server) moveHubClone(ctx context.Context, sv *service, p model.Project) model.Project {
	hubLinks.Lock() // links pick folders in the org's folder too
	defer hubLinks.Unlock()
	cur, err := sv.data.GetProject(p.ID)
	if err != nil {
		return p
	}
	p = cur
	if p.HubProjectID == "" || p.HubOrgFolder == "" || s.ProjectRoot == "" {
		return p // not the hub's, or its org's folder not known yet
	}
	dir, err := s.hubOrgDir(p.HubOrgFolder)
	if err != nil {
		slog.Warn("runner: can't move the hub's clone into its org's folder", "project", p.Name, "path", p.Path, "err", err)
		return p
	}
	if within(dir, p.Path) && filepath.Clean(p.Path) != dir {
		return p // in place
	}
	if p.HubCheckout != model.HubCloned {
		if _, seen := hubMoveNoted.LoadOrStore(p.ID, true); !seen {
			slog.Info("runner: the hub's project stays where it is: Dash didn't clone it there, it may be your own checkout",
				"project", p.Name, "path", p.Path, "checkout", p.HubCheckout)
		}
		return p
	}
	defer tryLock(p.ID)() // no try session starts while it moves
	if why := s.hubCloneBusy(sv, p); why != "" {
		slog.Info("runner: moving the hub's clone into its org's folder waits until it is idle", "project", p.Name, "path", p.Path, "busy", why)
		return p
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hubMoveTimeout)
	defer cancel()
	moved, err := s.moveClone(ctx, sv, p, dir)
	if err != nil {
		slog.Warn("runner: couldn't move the hub's clone into its org's folder; it stays where it is", "project", p.Name, "path", p.Path, "err", err)
		return p
	}
	slog.Info(fmt.Sprintf("runner: moved %s → %s", p.Path, moved.Path), "project", p.Name)
	return moved
}

// hubCloneBusy says what is using p's clone now ("" when nothing is).
func (s *Server) hubCloneBusy(sv *service, p model.Project) string {
	for _, t := range sv.data.ListTodos(p.ID) {
		if runningTask(t) {
			return fmt.Sprintf("task %q is %s", t.Title, t.Status)
		}
	}
	if s.Procs != nil {
		for _, x := range s.Procs.List(p.ID) {
			if x.Running {
				return "a command is running in it"
			}
		}
	}
	if s.Previews != nil {
		for _, pv := range s.Previews.List() {
			if pv.ProjectID == p.ID {
				return "a preview is running"
			}
		}
		if _, ok := s.Previews.Try(p.HubProjectID); ok {
			return "a try session is running"
		}
	}
	return ""
}

// moveClone moves p's checkout into dir, under the first free name of
// <repo>, <repo>-2, …, repairs its worktrees' links and stores the new
// paths. On any error it puts everything back as it was.
func (s *Server) moveClone(ctx context.Context, sv *service, p model.Project, dir string) (model.Project, error) {
	from := filepath.Clean(p.Path)
	if fi, err := os.Lstat(from); err != nil || !fi.IsDir() || !isRepo(from) {
		return p, fmt.Errorf("%s isn't a git checkout", from)
	}
	to, err := freeHubFolder(sv, dir, hubBase(p.Name, p.RemoteURL))
	if err != nil {
		return p, err
	}
	_, statErr := os.Lstat(dir)
	madeDir := errors.Is(statErr, fs.ErrNotExist)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return p, err
	}
	cleanDir := func() {
		if madeDir {
			_ = os.Remove(dir) // only if still empty
		}
	}
	// Worktrees (the tasks' are elsewhere, but a stored one may be inside
	// the checkout) whose folders move with it.
	inside := func(path string) bool { return path != "" && within(from, path) && filepath.Clean(path) != from }
	movedPath := func(path string) string {
		rel, _ := filepath.Rel(from, filepath.Clean(path))
		return filepath.Join(to, rel)
	}
	known, _ := s.Git.WorktreePaths(ctx, p.ID, from)
	var oldInside, newInside []string
	for _, w := range known {
		if inside(w) {
			oldInside, newInside = append(oldInside, w), append(newInside, movedPath(w))
		}
	}

	if err := os.Rename(from, to); err != nil {
		cleanDir()
		return p, fmt.Errorf("moving %s to %s: %w", from, to, err)
	}
	undo := func(cause error) error {
		if err := os.Rename(to, from); err != nil {
			return fmt.Errorf("%w; and moving it back from %s failed: %v", cause, to, err)
		}
		if err := s.Git.RepairWorktrees(ctx, p.ID, from, oldInside...); err != nil {
			slog.Warn("runner: repairing worktrees after moving a clone back", "path", from, "err", err)
		}
		cleanDir()
		return cause
	}
	if err := s.Git.RepairWorktrees(ctx, p.ID, to, newInside...); err != nil {
		return p, undo(fmt.Errorf("git worktree repair: %w", err))
	}
	// Each worktree git knows must work from the new place.
	worktrees, err := s.Git.WorktreePaths(ctx, p.ID, to)
	if err != nil {
		return p, undo(err)
	}
	for _, w := range worktrees {
		if _, err := os.Stat(w); err != nil {
			continue // gone already: git prunes it
		}
		if _, err := s.Git.Head(ctx, p.ID, w, "HEAD"); err != nil {
			return p, undo(fmt.Errorf("worktree %s doesn't work after the move: %w", w, err))
		}
	}
	if afterHubMove != nil {
		if err := afterHubMove(from, to); err != nil {
			return p, undo(err)
		}
	}

	// The store: the project's path, then any worktree stored inside it.
	moved, err := sv.data.UpdateProject(p.ID, func(x *model.Project) error { x.Path = to; return nil })
	if err != nil {
		return p, undo(err)
	}
	var fixed []string // todos whose worktree path changed
	restore := func(cause error) error {
		for _, id := range fixed {
			_, _ = sv.data.UpdateTodo(id, func(t *model.Todo) error {
				if t.Worktree != nil && within(to, t.Worktree.Path) {
					rel, _ := filepath.Rel(to, t.Worktree.Path)
					t.Worktree.Path = filepath.Join(from, rel)
				}
				return nil
			})
		}
		_, _ = sv.data.UpdateProject(p.ID, func(x *model.Project) error { x.Path = p.Path; return nil })
		return undo(cause)
	}
	for _, t := range sv.data.ListTodos(p.ID) {
		if t.Worktree == nil || !inside(t.Worktree.Path) {
			continue
		}
		if _, err := sv.data.UpdateTodo(t.ID, func(x *model.Todo) error {
			if x.Worktree != nil && inside(x.Worktree.Path) {
				x.Worktree.Path = movedPath(x.Worktree.Path)
			}
			return nil
		}); err != nil {
			return p, restore(err)
		}
		fixed = append(fixed, t.ID)
	}
	return moved, nil
}

// freeHubFolder is the first of dir/<base>, dir/<base>-2, … that nothing
// is in and no project has.
func freeHubFolder(sv *service, dir, base string) (string, error) {
	taken := map[string]bool{}
	for _, p := range sv.data.ListProjects() {
		taken[filepath.Clean(p.Path)] = true
	}
	for i := 1; i <= maxHubSuffix; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		path := filepath.Join(dir, name)
		if filepath.Dir(path) != dir || strings.HasPrefix(name, ".") {
			return "", fmt.Errorf("bad folder name %q", name)
		}
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) && !taken[path] {
			return path, nil
		}
	}
	return "", fmt.Errorf("no free folder for %s in %s (%s through %s-%d are taken)", base, dir, base, base, maxHubSuffix)
}
