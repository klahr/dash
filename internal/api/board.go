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
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// BoardMCP serves the chat's Dash tools (POST /mcp/dash), authenticated by
// the per-run tokens in s.Board. Every run acts as a user in an org: a
// chat's user in their active org, a task's creator in the task's org. Its
// tools only reach that org, and stop working once the user leaves it.
//
// On a runner, the runs of its hub's work (the hub's tasks, the chats the
// hub asks for) get the hub's board instead: their calls go to the hub
// (runnerBoard there), so what they create is on the board the hub's users
// see, under the hub's ids. If the hub can't be reached the call fails;
// it never falls back to this runner's own board.
func (s *Server) BoardMCP() http.Handler {
	h := s.boardHandler()
	h.Hub = s.toHubBoard
	return h
}

func (s *Server) boardHandler() *mcptools.Handler {
	return &mcptools.Handler{Tokens: s.Board, Board: func(sc mcptools.Scope) (mcptools.Board, error) {
		c := Caller{Org: sc.Org, User: sc.User}
		if sc.Todo != "" {
			if org, ok := s.Store.TodoOrg(sc.Todo); !ok || org != sc.Org {
				return nil, errors.New("the task is gone")
			}
		}
		if !s.Member(model.Owner{User: c.User, Org: c.Org}) {
			return nil, errors.New("this run's user no longer belongs to its organization")
		}
		return board{s.as(c)}, nil
	}}
}

// maxBoardRequest bounds one board tool call passed on to the hub.
const maxBoardRequest = 4 << 20

// toHubBoard passes on to the hub the board calls of a run of the hub's
// work on this runner, and reports whether sc is such a run.
func (s *Server) toHubBoard(w http.ResponseWriter, r *http.Request, sc mcptools.Scope) bool {
	hubTodo, hubProject := "", sc.HubProject
	switch {
	case sc.Hub:
	case sc.Todo != "":
		org, ok := s.Store.TodoOrg(sc.Todo)
		if !ok {
			return false
		}
		t, err := s.Store.ForOrg(org).GetTodo(sc.Todo)
		if err != nil || t.HubTodoID == "" {
			return false // this runner's own task
		}
		hubTodo = t.HubTodoID
	default:
		return false
	}
	if s.HubBoard == nil {
		http.Error(w, "this run's board is its hub's, and this Dash isn't connected to a hub", http.StatusBadGateway)
		return true
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBoardRequest))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return true
	}
	res, err := s.HubBoard(r.Context(), hubTodo, hubProject, body)
	if err != nil {
		http.Error(w, "the hub's board can't be reached: "+err.Error(), http.StatusBadGateway)
		return true
	}
	defer res.Body.Close()
	for _, k := range []string{"Content-Type", "Mcp-Session-Id"} {
		if v := res.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, res.Body)
	return true
}

// runnerBoard handles POST /runner/mcp/dash on a hub: the board tool calls
// of a run of the hub's work on one of its runners, authenticated with the
// runner's token. The hub decides who the run acts as, from its own
// records: a task's run as the task's owner, and only for a task the hub
// sent that runner; a chat as the runner's owner, in the runner's org, and
// in a project only if it is linked to that runner.
func (s *Server) runnerBoard(rh *tunnel.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		x, ok := rh.Authenticate(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unknown or revoked runner token"})
			return
		}
		if x.Owner == nil || x.OrgID() == 0 {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "this runner works for nobody"})
			return
		}
		sc, err := s.runnerScope(x, r.Header.Get(tunnel.BoardTodoHeader), r.Header.Get(tunnel.BoardProjectHeader))
		if err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		s.boardHandler().Serve(w, r, sc)
	}
}

// runnerScope is what a board call from runner x for the hub's task
// hubTodo, or else for a chat in hubProject, may reach.
func (s *Server) runnerScope(x tunnel.Runner, hubTodo, hubProject string) (mcptools.Scope, error) {
	org := x.OrgID()
	if hubTodo != "" {
		t, err := s.Store.ForOrg(org).GetTodo(hubTodo)
		if o, ok := s.Store.TodoOrg(hubTodo); !ok || o != org || err != nil || t.Runner != x.ID {
			return mcptools.Scope{}, errors.New("no such task on this runner")
		}
		owner, err := s.TaskOwner(t)
		if err != nil {
			return mcptools.Scope{}, err
		}
		return mcptools.Scope{Org: owner.Org, User: owner.User, Project: t.ProjectID, Todo: t.ID}, nil
	}
	if hubProject != "" {
		p, err := s.Store.ForOrg(org).GetProject(hubProject)
		if o, ok := s.Store.ProjectOrg(hubProject); !ok || o != org || err != nil || !slices.Contains(p.Runners, x.ID) {
			return mcptools.Scope{}, errors.New("no such project linked to this runner")
		}
	}
	return mcptools.Scope{Org: org, User: x.Owner.UserID, Project: hubProject}, nil
}

