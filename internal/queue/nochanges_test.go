package queue

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

// answerExec is a Claude run that only answers: it changes no file.
func answerExec() *fakeExec {
	return &fakeExec{fn: func(int, agent.ExecRequest) error { return nil }, result: "It already works; nothing to change."}
}

func TestNoChangesGoesToReviewFlagged(t *testing.T) {
	parallel(t)

	h := newHarness(t, answerExec(), model.Project{Name: "p"})
	td := h.waitFor(h.add("noop").ID, model.StatusReview)
	if !td.NoChanges || td.DiffFiles != 0 {
		t.Fatalf("want no_changes set and no files, got no_changes=%v diff_files=%d", td.NoChanges, td.DiffFiles)
	}
	chat := h.st.TodoChat(td.ID)
	if len(chat) != 1 || !strings.Contains(chat[0].Content, "Close the task, or ask for changes") || strings.Contains(chat[0].Content, "Approve") {
		t.Fatalf("want the chat to offer Close and Ask for changes, got %+v", chat)
	}
}

func TestCloseNoChangesWithoutMerge(t *testing.T) {
	parallel(t)

	marker := filepath.Join(t.TempDir(), "tested")
	h := newHarness(t, answerExec(), model.Project{Name: "p"})
	h.proj, _ = h.st.UpdateProject(h.proj.ID, func(p *model.Project) error {
		p.TestCommand = "touch " + sq(marker)
		return nil
	})
	td := h.waitFor(h.add("noop").ID, model.StatusReview)
	_ = os.Remove(marker) // the review's own test run, if any
	base := gitRun(t, h.proj.Path, "rev-parse", "main")

	got, err := h.merges.Close(context.Background(), td.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusMerged || !got.NoChanges || got.MergedCommit != "" || got.FinishedAt == nil {
		t.Fatalf("want closed (merged, no_changes, no commit), got status=%s no_changes=%v commit=%q", got.Status, got.NoChanges, got.MergedCommit)
	}
	if after := gitRun(t, h.proj.Path, "rev-parse", "main"); after != base {
		t.Fatalf("closing moved main from %s to %s", base, after)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("closing ran the tests")
	}
	if _, err := os.Stat(td.Worktree.Path); !os.IsNotExist(err) {
		t.Fatalf("want the worktree removed, got %v", err)
	}
	if log, _ := h.log.Read(td.ID); !strings.Contains(log, closedNote) {
		t.Fatalf("want %q in the log, got:\n%s", closedNote, log)
	}
	// Closed once is closed.
	if _, err := h.merges.Close(context.Background(), td.ID); !errors.Is(err, ErrNotClosable) {
		t.Fatalf("closing again: want ErrNotClosable, got %v", err)
	}
}

// A task edited in review has changes after all: Close refuses, and it is
// approved and merged as usual.
func TestCloseRefusesATaskEditedSince(t *testing.T) {
	parallel(t)

	h := newHarness(t, answerExec(), model.Project{Name: "p"})
	td := h.waitFor(h.add("noop").ID, model.StatusReview)
	write(t, td.Worktree.Path, "edited.txt", "x\n")
	gitRun(t, td.Worktree.Path, "add", "edited.txt")
	gitRun(t, td.Worktree.Path, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "edit")

	got, err := h.merges.Close(context.Background(), td.ID)
	if !errors.Is(err, ErrHasChanges) {
		t.Fatalf("want ErrHasChanges, got %v", err)
	}
	if got.Status != model.StatusReview || got.NoChanges || got.DiffFiles != 1 {
		t.Fatalf("want it left in review with its change counted, got status=%s no_changes=%v diff_files=%d", got.Status, got.NoChanges, got.DiffFiles)
	}
}

func TestCloseNoChangesAutomatically(t *testing.T) {
	parallel(t)

	h := newHarness(t, answerExec(), model.Project{Name: "p", CloseNoChanges: true})
	base := gitRun(t, h.proj.Path, "rev-parse", "main")
	td := h.waitFor(h.add("noop").ID, model.StatusMerged)
	if !td.NoChanges || td.MergedCommit != "" {
		t.Fatalf("want closed with no changes and no commit, got no_changes=%v commit=%q", td.NoChanges, td.MergedCommit)
	}
	if after := gitRun(t, h.proj.Path, "rev-parse", "main"); after != base {
		t.Fatalf("closing moved main from %s to %s", base, after)
	}
	waitLog(t, h, td.ID, closedNote) // written just after the task is Merged
	chat := h.st.TodoChat(td.ID)
	if len(chat) != 1 || !strings.Contains(chat[0].Content, "the task is closed") || !strings.Contains(chat[0].Content, "It already works") {
		t.Fatalf("want Claude's answer in the chat, saying it is closed, got %+v", chat)
	}
	log, _ := h.log.Read(td.ID)
	if strings.Contains(log, "ready for review") || !strings.Contains(log, closedNote) {
		t.Fatalf("want it closed without review, got log:\n%s", log)
	}
}

// With changes, nothing is different: Review, then a merge with a commit,
// also with the auto-close setting on.
func TestChangesStillGoToReviewAndMerge(t *testing.T) {
	parallel(t)

	h := newHarness(t, &fakeExec{fn: writeFile("", "x.txt")}, model.Project{Name: "p", CloseNoChanges: true})
	td := h.waitFor(h.add("change").ID, model.StatusReview)
	if td.NoChanges || td.DiffFiles != 1 {
		t.Fatalf("want a change without no_changes, got no_changes=%v diff_files=%d", td.NoChanges, td.DiffFiles)
	}
	if _, err := h.merges.Close(context.Background(), td.ID); !errors.Is(err, ErrNotClosable) {
		t.Fatalf("closing a task with changes: want ErrNotClosable, got %v", err)
	}
	h.approve(td.ID)
	if got := h.waitFor(td.ID, model.StatusMerged); got.MergedCommit == "" || got.NoChanges {
		t.Fatalf("want a merge commit and no no_changes, got commit=%q no_changes=%v", got.MergedCommit, got.NoChanges)
	}
}

// An approved task whose conflict fix ends with main's content isn't
// reviewed again (docs/merge-queue.md): it closes, saying main already has
// its changes and from which task.
func TestApprovedConflictFixWithoutChangesCloses(t *testing.T) {
	parallel(t)

	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		path := filepath.Join(req.Dir, "shared.txt")
		if b, _ := os.ReadFile(path); strings.Contains(string(b), "<<<<<<<") {
			main, err := exec.Command("git", "-C", req.Dir, "show", "main:shared.txt").Output()
			if err != nil {
				return err
			}
			return os.WriteFile(path, main, 0o644) // keep main's side
		}
		return os.WriteFile(path, []byte("line from "+filepath.Base(req.Dir)+"\n"), 0o644)
	}, result: "Kept main's version."}
	h := newHarness(t, exe, model.Project{Name: "p", MaxAutoFixes: 2})
	a, b := h.add("first"), h.add("second")
	h.waitFor(a.ID, model.StatusReview)
	h.waitFor(b.ID, model.StatusReview)
	h.approve(a.ID)
	h.waitFor(a.ID, model.StatusMerged)
	h.approve(b.ID)

	tb := h.waitFor(b.ID, model.StatusMerged)
	if !tb.NoChanges || tb.MergedCommit != "" {
		t.Fatalf("want closed with no changes, got no_changes=%v commit=%q", tb.NoChanges, tb.MergedCommit)
	}
	want := "its changes are already on main (from #" + a.ID + " first)"
	waitLog(t, h, b.ID, "✔ closed; ") // written just after the task is Merged
	log, _ := h.log.Read(b.ID)
	_, fix, _ := strings.Cut(log, "sending back to Claude")
	if strings.Contains(fix, "ready for review") || !strings.Contains(fix, "✔ closed; "+want) {
		t.Fatalf("want it closed as already on main, got log:\n%s", log)
	}
	chat := h.st.TodoChat(b.ID)
	if len(chat) == 0 || !strings.Contains(chat[len(chat)-1].Content, want) || !strings.Contains(chat[len(chat)-1].Content, "the task is closed") {
		t.Fatalf("want the chat to say main has its changes, got %+v", chat)
	}
}

// An approved task whose identical change merged first closes, naming that task.
func TestIdenticalChangeMergedFirstClosesAsAlreadyOnMain(t *testing.T) {
	parallel(t)

	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		return os.WriteFile(filepath.Join(req.Dir, "same.txt"), []byte("same\n"), 0o644)
	}}
	h := newHarness(t, exe, model.Project{Name: "p"})
	a, b := h.add("first"), h.add("second")
	h.waitFor(a.ID, model.StatusReview)
	h.waitFor(b.ID, model.StatusReview)
	h.approve(a.ID)
	h.waitFor(a.ID, model.StatusMerged)
	h.approve(b.ID)

	tb := h.waitFor(b.ID, model.StatusMerged)
	if !tb.NoChanges || tb.MergedCommit != "" {
		t.Fatalf("want closed with no changes, got no_changes=%v commit=%q", tb.NoChanges, tb.MergedCommit)
	}
	waitLog(t, h, b.ID, "✔ closed; ") // written just after the task is Merged
	if log, _ := h.log.Read(b.ID); !strings.Contains(log, "✔ closed; its changes are already on main (from #"+a.ID+" first)") {
		t.Fatalf("want it closed as already on main, got log:\n%s", log)
	}
}
