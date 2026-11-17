package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tunnel"
)

// A runner's Projects screen shows, from the runner's side, which of the
// org's shared projects it works on: those that run on labels it has (see
// model.LabelsMatch). Projects never name runners, so the runner's labels,
// set on its page, decide which projects it builds, and a project's labels
// (Project settings → Runners) decide which runners build it. Picking a
// GitHub repository that isn't a project yet makes the shared project, on
// labels the runner has; only the runner's owner lists (and so adds) GitHub
// repositories, with the runner's gh (see ghrepos.go).

// runnerLink is one of the org's shared projects as a runner's Projects
// screen shows it.
type runnerLink struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	RemoteURL string   `json:"remote_url"`
	GitHub    string   `json:"github,omitempty"` // OWNER/REPO, for a GitHub remote
	RunsOn    []string `json:"runs_on"`          // the labels the project runs on
	Linked    bool     `json:"linked"`           // the runner works on it: it has every label
	// Missing are the project's labels the runner lacks (none when Linked).
	Missing []string `json:"missing,omitempty"`
	// Running counts its tasks working on the runner now; Worktrees those
	// that ran there and aren't done, whose worktrees stay on it.
	Running   int `json:"running"`
	Worktrees int `json:"worktrees"`
}

// linkRequest is POST /api/runners/{id}/links: an existing project the
// runner works on, to clone now (as Add project does once it made it), or a
// GitHub repository (OWNER/REPO) to make a shared project from, unless it
// is one already, and the labels it runs on (default: the runner's own
// labels, or "any" when it has none).
type linkRequest struct {
	ProjectID string   `json:"project_id"`
	GitHub    string   `json:"github"`
	Name      string   `json:"name"`
	RunsOn    []string `json:"runs_on"`
}

// linkResult is what adding the repository did: the project, whether it was
// made, and why the runner couldn't clone it now (the project stands, and
// its work waits with that reason).
type linkResult struct {
	Project    model.Project `json:"project"`
	Created    bool          `json:"created"`
	Cloned     bool          `json:"cloned"`
	CloneError string        `json:"clone_error,omitempty"`
}

func (s *Server) runnerLinkRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/runners/{id}/links", s.runnerLinks)
	mux.HandleFunc("POST /api/runners/{id}/links", s.linkToRunner)
}

// runnerLinks handles GET /api/runners/{id}/links: the org's shared projects
// (those with a remote), each marked when the runner works on it. Anyone in
// the org may look.
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
			RunsOn: slices.Clone(p.RunsOn), Linked: x.Serves(p)}
		if l.RunsOn == nil {
			l.RunsOn = []string{}
		}
		if !l.Linked {
			l.Missing = model.MissingLabels(x.AllLabels(), p.RunsOn)
		}
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

// linkToRunner handles POST /api/runners/{id}/links: makes a shared
// project from a GitHub repository, unless the org has one for it (or takes
// an existing project), and has the runner clone it now when it is online
// and works on it.
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
		switch {
		case err != nil:
		case !res.Project.OnRunners():
			err = fmt.Errorf("%w: %s runs on no runner labels: set them in its Project settings → Runners", errBadRequest, res.Project.Name)
		case !x.Serves(res.Project):
			err = fmt.Errorf("%w: %s doesn't work on %s: it lacks the labels %s", errBadRequest, x.Name, res.Project.Name,
				strings.Join(model.MissingLabels(x.AllLabels(), res.Project.RunsOn), ", "))
		}
	case strings.Count(gh, "/") == 1:
		res.Project, res.Created, err = sv.linkGitHubRepo(x, gh, req.Name, req.RunsOn)
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
	if x.Serves(res.Project) {
		res.Cloned, res.CloneError = sv.cloneOn(r.Context(), x.ID, res.Project.ID)
	}
	code := http.StatusOK
	if res.Created {
		code = http.StatusCreated
	}
	writeJSON(w, code, res)
}

// linkGitHubRepo is the org's project for the GitHub repository ownerRepo,
// or makes it, to run on runsOn: by default x's own labels, so that it runs
// on the runners its owner gave them, or "any" (any of the org's
// runners) when x has none (see checkRunsOn). Only x's owner adds
// repositories: they come from their GitHub login's list.
func (sv *service) linkGitHubRepo(x tunnel.Runner, ownerRepo, name string, runsOn []string) (model.Project, bool, error) {
	remote := "https://github.com/" + ownerRepo + ".git"
	for _, p := range sv.data.ListProjects() {
		if gitops.SameRemote(p.RemoteURL, remote) {
			return p, false, nil
		}
	}
	if x.Owner == nil || x.Owner.UserID != sv.c.User {
		return model.Project{}, false, fmt.Errorf("%w: only %s's owner can add GitHub repositories with its GitHub login", errForbidden, x.Name)
	}
	if name = strings.TrimSpace(name); name == "" {
		name = ownerRepo[strings.IndexByte(ownerRepo, '/')+1:]
	}
	if len(runsOn) == 0 {
		runsOn = x.Labels
	}
	p, err := sv.createHubProject(projectRequest{Name: name, GitHub: ownerRepo, RunsOn: runsOn, DefaultBranch: "main"})
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

// runningTask reports whether t is being worked on now: by Claude, a
// review or the merge.
func runningTask(t model.Todo) bool {
	return t.Status == model.StatusInProgress || t.Status == model.StatusMerging || t.Reviewing
}

// checkUnlink refuses to take runners gone off p (by a change of p's or
// their labels) while one of p's tasks is running on them: the runner would
// refuse to carry on with it. Tasks that are done running there keep their
// worktrees on it; queued again, they start afresh on one of p's runners.
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
		return fmt.Errorf("%w: %s running on %s: wait for them to finish (or cancel them) before changing the labels", errConflict, tasks, name)
	}
	return nil
}
