package gitops

import (
	"context"
	"os"
	"path/filepath"
)

// RepairBare sets core.bare back to false in repo when it is plainly a
// working tree: its .git is a directory with files beside it. Git never
// sets core.bare on such a clone; a stray `git init` with GIT_DIR naming
// one of its worktrees does (see localrun.RepoEnv), and then every command
// in the folder fails with "this operation must be run in a work tree".
// It reports whether it changed anything.
func (g *Git) RepairBare(ctx context.Context, projectID, repo string) (bool, error) {
	dotgit := filepath.Join(repo, ".git")
	if fi, err := os.Stat(dotgit); err != nil || !fi.IsDir() {
		return false, nil
	}
	cfg := filepath.Join(dotgit, "config")
	if out, err := g.run(ctx, false, projectID, repo, "git", "config", "--file", cfg, "--type=bool", "core.bare"); err != nil || out != "true" {
		return false, nil // false, or not set
	}
	ents, err := os.ReadDir(repo)
	if err != nil || len(ents) < 2 {
		return false, err // nothing but .git: no working tree to speak of
	}
	if _, err := g.git(ctx, projectID, repo, "config", "--file", cfg, "core.bare", "false"); err != nil {
		return false, err
	}
	return true, nil
}

// Backup is what ResetToOrigin saved before it moved a branch.
type Backup struct {
	Branch  string // the branch holding it ("" = nothing needed saving)
	Commits int    // local commits origin didn't have
	Dirty   bool   // uncommitted changes, in one more commit on top
}

// ResetToOrigin points repo's branch at origin/<branch> (as of the last
// fetch) whatever is in the way, after saving it on a new branch named
// backup: the branch's commits origin doesn't have and, when repo has
// branch checked out, its uncommitted changes and untracked files (ignored
// ones stay as they are) as one more commit. The checkout's files then
// match origin's. Only repo's own checkout and branch move: worktrees and
// their branches stay as they are. Meant for a clone only Dash works in
// (see queue.RemoteSync), where nothing is anyone's work in progress.
func (g *Git) ResetToOrigin(ctx context.Context, projectID, repo, branch, backup string) (Backup, error) {
	var b Backup
	ref := "refs/heads/" + branch
	tip, err := g.run(ctx, false, projectID, repo, "git", "rev-parse", "--verify", ref)
	if err != nil {
		return b, err
	}
	target, err := g.run(ctx, false, projectID, repo, "git", "rev-parse", "--verify", "refs/remotes/origin/"+branch)
	if err != nil {
		return b, err
	}
	ahead, _, err := g.AheadBehind(ctx, projectID, repo, branch)
	if err != nil {
		return b, err
	}
	checkedOut := g.CheckedOut(ctx, projectID, repo) == branch
	save := tip
	if checkedOut {
		snap, err := g.snapshot(ctx, projectID, repo, tip, "Dash backup: uncommitted changes in "+repo)
		if err != nil {
			return b, err
		}
		if snap != "" {
			save, b.Dirty = snap, true
		}
	}
	if ahead > 0 || b.Dirty {
		if _, err := g.git(ctx, projectID, repo, "branch", "--no-track", backup, save); err != nil {
			return b, err
		}
		b.Branch, b.Commits = backup, ahead
	}
	if !checkedOut {
		_, err := g.git(ctx, projectID, repo, "update-ref", "-m", "dash: reset to origin", ref, target, tip)
		return b, err
	}
	if _, err := g.git(ctx, projectID, repo, "reset", "--hard", "-q", target); err != nil {
		return b, err
	}
	if b.Dirty {
		_, err = g.git(ctx, projectID, repo, "clean", "-fdq") // untracked files; the backup has them
	}
	return b, err
}

// snapshot commits dir's working tree (tracked changes and untracked files
// that aren't ignored) on top of parent without touching its index, HEAD
// or files. "" when there is nothing to commit.
func (g *Git) snapshot(ctx context.Context, projectID, dir, parent, msg string) (string, error) {
	st, err := g.run(ctx, false, projectID, dir, "git", "status", "--porcelain")
	if err != nil || st == "" {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "dash-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}
	if _, err := g.plumb(ctx, dir, env, nil, "read-tree", parent); err != nil {
		return "", err
	}
	if _, err := g.plumb(ctx, dir, env, nil, "add", "-A"); err != nil {
		return "", err
	}
	tree, err := g.plumb(ctx, dir, env, nil, "write-tree")
	if err != nil {
		return "", err
	}
	return g.git(ctx, projectID, dir, g.committing(ctx, dir, "commit-tree", tree, "-p", parent, "-m", msg)...)
}
