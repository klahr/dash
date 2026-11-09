package queue

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// A runner's adopted checkout of a hub's project that its owner is editing
// by hand: an approved task waits (nothing is pushed or moved) while the
// checkout has uncommitted changes to tracked files, even ones the task
// doesn't touch, and lands once they're stashed. The changes are never
// overwritten.
func TestHubCheckoutWithUncommittedChangesWaits(t *testing.T) {
	h := newHarness(t, &fakeExec{fn: writeFile("", "feature.txt")}, model.Project{Name: "p"})
	origin, _, _ := h.runnerProject()
	write(t, h.proj.Path, "notes.txt", "v1\n")
	gitRun(t, h.proj.Path, "add", "notes.txt")
	gitRun(t, h.proj.Path, "commit", "-q", "-m", "notes")
	gitRun(t, h.proj.Path, "push", "-q", "origin", "main")
	p, err := h.st.UpdateProject(h.proj.ID, func(p *model.Project) error { p.HubCheckout = model.HubAdopted; return nil })
	if err != nil {
		t.Fatal(err)
	}
	h.proj = p

	td := h.waitFor(h.add("feature").ID, model.StatusReview)
	write(t, h.proj.Path, "notes.txt", "my edit\n") // the owner's work in progress
	before := gitRun(t, origin, "rev-parse", "main")
	h.approve(td.ID)

	deadline := time.Now().Add(10 * time.Second)
	for x, _ := h.st.GetTodo(td.ID); !strings.Contains(x.Error, "commit or stash"); x, _ = h.st.GetTodo(td.ID) {
		if time.Now().After(deadline) {
			t.Fatalf("never waited: %s %q", x.Status, x.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if x, _ := h.st.GetTodo(td.ID); x.Status != model.StatusMerging || !strings.Contains(x.Error, "notes.txt") {
		t.Errorf("waiting task %s %q", x.Status, x.Error)
	}
	if gitRun(t, origin, "rev-parse", "main") != before {
		t.Error("pushed while the checkout was dirty")
	}
	if b := gitRun(t, h.proj.Path, "show", ":notes.txt"); b != "v1" {
		t.Errorf("index changed: %q", b)
	}

	gitRun(t, h.proj.Path, "stash", "-q")
	td = h.waitFor(td.ID, model.StatusMerged)
	if gitRun(t, origin, "rev-parse", "main") != td.MergedCommit {
		t.Error("not pushed after the stash")
	}
	gitRun(t, h.proj.Path, "stash", "pop", "-q")
	if b := gitRun(t, h.proj.Path, "diff", "--name-only"); b != "notes.txt" {
		t.Errorf("the owner's change after landing: %q", b)
	}
}

// checkoutClean only looks at a hub project's folder with the branch that
// lands checked out.
func TestCheckoutClean(t *testing.T) {
	h := newHarness(t, &fakeExec{}, model.Project{Name: "p"})
	ctx := context.Background()
	write(t, h.proj.Path, "a.txt", "1")
	gitRun(t, h.proj.Path, "add", "a.txt")
	gitRun(t, h.proj.Path, "commit", "-q", "-m", "a")
	write(t, h.proj.Path, "a.txt", "2")

	p := h.proj
	if err := h.merges.checkoutClean(ctx, p); err != nil {
		t.Errorf("the runner's own project: %v", err)
	}
	p.HubProjectID, p.HubCheckout = "hp", model.HubAdopted
	var dirty *gitops.ErrDirtyCheckout
	if err := h.merges.checkoutClean(ctx, p); !errors.As(err, &dirty) || len(dirty.Files) != 1 || dirty.Files[0] != "a.txt" {
		t.Errorf("dirty hub checkout: %v", err)
	}
	p.WorkBranch = "other" // tasks land on another branch: this folder doesn't move
	if err := h.merges.checkoutClean(ctx, p); err != nil {
		t.Errorf("other branch: %v", err)
	}
}
