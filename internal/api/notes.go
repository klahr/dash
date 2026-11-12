package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/notes"
	"github.com/klahr/dash/internal/store"
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

// ---- the hub's UI: status and removals, never a note ----
//
// The hub asks a project's online runners, over their tunnels, for counts
// of their copies (notes.Status) and has them remove notes (notes.Removal):
// each runner applies the removal to its own copy, after opening it, and
// answers only how many notes it removed. A removal asked while none of the
// project's runners is online is kept with the project (the command, never
// a note) and sent to the first of them that connects.

// notesCallTimeout bounds one runner's answer to the hub's notes command.
const notesCallTimeout = 20 * time.Second

// notesPeer is one runner of a project in the notes view.
type notesPeer struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
	// Fingerprint is its key's, as it told the hub when connecting.
	Fingerprint string        `json:"fingerprint,omitempty"`
	Status      *notes.Status `json:"status,omitempty"`
	Error       string        `json:"error,omitempty"`
}

// notesView is GET /api/projects/{pid}/notes.
type notesView struct {
	// Why there are no shared notes here ("": there are).
	Why     string               `json:"why,omitempty"`
	Runners []notesPeer          `json:"runners"`
	Online  int                  `json:"online"`
	Pending []model.NotesRemoval `json:"pending"`
}

// notesRemoved is a runner's answer to a removal, for the UI.
type notesRemoved struct {
	Runner  string `json:"runner"`
	Name    string `json:"name"`
	Removed int    `json:"removed"`
	Error   string `json:"error,omitempty"`
}

// notesRunners are p's runners (of p's org), online or not.
func (s *Server) notesRunners(p model.Project) []tunnel.Runner {
	var out []tunnel.Runner
	for _, id := range p.Runners {
		if x, ok := s.fleet().Get(id); ok && x.OrgID() == p.OrgID {
			out = append(out, x)
		}
	}
	return out
}

// notesCall posts in to a runner's notes command path for its hub project,
// and decodes the answer into out.
func (s *Server) notesCall(ctx context.Context, runner, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, notesCallTimeout)
	defer cancel()
	b, err := s.fleet().CallBytes(ctx, runner, path, http.Header{notes.CommandHeader: {"1"}}, body, 1<<20)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// getNotes handles GET /api/projects/{pid}/notes: per runner of the
// project, whether it is online, its key fingerprint, and the counts of its
// copy (if online); and the removals waiting for a runner.
func (s *Server) getNotes(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.project(w, r)
	if !ok {
		return
	}
	v := notesView{Runners: []notesPeer{}, Pending: p.NotesPending}
	if v.Pending == nil {
		v.Pending = []model.NotesRemoval{}
	}
	if !s.hub() {
		v.Why = "Shared notes are kept by the runners of a hub's projects, and this Dash has none."
		writeJSON(w, http.StatusOK, v)
		return
	}
	runs := s.notesRunners(p)
	v.Runners = make([]notesPeer, len(runs))
	var wg sync.WaitGroup
	for i, x := range runs {
		v.Runners[i] = notesPeer{ID: x.ID, Name: x.Name, Online: x.Online, Fingerprint: x.NotesFingerprint}
		if !x.Online {
			continue
		}
		v.Online++
		wg.Go(func() {
			var st notes.Status
			if err := s.notesCall(r.Context(), x.ID, notes.StatusPathFor(p.ID), struct{}{}, &st); err != nil {
				v.Runners[i].Error = err.Error()
				return
			}
			v.Runners[i].Status = &st
		})
	}
	wg.Wait()
	if len(runs) == 0 {
		v.Why = "No runner is linked to this project: link one in Project settings → Runners."
	}
	writeJSON(w, http.StatusOK, v)
}

