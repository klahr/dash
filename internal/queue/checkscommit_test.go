package queue

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

const gitIdent = "git -c user.name=t -c user.email=t@example.com"

// checksWorktree is a worktree of the harness repo on its own branch, with
// a.txt=pass committed, and the commit it is on.
func checksWorktree(t *testing.T, h *harness) (string, string) {
	t.Helper()
	wt := filepath.Join(t.TempDir(), "wt")
	run := func(cmd string) {
		t.Helper()
		if out, err := exec.Command("sh", "-c", cmd).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", cmd, err, out)
		}
	}
	run("git -C '" + h.proj.Path + "' worktree add -q -b checks '" + wt + "' main")
	write(t, wt, "a.txt", "pass\n")
	run("cd '" + wt + "' && git add a.txt && " + gitIdent + " commit -q -m pass")
	head, err := h.git.Head(context.Background(), h.proj.ID, wt, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return wt, head
}

// Checks that pass on a clean worktree record the commit they tested.
func TestRunChecksRecordsCleanCommit(t *testing.T) {
	h := newHarness(t, writesA(t), model.Project{Name: "p"})
	wt, head := checksWorktree(t, h)
	p := h.proj
	p.TestCommand = "grep -qx pass a.txt"
	td := model.Todo{ID: "t1", ProjectID: p.ID}

	c := runChecks(context.Background(), h.engine.Git, h.engine.Log, td, p, wt, "review", time.Minute)
	if !c.Passed || c.Commit != head {
		t.Fatalf("want passing checks on %s, got passed=%v commit=%q", head, c.Passed, c.Commit)
	}
	if !checksPassedOn(&c, p.TestCommand, head) {
		t.Error("checks that passed on HEAD should be reused for HEAD")
	}
}

// Checks run with uncommitted changes to tracked files tested code that
// isn't in HEAD, so they record no commit and are never reused at merge.
func TestRunChecksOnUncommittedChangesRecordNoCommit(t *testing.T) {
	h := newHarness(t, writesA(t), model.Project{Name: "p"})
	wt, head := checksWorktree(t, h)
	write(t, wt, "a.txt", "broken\n")
	p := h.proj
	p.TestCommand = "true"
	td := model.Todo{ID: "t1", ProjectID: p.ID}

	c := runChecks(context.Background(), h.engine.Git, h.engine.Log, td, p, wt, "review", time.Minute)
	if !c.Passed || c.Commit != "" {
		t.Fatalf("want passing checks with no commit, got passed=%v commit=%q", c.Passed, c.Commit)
	}
	if checksPassedOn(&c, p.TestCommand, head) {
		t.Error("checks on a dirty tree must not be reused for HEAD")
	}
}

// Checks during which HEAD moved don't know which commit they tested, so
// they record none.
func TestRunChecksWhileHeadMovesRecordNoCommit(t *testing.T) {
	h := newHarness(t, writesA(t), model.Project{Name: "p"})
	wt, head := checksWorktree(t, h)
	p := h.proj
	p.TestCommand = gitIdent + " commit -q --allow-empty -m x"
	td := model.Todo{ID: "t1", ProjectID: p.ID}

	c := runChecks(context.Background(), h.engine.Git, h.engine.Log, td, p, wt, "review", time.Minute)
	if !c.Passed {
		t.Fatalf("test command failed: %s", c.Output)
	}
	if c.Commit != "" {
		t.Fatalf("want no commit when HEAD moved during the checks, got %q", c.Commit)
	}
	after, err := h.git.Head(context.Background(), p.ID, wt, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if after == head {
		t.Fatal("the test command should have moved HEAD")
	}
	if checksPassedOn(&c, p.TestCommand, head) || checksPassedOn(&c, p.TestCommand, after) {
		t.Error("checks during which HEAD moved must not be reused")
	}
}

func TestChecksPassedOn(t *testing.T) {
	ok := model.Checks{Command: "make test", Passed: true, Commit: "abc"}
	if !checksPassedOn(&ok, "make test", "abc") {
		t.Error("passing checks on the same commit and command should be reused")
	}
	noCommit := ok
	noCommit.Commit = ""
	failed := ok
	failed.Passed = false
	for name, tc := range map[string]struct {
		c       *model.Checks
		command string
		commit  string
	}{
		"nil checks":         {nil, "make test", "abc"},
		"no commit":          {&noCommit, "make test", "abc"},
		"no commit, empty":   {&noCommit, "make test", ""},
		"other command":      {&ok, "go test ./...", "abc"},
		"other commit":       {&ok, "make test", "def"},
		"failed":             {&failed, "make test", "abc"},
		"empty commit asked": {&ok, "make test", ""},
	} {
		if checksPassedOn(tc.c, tc.command, tc.commit) {
			t.Errorf("%s: checksPassedOn = true, want false", name)
		}
	}
}
