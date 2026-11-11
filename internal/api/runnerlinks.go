package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tunnel"
)

// A runner's Projects screen picks, from the runner's side, which of the
// org's shared projects it builds: the same link a project's Settings →
// Runners edits (model.Project.Runners, through updateProject), so both
// follow the same rules and show each other's changes (project.updated).
// Ticking a GitHub repository that isn't a project yet makes the shared
// project and links it in one step; only the runner's owner lists (and so
// adds) GitHub repositories, with the runner's gh (see ghrepos.go).

// runnerLink is one of the org's shared projects as a runner's Projects
// screen shows it.
type runnerLink struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	RemoteURL string `json:"remote_url"`
	GitHub    string `json:"github,omitempty"` // OWNER/REPO, for a GitHub remote
	Linked    bool   `json:"linked"`           // the runner is linked to it
	// Running counts its tasks working on the runner now (unlinking waits
	// for them); Worktrees those that ran there and aren't done, whose
	// worktrees stay on it.
	Running   int `json:"running"`
	Worktrees int `json:"worktrees"`
}

// linkRequest is POST /api/runners/{id}/links: an existing project, or a
// GitHub repository (OWNER/REPO) to make one from unless it is one already.
type linkRequest struct {
	ProjectID string `json:"project_id"`
	GitHub    string `json:"github"`
	Name      string `json:"name"`
}

// linkResult is what linking did: the project, whether it was made, and
// why the runner couldn't clone it now (the link stands, as from Settings →
// Runners, and its work waits with that reason).
type linkResult struct {
	Project    model.Project `json:"project"`
	Created    bool          `json:"created"`
	Cloned     bool          `json:"cloned"`
	CloneError string        `json:"clone_error,omitempty"`
}

func (s *Server) runnerLinkRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/runners/{id}/links", s.runnerLinks)
	mux.HandleFunc("POST /api/runners/{id}/links", s.linkToRunner)
	mux.HandleFunc("POST /api/runners/{id}/links/all", s.linkAllToRunner)
	mux.HandleFunc("DELETE /api/runners/{id}/links/{pid}", s.unlinkFromRunner)
}

// runnerLinks handles GET /api/runners/{id}/links: the org's shared projects
// (those with a remote), each marked when the runner is linked to it. Anyone
// in the org may look; changing them is for its owner and the org's admins.
func (s *Server) runnerLinks(w http.ResponseWriter, r *http.Request) {
	c, x, ok := s.runnerFor(w, r, func(Caller, tunnel.Runner) bool { return true }, "")
	if !ok {
		return
	}
	sv := s.as(c)
	out := []runnerLink{}
	for _, p := range sv.data.ListProjects() {
		if p.RemoteURL == "" {
			continue // can't be shared: no runner could clone it
		}
		l := runnerLink{ID: p.ID, Name: p.Name, RemoteURL: p.RemoteURL, GitHub: gitops.GitHubOwnerRepo(p.RemoteURL),
			Linked: slices.Contains(p.Runners, x.ID)}
		for _, t := range sv.data.ListTodos(p.ID) {
			switch {
			case t.Runner != x.ID:
			case runningTask(t):
				l.Running++
			case t.Status != model.StatusMerged && t.Status != model.StatusDraft:
				l.Worktrees++
			}
		}
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b runnerLink) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	writeJSON(w, http.StatusOK, map[string]any{"runner": s.view(c, x), "projects": out})
}

