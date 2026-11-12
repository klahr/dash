package api

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/notes"
	"github.com/klahr/dash/internal/tunnel"
)

// Shared notes (package notes) belong to the runners of a hub project. The
// hub only relays them, sealed end to end: it holds no key, keeps no copy,
// reads none and shows none. All it sees is ciphertext, the project, the
// size and the runner that sent it, plus runners' public keys.

// notesRelay handles POST /runner/notes on a hub: a runner's message for
// its project's other runners, passed on unread to those online now.
func (s *Server) notesRelay(rh *tunnel.Hub) http.Handler {
	return &notes.Relay{
		Route: func(r *http.Request, project string) ([]string, []string, error) {
			x, ok := rh.Authenticate(r)
			if !ok {
				return nil, nil, &notes.RelayError{Status: http.StatusUnauthorized, Msg: "unknown or revoked runner token"}
			}
			org := x.OrgID()
			if x.Owner == nil || org == 0 {
				return nil, nil, &notes.RelayError{Status: http.StatusForbidden, Msg: "this runner works for nobody"}
			}
			p, err := s.Store.ForOrg(org).GetProject(project)
			if o, ok := s.Store.ProjectOrg(project); !ok || o != org || err != nil || !slices.Contains(p.Runners, x.ID) {
				return nil, nil, &notes.RelayError{Status: http.StatusForbidden, Msg: "no such project linked to this runner"}
			}
			var peers []string
			for _, id := range p.Runners {
				if id == x.ID || !rh.Online(id) {
					continue
				}
				if y, ok := rh.Get(id); ok && y.OrgID() == org {
					peers = append(peers, id)
				}
			}
			// The members tell runners who left: those still holding the
			// project's key then rotate it (notes.Service).
			return peers, slices.Clone(p.Runners), nil
		},
		Forward: func(ctx context.Context, peer, project string, msg []byte) ([]byte, error) {
			return rh.CallBytes(ctx, peer, notes.PeerPathFor(project), http.Header{notes.RelayHeader: {"1"}}, msg, notes.MaxReply)
		},
	}
}

// notesFromPeers handles POST /api/hub/projects/{hpid}/notes on a runner: a
// message from another runner of the project, which its hub relayed.
func (s *Server) notesFromPeers(w http.ResponseWriter, r *http.Request) {
	if !auth.FromHub(r.Context()) || s.NotesPeer == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": notes.ErrNoProject.Error()})
		return
	}
	s.NotesPeer.ServeHTTP(w, r)
}

// NotesMCP serves the shared notes tools of the tasks this runner runs
// (POST /mcp/notes), authenticated by their run's token. They act on this
// runner's copy of the notes of the task's hub project: unlike the board
// tools of a hub's task, nothing goes to the hub, so they answer at once.
func (s *Server) NotesMCP() http.Handler {
	return &mcptools.NotesHandler{Tokens: s.Board, Notes: func(sc mcptools.Scope) (mcptools.Notes, error) {
		n, _, err := s.TaskNotes(sc.Todo)
		return n, err
	}}
}

// TaskNotes is the shared notes a run of task todoID uses, and how many
// notes they hold now; notes.ErrNoProject if it has none: not on a
// runner, or not a task of a hub's project linked to it.
func (s *Server) TaskNotes(todoID string) (mcptools.Notes, int, error) {
	if s.Notes == nil || todoID == "" {
		return nil, 0, notes.ErrNoProject
	}
	org, ok := s.Store.TodoOrg(todoID)
	if !ok {
		return nil, 0, errors.New("the task is gone")
	}
	data := s.Store.ForOrg(org)
	t, err := data.GetTodo(todoID)
	if err != nil {
		return nil, 0, errors.New("the task is gone")
	}
	p, err := data.GetProject(t.ProjectID)
	if err != nil || !p.LinkedFromHub() {
		return nil, 0, notes.ErrNoProject
	}
	task := t.HubTodoID // the id every runner and the hub know it by
	if task == "" {
		task = t.ID
	}
	es, err := s.Notes.List(p.HubProjectID, notes.Query{})
	if err != nil {
		return nil, 0, err
	}
	return mcptools.ServiceNotes{S: s.Notes, Project: p.HubProjectID, Task: task}, len(es), nil
}
