package gitops

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// WorkFiles is what a task changed since its branch left main: its commits
// and, in its worktree, what it hasn't committed yet; not what main did
// meanwhile.
func TestWorkFiles(t *testing.T) {
	t.Parallel()

	ctx, g := context.Background(), New(nil)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := g.InitRepo(ctx, "p", repo, "main"); err != nil {
		t.Fatal(err)
	}
	write := func(dir, name, body string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(dir, msg string) {
		t.Helper()
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-m", msg)
	}
	write(repo, "a.go", "a")
	write(repo, "old.go", "old")
	write(repo, ".gitignore", "*.log\n")
	commit(repo, "base")

	wt := filepath.Join(root, "wt")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "dash/t1", wt)
	write(wt, "pkg/b.go", "b")
	gitIn(t, wt, "rm", "-q", "old.go")
	commit(wt, "task work")
	write(wt, "a.go", "changed, not committed")
	write(wt, "new.txt", "untracked")
	write(wt, "run.log", "ignored")

	write(repo, "main.go", "main moved on")
	commit(repo, "main work")

	got, err := g.WorkFiles(ctx, "p", repo, wt, "main", "dash/t1")
	if want := []string{"a.go", "new.txt", "old.go", "pkg/b.go"}; err != nil || !slices.Equal(got, want) {
		t.Fatalf("WorkFiles in the worktree = %v, %v; want %v", got, err, want)
	}
	// Without the worktree: the branch's commits only.
	got, err = g.WorkFiles(ctx, "p", repo, "", "main", "dash/t1")
	if want := []string{"old.go", "pkg/b.go"}; err != nil || !slices.Equal(got, want) {
		t.Fatalf("WorkFiles of the branch = %v, %v; want %v", got, err, want)
	}
	got, err = g.WorkFiles(ctx, "p", repo, filepath.Join(root, "gone"), "main", "dash/t1")
	if want := []string{"old.go", "pkg/b.go"}; err != nil || !slices.Equal(got, want) {
		t.Fatalf("WorkFiles of a removed worktree = %v, %v; want %v", got, err, want)
	}
}

func TestPatchFiles(t *testing.T) {
	t.Parallel()

	patch := ` a.go | 2 +-
 2 files changed

diff --git a/a.go b/a.go
index 1..2 100644
--- a/a.go
+++ b/a.go
@@ -1 +1 @@
-diff --git a/fake b/fake
+x
diff --git a/dir/old name.go b/dir/new name.go
similarity index 90%
`
	if got, want := PatchFiles(patch), []string{"a.go", "dir/new name.go", "dir/old name.go"}; !slices.Equal(got, want) {
		t.Fatalf("PatchFiles = %q, want %q", got, want)
	}
}
