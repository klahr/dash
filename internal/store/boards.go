package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
)

// ---- Boards ----
//
// A project's boards besides its main one are kept in the project
// (model.Project.Boards); each task names the board it is on (model.Todo.Board).

// ErrBadBoard is returned for a board that isn't the project's, or a board
// name or count that isn't allowed.
var ErrBadBoard = errors.New("invalid board")

// checkBoardLocked checks that t's board is one of its project's.
func (s *Store) checkBoardLocked(t model.Todo) error {
	if t.Board == "" {
		return nil
	}
	p, ok := s.data.Projects[t.ProjectID]
	if !ok || !p.HasBoard(t.Board) {
		return fmt.Errorf("%w: no board %q in this project", ErrBadBoard, t.Board)
	}
	return nil
}

// AddBoard adds a board named name to the end of project id's boards.
func (s *Store) AddBoard(id, name string) (model.Project, model.Board, error) {
	name, err := model.CleanBoardName(name)
	if err != nil {
		return model.Project{}, model.Board{}, fmt.Errorf("%w: %v", ErrBadBoard, err)
	}
	b := model.Board{ID: NewID(), Name: name}
	p, err := s.UpdateProject(id, func(p *model.Project) error {
		if len(p.Boards) >= model.MaxBoards {
			return fmt.Errorf("%w: a project has at most %d boards besides %s", ErrBadBoard, model.MaxBoards, model.MainBoardName)
		}
		if err := uniqueBoardName(*p, "", name); err != nil {
			return err
		}
		p.Boards = append(append([]model.Board(nil), p.Boards...), b)
		return nil
	})
	return p, b, err
}

// RenameBoard renames project id's board.
func (s *Store) RenameBoard(id, board, name string) (model.Project, error) {
	name, err := model.CleanBoardName(name)
	if err != nil {
		return model.Project{}, fmt.Errorf("%w: %v", ErrBadBoard, err)
	}
	return s.UpdateProject(id, func(p *model.Project) error {
		i := p.BoardIndex(board)
		if board == "" || i < 0 {
			return ErrNotFound
		}
		if err := uniqueBoardName(*p, board, name); err != nil {
			return err
		}
		p.Boards = append([]model.Board(nil), p.Boards...)
		p.Boards[i].Name = name
		return nil
	})
}

// MoveBoard puts project id's board at index to (0 is first after the
// main board, which always comes first).
func (s *Store) MoveBoard(id, board string, to int) (model.Project, error) {
	return s.UpdateProject(id, func(p *model.Project) error {
		i := p.BoardIndex(board)
		if board == "" || i < 0 {
			return ErrNotFound
		}
		to = max(0, min(to, len(p.Boards)-1))
		bs := append([]model.Board(nil), p.Boards...)
		b := bs[i]
		bs = append(bs[:i], bs[i+1:]...)
		p.Boards = append(bs[:to], append([]model.Board{b}, bs[to:]...)...)
		return nil
	})
}

// DeleteBoard removes project id's board. Its tasks move to the main
// board; it returns them.
func (s *Store) DeleteBoard(id, board string) (model.Project, []model.Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.data.Projects[id]
	if !ok {
		return model.Project{}, nil, ErrNotFound
	}
	i := cur.BoardIndex(board)
	if board == "" || i < 0 {
		return *cur, nil, ErrNotFound
	}
	p := *cur
	p.Boards = append(append([]model.Board(nil), cur.Boards[:i]...), cur.Boards[i+1:]...)
	now := time.Now()
	var moved []model.Todo
	for _, t := range s.data.Todos {
		if t.ProjectID == id && t.Board == board {
			cp := clone(*t)
			cp.Board, cp.UpdatedAt = "", now
			moved = append(moved, cp)
		}
	}
	err := s.writeLocked(func(tx *sql.Tx) error {
		if err := putProject(tx, &p); err != nil {
			return err
		}
		for i := range moved {
			if err := putTodo(tx, &moved[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return *cur, nil, err
	}
	s.data.Projects[id] = &p
	for i := range moved {
		t := moved[i]
		s.data.Todos[t.ID] = &t
		s.emit(model.EvTodoUpdated, t.ProjectID, t.ID, t)
	}
	s.emit(model.EvProjectUpdated, id, "", p)
	return p, moved, nil
}

// uniqueBoardName refuses name for board (""; a new one) when another of
// p's boards, or its main board, has it already (letter case aside).
func uniqueBoardName(p model.Project, board, name string) error {
	if strings.EqualFold(name, model.MainBoardName) {
		return fmt.Errorf("%w: %s is the project's main board", ErrBadBoard, model.MainBoardName)
	}
	for _, b := range p.Boards {
		if b.ID != board && strings.EqualFold(b.Name, name) {
			return fmt.Errorf("%w: the project has a board named %s already", ErrBadBoard, b.Name)
		}
	}
	return nil
}

// AddBoard is Store.AddBoard for one of the org's projects.
func (o *OrgStore) AddBoard(id, name string) (model.Project, model.Board, error) {
	if !o.ownsProject(id) {
		return model.Project{}, model.Board{}, ErrNotFound
	}
	return o.s.AddBoard(id, name)
}

// RenameBoard is Store.RenameBoard for one of the org's projects.
func (o *OrgStore) RenameBoard(id, board, name string) (model.Project, error) {
	if !o.ownsProject(id) {
		return model.Project{}, ErrNotFound
	}
	return o.s.RenameBoard(id, board, name)
}

// MoveBoard is Store.MoveBoard for one of the org's projects.
func (o *OrgStore) MoveBoard(id, board string, to int) (model.Project, error) {
	if !o.ownsProject(id) {
		return model.Project{}, ErrNotFound
	}
	return o.s.MoveBoard(id, board, to)
}

// DeleteBoard is Store.DeleteBoard for one of the org's projects.
func (o *OrgStore) DeleteBoard(id, board string) (model.Project, []model.Todo, error) {
	if !o.ownsProject(id) {
		return model.Project{}, nil, ErrNotFound
	}
	return o.s.DeleteBoard(id, board)
}
