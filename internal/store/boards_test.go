package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/klahr/dash/internal/model"
)

func TestBoards(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "dash.db")
	s := openTest(t, path)
	p, err := s.CreateProject(model.Project{Name: "app"})
	if err != nil {
		t.Fatal(err)
	}

	// Names: one line, not Main, each once.
	for _, name := range []string{"", "  ", "main", "a\nb"} {
		if _, _, err := s.AddBoard(p.ID, name); !errors.Is(err, ErrBadBoard) {
			t.Errorf("AddBoard(%q) = %v, want ErrBadBoard", name, err)
		}
	}
	_, web, err := s.AddBoard(p.ID, " Web ")
	if err != nil || web.Name != "Web" || web.ID == "" {
		t.Fatalf("AddBoard = %+v, %v", web, err)
	}
	if _, _, err := s.AddBoard(p.ID, "web"); !errors.Is(err, ErrBadBoard) {
		t.Errorf("a second board named web: %v", err)
	}
	_, ops, _ := s.AddBoard(p.ID, "Ops")
	if p, err = s.RenameBoard(p.ID, ops.ID, "Infra"); err != nil || p.Boards[p.BoardIndex(ops.ID)].Name != "Infra" {
		t.Fatalf("RenameBoard = %+v, %v", p.Boards, err)
	}
	if _, err := s.RenameBoard(p.ID, ops.ID, "Web"); !errors.Is(err, ErrBadBoard) {
		t.Errorf("renamed to another board's name: %v", err)
	}
	if _, err := s.RenameBoard(p.ID, "nope", "X"); !errors.Is(err, ErrNotFound) {
		t.Errorf("renamed a board that isn't there: %v", err)
	}
	if p, err = s.MoveBoard(p.ID, ops.ID, 0); err != nil || p.Boards[0].ID != ops.ID || p.Boards[1].ID != web.ID {
		t.Fatalf("MoveBoard = %+v, %v", p.Boards, err)
	}
	if id, ok := p.FindBoard("infra"); !ok || id != ops.ID {
		t.Errorf("FindBoard(infra) = %q, %v", id, ok)
	}

	// Tasks go on a board of their project's only.
	if _, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: "x", Board: "nope"}); !errors.Is(err, ErrBadBoard) {
		t.Errorf("a task on a board that isn't there: %v", err)
	}
	onWeb, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: "on web", Board: web.ID})
	if err != nil {
		t.Fatal(err)
	}
	main, _ := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: "on main"})
	if _, err := s.UpdateTodo(main.ID, func(t *model.Todo) error { t.Board = "nope"; return nil }); !errors.Is(err, ErrBadBoard) {
		t.Errorf("moved to a board that isn't there: %v", err)
	}
	if _, err := s.UpdateTodo(main.ID, func(t *model.Todo) error { t.Board = ops.ID; return nil }); err != nil {
		t.Fatal(err)
	}

	// Deleting a board moves its tasks to the main board, for good.
	p, moved, err := s.DeleteBoard(p.ID, web.ID)
	if err != nil || len(moved) != 1 || moved[0].ID != onWeb.ID || len(p.Boards) != 1 {
		t.Fatalf("DeleteBoard = %+v, %+v, %v", p.Boards, moved, err)
	}
	s.Close()
	s = openTest(t, path)
	if got, _ := s.GetTodo(onWeb.ID); got.Board != "" {
		t.Errorf("the deleted board's task is on %q, want the main board", got.Board)
	}
	if got, _ := s.GetTodo(main.ID); got.Board != ops.ID {
		t.Errorf("the other task is on %q, want %q", got.Board, ops.ID)
	}
	if got, _ := s.GetProject(p.ID); len(got.Boards) != 1 || got.Boards[0].Name != "Infra" {
		t.Errorf("boards after reopening = %+v", got.Boards)
	}
}
