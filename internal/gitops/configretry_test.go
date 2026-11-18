package gitops

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/testbin"
)

// A git that dies reading .git/config (another git process was renaming it,
// on Windows) is run again; one that fails for another reason is not.
func TestGitRetriesConfigReadRace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	bin, runs := t.TempDir(), filepath.Join(t.TempDir(), "runs")
	// Fails the first two runs as git does in the race, then succeeds. With
	// "other" as its first argument it always fails, another way.
	script := `#!/bin/sh
echo x >> ` + runs + `
if [ "$1" = other ]; then echo "fatal: not a git repository" >&2; exit 128; fi
n=$(wc -l < ` + runs + `)
if [ "$n" -le 2 ]; then
  echo "warning: unable to access '.git/config': Permission denied" >&2
  echo "fatal: unknown error occurred while reading the configuration files" >&2
  exit 255
fi
echo ok
`
	testbin.Write(t, filepath.Join(bin, "git"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, g, dir := context.Background(), New(nil), t.TempDir()
	count := func() int {
		b, _ := os.ReadFile(runs)
		return strings.Count(string(b), "x")
	}

	out, err := g.git(ctx, "p", dir, "worktree", "add", "-b", "b", "wt", "HEAD")
	if err != nil || out != "ok" {
		t.Fatalf("got %q, %v; want ok after retries", out, err)
	}
	if n := count(); n != 3 {
		t.Fatalf("git ran %d times, want 3", n)
	}

	if err := os.Remove(runs); err != nil {
		t.Fatal(err)
	}
	if _, err := g.git(ctx, "p", dir, "other"); err == nil {
		t.Fatal("want the other failure")
	}
	if n := count(); n != 1 {
		t.Fatalf("git ran %d times for another failure, want 1", n)
	}
}
