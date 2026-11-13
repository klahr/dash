package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/notes"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/release"
	"github.com/klahr/dash/internal/scan"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// The routes below are a runner's side of dispatch: its hub links projects
// to it and sends it their tasks. They answer only the hub, over the
// runner's tunnel, and the runner checks everything it is sent:
//
//   - a link names the hub's org the runner was registered for and a
//     network git remote; the runner clones it into <projects root>/<org
//     folder>/<repo>, a folder it picks itself (never a path the hub sends;
//     the org folder's name is made safe), or adopts a clone of the same
//     remote already in that org folder; a folder holding anything else is
//     never touched (the next free name, <repo>-2, … is used);
//   - a task names a project linked that way, by the hub's id, which the
//     runner keeps with its checkout; anything else is refused and logged;
//   - the hub reaches only tasks it sent, by its own ids;
//   - a scan names a linked project; its flags are checked (scan.Job.Check)
//     and its worktree is a throwaway one under the runner's scans folder.

func (s *Server) hubTaskRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/hub/projects/{hpid}", s.hubLink)
	mux.HandleFunc("DELETE /api/hub/projects/{hpid}", s.hubUnlink)
	mux.HandleFunc("POST "+dispatch.OriginMovedPath, s.hubOriginMoved)
	mux.HandleFunc("POST /api/hub/tasks", s.hubTask)
	mux.HandleFunc("DELETE /api/hub/tasks/{htid}", s.hubTaskAction("delete"))
	mux.HandleFunc("POST /api/hub/tasks/{htid}/approve", s.hubTaskAction("approve"))
	mux.HandleFunc("POST /api/hub/tasks/{htid}/cancel", s.hubTaskAction("cancel"))
	mux.HandleFunc("POST /api/hub/tasks/{htid}/revert", s.hubTaskAction("revert"))
	mux.HandleFunc("POST /api/hub/tasks/{htid}/test", s.hubTaskAction("test"))
	mux.HandleFunc("POST /api/hub/tasks/{htid}/open", s.hubTaskAction("open"))
	mux.HandleFunc("GET /api/hub/tasks/{htid}/diff", s.hubTaskAction("diff"))
	mux.HandleFunc("GET /api/hub/tasks/{htid}/log", s.hubTaskAction("log"))
	mux.HandleFunc("GET /api/hub/tasks/{htid}/file", s.hubTaskFile)
	mux.HandleFunc("PUT /api/hub/tasks/{htid}/file", s.hubTaskFile)
	mux.HandleFunc("GET /api/hub/tasks/{htid}/events", s.hubTaskAction("events"))
	mux.HandleFunc("POST "+dispatch.PermissionPath, s.hubPermission)
	mux.HandleFunc("GET "+dispatch.ScanPath, s.hubScan)
	mux.HandleFunc("POST /api/hub/github/{hpid}", s.hubGitHub)
	mux.HandleFunc("POST "+notes.PeerPath, s.notesFromPeers)
	mux.HandleFunc("POST "+notes.StatusPath, s.hubNotesStatus)
	mux.HandleFunc("POST "+notes.RemovePath, s.hubNotesRemove)
	mux.HandleFunc("POST "+tunnel.UpdatePath, s.hubUpdate)
}

