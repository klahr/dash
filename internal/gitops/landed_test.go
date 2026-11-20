package gitops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLandedOn(t *testing.T) {
	t.Parallel()

	ctx, g := context.Background(), New(nil)
	dir := filepath.Join(t.TempDir(), "repo")
	if err := g.InitRepo(ctx, "p", dir, "main"); err != nil {
		t.Fatal(err)
	}
	commit := func(file, content, msg string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, dir, "add", file)
		gitIn(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", msg)
	}
	merge := func(ref string) {
		t.Helper()
		gitIn(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "merge", "-q", "--no-edit", ref)
	}
	commit("a.txt", "a\n", "init")

	// No commits of its own: nothing landed.
	gitIn(t, dir, "checkout", "-q", "-b", "task")
	if _, ok, err := g.LandedOn(ctx, "p", dir, "main"); ok || err != nil {
		t.Fatalf("no commits: ok=%v err=%v", ok, err)
	}

	// The same change landed on main from another task, then merged in.
	commit("x.txt", "same\n", "task's change")
	gitIn(t, dir, "checkout", "-q", "main")
	commit("x.txt", "same\n", "Other task\n\nDash-Todo: abc123")
	gitIn(t, dir, "checkout", "-q", "task")
	merge("main")
	l, ok, err := g.LandedOn(ctx, "p", dir, "main")
	if err != nil || !ok || l.Todo != "abc123" || l.Subject != "Other task" {
		t.Fatalf("landed = %+v ok=%v err=%v; want from abc123, Other task", l, ok, err)
	}

	// A branch whose change was undone changed nothing on balance.
	gitIn(t, dir, "checkout", "-q", "-b", "undone", "main")
	commit("y.txt", "y\n", "add y")
	gitIn(t, dir, "rm", "-q", "y.txt")
	gitIn(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "remove y")
	if l, ok, err := g.LandedOn(ctx, "p", dir, "main"); ok || err != nil {
		t.Fatalf("undone change: landed = %+v ok=%v err=%v", l, ok, err)
	}
}
