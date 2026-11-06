package store

import (
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
