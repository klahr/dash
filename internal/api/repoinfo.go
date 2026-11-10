package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/release"
)

// repoInfoTimeout bounds asking GitHub about a project's repository.
const repoInfoTimeout = 15 * time.Second

// projectRepoInfo is GET /api/projects/{pid}/repo-info: what the project
// settings show about the repository. GitHub is what gh says about origin
// (nil when origin isn't on GitHub, or gh can't tell; Error says why);
// LatestTag is the highest tag in the project's tag format in the clone.
type projectRepoInfo struct {
	RemoteURL string           `json:"remote_url"`
	GitHub    *gitops.RepoInfo `json:"github"`
	Error     string           `json:"error,omitempty"`
	LatestTag string           `json:"latest_tag,omitempty"`
}

// repoInfoOnRunner is repoInfo on the hub: a runner answers it, and the
// links in its answer are kept only if they are pages on GitHub (the
// homepage: any http(s) page). The runner isn't trusted, and the hub's
// project settings show them as links (see model.IsGitHubWebURL).
func (s *Server) repoInfoOnRunner(w http.ResponseWriter, r *http.Request, sv *service, p model.Project) {
	res, err := sv.forward(r, p, "", nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !res.ok() {
		res.write(w)
		return
	}
	var out projectRepoInfo
	if err := json.Unmarshal(res.body, &out); err != nil {
		writeErr(w, fmt.Errorf("the runner's answer: %w", err))
		return
	}
	safeRepoLinks(&out)
	writeJSON(w, http.StatusOK, out)
}

// safeRepoLinks drops the links in x that may not be shown on the hub.
func safeRepoLinks(x *projectRepoInfo) {
	g := x.GitHub
	if g == nil {
		return
	}
	g.URL = model.GitHubWebURL(g.URL)
	g.Homepage = webURL(g.Homepage)
	if g.LatestRelease != nil {
		g.LatestRelease.URL = model.GitHubWebURL(g.LatestRelease.URL)
	}
}

// webURL is u if it is an http(s) page, else "".
func webURL(u string) string {
	x, err := url.Parse(u)
	if err != nil || (x.Scheme != "https" && x.Scheme != "http") || x.Host == "" || x.User != nil {
		return ""
	}
	return u
}

// repoInfo handles GET /api/projects/{pid}/repo-info.
func (s *Server) repoInfo(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.project(w, r)
	if !ok {
		return
	}
	out := projectRepoInfo{RemoteURL: p.RemoteURL}
	if s.Git == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	if url := s.Git.RemoteURL(r.Context(), p.ID, p.Path); url != "" {
		out.RemoteURL = url
	}
	if f, err := release.ParseFormat(p.TagFormat); err == nil {
		if tags, err := s.Git.Tags(r.Context(), p.ID, p.Path); err == nil {
			out.LatestTag = f.Suggest(tags).Latest
		}
	}
	switch {
	case out.RemoteURL == "":
		out.Error = "The project has no git remote."
	case gitops.RepoName(out.RemoteURL) == "":
		out.Error = "Origin isn't a GitHub repository."
	case !s.Git.HasGh():
		out.Error = "This computer has no gh (GitHub CLI) to ask GitHub; install it and run gh auth login."
	default:
		ctx, cancel := context.WithTimeout(r.Context(), repoInfoTimeout)
		defer cancel()
		info, err := s.Git.RepoInfo(ctx, out.RemoteURL)
		if err != nil {
			out.Error = "GitHub didn't answer: " + briefGit(err)
		} else {
			out.GitHub = &info
		}
	}
	writeJSON(w, http.StatusOK, out)
}
