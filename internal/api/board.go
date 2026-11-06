package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
)

// BoardMCP serves the chat's Dash tools (POST /mcp/dash), authenticated by
// the per-run tokens in s.Board.
func (s *Server) BoardMCP() http.Handler {
	return &mcptools.Handler{Tokens: s.Board, Board: board{s}}
}

// board is this computer's task board for the chat's MCP tools.
type board struct{ s *Server }

func (b board) Projects() ([]model.Project, []string, error) {
	return b.s.Store.ListProjects(), nil, nil
}

func (b board) Project(ref string) (model.Project, error) {
	for _, p := range b.s.Store.ListProjects() {
		if p.ID == ref || strings.EqualFold(p.Name, ref) {
			return p, nil
		}
	}
	return model.Project{}, fmt.Errorf("no project %q (see list_projects)", ref)
}

func (b board) Todos(projectID string) ([]model.Todo, error) {
	return nonNil(b.s.Store.ListTodos(projectID)), nil
}

func (b board) Todo(id string) (model.Todo, error) {
	t, err := b.s.Store.GetTodo(id)
	if err != nil {
		return t, fmt.Errorf("no task %q", id)
	}
	return t, nil
}

func (b board) CreateTodo(projectID string, t mcptools.NewTodo) (model.Todo, error) {
	return b.s.addTodo(model.Todo{
		ProjectID: projectID, Title: t.Title, Prompt: t.Prompt, Goal: t.Goal, Priority: t.Priority,
	}, t.Enqueue)
}

func (b board) QueueTodo(id string) (model.Todo, error) { return b.s.enqueue(id) }
