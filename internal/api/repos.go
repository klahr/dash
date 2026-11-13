package api

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/release"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// Adding a runner's repositories: after a runner is added, its owner picks
// which of the git repositories in its projects directory (-projects) become
// its own projects. The runner looks (the hub can't see its disk) and only
// for its owner; it reports names, paths inside its projects directory,
// origin remotes and branches, never what's in the files.

// LocalRepo is a git repository a runner found in its projects directory.
type LocalRepo struct {
	Name   string `json:"name"`             // its folder's name
	Path   string `json:"path"`             // relative to the projects directory, with /
	Remote string `json:"remote,omitempty"` // origin, without credentials
	Branch string `json:"branch,omitempty"` // checked out; "" when detached
}

// maxRepos caps a listing (a projects directory of thousands of folders
// is not something to tick through).
const maxRepos = 500

// findRepos lists the git repositories in root: its folders that are
// repositories and, inside the folders that aren't, theirs (root/org/repo).
// It goes no deeper, never follows a symlink (so it never leaves root),
// skips hidden folders, the top folder named skipTop (the hub's clones),
// and repositories have reports true for.
func findRepos(root, skipTop string, have func(abs string) bool) ([]LocalRepo, error) {
	if root == "" {
		return nil, fmt.Errorf("%w: this runner has no projects directory (-projects)", errBadRequest)
	}
	root = filepath.Clean(root)
	top, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := []LocalRepo{}
	add := func(abs string) {
		if len(out) >= maxRepos || have(abs) {
			return
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil || !within(root, abs) || rel == "." {
			return
		}
		repo := LocalRepo{Name: filepath.Base(abs), Path: filepath.ToSlash(rel)}
		repo.Branch, repo.Remote = repoHead(abs), repoOrigin(abs)
		out = append(out, repo)
	}
	for _, e := range top {
		if !plainDir(e) || (skipTop != "" && e.Name() == skipTop) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if isRepo(dir) {
			add(dir)
			continue
		}
		inner, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range inner {
			if sub := filepath.Join(dir, f.Name()); plainDir(f) && isRepo(sub) {
				add(sub)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// plainDir reports whether e is a folder that isn't hidden or a symlink.
func plainDir(e fs.DirEntry) bool {
	return e.IsDir() && e.Type()&fs.ModeSymlink == 0 && !strings.HasPrefix(e.Name(), ".")
}

// isRepo reports whether dir has a .git folder of its own (not a symlink,
// nor a .git file pointing elsewhere, as linked worktrees and submodules
// have).
func isRepo(dir string) bool {
	fi, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil && fi.IsDir()
}

// readGitFile reads a small regular file in dir's .git folder, or nil.
func readGitFile(dir, name string, limit int64) []byte {
	p := filepath.Join(dir, ".git", name)
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > limit {
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, limit))
	return b
}

// repoHead is the branch checked out in dir, or "" (detached, unreadable).
func repoHead(dir string) string {
	head := strings.TrimSpace(string(readGitFile(dir, "HEAD", 4<<10)))
	b, ok := strings.CutPrefix(head, "ref: refs/heads/")
	if !ok {
		return ""
	}
	return b
}

// repoOrigin is the url of dir's origin remote, from its .git/config,
// without credentials, or "".
func repoOrigin(dir string) string {
	sc := bufio.NewScanner(bytes.NewReader(readGitFile(dir, "config", 256<<10)))
	in := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			in = strings.EqualFold(strings.Join(strings.Fields(strings.Trim(line, "[]")), " "), `remote "origin"`)
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if in && ok && strings.EqualFold(strings.TrimSpace(k), "url") {
			v = strings.TrimSpace(v)
			if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
				v = v[1 : len(v)-1]
			}
			return gitops.RedactRemote(v)
		}
	}
	return ""
}

// ownRepos is findRepos for this Dash: its projects directory, without
// the hub's clones and the repositories that are already projects.
func (sv *service) ownRepos() ([]LocalRepo, error) {
	return findRepos(sv.s.ProjectRoot, "hub", sv.isProjectPath())
}

// isProjectPath returns a func reporting whether a folder is already one
// of the org's projects (following symlinks either side).
func (sv *service) isProjectPath() func(abs string) bool {
	have := map[string]bool{}
	mark := func(p string) {
		have[filepath.Clean(p)] = true
		if real, err := filepath.EvalSymlinks(p); err == nil {
			have[real] = true
		}
	}
	for _, p := range sv.data.ListProjects() {
		mark(p.Path)
	}
	return func(abs string) bool {
		if have[filepath.Clean(abs)] {
			return true
		}
		real, err := filepath.EvalSymlinks(abs)
		return err == nil && have[real]
	}
}

// checkExistingRepo makes sure path is a git repository that isn't one of
// the org's projects yet, before it's added as one.
func (sv *service) checkExistingRepo(path string) error {
	fi, err := os.Stat(path)
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("%w: no folder at %s", errBadRequest, path)
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		return fmt.Errorf("%w: %s is not a git repository (no .git in it)", errBadRequest, path)
	}
	if sv.isProjectPath()(path) {
		return fmt.Errorf("%w: %s is already a project", errBadRequest, path)
	}
	return nil
}

// adoptedBranch is the default branch of a project made from the repo at
// path: the branch checked out, else want if it exists, else its main line.
func (s *Server) adoptedBranch(ctx context.Context, id, path, want string) string {
	if b, err := s.Git.CurrentBranch(ctx, id, path); err == nil && b != "HEAD" {
		want = b
	}
	// A detached HEAD leaves the requested name, which may not exist (master repos).
	if !s.Git.HasBranch(ctx, id, path, want) {
		if b := s.Git.MainBranch(ctx, id, path); b != "" {
			want = b
		}
	}
	return want
}

// adoptRepo makes the repository at path a project, as adding a project
// with an existing folder does, with the default settings.
func (sv *service) adoptRepo(ctx context.Context, name, path string) (model.Project, error) {
	s := sv.s
	id := store.NewID()
	if err := s.Git.InitRepo(ctx, id, path, "main"); err != nil {
		return model.Project{}, err
	}
	return sv.data.CreateProject(model.Project{
		ID: id, Name: name, Path: path, DefaultBranch: s.adoptedBranch(ctx, id, path, "main"),
		RemoteURL: s.Git.RemoteURL(ctx, id, path), MaxAutoFixes: model.DefaultMaxAutoFixes,
		TagFormat: release.DefaultFormat,
	})
}

// emptyOrMissing reports whether dir doesn't exist or is an empty directory.
func emptyOrMissing(dir string) bool {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	return err == nil && len(entries) == 0
}

const ownerRepos = "lists and adds its repositories"

// hubRepos handles GET /api/hub/repos on a runner: the repositories in its
// projects directory that could become its projects, for its owner.
func (s *Server) hubRepos(w http.ResponseWriter, r *http.Request) {
	if err := s.onlyForOwner(r, ownerRepos); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	repos, err := sv.ownRepos()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": repos, "root": s.ProjectRoot})
}

// addReposRequest picks repositories by their path in the listing.
type addReposRequest struct {
	Paths []string `json:"paths"`
}

// AddedRepos is what adding repositories made of each.
type AddedRepos struct {
	Added  []model.Project `json:"added"`
	Failed []RepoFailure   `json:"failed"`
}

// RepoFailure is a repository that wasn't added, and why.
type RepoFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// hubAddRepos handles POST /api/hub/repos on a runner: its owner's picks
// from the listing become its own projects. Only paths the listing has now
// are taken, so nothing outside the projects directory (or already a
// project, or among the hub's clones) can be added this way.
func (s *Server) hubAddRepos(w http.ResponseWriter, r *http.Request) {
	if err := s.onlyForOwner(r, ownerRepos); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req addReposRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if len(req.Paths) == 0 || len(req.Paths) > maxRepos {
		writeErr(w, fmt.Errorf("%w: pick 1 to %d repositories", errBadRequest, maxRepos))
		return
	}
	repos, err := sv.ownRepos()
	if err != nil {
		writeErr(w, err)
		return
	}
	listed := map[string]LocalRepo{}
	for _, x := range repos {
		listed[x.Path] = x
	}
	out := AddedRepos{Added: []model.Project{}, Failed: []RepoFailure{}}
	for _, rel := range req.Paths {
		x, ok := listed[rel]
		if !ok {
			out.Failed = append(out.Failed, RepoFailure{rel, "not a repository in this runner's projects directory, or already a project"})
			continue
		}
		delete(listed, rel) // the same path twice adds it once
		path := filepath.Join(s.ProjectRoot, filepath.FromSlash(x.Path))
		if !within(s.ProjectRoot, path) {
			out.Failed = append(out.Failed, RepoFailure{rel, "outside the projects directory"})
			continue
		}
		p, err := sv.adoptRepo(r.Context(), x.Name, path)
		if err != nil {
			out.Failed = append(out.Failed, RepoFailure{rel, err.Error()})
			continue
		}
		out.Added = append(out.Added, p)
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- the hub's side ----

// ownedRunner is the runner in r's path if the caller registered it: only
// its owner sees and adds the repositories on its computer (the runner
// checks that too). Another org's runner is "not found".
func (s *Server) ownedRunner(w http.ResponseWriter, r *http.Request) (Caller, tunnel.Runner, bool) {
	return s.runnerFor(w, r, func(c Caller, x tunnel.Runner) bool {
		return x.Owner != nil && x.Owner.UserID == c.User
	}, "only the runner's owner can see and add the repositories on its computer")
}

// localProject is a new local-only project: a repository on its runner
// alone, with no remote.
type localProject struct {
	Name      string `json:"name"`
	LocalOnly bool   `json:"local_only"`
	commandFields
}

// createRunnerProject handles POST /api/runners/{id}/projects on the hub: a
// local-only project, the caller's runner's own, made there as a new
// repository (git init and an initial commit) in a new folder named after
// it in the runner's projects folder, with no remote. For throwaway work:
// deleting it deletes that folder too.
func (s *Server) createRunnerProject(w http.ResponseWriter, r *http.Request) {
	c, x, ok := s.ownedRunner(w, r)
	if !ok {
		return
	}
	var req localProject
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.Name = strings.TrimSpace(req.Name); req.Name == "" {
		writeErr(w, fmt.Errorf("%w: name is required", errBadRequest))
		return
	}
	req.LocalOnly = true
	ctx, cancel := context.WithTimeout(tunnel.WithUser(r.Context(), c.User), time.Minute)
	defer cancel()
	var out model.Project
	if err := s.fleet().Call(ctx, x.ID, http.MethodPost, "/api/projects", req, &out); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// runnerRepos handles GET /api/runners/{id}/repos: the repositories in the
// caller's runner's projects directory that could become its projects.
func (s *Server) runnerRepos(w http.ResponseWriter, r *http.Request) {
	c, x, ok := s.ownedRunner(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(tunnel.WithUser(r.Context(), c.User), 30*time.Second)
	defer cancel()
	var out struct {
		Repos []LocalRepo `json:"repos"`
		Root  string      `json:"root"`
	}
	if err := s.fleet().Call(ctx, x.ID, http.MethodGet, "/api/hub/repos", nil, &out); err != nil {
		writeErr(w, err)
		return
	}
	if out.Repos == nil {
		out.Repos = []LocalRepo{}
	}
	writeJSON(w, http.StatusOK, out)
}

// addRunnerRepos handles POST /api/runners/{id}/repos: {"paths"} from the
// listing become the caller's runner's own projects.
func (s *Server) addRunnerRepos(w http.ResponseWriter, r *http.Request) {
	c, x, ok := s.ownedRunner(w, r)
	if !ok {
		return
	}
	var req addReposRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if len(req.Paths) == 0 || len(req.Paths) > maxRepos {
		writeErr(w, fmt.Errorf("%w: pick 1 to %d repositories", errBadRequest, maxRepos))
		return
	}
	ctx, cancel := context.WithTimeout(tunnel.WithUser(r.Context(), c.User), 2*time.Minute)
	defer cancel()
	var out AddedRepos
	if err := s.fleet().Call(ctx, x.ID, http.MethodPost, "/api/hub/repos", req, &out); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
