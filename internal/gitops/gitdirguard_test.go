package gitops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A worktree whose .git (or commondir) was pointed at metadata planted in
// the worktree must not get git run in it: its config could name a
// command (core.fsmonitor, a filter, hooks) git would run as the user.
func TestCheckGitDirRefusesPlantedGitDir(t *testing.T) {
	t.Parallel()
	ctx, g := context.Background(), New(nil)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := g.InitRepo(ctx, "p", repo, "main"); err != nil {
		t.Fatal(err)
	}
	newWT := func(name string) string {
		wt := filepath.Join(root, name)
		gitIn(t, repo, "worktree", "add", "-q", "-b", name, wt)
		if err := CheckGitDir(wt); err != nil {
			t.Fatalf("a fresh worktree: %v", err)
		}
		if _, err := g.Head(ctx, "p", wt, "HEAD"); err != nil {
			t.Fatal(err)
		}
		return wt
	}
	marker := filepath.Join(root, "ran")
	plant := func(dir string) {
		// A repository of its own whose config runs a command.
		gitIn(t, root, "init", "-q", dir)
		gitIn(t, dir, "config", "core.fsmonitor", "touch "+marker+"; false")
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// .git points into the worktree.
	wt := newWT("a")
	plant(filepath.Join(wt, "evil"))
	write(filepath.Join(wt, ".git"), "gitdir: ./evil/.git\n")
	if err := CheckGitDir(wt); !errors.Is(err, ErrGitDir) {
		t.Fatalf("CheckGitDir = %v, want ErrGitDir", err)
	}
	if _, err := g.CommitAll(ctx, "p", wt, "x"); !errors.Is(err, ErrGitDir) {
		t.Fatalf("CommitAll = %v, want ErrGitDir", err)
	}

	// commondir points into the worktree.
	wt = newWT("b")
	plant(filepath.Join(wt, "evil"))
	gitdir := filepath.Join(repo, ".git", "worktrees", "b")
	write(filepath.Join(gitdir, "commondir"), filepath.Join(wt, "evil", ".git")+"\n")
	if err := CheckGitDir(wt); !errors.Is(err, ErrGitDir) {
		t.Fatalf("CheckGitDir = %v, want ErrGitDir", err)
	}

	// .git points at another worktree's gitdir.
	wt = newWT("c")
	write(filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(repo, ".git", "worktrees", "a")+"\n")
	if err := CheckGitDir(wt); err == nil || !strings.Contains(err.Error(), "another worktree") {
		t.Fatalf("CheckGitDir = %v, want another worktree", err)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("git ran the planted fsmonitor")
	}
}

// Dash's git never runs an fsmonitor or hooks, whatever the config says.
func TestSafeConfigDisablesHooks(t *testing.T) {
	t.Parallel()
	ctx, g := context.Background(), New(nil)
	repo := filepath.Join(t.TempDir(), "repo")
	if err := g.InitRepo(ctx, "p", repo, "main"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	hooks := filepath.Join(repo, ".githooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "config", "core.hooksPath", ".githooks")
	gitIn(t, repo, "config", "core.fsmonitor", "touch "+marker+"; false")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, err := g.CommitAll(ctx, "p", repo, "x"); err != nil || !ok {
		t.Fatalf("CommitAll = %v, %v", ok, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("git ran a hook or the fsmonitor")
	}
}

func TestGitDirState(t *testing.T) {
	t.Parallel()
	ctx, g := context.Background(), New(nil)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := g.InitRepo(ctx, "p", repo, "main"); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, "wt")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "wt", wt)
	before := GitDirState(wt)
	if err := os.WriteFile(filepath.Join(wt, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := g.CommitAll(ctx, "p", wt, "x"); err != nil {
		t.Fatal(err)
	}
	if GitDirState(wt) != before {
		t.Fatal("a commit changed GitDirState")
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "worktrees", "wt", "commondir"), []byte("/elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if GitDirState(wt) == before {
		t.Fatal("GitDirState missed a changed commondir")
	}
}
