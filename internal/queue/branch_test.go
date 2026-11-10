package queue

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

// A task started on a working branch lands there, not on the default
// branch; one started after switching back lands on the default branch, and
// a task that started before a switch keeps the branch it started from.
func TestTasksLandOnTheWorkingBranch(t *testing.T) {
	parallel(t)

	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		write(t, req.Dir, "f"+string(rune('0'+n))+".txt", "x\n")
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p"})
	if out, err := exec.Command("git", "-C", h.proj.Path, "branch", "feature", "main").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	setBranch := func(b string) {
		t.Helper()
		if _, err := h.st.UpdateProject(h.proj.ID, func(p *model.Project) error { p.WorkBranch = b; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	files := func(ref string) string {
		t.Helper()
		out, err := exec.Command("git", "-C", h.proj.Path, "ls-tree", "--name-only", ref).CombinedOutput()
		if err != nil {
			t.Fatalf("%v %s", err, out)
		}
		return strings.Join(strings.Fields(string(out)), " ")
	}

	setBranch("feature")
	a := h.add("on feature")
	a = h.waitFor(a.ID, model.StatusReview)
	if a.Worktree.BaseBranch != "feature" {
		t.Fatalf("worktree base branch %q", a.Worktree.BaseBranch)
	}
	setBranch("") // switched back before it is approved: it still lands on feature
	h.approve(a.ID)
	h.waitFor(a.ID, model.StatusMerged)
	if got := files("feature"); got != "f1.txt" {
		t.Errorf("feature has %q", got)
	}
	if got := files("main"); got != "" {
		t.Errorf("main has %q", got)
	}

	b := h.add("on main")
	h.waitFor(b.ID, model.StatusReview)
	h.approve(b.ID)
	h.waitFor(b.ID, model.StatusMerged)
	if got := files("main"); got != "f2.txt" {
		t.Errorf("main has %q", got)
	}
}