// Member reports whether owner's user still belongs to owner's org.
func (s *Server) Member(owner model.Owner) bool {
	if !owner.Valid() || s.Auth == nil {
		return false
	}
	_, err := s.Auth.Accounts().Membership(owner.User, owner.Org)
	return err == nil
}

// TaskOwner is who a task's runs act as: the user who created it, in the
// task's org. Tasks with no creator (from before users, made by Dash itself,
// or on a runner by its hub) act as this Dash's env account. Either way the
// user must still belong to the task's org.
func (s *Server) TaskOwner(t model.Todo) (model.Owner, error) {
	org, ok := s.Store.TodoOrg(t.ID)
	if !ok || org == 0 {
		return model.Owner{}, fmt.Errorf("task %s has no org", t.ID)
	}
	o := model.Owner{User: t.CreatedBy, Org: org}
	if o.User == 0 {
		u, err := s.Auth.Accounts().EnvUser()
		if err != nil {
			return model.Owner{}, fmt.Errorf("task has no creator and there is no env account: %w", err)
		}
		o.User = u.ID
	}
	if !s.Member(o) {
		return model.Owner{}, fmt.Errorf("user %d isn't a member of the task's org %d", o.User, o.Org)
	}
	return o, nil
}

// requestOwner is who a request acts as: the signed-in user (or token) in
// their org, or for another Dash's request (a hub over this runner's tunnel),
// this Dash's env account in the default org, where its records are.
func (s *Server) requestOwner(r *http.Request) model.Owner {
	if id, ok := auth.From(r.Context()); ok {
		return id.Owner()
	}
	if auth.FromMachine(r.Context()) && s.Auth != nil {
		acc := s.Auth.Accounts()
		u, err := acc.EnvUser()
		if err != nil {
			return model.Owner{}
		}
		org, err := acc.DefaultOrg()
		if err != nil {
			return model.Owner{}
		}
		return model.Owner{User: u.ID, Org: org.ID}
	}
	return model.Owner{}
}

// remoteMCP serves the tools of cmd/dash-mcp (POST /mcp, behind OAuth.Bearer)
// as the token's user, in the org the token is pinned to, within its scopes: this computer through the service, and, for
// platform admins, the other computers in the dashboard.
func (s *Server) remoteMCP() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := s.caller(r)
		if err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		sv := s.as(c)
		tools := mcptools.FleetTools(mcptools.Fleet{Computers: func() ([]mcptools.Machine, error) {
			return sv.computers(r.Context()), nil
		}})
		// A read-only token's tools may look but not change anything.
		if id, ok := auth.From(r.Context()); ok && !id.Can(store.ScopeWrite) {
			tools = mcptools.ReadOnly(tools, mcptools.FleetReadTools, errReadOnly)
		}
		srv := &mcptools.Server{
			Name:         "dash",
			Instructions: mcptools.FleetInstructions,
			Tools:        tools,
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

// errReadOnly is what a read-only token gets for a tool that changes things.
var errReadOnly = errors.New("this connection is read-only: its token doesn't have the write scope")

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
	if err := b.sv.writable(); err != nil {
		return model.Todo{}, err
	}
	return b.sv.addTodo(model.Todo{
		ProjectID: projectID, Title: t.Title, Prompt: t.Prompt, Priority: t.Priority,
		DependsOn: t.DependsOn,
	}, t.Enqueue)
}

func (b board) QueueTodo(id string) (model.Todo, error) {
	if err := b.sv.writable(); err != nil {
		return model.Todo{}, err
	}
	return b.sv.enqueue(id)
}

func (b board) UpdateTodo(id string, u mcptools.TodoUpdate) (model.Todo, error) {
	if err := b.sv.writable(); err != nil {
		return model.Todo{}, err
	}
	return b.sv.editTodo(id, u, nil)
}

func (b board) DeleteTodo(id string) error {
	if err := b.sv.writable(); err != nil {
		return err
	}
	return b.sv.removeTodo(context.Background(), id)
}

func (b board) Issues(projectID string) ([]model.Issue, error) {
	if _, err := b.sv.data.GetProject(projectID); err != nil {
		return nil, fmt.Errorf("no project %q (see list_projects)", projectID)
	}
	return b.sv.data.ListIssues(projectID), nil
}

func (b board) ReportIssue(projectID string, is model.Issue) (model.Issue, model.UpsertResult, error) {
	if err := b.sv.writable(); err != nil {
		return model.Issue{}, "", err
	}
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
	if err := c.sv.writable(); err != nil {
		return model.Project{}, err
	}
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
	if err := c.sv.writable(); err != nil {
		return err
	}
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
