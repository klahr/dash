package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAheadBehind(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		in            string
		ahead, behind int
		bad           bool
	}{
		{in: "0\t0"},
		{in: "3\t0", ahead: 3},
		{in: "0\t5", behind: 5},
		{in: "2\t7\n", ahead: 2, behind: 7},
		{in: "", bad: true},
		{in: "1", bad: true},
		{in: "x\t1", bad: true},
	} {
		a, b, err := ParseAheadBehind(c.in)
		if (err != nil) != c.bad || a != c.ahead || b != c.behind {
			t.Errorf("ParseAheadBehind(%q) = %d, %d, %v; want %d, %d, bad=%v", c.in, a, b, err, c.ahead, c.behind, c.bad)
		}
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "commit.gpgsign=false", "-c", "user.name=T", "-c", "user.email=t@t"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// remoteRepos makes a repo with main checked out, a bare origin it pushed
// to, and a second clone of origin standing in for another computer.
func remoteRepos(t *testing.T) (g *Git, repo, other string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// TestMain keeps the developer's commit signing out of git.
	base := t.TempDir()
	g = New(nil)
	repo, other = filepath.Join(base, "repo"), filepath.Join(base, "other")
	origin := filepath.Join(base, "origin.git")
	if err := g.InitRepo(context.Background(), "p", repo, "main"); err != nil {
		t.Fatal(err)
	}
	gitOut(t, base, "init", "-q", "--bare", "-b", "main", origin)
	gitOut(t, repo, "remote", "add", "origin", origin)
	gitOut(t, repo, "push", "-q", "origin", "main")
	gitOut(t, base, "clone", "-q", origin, other)
	return g, repo, other
}

func commitIn(t *testing.T, dir, file string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(file+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, dir, "add", file)
	gitOut(t, dir, "commit", "-q", "-m", file)
	return gitOut(t, dir, "rev-parse", "HEAD")
}

func TestFetchAndAheadBehind(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	g, repo, other := remoteRepos(t)
	commitIn(t, other, "a.txt")
	commitIn(t, other, "b.txt")
	gitOut(t, other, "push", "-q", "origin", "main")
	commitIn(t, repo, "mine.txt")

	before := gitOut(t, repo, "rev-parse", "main")
	if err := g.FetchBranch(ctx, "p", repo, "main", false); err != nil {
		t.Fatal(err)
	}
	if after := gitOut(t, repo, "rev-parse", "main"); after != before {
		t.Fatal("fetch moved the local branch")
	}
	ahead, behind, err := g.AheadBehind(ctx, "p", repo, "main")
	if err != nil || ahead != 1 || behind != 2 {
		t.Fatalf("AheadBehind = %d, %d, %v; want 1, 2", ahead, behind, err)
	}
}

func TestFastForwardToOriginOnlyWhenStrictlyBehind(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("behind", func(t *testing.T) {
		g, repo, other := remoteRepos(t)
		want := commitIn(t, other, "a.txt")
		gitOut(t, other, "push", "-q", "origin", "main")
		if err := g.FetchBranch(ctx, "p", repo, "main", false); err != nil {
			t.Fatal(err)
		}
		moved, ahead, err := g.FastForwardToOrigin(ctx, "p", repo, "main")
		if err != nil || moved != 1 || ahead != 0 {
			t.Fatalf("FastForwardToOrigin = %d, %d, %v; want 1, 0", moved, ahead, err)
		}
		if got := gitOut(t, repo, "rev-parse", "main"); got != want {
			t.Fatalf("main at %s, want %s", got, want)
		}
		// main is checked out in repo: its files follow.
		if _, err := os.Stat(filepath.Join(repo, "a.txt")); err != nil {
			t.Fatal("checkout didn't follow the fast-forward:", err)
		}
	})

	t.Run("diverged", func(t *testing.T) {
		g, repo, other := remoteRepos(t)
		commitIn(t, other, "a.txt")
		gitOut(t, other, "push", "-q", "origin", "main")
		mine := commitIn(t, repo, "mine.txt")
		if err := g.FetchBranch(ctx, "p", repo, "main", false); err != nil {
			t.Fatal(err)
		}
		moved, ahead, err := g.FastForwardToOrigin(ctx, "p", repo, "main")
		if err != nil || moved != 0 || ahead != 1 {
			t.Fatalf("FastForwardToOrigin = %d, %d, %v; want 0, 1", moved, ahead, err)
		}
		if got := gitOut(t, repo, "rev-parse", "main"); got != mine {
			t.Fatal("diverged main was moved")
		}
	})

	t.Run("ahead", func(t *testing.T) {
		g, repo, _ := remoteRepos(t)
		mine := commitIn(t, repo, "mine.txt")
		moved, ahead, err := g.FastForwardToOrigin(ctx, "p", repo, "main")
		if err != nil || moved != 0 || ahead != 1 || gitOut(t, repo, "rev-parse", "main") != mine {
			t.Fatalf("FastForwardToOrigin = %d, %d, %v; want main left alone", moved, ahead, err)
		}
	})

	t.Run("up to date", func(t *testing.T) {
		g, repo, _ := remoteRepos(t)
		moved, ahead, err := g.FastForwardToOrigin(ctx, "p", repo, "main")
		if err != nil || moved != 0 || ahead != 0 {
			t.Fatalf("FastForwardToOrigin = %d, %d, %v; want 0, 0", moved, ahead, err)
		}
	})

	t.Run("dirty checkout", func(t *testing.T) {
		g, repo, other := remoteRepos(t)
		commitIn(t, other, "a.txt")
		gitOut(t, other, "push", "-q", "origin", "main")
		if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("my own\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		before := gitOut(t, repo, "rev-parse", "main")
		if err := g.FetchBranch(ctx, "p", repo, "main", false); err != nil {
			t.Fatal(err)
		}
		if _, _, err := g.FastForwardToOrigin(ctx, "p", repo, "main"); err == nil {
			t.Fatal("fast-forward overwrote an untracked file")
		}
		if gitOut(t, repo, "rev-parse", "main") != before {
			t.Fatal("main moved despite the failure")
		}
	})
}
