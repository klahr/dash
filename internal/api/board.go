package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
)

// BoardMCP serves the chat's Dash tools (POST /mcp/dash), authenticated by
// the per-run tokens in s.Board.
func (s *Server) BoardMCP() http.Handler {
	return &mcptools.Handler{Tokens: s.Board, Board: board{s}}
}

// remoteMCP serves the tools of cmd/dash-mcp for every computer in the
// dashboard (POST /mcp, behind OAuth); api is this server's unauthenticated mux.
func (s *Server) remoteMCP(api http.Handler) http.Handler {
	return &mcptools.Server{
		Name:         "dash",
		Instructions: mcptools.FleetInstructions,
		Tools:        mcptools.FleetTools(mcptools.Fleet{API: hubAPI{api}}),
	}
}

// hubAPI calls this server's REST API in-process, past the login: the caller
// is already authenticated. /peer/<name>/… goes on to that computer through
// s.Peers, like the dashboard's own requests.
type hubAPI struct{ h http.Handler }

func (a hubAPI) Call(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, method, "http://dash"+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	a.h.ServeHTTP(w, r)
	if w.Code >= 400 {
		var e struct{ Error string }
		if json.Unmarshal(w.Body.Bytes(), &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("%s %s: %d %s", method, path, w.Code, strings.TrimSpace(w.Body.String()))
	}
	switch o := out.(type) {
	case nil:
	case *string:
		*o = w.Body.String()
	default:
		return json.Unmarshal(w.Body.Bytes(), out)
	}
	return nil
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
		ProjectID: projectID, Title: t.Title, Prompt: t.Prompt, Priority: t.Priority,
	}, t.Enqueue)
}

func (b board) QueueTodo(id string) (model.Todo, error) { return b.s.enqueue(id) }