// linkToRunner handles POST /api/runners/{id}/links: links the runner to a
// shared project, made first from a GitHub repository if the org has none
// for it, and has the runner clone it now when it is online.
func (s *Server) linkToRunner(w http.ResponseWriter, r *http.Request) {
	c, x, ok := s.managedRunner(w, r)
	if !ok {
		return
	}
	var req linkRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	sv := s.as(c)
	var res linkResult
	var err error
	switch gh := strings.TrimSuffix(strings.Trim(strings.TrimSpace(req.GitHub), "/"), ".git"); {
	case req.ProjectID != "":
		res.Project, err = sv.data.GetProject(req.ProjectID)
		if err == nil {
			res.Project, err = sv.linkRunner(r.Context(), x, res.Project)
		}
	case strings.Count(gh, "/") == 1:
		res.Project, res.Created, err = sv.linkGitHubRepo(r.Context(), x, gh, req.Name)
	default:
		err = fmt.Errorf("%w: give project_id, or github as OWNER/REPO", errBadRequest)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if res.Created {
		s.Todos.Kick()
	}
	res.Cloned, res.CloneError = sv.cloneOn(r.Context(), x.ID, res.Project.ID)
	code := http.StatusOK
	if res.Created {
		code = http.StatusCreated
	}
	writeJSON(w, code, res)
}

// linkRunner adds x to p's runners, as Settings → Runners does.
func (sv *service) linkRunner(ctx context.Context, x tunnel.Runner, p model.Project) (model.Project, error) {
	if slices.Contains(p.Runners, x.ID) {
		return p, nil
	}
	runners := append(slices.Clone(p.Runners), x.ID)
	return sv.updateProject(ctx, p.ID, projectUpdate{Runners: &runners})
}

// linkGitHubRepo links x to the org's project for the GitHub repository
// ownerRepo, or makes that project, linked to x. Only x's owner adds
// repositories: they come from their GitHub login's list.
func (sv *service) linkGitHubRepo(ctx context.Context, x tunnel.Runner, ownerRepo, name string) (model.Project, bool, error) {
	remote := "https://github.com/" + ownerRepo + ".git"
	for _, p := range sv.data.ListProjects() {
		if gitops.SameRemote(p.RemoteURL, remote) {
			p, err := sv.linkRunner(ctx, x, p)
			return p, false, err
		}
	}
	if x.Owner == nil || x.Owner.UserID != sv.c.User {
		return model.Project{}, false, fmt.Errorf("%w: only %s's owner can add GitHub repositories with its GitHub login; pick one of the org's shared projects", errForbidden, x.Name)
	}
	if name = strings.TrimSpace(name); name == "" {
		name = ownerRepo[strings.IndexByte(ownerRepo, '/')+1:]
	}
	p, err := sv.createHubProject(projectRequest{Name: name, GitHub: ownerRepo, Runners: []string{x.ID}, DefaultBranch: "main"})
	return p, err == nil, err
}

// cloneOn has runner id clone (or adopt) project pid now, as its first task
// would, so that a remote it can't read says so at once ("runner laptop
// cannot access owner/repo over ssh: …"). An offline runner clones it when
// it next gets work for it.
func (sv *service) cloneOn(ctx context.Context, id, pid string) (bool, string) {
	p, err := sv.data.GetProject(pid)
	if err != nil {
		return false, ""
	}
	x, ok := sv.s.fleet().Get(id)
	if !ok || !x.Online || sv.s.Dispatch == nil {
		return false, ""
	}
	if _, err := sv.linkOn(ctx, x, p); err != nil {
		if errors.Is(err, errNoRunnerOnline) {
			return false, ""
		}
		return false, err.Error()
	}
	return true, ""
}

// linkAllToRunner handles POST /api/runners/{id}/links/all, for a new
// computer: links the runner to every shared project of the org it isn't
// linked to yet. It clones them in the background, one after another.
func (s *Server) linkAllToRunner(w http.ResponseWriter, r *http.Request) {
	c, x, ok := s.managedRunner(w, r)
	if !ok {
		return
	}
	sv := s.as(c)
	type failed struct {
		Project string `json:"project"`
		Error   string `json:"error"`
	}
	linked, failures := []string{}, []failed{}
	var ids []string
	for _, p := range sv.data.ListProjects() {
		if p.RemoteURL == "" || slices.Contains(p.Runners, x.ID) {
			continue
		}
		if _, err := sv.linkRunner(r.Context(), x, p); err != nil {
			failures = append(failures, failed{Project: p.Name, Error: err.Error()})
			continue
		}
		linked, ids = append(linked, p.Name), append(ids, p.ID)
	}
	if len(ids) > 0 {
		go func() {
			for _, pid := range ids {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				if _, msg := sv.cloneOn(ctx, x.ID, pid); msg != "" {
					slog.Info("clone a linked project", "runner", x.Name, "project", pid, "err", msg)
				}
				cancel()
			}
		}()
	}
	writeJSON(w, http.StatusOK, map[string]any{"linked": linked, "failed": failures})
}

// unlinkFromRunner handles DELETE /api/runners/{id}/links/{pid}: the runner
// no longer builds the project. The project, its tasks and the other
// runners' links stay; the runner's clone stays on its disk as a project of
// its own (see hubUnlink).
func (s *Server) unlinkFromRunner(w http.ResponseWriter, r *http.Request) {
	c, x, ok := s.managedRunner(w, r)
	if !ok {
		return
	}
	sv := s.as(c)
	p, err := sv.data.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if slices.Contains(p.Runners, x.ID) {
		runners := slices.DeleteFunc(slices.Clone(p.Runners), func(id string) bool { return id == x.ID })
		if p, err = sv.updateProject(r.Context(), p.ID, projectUpdate{Runners: &runners}); err != nil {
			writeErr(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, p)
}

// runningTask reports whether t is being worked on now: by Claude, a
// review or the merge.
func runningTask(t model.Todo) bool {
	return t.Status == model.StatusInProgress || t.Status == model.StatusMerging || t.Reviewing
}

// checkUnlink refuses to unlink runners from p while one of p's tasks is
// running on them: the runner would refuse to carry on with it. Tasks that
// are done running there keep their worktrees on it; queued again, they
// start afresh on one of p's linked runners.
func (sv *service) checkUnlink(p model.Project, gone []string) error {
	for _, id := range gone {
		n := 0
		for _, t := range sv.data.ListTodos(p.ID) {
			if t.Runner == id && runningTask(t) {
				n++
			}
		}
		if n == 0 {
			continue
		}
		name := id
		if r, ok := sv.s.orgRunner(sv.c, id); ok {
			name = r.Name
		}
		tasks := "1 task of " + p.Name + " is"
		if n > 1 {
			tasks = fmt.Sprintf("%d tasks of %s are", n, p.Name)
		}
		return fmt.Errorf("%w: %s running on %s: wait for them to finish (or cancel them) before unlinking it", errConflict, tasks, name)
	}
	return nil
}
