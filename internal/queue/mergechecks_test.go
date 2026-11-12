package queue

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

// approveFresh approves like the API does: a fresh budget of automatic fixes.
func (h *harness) approveFresh(id string) {
	h.t.Helper()
	if _, err := h.st.UpdateTodo(id, func(x *model.Todo) error { x.AutoFixes = 0; return nil }); err != nil {
		h.t.Fatal(err)
	}
	h.approve(id)
}

// onMain is file's content on the project's main branch, or "" when main
// doesn't have it.
func (h *harness) onMain(file string) string {
	h.t.Helper()
	out, err := exec.Command("git", "-C", h.proj.Path, "show", "main:"+file).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func testRuns(t *testing.T, h *harness, id string) int {
	t.Helper()
	log, err := h.engine.Log.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(log), "▶ tests: ")
}

// A task approved while its tests fail is tested again before it lands, and
// goes back to Claude instead of onto main; once green it merges.
func TestApprovedRedBuildGoesBackToClaude(t *testing.T) {
	parallel(t)

	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		write(t, req.Dir, "a.txt", map[bool]string{true: "pass\n", false: "broken\n"}[n >= 3])
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p", TestCommand: "grep -qx pass a.txt", MaxAutoFixes: 1})
	td := h.waitFor(h.add("make a").ID, model.StatusReview)
	if td.Checks == nil || td.Checks.Passed {
		t.Fatalf("want failing checks in review, got %+v", td.Checks)
	}

	h.approveFresh(td.ID)
	td = h.waitFor(td.ID, model.StatusMerged)
	if td.Runs != 3 {
		t.Errorf("want Claude to run a third time to fix the red build, ran %d times", td.Runs)
	}
	if !strings.Contains(td.SentBack, "tests fail") {
		t.Errorf("sent back = %q", td.SentBack)
	}
	if fb := strings.Join(td.Feedback, "\n"); !strings.Contains(fb, "wasn't merged") || !strings.Contains(fb, "grep -qx pass a.txt") {
		t.Errorf("Claude should be told the merge's test failure, feedback:\n%s", fb)
	}
	if got := h.onMain("a.txt"); got != "pass\n" {
		t.Errorf("main has a.txt %q, want the fixed version", got)
	}
}

// Without automatic fixes left, a red build fails the task and main stays as it was.
func TestRedBuildWithoutFixesLeftNeverLands(t *testing.T) {
	parallel(t)

	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		write(t, req.Dir, "a.txt", "broken\n")
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p", TestCommand: "grep -qx pass a.txt", MaxAutoFixes: 1})
	td := h.waitFor(h.add("make a").ID, model.StatusReview)

	h.approve(td.ID) // the fix was spent in review
	td = h.waitFor(td.ID, model.StatusFailed)
	if !strings.Contains(td.Error, "tests fail") {
		t.Errorf("error = %q", td.Error)
	}
	if td.Checks == nil || td.Checks.Stage != "merge" || td.Checks.Passed {
		t.Errorf("want the merge's failing checks, got %+v", td.Checks)
	}
	if got := h.onMain("a.txt"); got != "" {
		t.Errorf("a red build landed on main: a.txt = %q", got)
	}
}

// Checks that passed on an earlier commit don't count for the one that lands.
func TestCommitAfterGreenChecksIsTestedBeforeMerging(t *testing.T) {
	parallel(t)

	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		write(t, req.Dir, "a.txt", "pass\n")
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p", TestCommand: "grep -qx pass a.txt", MaxAutoFixes: 1})
	td := h.waitFor(h.add("make a").ID, model.StatusReview)
	if td.Checks == nil || !td.Checks.Passed || td.Checks.Commit == "" {
		t.Fatalf("want passing checks on a commit, got %+v", td.Checks)
	}

	write(t, td.Worktree.Path, "a.txt", "broken\n")
	gitIn(t, td.Worktree.Path, "commit", "-qam", "break it")

	h.approveFresh(td.ID)
	td = h.waitFor(td.ID, model.StatusMerged)
	if td.Runs != 2 || !strings.Contains(td.SentBack, "tests fail") {
		t.Errorf("want the broken commit sent back to Claude once, runs=%d sent back=%q", td.Runs, td.SentBack)
	}
	if got := h.onMain("a.txt"); got != "pass\n" {
		t.Errorf("main has a.txt %q", got)
	}
}

// Checks that passed on exactly the commit that lands aren't run again.
func TestGreenChecksOnTheSameCommitAreNotRunAgain(t *testing.T) {
	parallel(t)

	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		write(t, req.Dir, "a.txt", "pass\n")
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p", TestCommand: "grep -qx pass a.txt"})
	td := h.waitFor(h.add("make a").ID, model.StatusReview)
	h.approve(td.ID)
	td = h.waitFor(td.ID, model.StatusMerged)
	if td.Checks == nil || td.Checks.Stage != "review" {
		t.Errorf("want the review's checks kept, got %+v", td.Checks)
	}
	if n := testRuns(t, h, td.ID); n != 1 {
		t.Errorf("tests ran %d times, want 1", n)
	}
}
