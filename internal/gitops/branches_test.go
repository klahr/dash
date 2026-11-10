package gitops

import (
	"context"
	"path/filepath"
	"testing"
)

func TestBranches(t *testing.T) {
	t.Parallel()

	ctx, g := context.Background(), New(nil)
	root := t.TempDir()
	origin, dir := filepath.Join(root, "origin.git"), filepath.Join(root, "repo")
	gitIn(t, root, "init", "-q", "--bare", "-b", "main", origin)
	if err := g.InitRepo(ctx, "p", dir, "main"); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "remote", "add", "origin", origin)
	gitIn(t, dir, "push", "-q", "origin", "main", "main:refs/heads/shared")
	gitIn(t, dir, "branch", "local-only")
	gitIn(t, dir, "branch", "dash/todo-1")
	if err := g.FetchBranches(ctx, "p", dir); err != nil {
		t.Fatal(err)
	}

	list, err := g.ListBranches(ctx, "p", dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []BranchInfo{{"local-only", true, false}, {"main", true, true}, {"shared", false, true}}
	if len(list) != len(want) {
		t.Fatalf("ListBranches = %+v", list)
	}
	for i := range want {
		if list[i] != want[i] {
			t.Errorf("branch %d = %+v, want %+v", i, list[i], want[i])
		}
	}

	// origin's branch is checked out from origin; a new one needs a start.
	if created, err := g.EnsureBranch(ctx, "p", dir, "shared", ""); err != nil || created || !g.HasBranch(ctx, "p", dir, "shared") {
		t.Fatalf("shared: created %v, %v", created, err)
	}
	if _, err := g.EnsureBranch(ctx, "p", dir, "new", ""); err == nil {
		t.Fatal("made a branch without a start")
	}
	if created, err := g.EnsureBranch(ctx, "p", dir, "new", "refs/heads/main"); err != nil || !created {
		t.Fatalf("new: created %v, %v", created, err)
	}
	if ahead, behind, err := g.CompareBranches(ctx, "p", dir, "main", "new"); err != nil || ahead != 0 || behind != 0 {
		t.Fatalf("compare: %d %d %v", ahead, behind, err)
	}
	for _, bad := range []string{"", "-x", "a..b", "dash/mine", "a b"} {
		if _, err := g.EnsureBranch(ctx, "p", dir, bad, "refs/heads/main"); err == nil {
			t.Errorf("accepted branch name %q", bad)
		}
	}
}
