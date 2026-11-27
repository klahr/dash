package gitops

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// DiffSince is what a task's branch changed since the commit the user
// reviewed; once main is merged into the branch, or the branch is rebased,
// it says so instead of showing a patch that mixes in other work.
func TestDiffSince(t *testing.T) {
	t.Parallel()

	ctx, g := context.Background(), New(nil)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := g.InitRepo(ctx, "p", repo, "main"); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, "wt")
	gitOut(t, repo, "worktree", "add", "-q", "-b", "dash/t1", wt)
	commitIn(t, wt, "first.txt")

	// The first review: the branch's head and its merge base with main.
	head, base, err := g.ReviewPoint(ctx, "p", wt, "main")
	if err != nil || head != gitOut(t, wt, "rev-parse", "HEAD") || base != gitOut(t, repo, "rev-parse", "main") {
		t.Fatalf("ReviewPoint = %q, %q, %v", head, base, err)
	}

	// Main moves on, but not into the branch: the rework's commit alone.
	commitIn(t, repo, "main-only.txt")
	commitIn(t, wt, "rework.txt")
	patch, moved, err := g.DiffSince(ctx, "p", repo, "main", "dash/t1", head, base)
	if err != nil || moved != "" {
		t.Fatalf("DiffSince = moved %q, %v", moved, err)
	}
	if !strings.Contains(patch, "rework.txt") || strings.Contains(patch, "first.txt") || strings.Contains(patch, "main-only.txt") {
		t.Fatalf("the patch since the review:\n%s", patch)
	}

	// Nothing new: an empty patch, not a fallback.
	now, nowBase, _ := g.ReviewPoint(ctx, "p", wt, "main")
	if patch, moved, err := g.DiffSince(ctx, "p", repo, "main", "dash/t1", now, nowBase); err != nil || moved != "" || strings.TrimSpace(patch) != "" {
		t.Fatalf("DiffSince with no new commits = %q, moved %q, %v", patch, moved, err)
	}

	// Main merged into the branch: its merge base moved.
	gitOut(t, wt, "merge", "-q", "--no-edit", "main")
	if _, moved, err := g.DiffSince(ctx, "p", repo, "main", "dash/t1", head, base); err != nil || !strings.Contains(moved, "main was merged") {
		t.Fatalf("DiffSince after merging main = moved %q, %v", moved, err)
	}

	// Rebased: the reviewed commit isn't in the branch any more.
	gitOut(t, wt, "reset", "-q", "--hard", head)
	gitOut(t, wt, "rebase", "-q", "main")
	if _, moved, err := g.DiffSince(ctx, "p", repo, "main", "dash/t1", head, base); err != nil || !strings.Contains(moved, "rebased") {
		t.Fatalf("DiffSince after a rebase = moved %q, %v", moved, err)
	}

	// A commit the repository doesn't have.
	if _, moved, err := g.DiffSince(ctx, "p", repo, "main", "dash/t1", strings.Repeat("1", 40), base); err != nil || !strings.Contains(moved, "gone") {
		t.Fatalf("DiffSince of an unknown commit = moved %q, %v", moved, err)
	}
}
