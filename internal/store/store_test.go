package store

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/klahr/dash/internal/model"
)

func TestChatSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.ChatMessage{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}
	if err := s.SetChat(want); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Chat(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after reopen: %v, want %v", got, want)
	}

	if err := s.SetChat(nil); err != nil {
		t.Fatal(err)
	}
	if s, _ = Open(path, nil); len(s.Chat()) != 0 {
		t.Fatalf("cleared chat came back: %v", s.Chat())
	}
}

func TestTodoChatSurvivesReopenAndGoesWithTodo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTodoChat("missing", []model.ChatMessage{{Role: "user", Content: "hi"}}); err != ErrNotFound {
		t.Fatalf("chat on a missing todo: %v, want ErrNotFound", err)
	}
	p, err := s.CreateProject(model.Project{Name: "p", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	td, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	want := []model.ChatMessage{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}
	if err := s.SetTodoChat(td.ID, want); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(path, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.TodoChat(td.ID); !reflect.DeepEqual(got, want) {
		t.Fatalf("after reopen: %v, want %v", got, want)
	}
	if len(s.Chat()) != 0 {
		t.Fatalf("todo chat leaked into the general chat: %v", s.Chat())
	}
	if _, err := s.DeleteTodo(td.ID); err != nil {
		t.Fatal(err)
	}
	if s, _ = Open(path, nil); len(s.TodoChat(td.ID)) != 0 {
		t.Fatalf("deleted todo's chat came back: %v", s.TodoChat(td.ID))
	}
}

func TestReviewingLocksStatusUntilReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProject(model.Project{Name: "p", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	td, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []model.TodoStatus{model.StatusQueued, model.StatusInProgress, model.StatusReview} {
		if _, err := s.SetStatus(td.ID, st, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.UpdateTodo(td.ID, func(t *model.Todo) error { t.Reviewing = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(td.ID, model.StatusQueued, ""); err != ErrReviewing {
		t.Fatalf("status change while reviewing: %v, want ErrReviewing", err)
	}
	if _, err := s.UpdateTodo(td.ID, func(t *model.Todo) error { t.Title = "renamed"; return nil }); err != nil {
		t.Fatalf("non-status edit while reviewing: %v", err)
	}
	if s, err = Open(path, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTodo(td.ID); got.Reviewing {
		t.Fatal("reviewing survived a restart")
	}
	if _, err := s.SetStatus(td.ID, model.StatusQueued, ""); err != nil {
		t.Fatalf("status change after the review: %v", err)
	}
}

func TestDependsOnValidationAndDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.CreateProject(model.Project{Name: "p"})
	q, _ := s.CreateProject(model.Project{Name: "q"})
	a, _ := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: "a"})
	elsewhere, _ := s.CreateTodo(model.Todo{ProjectID: q.ID, Title: "x"})

	for name, deps := range map[string][]string{
		"unknown":       {"nope"},
		"other project": {elsewhere.ID},
	} {
		if _, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: name, DependsOn: deps}); !errors.Is(err, ErrBadDependency) {
			t.Errorf("%s: got %v, want ErrBadDependency", name, err)
		}
	}
	if _, err := s.UpdateTodo(a.ID, func(x *model.Todo) error { x.DependsOn = []string{a.ID}; return nil }); !errors.Is(err, ErrBadDependency) {
		t.Errorf("self: got %v, want ErrBadDependency", err)
	}

	b, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: "b", DependsOn: []string{a.ID, " " + a.ID, ""}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.DependsOn, []string{a.ID}) {
		t.Fatalf("depends_on not cleaned: %q", b.DependsOn)
	}
	// Survives a reopen.
	if s, err = Open(path, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTodo(b.ID); !reflect.DeepEqual(got.DependsOn, []string{a.ID}) {
		t.Fatalf("after reopen: %q", got.DependsOn)
	}
	// Deleting a forgets it in others' dependencies.
	if _, err := s.DeleteTodo(a.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTodo(b.ID); len(got.DependsOn) != 0 {
		t.Fatalf("deleted dependency kept: %q", got.DependsOn)
	}
}

func TestOldStateWithoutDependsOnLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	old := `{"projects":{"p":{"id":"p","name":"p"}},"todos":{"t":{"id":"t","project_id":"p","title":"t","status":"queued"}}}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetTodo("t"); err != nil || len(got.DependsOn) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}
