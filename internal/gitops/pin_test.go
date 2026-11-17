package gitops

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// Claude, sandboxed or not, can rewrite a worktree's .git file. Dash's git
// there must still use the worktree's folder in the project's .git, so a
// config, hook, fsmonitor or filter of Claude's never runs as the runner's user.
func TestPinnedWorktreeIgnoresRewrittenGitFile(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("hooks are shell scripts")
	}
	ctx, g := context.Background(), New(nil)
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	if err := g.InitRepo(ctx, "p", repo, "main"); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(tmp, "worktrees")
	g.WorktreeRoots = []string{root}
	g.RepoOf = func(id string) (string, bool) { return repo, id == "p" }
	if err := os.MkdirAll(filepath.Join(root, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	wt := model.Worktree{Name: "todo-1", Path: filepath.Join(root, "p", "todo-1"), Branch: "dash/todo-1", BaseCommit: "main"}
	if err := g.AddWorktree(ctx, "p", repo, wt); err != nil {
		t.Fatal(err)
	}

	// What a prompt-injected task would do: a git folder of its own, with a
	// hook and an fsmonitor, and the worktree's .git pointed at it.
	evil := filepath.Join(tmp, "evil")
	gitIn(t, tmp, "init", "-q", "-b", "main", evil)
	ran := filepath.Join(tmp, "ran")
	hook := "#!/bin/sh\ntouch '" + ran + "'\n"
	if err := os.WriteFile(filepath.Join(evil, ".git", "hooks", "pre-commit"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evil, "fsmon"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, evil, "config", "core.fsmonitor", filepath.Join(evil, "fsmon"))
	// Hooks and fsmonitor are off for all of Dash's git (safeConfig); a
	// clean filter isn't, and runs on git add.
	gitIn(t, evil, "config", "filter.x.clean", filepath.Join(evil, "fsmon"))
	if err := os.WriteFile(filepath.Join(wt.Path, ".gitattributes"), []byte("* filter=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, ".git"), []byte("gitdir: "+filepath.Join(evil, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "work.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if ok, err := g.CommitAll(ctx, "p", wt.Path, "work"); err != nil || !ok {
		t.Fatalf("CommitAll = %v, %v", ok, err)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("the worktree's planted hook, fsmonitor or filter ran")
	}
	// The commit is on the task's branch in the project's repository.
	if out, err := g.git(ctx, "p", repo, "log", "-1", "--format=%s", wt.Branch); err != nil || out != "work" {
		t.Fatalf("task branch tip = %q, %v", out, err)
	}
}

func TestPinnedEnv(t *testing.T) {
	t.Parallel()
	ctx, g := context.Background(), New(nil)
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	if err := g.InitRepo(ctx, "p", repo, "main"); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(tmp, "worktrees")
	g.WorktreeRoots = []string{root}
	g.RepoOf = func(id string) (string, bool) { return repo, id == "p" }
	path := filepath.Join(root, "p", "a")
	if err := g.AddWorktreeAt(ctx, "p", repo, path, "dash/a", "main"); err != nil {
		t.Fatal(err)
	}

	// Outside the roots: nothing pinned.
	if env, err := g.PinnedEnv(repo); err != nil || env != nil {
		t.Fatalf("PinnedEnv(repo) = %v, %v", env, err)
	}
	// In a worktree, or a folder in it: its git folder in the repository.
	want := filepath.Join(repo, ".git", "worktrees", "a")
	for _, dir := range []string{path, filepath.Join(path, "sub")} {
		env, err := g.PinnedEnv(dir)
		if err != nil || len(env) != 2 || env[0] != "GIT_DIR="+want || env[1] != "GIT_WORK_TREE="+path {
			t.Fatalf("PinnedEnv(%s) = %v, %v", dir, env, err)
		}
	}
	// A folder that isn't one of the repository's worktrees, or of a
	// project Dash doesn't know: refused rather than trusting its .git.
	gitIn(t, tmp, "init", "-q", filepath.Join(root, "p", "fake"))
	for _, dir := range []string{filepath.Join(root, "p", "fake"), filepath.Join(root, "q", "a")} {
		if env, err := g.PinnedEnv(dir); err == nil {
			t.Fatalf("PinnedEnv(%s) = %v, want an error", dir, env)
		}
	}
}
