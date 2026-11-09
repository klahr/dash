package gitops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/localrun"
)

// On the hub, git and gh refuse to start, whichever Git method asks.
func TestHubRunsNoGitOrGh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	// Fake git and gh that leave a mark if they run.
	bin := t.TempDir()
	mark := filepath.Join(t.TempDir(), "ran")
	for _, name := range []string{"git", "gh"} {
		script := "#!/bin/sh\necho " + name + " >> " + mark + "\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	localrun.Set(func() bool { return false })
	t.Cleanup(func() { localrun.Set(nil) })
	ctx, g := context.Background(), New(nil)
	g.GhBin = filepath.Join(bin, "gh")
	dir := t.TempDir()

	_, err := g.CurrentBranch(ctx, "p", dir)
	expectRefused(t, err, "git")
	_, _, err = g.ReadBlob(ctx, "p", dir, "deadbeef", 1<<20)
	expectRefused(t, err, "git")
	_, err = g.ghQuiet(ctx, "p", dir, "api", "user")
	expectRefused(t, err, "gh")
	if b, err := os.ReadFile(mark); err == nil {
		t.Fatalf("the hub ran: %s", b)
	}

	// The fakes do run off the hub, so the check above means something.
	localrun.Set(nil)
	_, _ = g.ghQuiet(ctx, "p", dir, "api", "user")
	if _, err := os.Stat(mark); err != nil {
		t.Fatal("fake gh didn't run on a runner:", err)
	}
}

func expectRefused(t *testing.T, err error, tool string) {
	t.Helper()
	if !errors.Is(err, localrun.ErrHub) || !strings.Contains(err.Error(), "this hub doesn't run "+tool) {
		t.Fatalf("%s on the hub: %v", tool, err)
	}
}
