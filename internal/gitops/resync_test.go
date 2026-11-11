package gitops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func coreBare(t *testing.T, repo string) string {
	t.Helper()
	return gitOut(t, repo, "config", "--file", filepath.Join(repo, ".git", "config"), "core.bare")
}

// RepairBare puts core.bare back on a clone with a working tree, and leaves
// a real bare repository, and a clone that is just its .git, alone.
func TestRepairBare(t *testing.T) {
	t.Parallel()

	ctx, g := context.Background(), New(nil)
	clone := filepath.Join(t.TempDir(), "clone")
	gitOut(t, filepath.Dir(clone), "init", "-q", "-b", "main", clone)
	if err := os.WriteFile(filepath.Join(clone, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, clone, "add", "a.txt")
	gitOut(t, clone, "commit", "-q", "-m", "a")

	if fixed, err := g.RepairBare(ctx, "p", clone); err != nil || fixed {
		t.Fatalf("a healthy clone: %v %v", fixed, err)
	}
	gitOut(t, clone, "config", "core.bare", "true")
	if fixed, err := g.RepairBare(ctx, "p", clone); err != nil || !fixed {
		t.Fatalf("core.bare = true: %v %v", fixed, err)
	}
	if got := coreBare(t, clone); got != "false" {
		t.Fatalf("core.bare = %q", got)
	}
	if _, _, err := g.CheckoutState(ctx, "p", clone); err != nil {
		t.Fatalf("the checkout still can't be read: %v", err)
	}

	bare := filepath.Join(t.TempDir(), "origin.git")
	gitOut(t, filepath.Dir(bare), "init", "-q", "--bare", bare)
	if fixed, _ := g.RepairBare(ctx, "p", bare); fixed {
		t.Fatal("a bare repository was made non-bare")
	}
	empty := filepath.Join(t.TempDir(), "empty")
	gitOut(t, filepath.Dir(empty), "init", "-q", empty)
	gitOut(t, empty, "config", "core.bare", "true")
	if fixed, _ := g.RepairBare(ctx, "p", empty); fixed {
		t.Fatal("a folder with nothing but .git counted as a working tree")
	}
}

// With the branch not checked out, ResetToOrigin moves only the ref and
// saves its local commits; the checkout (another branch) stays as it is.
func TestResetToOriginBranchNotCheckedOut(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	g, repo, other := remoteRepos(t)
	commitIn(t, other, "theirs.txt")
	gitOut(t, other, "push", "-q", "origin", "main")
	mine := commitIn(t, repo, "mine.txt")
	gitOut(t, repo, "checkout", "-q", "-b", "side")
	if err := os.WriteFile(filepath.Join(repo, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.FetchBranch(ctx, "p", repo, "main", false); err != nil {
		t.Fatal(err)
	}

	b, err := g.ResetToOrigin(ctx, "p", repo, "main", "dash-backup/x")
	if err != nil {
		t.Fatal(err)
	}
	if b.Branch != "dash-backup/x" || b.Commits != 1 || b.Dirty {
		t.Fatalf("backup = %+v", b)
	}
	if got := gitOut(t, repo, "rev-parse", "dash-backup/x"); got != mine {
		t.Fatalf("backup at %s; want %s", got, mine)
	}
	if got, want := gitOut(t, repo, "rev-parse", "main"), gitOut(t, other, "rev-parse", "main"); got != want {
		t.Fatalf("main = %s; want origin's %s", got, want)
	}
	if got := gitOut(t, repo, "symbolic-ref", "--short", "HEAD"); got != "side" {
		t.Fatalf("checkout moved to %s", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "wip.txt")); err != nil {
		t.Fatal("the other branch's untracked file is gone")
	}
}

// Dash's own git finds the repository from the folder it runs in, even
// when this process inherited GIT_DIR naming another one (a worktree's, as
// a git hook would set it): there, `git init` would set core.bare = true in
// that worktree's main clone.
func TestGitIgnoresInheritedGitDir(t *testing.T) {
	ctx, g := context.Background(), New(nil)
	root := t.TempDir()
	clone, wt := filepath.Join(root, "clone"), filepath.Join(root, "wt")
	gitOut(t, root, "init", "-q", "-b", "main", clone)
	gitOut(t, clone, "commit", "-q", "--allow-empty", "-m", "first")
	gitOut(t, clone, "worktree", "add", "-q", "-b", "dash/task", wt)
	t.Setenv("GIT_DIR", filepath.Join(clone, ".git", "worktrees", "wt"))

	repo := filepath.Join(t.TempDir(), "new")
	if err := g.InitRepo(ctx, "p", repo, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		t.Fatalf("InitRepo worked on GIT_DIR's repository instead: %v", err)
	}
	if got := coreBare(t, clone); got != "false" {
		t.Fatalf("the clone's core.bare = %q", got)
	}
}
