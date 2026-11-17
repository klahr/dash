package store

import (
	"maps"
	"path/filepath"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// RunnerLoad counts the todos in the given statuses by their runner, and
// leaves out other statuses and todos on no runner.
func TestRunnerLoad(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p, err := s.CreateProject(model.Project{Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		runner string
		status model.TodoStatus
	}{
		{"a", model.StatusInProgress},
		{"a", model.StatusInProgress},
		{"b", model.StatusInProgress},
		{"b", model.StatusQueued},
		{"", model.StatusInProgress},
	} {
		td, err := s.CreateQueuedTodo(model.Todo{ProjectID: p.ID, Title: "t", Prompt: "x"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdateTodo(td.ID, func(td *model.Todo) error {
			td.Runner, td.Status = c.runner, c.status
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := s.RunnerLoad(model.StatusInProgress), map[string]int{"a": 2, "b": 1}; !maps.Equal(got, want) {
		t.Errorf("in progress: %v, want %v", got, want)
	}
	if got, want := s.RunnerLoad(model.StatusInProgress, model.StatusQueued), map[string]int{"a": 2, "b": 2}; !maps.Equal(got, want) {
		t.Errorf("in progress or queued: %v, want %v", got, want)
	}
}
