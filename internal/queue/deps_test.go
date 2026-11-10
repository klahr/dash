package queue

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// A dependent waits queued while its dependency is in review, an unblocked
// task with a worse priority runs meanwhile, and the dependent starts once
// the dependency is merged, on top of its code.
func TestDependentWaitsUntilDependencyIsMerged(t *testing.T) {
	parallel(t)

	var sawBase atomic.Bool
	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		switch {
		case strings.Contains(req.Prompt, "do base"):
			write(t, req.Dir, "base.txt", "base\n")
		case strings.Contains(req.Prompt, "do next"):
			_, err := os.Stat(filepath.Join(req.Dir, "base.txt"))
			sawBase.Store(err == nil)
			write(t, req.Dir, "next.txt", "next\n")
		default:
			write(t, req.Dir, "other.txt", "other\n")
		}
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p"})

	base, err := h.st.CreateTodo(model.Todo{ProjectID: h.proj.ID, Title: "base", Prompt: "do base", Priority: new(5)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SetStatus(base.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	h.engine.Kick()
	h.waitFor(base.ID, model.StatusReview)

	next, err := h.st.CreateTodo(model.Todo{ProjectID: h.proj.ID, Title: "next", Prompt: "do next", Priority: new(0), DependsOn: []string{base.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SetStatus(next.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	other, err := h.st.CreateTodo(model.Todo{ProjectID: h.proj.ID, Title: "other", Prompt: "do other", Priority: new(10)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SetStatus(other.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	h.engine.Kick()
	h.waitFor(other.ID, model.StatusReview)

	// Give the dispatcher a few more chances; next must still wait.
	for range 3 {
		h.engine.Kick()
		time.Sleep(50 * time.Millisecond)
	}
	if td, _ := h.st.GetTodo(next.ID); td.Status != model.StatusQueued || td.Worktree != nil {
		t.Fatalf("dependent started while its dependency is in review: %s", td.Status)
	}
	for _, c := range exe.Calls() {
		if strings.Contains(c.Prompt, "do next") {
			t.Fatal("Claude ran the dependent before its dependency was merged")
		}
	}

	h.approve(base.ID)
	h.waitFor(base.ID, model.StatusMerged)
	h.waitFor(next.ID, model.StatusReview) // started by the merge's Kick, well before the 5s tick
	if !sawBase.Load() {
		t.Fatal("dependent's worktree doesn't contain its dependency's merged code")
	}
}

func TestDependencyCyclesAreRejected(t *testing.T) {
	parallel(t)

	st, err := store.Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := st.CreateProject(model.Project{Name: "p"})
	a, _ := st.CreateTodo(model.Todo{ProjectID: p.ID, Title: "a"})
	b, err := st.CreateTodo(model.Todo{ProjectID: p.ID, Title: "b", DependsOn: []string{a.ID}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateTodo(model.Todo{ProjectID: p.ID, Title: "c", DependsOn: []string{b.ID}})
	if err != nil {
		t.Fatal(err)
	}
	// a → c → b → a
	_, err = st.UpdateTodo(a.ID, func(x *model.Todo) error { x.DependsOn = []string{c.ID}; return nil })
	if !errors.Is(err, store.ErrBadDependency) {
		t.Fatalf("cycle: got %v, want ErrBadDependency", err)
	}
	if got, _ := st.GetTodo(a.ID); len(got.DependsOn) != 0 {
		t.Fatalf("rejected dependencies were stored: %v", got.DependsOn)
	}
}
