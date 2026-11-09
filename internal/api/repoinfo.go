package api

import (
	"context"
	"net/http"
	"time"

	"github.com/klahr/dash/internal/gitops"
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

// repoInfo handles GET /api/projects/{pid}/repo-info.
func (s *Server) repoInfo(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	p, err := sv.data.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
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
