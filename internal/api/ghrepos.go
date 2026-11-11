package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tunnel"
)

// The hub's new-project form makes a project from a GitHub repository: one
// of its user's, picked from a list, or a new one. The hub runs no gh and
// has no GitHub login of its own, so both go through one of the user's
// runners, with the gh its owner signed in to there: only the runner's
// owner may list or create repositories with it (they are that person's).
// Neither touches the runner's disk; the project is then linked to the
// runner as any shared project is (PUT /api/hub/projects/{hpid}), which
// clones or adopts it in the runner's projects folder.

const ownerGitHub = "lists and creates GitHub repositories with its gh"

// GitHubRepoList is GET /api/hub/github-repos on a runner, and the hub's
// GET /api/runners/{id}/github-repos.
type GitHubRepoList struct {
	User  string           `json:"user"` // gh's signed-in login
	Repos []GitHubRepoItem `json:"repos"`
}

// GitHubRepoItem is a repository in the list; Linked names the hub's
// project for it, if there is one already, and Project its id (only the
// hub fills them in).
type GitHubRepoItem struct {
	gitops.GitHubListing
	Linked  string `json:"linked,omitempty"`
	Project string `json:"project,omitempty"`
}

// newGitHubRepo is POST …/github-repos: create Name (REPO, or OWNER/REPO
// for an organisation) on GitHub, empty, private unless Public.
type newGitHubRepo struct {
	Name   string `json:"name"`
	Public bool   `json:"public"`
}

// hubGitHubRepos handles GET /api/hub/github-repos on a runner: its
// owner's GitHub repositories (and their organisations'), one gh call per
// owner.
func (s *Server) hubGitHubRepos(w http.ResponseWriter, r *http.Request) {
	if err := s.onlyForOwner(r, ownerGitHub); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	if !s.Git.HasGh() {
		writeErr(w, fmt.Errorf("%w: this runner has no gh (GitHub CLI); install it and run gh auth login", errBadRequest))
		return
	}
	user, repos, err := s.Git.GitHubRepos(r.Context())
	if err != nil {
		writeErr(w, fmt.Errorf("%w: %v", errBadRequest, err))
		return
	}
	out := GitHubRepoList{User: user, Repos: make([]GitHubRepoItem, len(repos))}
	for i, x := range repos {
		out.Repos[i] = GitHubRepoItem{GitHubListing: x}
	}
	writeJSON(w, http.StatusOK, out)
}

// hubCreateGitHubRepo handles POST /api/hub/github-repos on a runner: a new,
// empty repository on GitHub with its owner's gh. Nothing is cloned here.
func (s *Server) hubCreateGitHubRepo(w http.ResponseWriter, r *http.Request) {
	if err := s.onlyForOwner(r, ownerGitHub); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var req newGitHubRepo
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if !s.Git.HasGh() {
		writeErr(w, fmt.Errorf("%w: this runner has no gh (GitHub CLI); install it and run gh auth login", errBadRequest))
		return
	}
	name := strings.Trim(strings.TrimSpace(req.Name), "/")
	if name == "" {
		writeErr(w, fmt.Errorf("%w: name is required", errBadRequest))
		return
	}
	ctx := r.Context()
	if repo, exists, err := s.Git.LookupGitHubRepo(ctx, name); err == nil && exists {
		writeErr(w, fmt.Errorf("%w: %s already exists on GitHub; pick it from your repositories instead", errBadRequest, repo.Name))
		return
	}
	repo, err := s.Git.CreateEmptyGitHubRepo(ctx, name, !req.Public)
	if err != nil {
		writeErr(w, fmt.Errorf("%w: %v", errBadRequest, err))
		return
	}
	writeJSON(w, http.StatusCreated, repo)
}

// runnerGitHubRepos handles GET /api/runners/{id}/github-repos on the hub:
// the caller's GitHub repositories, through their runner's gh, each marked
// with the org's project already made from it.
func (s *Server) runnerGitHubRepos(w http.ResponseWriter, r *http.Request) {
	c, x, ok := s.ownedRunner(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(tunnel.WithUser(r.Context(), c.User), time.Minute)
	defer cancel()
	var out GitHubRepoList
	if err := s.fleet().Call(ctx, x.ID, http.MethodGet, "/api/hub/github-repos", nil, &out); err != nil {
		writeErr(w, err)
		return
	}
	if out.Repos == nil {
		out.Repos = []GitHubRepoItem{}
	}
	markLinked(out.Repos, s.as(c).data.ListProjects())
	writeJSON(w, http.StatusOK, out)
}

// markLinked sets Linked and Project on the repositories some project's remote is.
func markLinked(repos []GitHubRepoItem, projects []model.Project) {
	for i := range repos {
		for _, p := range projects {
			if gitops.SameRemote(p.RemoteURL, repos[i].URL) || gitops.SameRemote(p.RemoteURL, "https://github.com/"+repos[i].NameWithOwner) {
				repos[i].Linked, repos[i].Project = p.Name, p.ID
				break
			}
		}
	}
}

// createRunnerGitHubRepo handles POST /api/runners/{id}/github-repos on the
// hub: a new GitHub repository, with the caller's runner's gh, for a new
// project ({"name", "url"} back).
func (s *Server) createRunnerGitHubRepo(w http.ResponseWriter, r *http.Request) {
	c, x, ok := s.ownedRunner(w, r)
	if !ok {
		return
	}
	var req newGitHubRepo
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(tunnel.WithUser(r.Context(), c.User), time.Minute)
	defer cancel()
	var out gitops.GitHubRepo
	if err := s.fleet().Call(ctx, x.ID, http.MethodPost, "/api/hub/github-repos", req, &out); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}
