package queue

import (
	"slices"
	"testing"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

// A task's model reaches Claude as --model; a task without one passes none,
// leaving Dash's -claude-args (or Claude Code) to choose.
func TestRunUsesTaskModel(t *testing.T) {
	exe := &fakeExec{fn: func(_ int, req agent.ExecRequest) error {
		write(t, req.Dir, "a.txt", "a")
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p"})
	td, err := h.st.CreateTodo(model.Todo{ProjectID: h.proj.ID, Title: "hard", Prompt: "do it", Model: "opus"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SetStatus(td.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	h.engine.Kick()
	h.waitFor(td.ID, model.StatusReview)
	args := exe.Calls()[0].ExtraArgs
	if i := slices.Index(args, "--model"); i < 0 || args[i+1] != "opus" {
		t.Fatalf("want --model opus, got %v", args)
	}

	h.waitFor(h.add("small").ID, model.StatusReview)
	if args := exe.Calls()[1].ExtraArgs; slices.Contains(args, "--model") {
		t.Fatalf("task without a model got --model: %v", args)
	}
}
