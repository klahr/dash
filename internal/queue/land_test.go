package queue

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writesA(t *testing.T) *fakeExec {
	return &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		write(t, req.Dir, "a.txt", "from claude\n")
		return nil
	}}
}

func TestMergeWaitsForUncommittedChangesToItsFiles(t *testing.T) {
	h := newHarness(t, writesA(t), model.Project{Name: "p"})
	td := h.waitFor(h.add("a").ID, model.StatusReview)
	write(t, h.proj.Path, "a.txt", "mine\n")
	h.approve(td.ID)

	deadline := time.Now().Add(10 * time.Second)
	for {
		cur, _ := h.st.GetTodo(td.ID)
		if cur.Status == model.StatusMerging && strings.Contains(cur.Error, "a.txt") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("want merging and waiting on a.txt, got %s (error %q)", cur.Status, cur.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := read(t, filepath.Join(h.proj.Path, "a.txt")); got != "mine\n" {
		t.Fatalf("waiting must not touch the user's file, got %q", got)
	}

	if err := os.Remove(filepath.Join(h.proj.Path, "a.txt")); err != nil {
		t.Fatal(err)
	}
	merged := h.waitFor(td.ID, model.StatusMerged)
	if merged.Error != "" {
		t.Fatalf("error should be cleared once merged: %q", merged.Error)
	}
	if got := read(t, filepath.Join(h.proj.Path, "a.txt")); got != "from claude\n" {
		t.Fatalf("folder should follow main, a.txt = %q", got)
	}
}

func TestMergeKeepsUnrelatedUncommittedWork(t *testing.T) {
	exe := writesA(t)
	h := newHarness(t, exe, model.Project{Name: "p"})
	write(t, h.proj.Path, "b.txt", "b\n")
	gitIn(t, h.proj.Path, "add", "b.txt")
	gitIn(t, h.proj.Path, "commit", "-qm", "b")
	td := h.waitFor(h.add("a").ID, model.StatusReview)

	write(t, h.proj.Path, "b.txt", "edited\n")
	write(t, h.proj.Path, "c.txt", "staged\n")
	gitIn(t, h.proj.Path, "add", "c.txt")
	h.approve(td.ID)
	h.waitFor(td.ID, model.StatusMerged)

	if got := read(t, filepath.Join(h.proj.Path, "a.txt")); got != "from claude\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if got := read(t, filepath.Join(h.proj.Path, "b.txt")); got != "edited\n" {
		t.Fatalf("unrelated edit lost: b.txt = %q", got)
	}
	if got := gitIn(t, h.proj.Path, "status", "--porcelain"); got != "M b.txt\nA  c.txt" {
		t.Fatalf("status should only show the user's own work, got:\n%s", got)
	}
}

func TestMergeLandsWhileFolderIsOnAnotherBranch(t *testing.T) {
	h := newHarness(t, writesA(t), model.Project{Name: "p"})
	td := h.waitFor(h.add("a").ID, model.StatusReview)
	gitIn(t, h.proj.Path, "checkout", "-qb", "mine")
	h.approve(td.ID)
	h.waitFor(td.ID, model.StatusMerged)

	if got := gitIn(t, h.proj.Path, "show", "main:a.txt"); got != "from claude" {
		t.Fatalf("main:a.txt = %q", got)
	}
	if got := gitIn(t, h.proj.Path, "branch", "--show-current"); got != "mine" {
		t.Fatalf("the folder should stay on its branch, is on %q", got)
	}
	if _, err := os.Stat(filepath.Join(h.proj.Path, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("a.txt shouldn't appear on another branch: %v", err)
	}
}