// removeNotes handles POST /api/projects/{pid}/notes/remove {"kind",
// "task", "runner"}: the project's online runners remove the notes that
// match, each from its own copy, and say how many. With none online, the
// removal waits for the first that connects (pending).
func (s *Server) removeNotes(w http.ResponseWriter, r *http.Request) {
	sv, p, ok := s.project(w, r)
	if !ok {
		return
	}
	if !s.hub() {
		writeErr(w, fmt.Errorf("%w: shared notes are kept by the runners of a hub's projects", errBadRequest))
		return
	}
	if len(s.notesRunners(p)) == 0 {
		writeErr(w, errNoRunnerLinked)
		return
	}
	var req struct {
		Kind   string `json:"kind"`
		Task   string `json:"task"`
		Runner string `json:"runner"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	rm := model.NotesRemoval{ID: store.NewID(), Kind: req.Kind, Before: time.Now().UTC(), By: sv.c.User}
	switch req.Kind {
	case notes.RemoveAll, notes.RemoveExpired:
	case notes.RemoveTask:
		t, err := sv.data.GetTodo(req.Task)
		if err != nil || t.ProjectID != p.ID {
			writeErr(w, fmt.Errorf("%w: pick one of this project's tasks", errBadRequest))
			return
		}
		rm.Task = t.ID
	case notes.RemoveRunner:
		if !slices.Contains(p.Runners, req.Runner) {
			writeErr(w, fmt.Errorf("%w: pick one of this project's runners", errBadRequest))
			return
		}
		rm.Runner = req.Runner
	default:
		writeErr(w, fmt.Errorf("%w: kind must be %q, %q, %q or %q", errBadRequest, notes.RemoveAll, notes.RemoveTask, notes.RemoveRunner, notes.RemoveExpired))
		return
	}
	// Who asked what, never which notes (the hub doesn't know).
	slog.Info("shared notes: removal asked", "project", p.ID, "user", sv.c.User, "removal", rm.ID, "kind", rm.Kind, "task", rm.Task, "runner", rm.Runner)
	results := s.sendNotesRemoval(r.Context(), p, rm)
	out := struct {
		Removal model.NotesRemoval `json:"removal"`
		Results []notesRemoved     `json:"results"`
		Pending bool               `json:"pending"`
	}{Removal: rm, Results: results}
	if !slices.ContainsFunc(results, func(x notesRemoved) bool { return x.Error == "" }) {
		if _, err := sv.data.UpdateProject(p.ID, func(p *model.Project) error {
			p.NotesPending = append(p.NotesPending, rm)
			return nil
		}); err != nil {
			writeErr(w, err)
			return
		}
		out.Pending = true
		slog.Info("shared notes: removal waits for a runner of the project", "project", p.ID, "removal", rm.ID)
	}
	if out.Results == nil {
		out.Results = []notesRemoved{}
	}
	writeJSON(w, http.StatusOK, out)
}

// cancelNotesRemoval handles DELETE /api/projects/{pid}/notes/pending/{rid}:
// a pending removal is dropped before any runner gets it.
func (s *Server) cancelNotesRemoval(w http.ResponseWriter, r *http.Request) {
	sv, p, ok := s.project(w, r)
	if !ok {
		return
	}
	id, found := r.PathValue("rid"), false
	if _, err := sv.data.UpdateProject(p.ID, func(p *model.Project) error {
		n := len(p.NotesPending)
		p.NotesPending = slices.DeleteFunc(p.NotesPending, func(x model.NotesRemoval) bool { return x.ID == id })
		found = len(p.NotesPending) < n
		return nil
	}); err != nil {
		writeErr(w, err)
		return
	}
	if !found {
		writeErr(w, store.ErrNotFound)
		return
	}
	slog.Info("shared notes: pending removal cancelled", "project", p.ID, "user", sv.c.User, "removal", id)
	w.WriteHeader(http.StatusNoContent)
}

// sendNotesRemoval sends rm to p's online runners, at once, and returns
// their answers.
func (s *Server) sendNotesRemoval(ctx context.Context, p model.Project, rm model.NotesRemoval) []notesRemoved {
	cmd := notes.Removal{ID: rm.ID, Kind: rm.Kind, Runner: rm.Runner, Before: rm.Before}
	if rm.Task != "" {
		cmd.Tasks = []string{rm.Task}
	}
	var out []notesRemoved
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, x := range s.notesRunners(p) {
		if !x.Online {
			continue
		}
		wg.Go(func() {
			res := notesRemoved{Runner: x.ID, Name: x.Name}
			var a notes.RemoveResult
			if err := s.notesCall(ctx, x.ID, notes.RemovePathFor(p.ID), cmd, &a); err != nil {
				res.Error = err.Error()
			} else {
				res.Removed = a.Removed
			}
			slog.Info("shared notes: removal sent", "project", p.ID, "removal", rm.ID, "runner", x.Name, "removed", res.Removed, "err", res.Error)
			mu.Lock()
			out = append(out, res)
			mu.Unlock()
		})
	}
	wg.Wait()
	slices.SortFunc(out, func(a, b notesRemoved) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// NotesRunnerOnline sends the removals waiting in the projects of runner
// id, which just connected, to it; each is dropped once it took it.
func (s *Server) NotesRunnerOnline(id string) {
	if !s.hub() || s.Store == nil {
		return
	}
	s.notesDeliver.Lock()
	defer s.notesDeliver.Unlock()
	x, ok := s.fleet().Get(id)
	if !ok {
		return
	}
	org := s.Store.ForOrg(x.OrgID())
	for _, p := range org.ListProjects() {
		if len(p.NotesPending) == 0 || !slices.Contains(p.Runners, id) {
			continue
		}
		for _, rm := range p.NotesPending {
			cmd := notes.Removal{ID: rm.ID, Kind: rm.Kind, Runner: rm.Runner, Before: rm.Before}
			if rm.Task != "" {
				cmd.Tasks = []string{rm.Task}
			}
			var a notes.RemoveResult
			if err := s.notesCall(context.Background(), id, notes.RemovePathFor(p.ID), cmd, &a); err != nil {
				slog.Info("shared notes: pending removal not taken yet", "project", p.ID, "removal", rm.ID, "runner", x.Name, "err", err)
				continue
			}
			slog.Info("shared notes: pending removal sent", "project", p.ID, "removal", rm.ID, "runner", x.Name, "removed", a.Removed)
			if _, err := org.UpdateProject(p.ID, func(p *model.Project) error {
				p.NotesPending = slices.DeleteFunc(p.NotesPending, func(y model.NotesRemoval) bool { return y.ID == rm.ID })
				return nil
			}); err != nil {
				slog.Warn("shared notes: drop a sent removal", "project", p.ID, "err", err)
			}
		}
	}
}

// ---- a runner's side of them ----

// notesCommand is the hub project {hpid} of a notes command from this
// runner's hub, or answers r and returns false. The hub's proxy strips
// notes.CommandHeader, so only the hub itself sends one, and only for a
// project linked to this runner.
func (s *Server) notesCommand(w http.ResponseWriter, r *http.Request) (*service, model.Project, bool) {
	if !auth.FromHub(r.Context()) || r.Header.Get(notes.CommandHeader) == "" || s.Notes == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": notes.ErrNoProject.Error()})
		return nil, model.Project{}, false
	}
	sv, ok := s.fromHub(w, r)
	if !ok {
		return nil, model.Project{}, false
	}
	p, ok := sv.hubProject(r.PathValue("hpid"))
	if !ok {
		slog.Warn("runner: refused a shared notes command for a project not linked to it", "project", r.PathValue("hpid"))
		writeJSON(w, http.StatusForbidden, map[string]string{"error": errNotLinked.Error()})
		return nil, model.Project{}, false
	}
	return sv, p, true
}

func notesErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, notes.ErrNoProject):
		code = http.StatusNotFound
	case errors.Is(err, notes.ErrBadRemoval):
		code = http.StatusBadRequest
	case errors.Is(err, notes.ErrStale):
		code = http.StatusConflict
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// hubNotesStatus handles POST notes.StatusPath: counts of this runner's
// copy of the project's notes.
func (s *Server) hubNotesStatus(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.notesCommand(w, r); !ok {
		return
	}
	st, err := s.Notes.Status(r.PathValue("hpid"))
	if err != nil {
		notesErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// hubNotesRemove handles POST notes.RemovePath: a notes.Removal, applied to
// this runner's copy and synced to the others; the answer is a count.
func (s *Server) hubNotesRemove(w http.ResponseWriter, r *http.Request) {
	sv, p, ok := s.notesCommand(w, r)
	if !ok {
		return
	}
	var rm notes.Removal
	if err := decode(r, &rm); err != nil {
		writeErr(w, err)
		return
	}
	if rm.Kind == notes.RemoveTask {
		// Notes name the task that wrote them by the hub's id or this
		// runner's own.
		for _, t := range sv.data.ListTodos(p.ID) {
			if t.HubTodoID != "" && slices.Contains(rm.Tasks, t.HubTodoID) && !slices.Contains(rm.Tasks, t.ID) && len(rm.Tasks) < 4 {
				rm.Tasks = append(rm.Tasks, t.ID)
			}
		}
	}
	n, err := s.Notes.Remove(r.Context(), r.PathValue("hpid"), rm)
	if err != nil {
		slog.Info("shared notes: removal failed", "project", r.PathValue("hpid"), "removal", rm.ID, "kind", rm.Kind, "err", err)
		notesErr(w, err)
		return
	}
	slog.Info("shared notes: removed on the hub's command", "project", r.PathValue("hpid"), "removal", rm.ID, "kind", rm.Kind, "removed", n)
	writeJSON(w, http.StatusOK, notes.RemoveResult{Removed: n})
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
