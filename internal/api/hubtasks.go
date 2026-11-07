package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// The routes below are a runner's side of dispatch: its hub links projects
// to it and sends it their tasks. They answer only the hub, over the
// runner's tunnel, and the runner checks everything it is sent:
//
//   - a link names the hub's org the runner was registered for and a
//     network git remote; the runner clones it into <projects root>/hub/,
//     a folder it picks itself;
//   - a task names a project linked that way (and still inside the
//     projects root); anything else is refused and logged;
//   - the hub reaches only tasks it sent, by its own ids.

func (s *Server) hubTaskRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/hub/projects/{hpid}", s.hubLink)
	mux.HandleFunc("DELETE /api/hub/projects/{hpid}", s.hubUnlink)
	mux.HandleFunc("POST /api/hub/tasks", s.hubTask)
	mux.HandleFunc("DELETE /api/hub/tasks/{htid}", s.hubTaskAction("delete"))
	mux.HandleFunc("POST /api/hub/tasks/{htid}/approve", s.hubTaskAction("approve"))
	mux.HandleFunc("POST /api/hub/tasks/{htid}/cancel", s.hubTaskAction("cancel"))
	mux.HandleFunc("GET /api/hub/tasks/{htid}/diff", s.hubTaskAction("diff"))
	mux.HandleFunc("GET /api/hub/tasks/{htid}/log", s.hubTaskAction("log"))
	mux.HandleFunc("GET /api/hub/tasks/{htid}/events", s.hubTaskAction("events"))
}

var errNotLinked = errors.New("that project isn't linked to this runner")

// fromHub is the service for a request from this runner's hub, or answers
// r with 403 and returns false.
func (s *Server) fromHub(w http.ResponseWriter, r *http.Request) (*service, bool) {
	if !auth.FromHub(r.Context()) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only this runner's hub sends it tasks"})
		return nil, false
	}
	return s.service(w, r)
}

// hubRoot is where a runner clones its hub's projects.
func (s *Server) hubRoot() string { return filepath.Join(s.ProjectRoot, "hub") }

// hubProject is the runner's clone of the hub's project hpid, if the hub
// linked it and it is inside the projects root.
func (sv *service) hubProject(hpid string) (model.Project, bool) {
	if hpid == "" {
		return model.Project{}, false
	}
	for _, p := range sv.data.ListProjects() {
		if p.HubProjectID == hpid && within(sv.s.hubRoot(), p.Path) {
			return p, true
		}
	}
	return model.Project{}, false
}

// hubTodo is the runner's copy of the hub's task htid, in a linked project.
func (sv *service) hubTodo(htid string) (model.Todo, bool) {
	if htid == "" {
		return model.Todo{}, false
	}
	for _, t := range sv.data.ListTodos("") {
		if t.HubTodoID != htid {
			continue
		}
		if p, err := sv.data.GetProject(t.ProjectID); err == nil && p.HubProjectID != "" && within(sv.s.hubRoot(), p.Path) {
			return t, true
		}
	}
	return model.Todo{}, false
}

