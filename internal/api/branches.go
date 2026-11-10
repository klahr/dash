package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// branchFetchTimeout bounds fetching origin's branches for the switcher.
const branchFetchTimeout = 20 * time.Second

// branchList is GET /api/projects/{pid}/branches.
type branchList struct {
	Default  string              `json:"default"` // the project's default branch
	Current  string              `json:"current"` // the branch tasks start from and land on
	Branches []gitops.BranchInfo `json:"branches"`
	// Ahead and Behind compare Current with Default (when they differ).
	Ahead  int `json:"ahead"`
	Behind int `json:"behind"`
	// PR is Current's open pull request into Default, if there is one.
	PR *model.PullRequest `json:"pr,omitempty"`
	// FetchError is why origin's branches couldn't be fetched (the list is
	// then as of the last fetch).
	FetchError string `json:"fetch_error,omitempty"`
}

// listBranches handles GET /api/projects/{pid}/branches[?fetch=1]: the
// branches the switcher offers. With fetch, origin's are fetched first.
func (s *Server) listBranches(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.releaseProject(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	out := branchList{Default: p.DefaultBranch, Current: p.Base()}
	if p.RemoteURL != "" && r.URL.Query().Get("fetch") == "1" {
		if err := s.withRemoteLock(ctx, p, func() error {
			fctx, cancel := context.WithTimeout(ctx, branchFetchTimeout)
			defer cancel()
			return s.Git.FetchBranches(fctx, p.ID, p.Path)
		}); err != nil {
			out.FetchError = briefGit(err)
		}
	}
	branches, err := s.Git.ListBranches(ctx, p.ID, p.Path)
	if err != nil {
		writeErr(w, err)
		return
	}
	out.Branches = branches
	if p.OnWorkBranch() {
		out.Ahead, out.Behind, _ = s.Git.CompareBranches(ctx, p.ID, p.Path, p.DefaultBranch, p.Base())
		if p.RemoteURL != "" {
			pctx, cancel := context.WithTimeout(ctx, branchFetchTimeout)
			if n, url, ok, err := s.Git.FindPR(pctx, p.ID, p.Path, p.DefaultBranch, p.Base()); err == nil && ok {
				out.PR = &model.PullRequest{Number: n, URL: url, State: "OPEN"}
			}
			cancel()
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// switchBranch handles PUT /api/projects/{pid}/branch {"branch", "create"}:
// new tasks start from branch and land on it. A branch only origin has is
// checked out from origin's; with create, a branch neither has is made from
// the current one. When the project has a remote, a branch origin lacks is
// pushed, so tasks' pull requests (and pushes) have somewhere to go. Tasks
// already started keep landing on the branch they started from.
func (s *Server) switchBranch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Branch string `json:"branch"`
		Create bool   `json:"create"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	sv, p, ok := s.releaseProject(w, r)
	if !ok {
		return
	}
	p, err := s.setWorkBranch(r.Context(), sv, p, strings.TrimSpace(req.Branch), req.Create)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) setWorkBranch(ctx context.Context, sv *service, p model.Project, branch string, create bool) (model.Project, error) {
	if branch == "" {
		branch = p.DefaultBranch
	}
	if err := gitops.ValidBranchName(ctx, s.Git, branch); err != nil {
		return p, fmt.Errorf("%w: %v", errBadRequest, err)
	}
	if branch != p.DefaultBranch {
		from := ""
		if create {
			from = "refs/heads/" + p.Base()
		}
		var created bool
		err := s.withRemoteLock(ctx, p, func() (err error) {
			created, err = s.Git.EnsureBranch(ctx, p.ID, p.Path, branch, from)
			return err
		})
		if err != nil {
			if ce := (*gitops.CmdError)(nil); !errors.As(err, &ce) {
				err = fmt.Errorf("%w: %v", errBadRequest, err)
			}
			return p, err
		}
		if p.RemoteURL != "" && (created || !s.Git.RemoteHasBranch(ctx, p.ID, p.Path, branch)) {
			if err := s.withRemoteLock(ctx, p, func() error { return s.Git.Push(ctx, p.ID, p.Path, branch) }); err != nil {
				return p, fmt.Errorf("the branch %s was made here, but pushing it to origin failed: %w", branch, err)
			}
			s.reportPush(ctx, p, branch)
		}
	}
	work := branch
	if work == p.DefaultBranch {
		work = ""
	}
	np, err := sv.data.UpdateProject(p.ID, func(x *model.Project) error {
		x.WorkBranch = work
		x.SyncError = "" // it was about the other branch
		return nil
	})
	if err != nil {
		return p, err
	}
	if s.Merges != nil && s.Merges.Remote != nil {
		s.Merges.Remote.Select(ctx, np, np.SyncsRemote(), true)
	}
	return np, nil
}

// branchPR handles POST /api/projects/{pid}/branch/pr {"title", "body"}:
// push the working branch and open (or find) its pull request into the
// default branch.
func (s *Server) branchPR(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	sv, p, ok := s.releaseProject(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	switch {
	case !p.OnWorkBranch():
		writeErr(w, fmt.Errorf("%w: tasks land on %s itself; switch to another branch first", errBadRequest, p.DefaultBranch))
		return
	case p.RemoteURL == "":
		writeErr(w, fmt.Errorf("%w: the project has no GitHub repository", errBadRequest))
		return
	}
	branch := p.Base()
	if err := s.withRemoteLock(ctx, p, func() error { return s.Git.Push(ctx, p.ID, p.Path, branch) }); err != nil {
		writeErr(w, fmt.Errorf("push %s: %w", branch, err))
		return
	}
	s.reportPush(ctx, p, branch)
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = branch
	}
	body := strings.TrimSpace(req.Body)
	if body == "" {
		body = s.branchPRBody(ctx, sv, p)
	}
	n, url, err := s.Git.OpenPR(ctx, p.ID, p.Path, p.DefaultBranch, branch, title, body)
	if err != nil {
		writeErr(w, fmt.Errorf("open pull request: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, model.PullRequest{Number: n, URL: url, State: "OPEN"})
}

// branchPRBody lists the Dash tasks that landed on the working branch and
// aren't on the default branch yet.
func (s *Server) branchPRBody(ctx context.Context, sv *service, p model.Project) string {
	var b strings.Builder
	commits, _ := s.Git.CommitsBetween(ctx, p.ID, p.Path, "refs/heads/"+p.DefaultBranch, "refs/heads/"+p.Base())
	for _, c := range commits {
		for _, id := range c.Todos {
			if t, err := sv.data.GetTodo(id); err == nil {
				fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(t.Title))
			}
		}
	}
	if b.Len() > 0 {
		return "Tasks on `" + p.Base() + "`:\n\n" + b.String() + "\n---\nOpened by Dash."
	}
	return "Merges `" + p.Base() + "` into `" + p.DefaultBranch + "`.\n\n---\nOpened by Dash."
}

// withRemoteLock runs fn holding the project's remote lock, so it doesn't
// race a merge or a background fetch.
func (s *Server) withRemoteLock(ctx context.Context, p model.Project, fn func() error) error {
	if s.Merges == nil || s.Merges.Remote == nil {
		return fn()
	}
	return s.Merges.Remote.With(ctx, p.ID, fn)
}

// reportPush tells whoever follows Dash's pushes (a runner's hub, see
// queue.MergeQueue.OnPushed) that branch was just pushed to origin.
func (s *Server) reportPush(ctx context.Context, p model.Project, branch string) {
	if s.Merges == nil || s.Merges.OnPushed == nil {
		return
	}
	if sha, err := s.Git.Head(ctx, p.ID, p.Path, branch); err == nil {
		s.Merges.OnPushed(p, branch, sha)
	}
}
