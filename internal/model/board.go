package model

import (
	"errors"
	"fmt"
	"strings"
)

// A project's tasks are on one or more boards. Every project has its main
// board, which is never stored: a task whose Board is "" is on it. The
// project's other boards are in Project.Boards (see docs/boards.md).

// MainBoardName is the name of a project's main board.
const MainBoardName = "Main"

// Limits on a project's boards.
const (
	MaxBoards    = 50 // besides the main board
	MaxBoardName = 60
)

// Board is one of a project's boards besides its main one.
type Board struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CleanBoardName trims a board's name and checks it.
func CleanBoardName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", errors.New("a board needs a name")
	case len(name) > MaxBoardName:
		return "", fmt.Errorf("the board's name is longer than %d characters", MaxBoardName)
	case strings.ContainsAny(name, "\r\n"):
		return "", errors.New("a board's name is one line")
	}
	return name, nil
}

// HasBoard reports whether id is one of p's boards ("" is the main board).
func (p Project) HasBoard(id string) bool {
	return id == "" || p.BoardIndex(id) >= 0
}

// BoardIndex is where board id is in p.Boards (-1: not there).
func (p Project) BoardIndex(id string) int {
	for i, b := range p.Boards {
		if b.ID == id {
			return i
		}
	}
	return -1
}

// FindBoard is the id of p's board ref names: its id, or its name (letter
// case aside; the main board's is ""). ok is false for no such board.
func (p Project) FindBoard(ref string) (id string, ok bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.EqualFold(ref, MainBoardName) {
		return "", true
	}
	if p.BoardIndex(ref) >= 0 {
		return ref, true
	}
	for _, b := range p.Boards {
		if strings.EqualFold(b.Name, ref) {
			return b.ID, true
		}
	}
	return "", false
}