// hubLink handles PUT /api/hub/projects/{hpid}: the hub links its project
// to this runner, which clones it (once).
func (s *Server) hubLink(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.fromHub(w, r)
	if !ok {
		return
	}
	var req dispatch.Link
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	hpid := r.PathValue("hpid")
	if s.HubOrg != 0 && req.OrgID != s.HubOrg {
		slog.Warn("runner: refused a project of another org", "project", hpid, "org", req.OrgID)
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this runner works for another organization"})
		return
	}
	if p, ok := sv.hubProject(hpid); ok {
		writeJSON(w, http.StatusOK, dispatch.Linked{ProjectID: p.ID})
		return
	}
	if err := gitops.CheckRemote(req.RemoteURL); err != nil {
		writeErr(w, errors.Join(errBadRequest, err))
		return
	}
	if s.ProjectRoot == "" {
		writeErr(w, fmt.Errorf("%w: this runner has no projects root (-projects)", errBadRequest))
		return
	}
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(req.Name), "-"), "-")
	if slug == "" {
		slug = "project"
	}
	path := filepath.Join(s.hubRoot(), slug)
	if _, err := os.Stat(path); err == nil {
		path += "-" + strings.Trim(slugRe.ReplaceAllString(strings.ToLower(hpid), "-"), "-")
	}
	if !within(s.hubRoot(), path) || filepath.Clean(path) == filepath.Clean(s.hubRoot()) {
		writeErr(w, fmt.Errorf("%w: bad project name", errBadRequest))
		return
	}
	id := store.NewID()
	ctx := r.Context()
	if err := s.Git.Clone(ctx, id, req.RemoteURL, path); err != nil {
		writeErr(w, errors.Join(errBadRequest, err))
		return
	}
	branch := req.DefaultBranch
	if branch == "" || !s.Git.HasBranch(ctx, id, path, branch) {
		if b := s.Git.MainBranch(ctx, id, path); b != "" {
			branch = b
		}
	}
	if branch == "" {
		branch = "main"
	}
	if err := s.Git.InitRepo(ctx, id, path, branch); err != nil { // an empty remote gets a first commit
		writeErr(w, err)
		return
	}
	p, err := sv.data.CreateProject(model.Project{
		ID: id, Name: req.Name, Path: path, DefaultBranch: branch, RemoteURL: s.Git.RemoteURL(ctx, id, path),
		MergeMode: model.MergePR, MaxAutoFixes: model.DefaultMaxAutoFixes, HubProjectID: hpid,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	slog.Info("runner: hub linked a project", "name", p.Name, "path", p.Path)
	writeJSON(w, http.StatusCreated, dispatch.Linked{ProjectID: p.ID})
}

// hubUnlink handles DELETE /api/hub/projects/{hpid}: the runner takes no
// more tasks for it. Its clone stays, as a project of the runner's own.
func (s *Server) hubUnlink(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.fromHub(w, r)
	if !ok {
		return
	}
	if p, ok := sv.hubProject(r.PathValue("hpid")); ok {
		if _, err := sv.data.UpdateProject(p.ID, func(x *model.Project) error { x.HubProjectID = ""; return nil }); err != nil {
			writeErr(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// hubTask handles POST /api/hub/tasks: run the hub's task, or continue it.
func (s *Server) hubTask(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.fromHub(w, r)
	if !ok {
		return
	}
	var req dispatch.Task
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, ok := sv.hubProject(req.HubProjectID)
	if !ok {
		slog.Warn("runner: refused a task for a project not linked to it", "project", req.HubProjectID, "task", req.HubTodoID)
		writeJSON(w, http.StatusForbidden, map[string]string{"error": errNotLinked.Error()})
		return
	}
	if req.HubTodoID == "" {
		writeErr(w, fmt.Errorf("%w: hub_todo_id is required", errBadRequest))
		return
	}
	if cur, ok := sv.hubTodo(req.HubTodoID); ok && (req.Fresh || cur.ProjectID != p.ID) {
		// Start over: the old copy goes, with its worktree.
		if err := sv.removeTodo(r.Context(), cur.ID); err != nil {
			writeErr(w, err)
			return
		}
	}
	if cur, ok := sv.hubTodo(req.HubTodoID); ok {
		t, err := sv.data.UpdateTodo(cur.ID, func(x *model.Todo) error {
			if x.Status == model.StatusMerged {
				return fmt.Errorf("%w: this task was already merged", errBadRequest)
			}
			if n := len(req.Feedback); n > x.HubNotes {
				x.Feedback = append(x.Feedback, req.Feedback[x.HubNotes:]...)
				x.HubNotes = n
			}
			switch x.Status {
			case model.StatusReview, model.StatusFailed, model.StatusDraft:
				x.Status, x.Error, x.Paused = model.StatusQueued, "", false
				x.AutoFixes, x.AutoMerge, x.SentBack = 0, false, ""
			}
			return nil
		})
		if err != nil {
			writeErr(w, err)
			return
		}
		s.Todos.Kick()
		writeJSON(w, http.StatusOK, dispatch.Accepted{ID: t.ID, Resumed: true})
		return
	}
	t, err := sv.data.CreateTodo(model.Todo{
		ProjectID: p.ID, Title: req.Title, Prompt: req.Prompt, Feedback: req.Feedback,
		HubTodoID: req.HubTodoID, HubNotes: len(req.Feedback),
	})
	if err == nil {
		t, err = sv.data.SetStatus(t.ID, model.StatusQueued, "")
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Todos.Kick()
	writeJSON(w, http.StatusCreated, dispatch.Accepted{ID: t.ID})
}

// hubTaskAction handles what the hub does to a task it sent.
func (s *Server) hubTaskAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sv, ok := s.fromHub(w, r)
		if !ok {
			return
		}
		t, ok := sv.hubTodo(r.PathValue("htid"))
		if !ok {
			writeErr(w, fmt.Errorf("%w: no such task from the hub", store.ErrNotFound))
			return
		}
		switch action {
		case "approve":
			t, err := sv.approve(t.ID)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusAccepted, dispatch.StateOf(t))
		case "cancel":
			if _, _, err := sv.cancel(t.ID); err != nil {
				writeErr(w, err)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		case "delete":
			// The hub forgot it: stop it and keep it as the runner's own.
			s.Todos.Cancel(t.ID)
			if _, err := sv.data.UpdateTodo(t.ID, func(x *model.Todo) error { x.HubTodoID = ""; return nil }); err != nil {
				writeErr(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "diff":
			if t.Worktree == nil {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				return
			}
			diff, err := sv.diff(r.Context(), t.ID)
			if err != nil {
				writeErr(w, err)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(diff))
		case "log":
			log, err := sv.log(t.ID)
			if err != nil {
				writeErr(w, err)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(log))
		case "events":
			evs, err := s.Todos.Log.Events(t.ID)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusOK, evs)
		}
	}
}
