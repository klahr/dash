package gitops

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestMainBranch(t *testing.T) {
	ctx, g := context.Background(), New(nil)
	dir := filepath.Join(t.TempDir(), "repo")
	if err := g.InitRepo(ctx, "p", dir, "master"); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "checkout", "-q", "-b", "feature")
	if g.HasBranch(ctx, "p", dir, "main") || !g.HasBranch(ctx, "p", dir, "master") {
		t.Fatal("HasBranch wrong")
	}
	if b := g.MainBranch(ctx, "p", dir); b != "master" {
		t.Fatalf("MainBranch = %q, want master", b)
	}

	// A remote's HEAD wins over the main/master names.
	gitIn(t, dir, "update-ref", "refs/remotes/github/trunk", "HEAD")
	gitIn(t, dir, "symbolic-ref", "refs/remotes/github/HEAD", "refs/remotes/github/trunk")
	gitIn(t, dir, "branch", "trunk")
	if b := g.MainBranch(ctx, "p", dir); b != "trunk" {
		t.Fatalf("MainBranch = %q, want trunk", b)
	}
}
