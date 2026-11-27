package queue

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

// A task that meets the rules merges as Approve does, and records which
// rules it met.
func TestAutoApproveRecordsTheRulesItMet(t *testing.T) {
	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		if err := os.MkdirAll(filepath.Join(req.Dir, "docs"), 0o755); err != nil {
			return err
		}
		write(t, req.Dir, "docs/a.md", "pass\n")
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p", AutoApprove: true, TestCommand: "grep -qx pass docs/a.md",
		AutoApproveRules: &model.AutoApproveRules{MaxLines: 5, Paths: []string{"docs/**"}}})
	td := h.waitFor(h.add("docs").ID, model.StatusMerged)
	a := td.AutoApproval
	if a == nil || !a.Approved || strings.Join(a.Matched, "|") != "tests passed|at most 5 changed lines (1)|only docs/** changed" {
		t.Fatalf("auto approval = %+v", a)
	}
	if td.MergedCommit == "" {
		t.Error("want it merged with a commit, as Approve does")
	}
}

// A task that misses a rule waits in Review, saying which.
func TestAutoApproveMissedRuleGoesToReview(t *testing.T) {
	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		write(t, req.Dir, "a.txt", "pass\n")
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p", AutoApprove: true, TestCommand: "grep -qx pass a.txt",
		AutoApproveRules: &model.AutoApproveRules{Paths: []string{"docs/**"}}})
	td := h.waitFor(h.add("code").ID, model.StatusReview)
	if a := td.AutoApproval; a == nil || a.Approved || !strings.Contains(a.Failed, "a.txt, outside docs/**") {
		t.Fatalf("auto approval = %+v", a)
	}
	log, _ := h.engine.Log.Read(td.ID)
	if !strings.Contains(string(log), "not approved automatically: it changed a.txt") {
		t.Errorf("the log doesn't say why:\n%s", log)
	}
}

// Without a test command the tests are skipped, so nothing is approved on its own.
func TestAutoApproveNeedsTests(t *testing.T) {
	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		write(t, req.Dir, "a.txt", "a\n")
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p", AutoApprove: true})
	td := h.waitFor(h.add("untested").ID, model.StatusReview)
	if a := td.AutoApproval; a == nil || a.Approved || !strings.Contains(a.Failed, "no test command") {
		t.Fatalf("auto approval = %+v", a)
	}
}

// A task that had a merge conflict is never approved on its own.
func TestAutoApproveNeverAfterAConflict(t *testing.T) {
	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		write(t, req.Dir, "a.txt", "pass\n")
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p", AutoApprove: true, TestCommand: "grep -qx pass a.txt"})
	td, err := h.st.CreateTodo(model.Todo{ProjectID: h.proj.ID, Title: "c", Prompt: "do c", HadConflict: true})
	if err != nil {
		t.Fatal(err)
	}
	if !td.HadConflict {
		t.Fatal("the store didn't keep HadConflict on the new task")
	}
	if _, err := h.st.SetStatus(td.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	h.engine.Kick()
	td = h.waitFor(td.ID, model.StatusReview)
	if a := td.AutoApproval; a == nil || a.Approved || !strings.Contains(a.Failed, "merge conflict") {
		t.Fatalf("auto approval = %+v", a)
	}
}

// With the review rule, Claude's review runs when the task finishes and
// decides: "Ready to merge" merges it, anything else sends it to Review.
func TestAutoApproveReviewRule(t *testing.T) {
	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		name := "good.txt"
		if strings.Contains(req.Prompt, "do bad") {
			name = "bad.txt"
		}
		write(t, req.Dir, name, "pass\n")
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p", AutoApprove: true, TestCommand: "true",
		AutoApproveRules: &model.AutoApproveRules{Review: true}})
	var reviews atomic.Int32
	h.engine.Review = func(_ context.Context, td model.Todo, p model.Project) (string, error) {
		reviews.Add(1)
		if td.Title == "good" {
			return "**Ready to merge**\n\nNothing to fix.", nil
		}
		return "Needs changes\n1. bad.txt:1 is wrong", nil
	}
	good := h.waitFor(h.add("good").ID, model.StatusMerged)
	if a := good.AutoApproval; a == nil || !a.Approved || a.Review != "**Ready to merge**" {
		t.Fatalf("good: auto approval = %+v", a)
	}
	bad := h.waitFor(h.add("bad").ID, model.StatusReview)
	if a := bad.AutoApproval; a == nil || a.Approved || !strings.Contains(a.Failed, "review found issues: Needs changes") {
		t.Fatalf("bad: auto approval = %+v", a)
	}
	if n := reviews.Load(); n != 2 {
		t.Errorf("reviews = %d, want one per task", n)
	}
}

// The review isn't run for a task another rule already refuses.
func TestAutoApproveReviewOnlyWhenTheOtherRulesHold(t *testing.T) {
	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		write(t, req.Dir, "a.txt", "broken\n")
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p", AutoApprove: true, TestCommand: "grep -qx pass a.txt",
		AutoApproveRules: &model.AutoApproveRules{Review: true}})
	h.engine.Review = func(context.Context, model.Todo, model.Project) (string, error) {
		t.Error("the review ran for a task whose tests fail")
		return "Ready to merge", nil
	}
	td := h.waitFor(h.add("failing").ID, model.StatusReview)
	if a := td.AutoApproval; a == nil || a.Approved || !strings.Contains(a.Failed, "tests failed") {
		t.Fatalf("auto approval = %+v", a)
	}
}
