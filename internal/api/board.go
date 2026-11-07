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
// the per-run tokens in s.Board. A chat's tools act as the user it runs for,
// in their org; a task's issue tools act in the task's project's org.
func (s *Server) BoardMCP() http.Handler {
	return &mcptools.Handler{Tokens: s.Board, Board: func(sc mcptools.Scope) (mcptools.Board, error) {
		c := Caller{Org: sc.Org, User: sc.User}
		if sc.Todo != "" {
			org, ok := s.Store.TodoOrg(sc.Todo)
			if !ok {
				return nil, errors.New("the task is gone")
			}
			c = Caller{Org: org}
		}
		if c.Org == 0 {
			return nil, errNoOrg
		}
		return board{s.as(c)}, nil
	}}
}

// remoteMCP serves the tools of cmd/dash-mcp (POST /mcp, behind OAuth) as the
// token's user, in their org: this computer through the service, and, for
// platform admins, the other computers in the dashboard.
func (s *Server) remoteMCP() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := s.caller(r)
		if err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		sv := s.as(c)
		srv := &mcptools.Server{
			Name:         "dash",
			Instructions: mcptools.FleetInstructions,
			Tools: mcptools.FleetTools(mcptools.Fleet{Computers: func() ([]mcptools.Machine, error) {
				return sv.computers(r.Context()), nil
			}}),
		}
		srv.ServeHTTP(w, r)
	})
}

// computers are the computers the caller's tools reach: this one, and the
// others in the dashboard for platform admins (they belong to the whole
// Dash, not to one org).
func (sv *service) computers(ctx context.Context) []mcptools.Machine {
	s := sv.s
	self := "local"
	if s.Peers != nil {
		self = s.Peers.Self
	}
	out := []mcptools.Machine{{Name: self, OK: true, Board: localComputer{board{sv}, ctx}}}
	if !sv.c.PlatformAdmin || s.Peers == nil {
		return out
	}
	for _, m := range s.Peers.Machines(ctx) {
		if m.Local {
			continue
		}
		mm := mcptools.Machine{Name: m.Name, OK: m.OK, Error: m.Error}
		if m.OK {
			mm.Board = mcptools.RESTComputer{API: peerAPI{s, m.Name}, Prefix: ""}
		}
		out = append(out, mm)
	}
	return out
}

// peerAPI calls another computer's REST API through the dashboard's peer
// forwarding (s.Peers), which signs the request with that computer's
// credentials. Only platform admins' tools use it.
type peerAPI struct {
	s    *Server
	name string
}

func (a peerAPI) Call(method, path string, body, out any) error {
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
	r, err := http.NewRequestWithContext(ctx, method, "http://dash/peer/"+a.name+path, rd)
	if err != nil {
		return err
	}
	r.SetPathValue("name", a.name)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	a.s.Peers.ServeHTTP(w, r)
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

// board is this computer's task board, as one caller may use it, for the
// chat's MCP tools.
type board struct{ sv *service }

func (b board) Projects() ([]model.Project, []string, error) {
	return nonNil(b.sv.data.ListProjects()), nil, nil
}

func (b board) Project(ref string) (model.Project, error) {
	for _, p := range b.sv.data.ListProjects() {
		if p.ID == ref || strings.EqualFold(p.Name, ref) {
			return p, nil
		}
	}
	return model.Project{}, fmt.Errorf("no project %q (see list_projects)", ref)
}

func (b board) Todos(projectID string) ([]model.Todo, error) {
	if _, err := b.sv.data.GetProject(projectID); err != nil {
		return nil, fmt.Errorf("no project %q (see list_projects)", projectID)
	}
	return nonNil(b.sv.data.ListTodos(projectID)), nil
}

func (b board) Todo(id string) (model.Todo, error) {
	t, err := b.sv.data.GetTodo(id)
	if err != nil {
		return t, fmt.Errorf("no task %q", id)
	}
	return t, nil
}

func (b board) CreateTodo(projectID string, t mcptools.NewTodo) (model.Todo, error) {
	return b.sv.addTodo(model.Todo{
		ProjectID: projectID, Title: t.Title, Prompt: t.Prompt, Priority: t.Priority,
		DependsOn: t.DependsOn,
	}, t.Enqueue)
}

func (b board) QueueTodo(id string) (model.Todo, error) { return b.sv.enqueue(id) }

func (b board) UpdateTodo(id string, u mcptools.TodoUpdate) (model.Todo, error) {
	return b.sv.editTodo(id, u, nil)
}

func (b board) DeleteTodo(id string) error { return b.sv.removeTodo(context.Background(), id) }

func (b board) Issues(projectID string) ([]model.Issue, error) {
	if _, err := b.sv.data.GetProject(projectID); err != nil {
		return nil, fmt.Errorf("no project %q (see list_projects)", projectID)
	}
	return b.sv.data.ListIssues(projectID), nil
}

func (b board) ReportIssue(projectID string, is model.Issue) (model.Issue, model.UpsertResult, error) {
	is.ProjectID = projectID
	return b.sv.data.UpsertIssue(is)
}

// localComputer is this computer for the fleet tools of /mcp: the board,
// plus reviewing and project settings.
type localComputer struct {
	board
	ctx context.Context
}

func (c localComputer) Projects() ([]model.Project, error) {
	ps, _, err := c.board.Projects()
	return ps, err
}

func (c localComputer) UpdateProject(id string, changes map[string]any) (model.Project, error) {
	b, err := json.Marshal(changes)
	if err != nil {
		return model.Project{}, err
	}
	var req projectUpdate
	if err := json.Unmarshal(b, &req); err != nil {
		return model.Project{}, fmt.Errorf("%w: %v", errBadRequest, err)
	}
	return c.sv.updateProject(c.ctx, id, req)
}

func (c localComputer) Act(id, verb, feedback string) error {
	var err error
	switch verb {
	case "enqueue":
		_, err = c.sv.enqueue(id)
	case "cancel":
		_, _, err = c.sv.cancel(id)
	case "approve":
		_, err = c.sv.approve(id)
	case "rework":
		_, err = c.sv.rework(id, feedback, nil)
	default:
		err = fmt.Errorf("unknown action %q", verb)
	}
	return err
}

func (c localComputer) Text(id, kind string) (string, error) {
	switch kind {
	case "diff":
		return c.sv.diff(c.ctx, id)
	case "log":
		return c.sv.log(id)
	}
	return "", fmt.Errorf("unknown text %q", kind)
}
