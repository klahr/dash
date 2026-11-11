package testgit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain isolates this package's tests too: they set up their clones with
// git before calling Isolate themselves.
func TestMain(m *testing.M) { os.Exit(Run(m)) }

// cloneWithWorktree makes a clone with a commit and a task's worktree of
// it, as a runner has them, and returns the clone and the worktree's git
// directory (what GIT_DIR names in a hook run in the worktree).
func cloneWithWorktree(t *testing.T) (clone, wtGitDir string) {
	t.Helper()
	root := t.TempDir()
	clone = filepath.Join(root, "clone")
	git(t, root, "init", "-q", "-b", "main", clone)
	git(t, clone, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "first")
	git(t, clone, "worktree", "add", "-q", "-b", "dash/todo-1", filepath.Join(root, "wt"))
	return clone, filepath.Join(clone, ".git", "worktrees", "wt")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// keepEnv puts back, after the test, the variables Isolate changes.
func keepEnv(t *testing.T) {
	for _, v := range []string{"GIT_DIR", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM", "GIT_CEILING_DIRECTORIES"} {
		old, ok := os.LookupEnv(v)
		t.Cleanup(func() {
			if ok {
				os.Setenv(v, old)
			} else {
				os.Unsetenv(v)
			}
		})
	}
}

func bare(t *testing.T, clone string) string {
	t.Helper()
	cmd := exec.Command("git", "config", "--file", filepath.Join(clone, ".git", "config"), "core.bare")
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

// A GIT_DIR inherited from a task's worktree (a hook, rebase --exec) made
// a test's `git init` of a temporary repository rewrite the runner's clone
// to core.bare = true. After Isolate it can't.
func TestIsolateKeepsTestsOffTheClone(t *testing.T) {
	keepEnv(t)
	clone, wtGitDir := cloneWithWorktree(t)

	// The leak, as it happened: what a test helper runs, in its own folder.
	os.Setenv("GIT_DIR", wtGitDir)
	git(t, t.TempDir(), "init", "-q", filepath.Join(t.TempDir(), "repo"))
	if got := bare(t, clone); got != "true" {
		t.Fatalf("without Isolate the clone's core.bare is %q; the test no longer shows the leak", got)
	}
	os.Unsetenv("GIT_DIR")
	git(t, clone, "config", "core.bare", "false")

	os.Setenv("GIT_DIR", wtGitDir)
	cleanup, err := Isolate()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	repo := filepath.Join(t.TempDir(), "repo")
	git(t, t.TempDir(), "init", "-q", repo)
	if got := bare(t, clone); got != "false" {
		t.Errorf("after Isolate a test's git init set the clone's core.bare to %q", got)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		t.Errorf("git init didn't make the test's own repository: %v", err)
	}
	if git(t, clone, "status", "--porcelain") != "" {
		t.Error("the clone's checkout changed")
	}
}

// A git a test starts without a folder of its own runs in the package's
// folder, inside the checkout the tests run in; after Isolate it finds no
// repository there instead of working on that checkout.
func TestIsolateStopsAtThePackageFolder(t *testing.T) {
	keepEnv(t)
	clone, _ := cloneWithWorktree(t)
	pkg := filepath.Join(clone, "internal", "pkg")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(pkg)
	cleanup, err := Isolate()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	cmd := exec.Command("git", "config", "core.bare", "true")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("git config in the package's folder worked on the checkout: %s", out)
	}
	if got := bare(t, clone); got != "false" {
		t.Errorf("core.bare = %q", got)
	}
}