// hubUpdate updates this runner to the release its hub names, once none
// of the hub's tasks runs or merges here (409 until then), then restarts
// it (see SelfUpdate). A failed update leaves the old binary in place.
func (s *Server) hubUpdate(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.fromHub(w, r)
	if !ok {
		return
	}
	var req struct {
		Version string `json:"version"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if s.SelfUpdate == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "this runner can't update itself"})
		return
	}
	if n := len(sv.data.ListTodos("", model.StatusInProgress, model.StatusMerging)); n > 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("%d tasks are still running", n)})
		return
	}
	if err := s.SelfUpdate(r.Context(), req.Version); err != nil {
		slog.Error("runner: update", "version", req.Version, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"version": req.Version})
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

// hubProject is the runner's checkout of the hub's project hpid, if the
// hub linked it: found by the hub's id, wherever it is (see
// model.Project.LinkedFromHub).
func (sv *service) hubProject(hpid string) (model.Project, bool) {
	if hpid == "" {
		return model.Project{}, false
	}
	for _, p := range sv.data.ListProjects() {
		if p.HubProjectID == hpid && p.LinkedFromHub() {
			return p, true
		}
	}
	return model.Project{}, false
}

// ownProject is the runner's own project pid: made or added on this
// runner, not a checkout of a hub's project. The hub keeps no record of it
// and reaches it through /peer/{runner}/… by this runner's id.
func (sv *service) ownProject(pid string) (model.Project, error) {
	p, err := sv.data.GetProject(pid)
	if err == nil && p.LinkedFromHub() {
		return model.Project{}, fmt.Errorf("%w: project %s", store.ErrNotFound, pid)
	}
	return p, err
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
		if p, err := sv.data.GetProject(t.ProjectID); err == nil && p.LinkedFromHub() {
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
	if err := checkMergeMode(req.MergeMode, req.RemoteURL); err != nil {
		writeErr(w, err)
		return
	}
	mode := model.MergeLocal
	if req.MergeMode == model.MergePR && (req.MergeModeSet || s.Git.HasGh()) {
		// Pull requests only by default, and a runner with no gh to open
		// them merges and pushes instead.
		mode = model.MergePR
	}
	if s.HubOrg != 0 && req.OrgID != s.HubOrg {
		slog.Warn("runner: refused a project of another org", "project", hpid, "org", req.OrgID)
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this runner works for another organization"})
		return
	}
	// The hub's commands.
	commands := func(x *model.Project) {
		x.SetupCommand, x.TestCommand, x.RunCommand, x.DeployCommand = req.SetupCommand, req.TestCommand, req.RunCommand, req.DeployCommand
		if k, err := checkPreviewKind(req.PreviewKind); err == nil {
			x.PreviewKind, x.PreviewCommand = k, strings.TrimSpace(req.PreviewCommand)
		}
	}
	// The hub's review settings; an older hub that doesn't send them leaves
	// the runner's own.
	review := func(x *model.Project) {
		if req.AutoApprove != nil {
			x.AutoApprove = *req.AutoApprove
		}
		if req.MaxAutoFixes != nil {
			x.MaxAutoFixes = max(0, *req.MaxAutoFixes)
		}
	}
	folder := hubOrgFolder(req)
	if p, ok := sv.hubProject(hpid); ok {
		want := p
		commands(&want)
		review(&want)
		if p.HubOrgFolder != folder || p.TaskInstructions != req.TaskInstructions || p.MergeMode != mode ||
			want.AutoApprove != p.AutoApprove || want.MaxAutoFixes != p.MaxAutoFixes ||
			want.SetupCommand != p.SetupCommand || want.TestCommand != p.TestCommand || want.RunCommand != p.RunCommand || want.DeployCommand != p.DeployCommand ||
			want.PreviewKind != p.PreviewKind || want.PreviewCommand != p.PreviewCommand {
			// The hub's project decides how its tasks land.
			var err error
			if p, err = sv.data.UpdateProject(p.ID, func(x *model.Project) error {
				x.TaskInstructions, x.MergeMode = req.TaskInstructions, mode
				x.HubOrgFolder = folder
				commands(x)
				review(x)
				return nil
			}); err != nil {
				writeErr(w, err)
				return
			}
		}
		// A clone from before clones went in the org's folder moves there
		// now, unless it is busy; the hub gets its new path either way.
		p = s.moveHubClone(r.Context(), sv, p)
		if err := s.followWorkBranch(r.Context(), sv, p, req.WorkBranch); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, dispatch.Linked{ProjectID: p.ID, DefaultBranch: p.DefaultBranch, Path: p.Path})
		return
	}
	if err := gitops.CheckRemote(req.RemoteURL); err != nil {
		writeErr(w, errors.Join(errBadRequest, err))
		return
	}
	// What this runner clones: the project's own clone URL, else its
	// GitHub repository over this runner's protocol.
	cloneURL := gitops.CloneRemote(req.RemoteURL, req.CloneURL, s.GitHubProtocol)
	if err := gitops.CheckRemote(cloneURL); err != nil {
		writeErr(w, errors.Join(errBadRequest, err))
		return
	}
	if s.ProjectRoot == "" {
		writeErr(w, fmt.Errorf("%w: this runner has no projects root (-projects)", errBadRequest))
		return
	}
	// The link finishes even when the hub's request goes away (the browser
	// that asked reloads, say): a clone killed half way leaves a folder with
	// origin set but no valid HEAD, which hubPlace would then adopt on every
	// later link, and fail on.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), hubLinkTimeout)
	defer cancel()
	hubLinks.Lock() // two links never pick the same free folder
	defer hubLinks.Unlock()
	if p, ok := sv.hubProject(hpid); ok { // linked while this waited
		writeJSON(w, http.StatusOK, dispatch.Linked{ProjectID: p.ID, DefaultBranch: p.DefaultBranch, Path: p.Path})
		return
	}
	place, err := s.hubPlace(ctx, sv, hpid, folder, req.Name, req.RemoteURL, cloneURL)
	if err != nil {
		writeErr(w, err)
		return
	}
	path, id := place.Path, store.NewID()
	if place.Own != nil {
		id = place.Own.ID
	}
	how := model.HubAdopted
	if !place.Adopt {
		how = model.HubCloned
		// A remote this computer can't read says so, rather than with
		// git's exit 128 ("could not read Username", "Permission denied").
		// With -github-protocol=auto, a GitHub repository SSH can't read is
		// cloned over HTTPS with gh's login; that URL is the clone's origin.
		picked, err := s.Git.PickCloneRemote(ctx, id, s.machineName(), req.RemoteURL, req.CloneURL, s.GitHubProtocol)
		if err != nil {
			writeErr(w, errors.Join(errBadRequest, err))
			return
		}
		cloneURL = picked
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			writeErr(w, err)
			return
		}
		if err := s.Git.Clone(ctx, id, cloneURL, path); err != nil {
			// hubPlace picked a folder that didn't exist: what the failed
			// clone left there would look like a clone of this remote.
			_ = os.RemoveAll(path)
			writeErr(w, errors.Join(errBadRequest, err))
			return
		}
	}
	branch := s.hubBranch(ctx, id, path, req.DefaultBranch)
	seeded, err := s.Git.SeedRepo(ctx, id, path, branch) // an empty remote gets a first commit
	if err != nil {
		writeErr(w, err)
		return
	}
	if seeded {
		// on branch, published so the other runners clone the same one
		if err := s.Git.PushUpstream(ctx, id, path, branch); err != nil {
			slog.Warn("runner: pushing the first commit of an empty repository", "path", path, "branch", branch, "err", err)
		}
	}
	link := func(x *model.Project) {
		x.HubProjectID, x.HubCheckout, x.HubOrgFolder = hpid, how, folder
		x.DefaultBranch, x.RemoteURL = branch, s.Git.RemoteURL(ctx, id, path)
		x.TaskInstructions, x.MergeMode = req.TaskInstructions, mode
		x.MergeModeSet = false
		commands(x)
		review(x)
	}
	var p model.Project
	status := http.StatusCreated
	if place.Own != nil {
		// The runner's own project in that folder becomes the hub's.
		p, err = sv.data.UpdateProject(id, func(x *model.Project) error { link(x); return nil })
		status = http.StatusOK
	} else {
		np := model.Project{ID: id, Name: req.Name, Path: path, MaxAutoFixes: model.DefaultMaxAutoFixes, TagFormat: release.DefaultFormat}
		link(&np)
		p, err = sv.data.CreateProject(np)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.followWorkBranch(ctx, sv, p, req.WorkBranch); err != nil {
		writeErr(w, err)
		return
	}
	slog.Info("runner: hub linked a project", "name", p.Name, "path", p.Path, "checkout", how)
	writeJSON(w, status, dispatch.Linked{ProjectID: p.ID, DefaultBranch: p.DefaultBranch, Path: p.Path, Adopted: place.Adopt})
}

// hubLinks serialises linking, which picks folders.
var hubLinks sync.Mutex

// hubLinkTimeout bounds a link, its first clone included.
const hubLinkTimeout = 30 * time.Minute

// maxHubSuffix bounds the names tried for a hub's project: <repo>,
// <repo>-2, … <repo>-<maxHubSuffix>.
const maxHubSuffix = 100

// hubPlacement is where a hub's project goes on this runner.
type hubPlacement struct {
	Path  string
	Adopt bool           // a clone of the same remote is there already
	Own   *model.Project // the runner's own project in that folder, to link
}

// hubOrgFolder is the folder under the projects root that the hub's
// clones of req's org go in: the org's folder as the hub names it (the same
// one its own projects of the org go in), else org-<id> from a hub too old
// to send it. Never "": a new clone never goes in the bare root.
func hubOrgFolder(req dispatch.Link) string {
	if f := store.FolderSlug(req.OrgFolder); f != "" {
		return f
	}
	return fmt.Sprintf("org-%d", req.OrgID)
}

// hubOrgDir is <projects root>/<folder>, where the hub's clones of an org
// go. It needn't exist yet; when it does it must be a plain folder, not a
// file, a symlink or a repository of its own.
func (s *Server) hubOrgDir(folder string) (string, error) {
	if s.ProjectRoot == "" {
		return "", fmt.Errorf("%w: this runner has no projects root (-projects)", errBadRequest)
	}
	root := filepath.Clean(s.ProjectRoot)
	dir := filepath.Join(root, folder)
	if folder == "" || store.FolderSlug(folder) != folder || filepath.Dir(dir) != root {
		return "", fmt.Errorf("%w: bad organization folder %q", errBadRequest, folder)
	}
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return dir, nil
	case err != nil:
		return "", err
	case !fi.IsDir() || isRepo(dir):
		return "", fmt.Errorf("%w: %s is in the way of the folder for the organization's projects; move it", errBadRequest, dir)
	}
	return dir, nil
}

// hubCloneDir is the folder a clone of remote goes in: the org's folder
// (hubOrgDir), then the remote's owner (the GitHub user or organization),
// <projects root>/<folder>/<owner>, when remote names one. Like the org's
// folder it needn't exist yet, and when it does must be a plain folder.
func (s *Server) hubCloneDir(folder, remote string) (string, error) {
	dir, err := s.hubOrgDir(folder)
	if err != nil {
		return "", err
	}
	owner := folderName(gitops.RemoteOwner(remote))
	if owner == "" {
		return dir, nil
	}
	dir = filepath.Join(dir, owner)
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return dir, nil
	case err != nil:
		return "", err
	case !fi.IsDir() || isRepo(dir):
		return "", fmt.Errorf("%w: %s is in the way of the folder for %s's projects; move it", errBadRequest, dir, owner)
	}
	return dir, nil
}

// hubPlace picks the folder for the hub's project hpid with remote, in
// the org's folder for remote's owner, <projects root>/<folder>/<owner>/
// (hubCloneDir): its <repo> (the remote's last path part, else the
// project's name), else the first of <repo>-2, <repo>-3, … that is free or
// holds a clone of the same remote. A folder holding anything else (another
// repository, files, a symlink, a clone already linked to another of the
// hub's projects) is never used, and nothing outside that folder is ever
// adopted (a checkout of the same remote elsewhere in the projects root may
// be the user's own). The path is always a direct child of that folder;
// nothing the hub sends is a path. A clone whose origin is one of also (the
// URL this runner clones) is of the same remote too.
func (s *Server) hubPlace(ctx context.Context, sv *service, hpid, folder, name, remote string, also ...string) (hubPlacement, error) {
	dir, err := s.hubCloneDir(folder, remote)
	if err != nil {
		return hubPlacement{}, err
	}
	root := filepath.Clean(s.ProjectRoot)
	base := hubBase(name, remote)
	projects := map[string]model.Project{}
	for _, p := range sv.data.ListProjects() {
		projects[filepath.Clean(p.Path)] = p
	}
	for i := 1; i <= maxHubSuffix; i++ {
		folder := base
		if i > 1 {
			folder = fmt.Sprintf("%s-%d", base, i)
		}
		path := filepath.Join(dir, folder)
		if !within(root, path) || path == dir || filepath.Dir(path) != dir {
			return hubPlacement{}, fmt.Errorf("%w: bad project name", errBadRequest)
		}
		own, isProject := projects[path]
		fi, err := os.Lstat(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if !isProject { // a project whose folder is gone keeps its name
				return hubPlacement{Path: path}, nil
			}
			continue
		case err != nil || !fi.IsDir() || !isRepo(path):
			continue // a file, a symlink, a plain folder: not ours to touch
		case !sameRemote(s.Git.RemoteURL(ctx, "", path), append([]string{remote}, also...)):
			continue // another repository
		case isProject && own.HubProjectID != "" && own.HubProjectID != hpid:
			continue // a clone of the same remote, already the hub's for another project
		case isProject:
			return hubPlacement{Path: path, Adopt: true, Own: &own}, nil
		}
		return hubPlacement{Path: path, Adopt: true}, nil
	}
	return hubPlacement{}, fmt.Errorf("%w: no free folder for %s in %s (%s through %s-%d are taken)", errBadRequest, base, dir, base, base, maxHubSuffix)
}

// hubBase is the folder name for a clone of remote (the hub's project
// name): the repository's name, else the project's, else "project".
func hubBase(name, remote string) string {
	base := folderName(gitops.RemoteRepo(remote))
	if base == "" {
		base = folderName(name)
	}
	if base == "" {
		base = "project"
	}
	return base
}

var folderRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// folderName makes s a safe folder name: letters, digits, '.', '_' and
// '-', not starting with '.' or '-' ("" when nothing is left).
func folderName(s string) string {
	return strings.TrimLeft(strings.Trim(folderRe.ReplaceAllString(strings.TrimSpace(s), "-"), "-"), ".-")
}

// hubBranch is the default branch of the runner's checkout at path: want
// (the hub's), made from origin's when the checkout lacks it, else the
// checkout's main line, else "main".
func (s *Server) hubBranch(ctx context.Context, id, path, want string) string {
	if want != "" && !s.Git.HasBranch(ctx, id, path, want) && s.Git.RemoteHasBranch(ctx, id, path, want) {
		_, _ = s.Git.EnsureBranch(ctx, id, path, want, "")
	}
	if want == "" || !s.Git.HasBranch(ctx, id, path, want) {
		if b := s.Git.MainBranch(ctx, id, path); b != "" {
			return b
		}
	}
	if want == "" {
		want = "main"
	}
	return want
}

// LegacyHubClone reports whether a project with a HubProjectID was its
// hub's under the old rule: cloned under <projectRoot>/hub/. See
// store.BackfillHubCheckouts.
func LegacyHubClone(projectRoot string) func(model.Project) bool {
	hub := filepath.Join(projectRoot, "hub")
	return func(p model.Project) bool {
		return projectRoot != "" && within(hub, p.Path) && filepath.Clean(p.Path) != hub
	}
}

// hubUnlink handles DELETE /api/hub/projects/{hpid}: the runner takes no
// more tasks for it. Its clone stays, as a project of the runner's own.
func (s *Server) hubUnlink(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.fromHub(w, r)
	if !ok {
		return
	}
	if p, ok := sv.hubProject(r.PathValue("hpid")); ok {
		if _, err := sv.data.UpdateProject(p.ID, func(x *model.Project) error { x.HubProjectID, x.HubCheckout = "", ""; return nil }); err != nil {
			writeErr(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// hubOriginMoved handles POST /api/hub/projects/{hpid}/origin: another
// runner pushed a branch of the hub's project. The fetch (and fast-forward
// of a clean checkout) runs in the background; the hub doesn't wait for it.
// Only this runner's clone's main checkout may move, never a task's worktree.
func (s *Server) hubOriginMoved(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.fromHub(w, r)
	if !ok {
		return
	}
	var req dispatch.OriginMoved
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if !gitops.IsSHA(req.SHA) || gitops.CheckRefName("refs/heads/"+req.Branch) != nil {
		writeErr(w, fmt.Errorf("%w: want a branch and a commit id", errBadRequest))
		return
	}
	p, ok := sv.hubProject(r.PathValue("hpid"))
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": errNotLinked.Error()})
		return
	}
	if s.Merges != nil && s.Merges.Remote != nil {
		s.following.Add(1)
		go func() {
			defer s.following.Done()
			if err := s.Merges.Remote.OriginMoved(context.WithoutCancel(r.Context()), p, req.Branch, req.SHA); err != nil {
				slog.Info("runner: follow origin", "project", p.Name, "branch", req.Branch, "err", err)
			}
		}()
	}
	w.WriteHeader(http.StatusAccepted)
}

// hubPermission handles POST dispatch.PermissionPath: the hub's user
// answered a permission request of the hub's task {htid} on its board.
func (s *Server) hubPermission(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.fromHub(w, r)
	if !ok {
		return
	}
	var req dispatch.PermissionAnswer
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	t, ok := sv.hubTodo(r.PathValue("htid"))
	if !ok || s.Perms == nil {
		writeErr(w, fmt.Errorf("%w: no such task from the hub", store.ErrNotFound))
		return
	}
	id := r.PathValue("id")
	if pr, ok := s.Perms.Get(id); !ok || pr.TodoID != t.ID {
		writeErr(w, fmt.Errorf("%w: that permission request was answered or has ended", store.ErrNotFound))
		return
	}
	if err := s.Perms.Decide(id, req.Decision, req.Message); err != nil {
		if errors.Is(err, perm.ErrNotFound) {
			writeErr(w, fmt.Errorf("%w: that permission request was answered or has ended", store.ErrNotFound))
			return
		}
		writeErr(w, fmt.Errorf("%w: %v", errBadRequest, err))
		return
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
	// Too full to build: the task waits on the hub, its log saying why,
	// rather than failing halfway with a write error.
	if s.DiskRoom != nil {
		if err := s.DiskRoom(req.Disk); err != nil {
			slog.Warn("runner: not starting a task: disk nearly full", "task", req.HubTodoID, "err", err)
			writeJSON(w, http.StatusInsufficientStorage, map[string]string{"error": err.Error()})
			return
		}
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
			x.Model = req.Model // the hub's task may have changed model since
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
	// Queued from the start: a report caught between creating a draft and
	// queueing it would tell the hub the task was canceled here.
	t, err := sv.data.CreateQueuedTodo(model.Todo{
		ProjectID: p.ID, Title: req.Title, Prompt: req.Prompt, Model: req.Model, Feedback: req.Feedback,
		HubTodoID: req.HubTodoID, HubNotes: len(req.Feedback),
	})
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
		case "revert":
			if err := sv.revert(r.Context(), t); err != nil {
				writeErr(w, err)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		case "test":
			if err := sv.runTests(r.Context(), t); err != nil {
				writeErr(w, err)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		case "open":
			if err := s.openWorktree(t); err != nil {
				writeErr(w, err)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		case "cancel":
			if _, _, err := sv.cancel(t.ID); err != nil {
				writeErr(w, err)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		case "delete":
			// The hub forgot it: stop it and keep it as the runner's own.
			// Deleting it there rejected it: its pull request closes.
			s.Todos.Cancel(t.ID)
			s.rejectPR(t)
			if _, err := sv.data.UpdateTodo(t.ID, func(x *model.Todo) error { x.HubTodoID = ""; return nil }); err != nil {
				writeErr(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "diff":
			if t.Worktree == nil {
				writeText(w, "")
				return
			}
			diff, err := sv.diff(r.Context(), t.ID)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeText(w, diff)
		case "log":
			log, err := sv.log(t.ID)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeText(w, log)
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

// followWorkBranch makes the runner's clone p start tasks from, and land
// them on, the hub's working branch: one origin has (the hub pushes it when
// it switches), fetched first when the clone hasn't got it yet.
func (s *Server) followWorkBranch(ctx context.Context, sv *service, p model.Project, branch string) error {
	if branch == p.DefaultBranch {
		branch = ""
	}
	if branch == p.WorkBranch {
		return nil
	}
	if branch != "" {
		err := s.withRemoteLock(ctx, p, func() error {
			if !s.Git.HasBranch(ctx, p.ID, p.Path, branch) {
				fctx, cancel := context.WithTimeout(ctx, branchFetchTimeout)
				defer cancel()
				if err := s.Git.FetchBranch(fctx, p.ID, p.Path, branch, false); err != nil {
					return fmt.Errorf("fetch the hub's branch %s: %w", branch, err)
				}
			}
			_, err := s.Git.EnsureBranch(ctx, p.ID, p.Path, branch, "")
			return err
		})
		if err != nil {
			return err
		}
	}
	_, err := sv.data.UpdateProject(p.ID, func(x *model.Project) error { x.WorkBranch = branch; return nil })
	return err
}

// hubScan handles GET /ws/hub/scan: the Claude part of one of the hub's
// scan runs, in this runner's clone of a project the hub linked (see
// scan.Job). The hub keeps the run, its log and the issues it files; this
// side streams back what it scans and Claude's progress, and stops when the
// hub closes the socket.
func (s *Server) hubScan(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.fromHub(w, r)
	if !ok {
		return
	}
	if s.Scans == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "this runner doesn't run scans"})
		return
	}
	conn, err := hub.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	var wmu sync.Mutex
	send := func(m scan.Message) {
		wmu.Lock()
		defer wmu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		_ = conn.WriteJSON(m)
	}
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var req scan.Request
	if err := conn.ReadJSON(&req); err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	p, ok := sv.hubProject(req.Project)
	if !ok {
		slog.Warn("runner: refused a scan of a project not linked to it", "project", req.Project)
		send(scan.Message{Error: errNotLinked.Error()})
		return
	}
	if err := req.Job.Check(); err != nil {
		send(scan.Message{Error: err.Error()})
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		// The hub closing the socket stops the scan.
		defer cancel()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	// Scan origin's default branch as it is now: this clone's own branch
	// moves only when a task lands here.
	job := req.Job
	err = s.withRemoteLock(ctx, p, func() error {
		fctx, cancel := context.WithTimeout(ctx, branchFetchTimeout)
		defer cancel()
		return s.Git.FetchBranch(fctx, p.ID, p.Path, p.DefaultBranch, false)
	})
	if err == nil && s.Git.RemoteHasBranch(ctx, p.ID, p.Path, p.DefaultBranch) {
		job.Ref = "refs/remotes/origin/" + p.DefaultBranch
	} else if err != nil {
		send(scan.Message{Log: "⚠ couldn't fetch " + p.DefaultBranch + " from origin, scanning this runner's copy: " + err.Error()})
	}
	slog.Info("runner: scan for the hub", "project", p.Name, "scan", job.Template.Name)
	out, err := s.Scans.Work(ctx, p, job, scan.Hooks{
		Log:    func(l string) { send(scan.Message{Log: l}) },
		Target: func(o scan.Outcome) { send(scan.Message{Target: &o}) },
		Event:  func(e model.RunEvent) { send(scan.Message{Event: &e}) },
	})
	if err != nil {
		send(scan.Message{Error: err.Error(), Done: &out})
		return
	}
	send(scan.Message{Done: &out})
}

// hubGitHub handles POST /api/hub/github/{hpid}: gh in this runner's clone
// of a project the hub linked, for the hub's CI view (see
// api.RunnerGitHub): reading builds, a release's page, a failed build's
// log, and re-running a build. Nothing else.
func (s *Server) hubGitHub(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.fromHub(w, r)
	if !ok {
		return
	}
	p, ok := sv.hubProject(r.PathValue("hpid"))
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": errNotLinked.Error()})
		return
	}
	var req dispatch.GitHubCall
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	limit := min(max(req.Limit, 1), 200)
	ctx := r.Context()
	var out dispatch.GitHubAnswer
	var err error
	switch req.Op {
	case dispatch.GitHubRuns:
		out.Runs, err = s.Git.Runs(ctx, p.ID, p.Path, req.Ref, limit)
	case dispatch.GitHubCommitRuns:
		out.Runs, err = s.Git.RunsForCommit(ctx, p.ID, p.Path, req.Ref, limit)
	case dispatch.GitHubReleaseURL:
		out.URL, err = s.Git.ReleaseURL(ctx, p.ID, p.Path, req.Ref)
	case dispatch.GitHubRerun:
		err = s.Git.RerunRun(ctx, p.ID, p.Path, req.RunID, req.FailedOnly)
	case dispatch.GitHubFailedLog:
		out.Log = s.Git.RunFailedLog(ctx, p.ID, p.Path, req.RunID, limit)
	default:
		err = fmt.Errorf("%w: unknown op %q", errBadRequest, req.Op)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// machineName is this computer's name in the dashboard, for messages.
func (s *Server) machineName() string {
	if s.Peers != nil {
		return s.Peers.Self
	}
	return ""
}

// sameRemote reports whether origin is any of remotes (gitops.SameRemote).
func sameRemote(origin string, remotes []string) bool {
	for _, r := range remotes {
		if gitops.SameRemote(origin, r) {
			return true
		}
	}
	return false
}
