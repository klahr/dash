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
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/fsutil"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/release"
	"github.com/klahr/dash/internal/scan"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/textutil"
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

func (s *Server) hubTaskRoutes(mux routeMux) {
	mux.HandleFunc("PUT /api/hub/projects/{hpid}", s.hubLink)
	mux.HandleFunc("DELETE /api/hub/projects/{hpid}", s.hubUnlink)
	mux.HandleFunc("POST "+dispatch.OriginMovedPath, s.hubOriginMoved)
	mux.HandleFunc("POST /api/hub/tasks", s.hubTask)
	mux.HandleFunc("PUT "+dispatch.UploadPath, s.hubUpload)
	mux.HandleFunc("DELETE /api/hub/tasks/{htid}", s.hubForget)
	mux.HandleFunc("POST /api/hub/tasks/{htid}/approve", s.hubApprove)
	mux.HandleFunc("POST /api/hub/tasks/{htid}/cancel", s.hubCancel)
	mux.HandleFunc("POST /api/hub/tasks/{htid}/revert", s.hubRevert)
	mux.HandleFunc("POST /api/hub/tasks/{htid}/merge-pr", s.hubMergePR)
	mux.HandleFunc("POST /api/hub/tasks/{htid}/test", s.hubRunTests)
	mux.HandleFunc("POST /api/hub/tasks/{htid}/open", s.hubOpenWorktree)
	mux.HandleFunc("GET /api/hub/tasks/{htid}/diff", s.hubDiff)
	mux.HandleFunc("GET /api/hub/tasks/{htid}/files", s.hubFiles)
	mux.HandleFunc("GET /api/hub/tasks/{htid}/image", s.hubTaskImage)
	mux.HandleFunc("GET /api/hub/tasks/{htid}/log", s.hubLog)
	mux.HandleFunc("GET /api/hub/tasks/{htid}/file", s.hubTaskFile)
	mux.HandleFunc("PUT /api/hub/tasks/{htid}/file", s.hubTaskFile)
	mux.HandleFunc("GET /api/hub/tasks/{htid}/events", s.hubEvents)
	mux.HandleFunc("POST "+dispatch.PermissionPath, s.hubPermission)
	mux.HandleFunc("GET "+dispatch.ScanPath, s.hubScan)
	mux.HandleFunc("POST /api/hub/github/{hpid}", s.hubGitHub)
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
		writeError(w, http.StatusNotImplemented, "this runner can't update itself")
		return
	}
	if n := len(sv.data.ListTodos("", model.StatusInProgress, model.StatusMerging)); n > 0 {
		writeError(w, http.StatusConflict, fmt.Sprintf("%d tasks are still running", n))
		return
	}
	if err := s.SelfUpdate(r.Context(), req.Version); err != nil {
		slog.Error("runner: update", "version", req.Version, "err", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"version": req.Version})
}

var errNotLinked = errors.New(dispatch.NotLinked)

// fromHub is the service for a request from this runner's hub, or answers
// r with 403 and returns false.
func (s *Server) fromHub(w http.ResponseWriter, r *http.Request) (*service, bool) {
	if !auth.FromHub(r.Context()) {
		writeError(w, http.StatusForbidden, "only this runner's hub sends it tasks")
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

// linkedProject is hubProject, logging a refusal when the hub's project hpid
// isn't linked here: what names the request ("a task", "a scan"); an empty
// what refuses quietly. attrs go into the log line.
func (sv *service) linkedProject(hpid, what string, attrs ...any) (model.Project, bool) {
	p, ok := sv.hubProject(hpid)
	if !ok && what != "" {
		slog.Warn("runner: refused "+what+" of a project not linked to it", append([]any{"project", hpid}, attrs...)...)
	}
	return p, ok
}

// hubLinked is linkedProject, answering w with 403 errNotLinked when the
// project isn't linked here.
func (sv *service) hubLinked(w http.ResponseWriter, hpid, what string, attrs ...any) (model.Project, bool) {
	p, ok := sv.linkedProject(hpid, what, attrs...)
	if !ok {
		writeError(w, http.StatusForbidden, errNotLinked.Error())
	}
	return p, ok
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
	if s.HubOrg != 0 && req.OrgID != s.HubOrg {
		slog.Warn("runner: refused a project of another org", "project", hpid, "org", req.OrgID)
		writeError(w, http.StatusForbidden, "this runner works for another organization")
		return
	}
	settings := hubSettings(req, s.hubMergeMode(req))
	// The link finishes even when the hub's request goes away (the browser
	// that asked reloads, say): a clone killed half way leaves a folder with
	// origin set but no valid HEAD, which hubPlace would then adopt on every
	// later link, and fail on. Every step runs under ctx, waiting for another
	// link of the same project included, never under r.Context().
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), hubLinkTimeout)
	defer cancel()
	if p, ok := sv.hubProject(hpid); ok {
		p, err := s.relinkHubProject(ctx, sv, p, req.WorkBranch, settings)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, dispatch.Linked{ProjectID: p.ID, DefaultBranch: p.DefaultBranch, Path: p.Path})
		return
	}
	cloneURL, err := s.hubCloneURL(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	place, linked, release, err := s.reserveHubLink(ctx, sv, hpid, hubOrgFolder(req), req, cloneURL)
	if err != nil {
		writeErr(w, err)
		return
	}
	if linked != nil { // linked while this waited
		writeJSON(w, http.StatusOK, dispatch.Linked{ProjectID: linked.ID, DefaultBranch: linked.DefaultBranch, Path: linked.Path})
		return
	}
	defer release()
	id := store.NewID()
	if place.Own != nil {
		id = place.Own.ID
	}
	branch, err := s.cloneHubProject(ctx, id, place, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	p, status, err := s.storeHubProject(ctx, sv, id, hpid, place, req, branch, settings)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.followWorkBranch(ctx, sv, p, req.WorkBranch); err != nil {
		writeErr(w, err)
		return
	}
	slog.Info("runner: hub linked a project", "name", p.Name, "path", p.Path, "checkout", p.HubCheckout)
	writeJSON(w, status, dispatch.Linked{ProjectID: p.ID, DefaultBranch: p.DefaultBranch, Path: p.Path, Adopted: place.Adopt})
}

// hubMergeMode is how the tasks of the hub's project req land here: pull
// requests only when asked for, and a runner with no gh to open them,
// unless the hub's project set it, merges and pushes instead.
func (s *Server) hubMergeMode(req dispatch.Link) string {
	if req.MergeMode == model.MergePR && (req.MergeModeSet || s.Git.HasGh()) {
		return model.MergePR
	}
	return model.MergeLocal
}

// hubSettings sets on a project what the hub's project req decides: how its
// tasks land, its org's folder, its commands and its review settings.
func hubSettings(req dispatch.Link, mode string) func(*model.Project) {
	folder := hubOrgFolder(req)
	return func(x *model.Project) {
		x.TaskInstructions, x.MergeMode, x.HubOrgFolder = req.TaskInstructions, mode, folder
		x.Agent = req.Agent
		// The hub's commands.
		x.SetupCommand, x.TestCommand, x.RunCommand = req.SetupCommand, req.TestCommand, req.RunCommand
		// Where it deploys: the hub sends the commands this runner may not
		// run empty. An older hub sends one deploy command, "production".
		x.Environments, x.DeployCommand, x.DeployRunsOn = model.CloneEnvironments(req.Environments), req.DeployCommand, slices.Clone(req.DeployRunsOn)
		x.MigrateDeploy()
		if k, err := checkPreviewKind(req.PreviewKind); err == nil {
			x.PreviewKind, x.PreviewCommand = k, strings.TrimSpace(req.PreviewCommand)
		}
		// Those it must not run: the steps that would fail, saying why.
		x.HeldCommands = nil
		for kind, why := range req.Held {
			env, isEnv := strings.CutPrefix(kind, model.CmdDeploy+":")
			if !slices.Contains(model.CommandKinds, kind) && !(isEnv && model.ValidEnvName(env)) {
				continue
			}
			if x.HeldCommands == nil {
				x.HeldCommands = map[string]string{}
			}
			x.HeldCommands[kind] = textutil.Clip(why, 500)
			x.SetCommand(kind, "")
			for i, e := range x.Environments {
				if isEnv && e.Name == env {
					x.Environments[i].ClearCommands()
				}
			}
		}
		// The hub's review settings; an older hub that doesn't send them
		// leaves the runner's own.
		if req.AutoApprove != nil {
			x.AutoApprove = *req.AutoApprove
		}
		if req.CloseNoChanges != nil {
			x.CloseNoChanges = *req.CloseNoChanges
		}
		if req.MaxAutoFixes != nil {
			x.MaxAutoFixes = max(0, *req.MaxAutoFixes)
		}
	}
}

// relinkHubProject links again the hub's project p, already linked here:
// it takes the hub's settings (saving only when they change), moves the
// clone to its org's folder and follows the work branch.
func (s *Server) relinkHubProject(ctx context.Context, sv *service, p model.Project, workBranch string, settings func(*model.Project)) (model.Project, error) {
	want := p
	settings(&want)
	if len(want.HeldCommands) == 0 && len(p.HeldCommands) == 0 {
		want.HeldCommands = p.HeldCommands // nil and empty are the same
	}
	if !reflect.DeepEqual(want, p) {
		var err error
		if p, err = sv.data.UpdateProject(p.ID, func(x *model.Project) error { settings(x); return nil }); err != nil {
			return p, err
		}
	}
	// A clone from before clones went in the org's folder moves there now,
	// unless it is busy; the hub gets its new path either way.
	p = s.moveHubClone(ctx, sv, p)
	return p, s.followWorkBranch(ctx, sv, p, workBranch)
}

// hubCloneURL is what this runner clones for the hub's project req: the
// project's own clone URL, else its GitHub repository over this runner's
// protocol. It checks both remotes and that this runner has a projects root.
func (s *Server) hubCloneURL(req dispatch.Link) (string, error) {
	if err := gitops.CheckRemote(req.RemoteURL); err != nil {
		return "", fmt.Errorf("%w: %w", errBadRequest, err)
	}
	cloneURL := gitops.CloneRemote(req.RemoteURL, req.CloneURL, s.GitHubProtocol)
	if err := gitops.CheckRemote(cloneURL); err != nil {
		return "", fmt.Errorf("%w: %w", errBadRequest, err)
	}
	if s.ProjectRoot == "" {
		return "", fmt.Errorf("%w: this runner has no projects root (-projects)", errBadRequest)
	}
	return cloneURL, nil
}

// reserveHubLink picks and reserves the folder for the hub's project hpid,
// under hubLinks, first waiting for any other link of the same project. If
// that link linked it, linked is the project and there is nothing to
// release; else release, called once the project is stored (or the link
// failed), frees the reservation. The clone and the rest run without
// hubLinks, so one large clone doesn't hold up other links.
func (s *Server) reserveHubLink(ctx context.Context, sv *service, hpid, folder string, req dispatch.Link, cloneURL string) (place hubPlacement, linked *model.Project, release func(), err error) {
	for {
		hubLinks.Lock()
		if p, ok := sv.hubProject(hpid); ok {
			hubLinks.Unlock()
			return hubPlacement{}, &p, nil, nil
		}
		done, busy := hubLinking[hpid]
		if !busy {
			break
		}
		// The same project is being linked: wait for it, then look again.
		hubLinks.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return hubPlacement{}, nil, nil, ctx.Err()
		}
	}
	defer hubLinks.Unlock()
	place, err = s.hubPlace(ctx, sv, hpid, folder, req.Name, req.RemoteURL, cloneURL)
	if err != nil {
		return hubPlacement{}, nil, nil, err
	}
	done := make(chan struct{})
	hubLinking[hpid], hubReserved[place.Path] = done, true
	return place, nil, func() {
		hubLinks.Lock()
		delete(hubLinking, hpid)
		delete(hubReserved, place.Path)
		close(done)
		hubLinks.Unlock()
	}, nil
}

// cloneHubProject clones the hub's project req into place.Path, unless
// place adopts a clone that is there, and gives an empty repository a
// first commit on its branch, pushed. It returns that branch.
func (s *Server) cloneHubProject(ctx context.Context, id string, place hubPlacement, req dispatch.Link) (string, error) {
	path := place.Path
	if !place.Adopt {
		// A remote this computer can't read says so, rather than with
		// git's exit 128 ("could not read Username", "Permission denied").
		// With -github-protocol=auto, a GitHub repository SSH can't read is
		// cloned over HTTPS with gh's login; that URL is the clone's origin.
		cloneURL, err := s.Git.PickCloneRemote(ctx, id, s.machineName(), req.RemoteURL, req.CloneURL, s.GitHubProtocol)
		if err != nil {
			return "", fmt.Errorf("%w: %w", errBadRequest, err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := s.Git.Clone(ctx, id, cloneURL, path); err != nil {
			// hubPlace picked a folder that didn't exist: what the failed
			// clone left there would look like a clone of this remote.
			_ = os.RemoveAll(path)
			return "", fmt.Errorf("%w: %w", errBadRequest, err)
		}
	}
	branch := s.hubBranch(ctx, id, path, req.DefaultBranch)
	seeded, err := s.Git.SeedRepo(ctx, id, path, branch) // an empty remote gets a first commit
	if err != nil {
		return "", err
	}
	if seeded {
		// on branch, published so the other runners clone the same one
		if err := s.Git.PushUpstream(ctx, id, path, branch); err != nil {
			slog.Warn("runner: pushing the first commit of an empty repository", "path", path, "branch", branch, "err", err)
		}
	}
	return branch, nil
}

// storeHubProject saves the hub's project hpid, cloned or adopted at
// place.Path: the runner's own project in that folder becomes the hub's
// (200), else a new project is made (201).
func (s *Server) storeHubProject(ctx context.Context, sv *service, id, hpid string, place hubPlacement, req dispatch.Link, branch string, settings func(*model.Project)) (model.Project, int, error) {
	how := model.HubCloned
	if place.Adopt {
		how = model.HubAdopted
	}
	remote := s.Git.RemoteURL(ctx, id, place.Path)
	link := func(x *model.Project) {
		x.HubProjectID, x.HubCheckout = hpid, how
		x.DefaultBranch, x.RemoteURL = branch, remote
		x.MergeModeSet = false
		settings(x)
	}
	if place.Own != nil {
		p, err := sv.data.UpdateProject(id, func(x *model.Project) error { link(x); return nil })
		return p, http.StatusOK, err
	}
	np := model.Project{ID: id, Name: req.Name, Path: place.Path, MaxAutoFixes: model.DefaultMaxAutoFixes, TagFormat: release.DefaultFormat}
	link(&np)
	p, err := sv.data.CreateProject(np)
	return p, http.StatusCreated, err
}

// hubLinks guards picking folders (links and moves) and hubReserved and
// hubLinking. It is held only while a folder is picked, never across a
// clone or other network work.
var hubLinks sync.Mutex

// hubReserved are the folders links in progress have picked, so no other
// link or move picks them too before their project is stored.
var hubReserved = map[string]bool{}

// hubLinking are the hub's projects being linked now, each with a channel
// closed when its link finishes, so a second link of one waits for it.
var hubLinking = map[string]chan struct{}{}

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
		if !fsutil.Within(root, path) || path == dir || filepath.Dir(path) != dir {
			return hubPlacement{}, fmt.Errorf("%w: bad project name", errBadRequest)
		}
		if hubReserved[path] {
			continue // another link in progress picked it
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
		return projectRoot != "" && fsutil.Within(hub, p.Path) && filepath.Clean(p.Path) != hub
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
	p, ok := sv.hubLinked(w, r.PathValue("hpid"), "a moved origin", "branch", req.Branch)
	if !ok {
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
	_, t, ok := s.hubTaskFor(w, r)
	if !ok {
		return
	}
	var req dispatch.PermissionAnswer
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if s.Perms == nil {
		writeErr(w, fmt.Errorf("%w: no such task from the hub", store.ErrNotFound))
		return
	}
	id := r.PathValue("id")
	if pr, ok := s.Perms.Get(id); !ok || pr.TodoID != t.ID {
		writeErr(w, fmt.Errorf("%w: that permission request was answered or has ended", store.ErrNotFound))
		return
	}
	if err := s.Perms.DecideAnswers(id, req.Decision, req.Message, req.Answers); err != nil {
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
	p, ok := sv.hubLinked(w, req.HubProjectID, "a task", "task", req.HubTodoID)
	if !ok {
		return
	}
	if req.HubTodoID == "" {
		writeErr(w, fmt.Errorf("%w: hub_todo_id is required", errBadRequest))
		return
	}
	// Whose work: another member's only on a shared runner (hubshared.go).
	shared, err := s.refuseOthers(req.For, "task "+req.HubTodoID)
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	// Too full to build: the task waits on the hub, its log saying why,
	// rather than failing halfway with a write error.
	if s.DiskRoom != nil {
		if err := s.DiskRoom(req.Disk); err != nil {
			slog.Warn("runner: not starting a task: disk nearly full", "task", req.HubTodoID, "err", err)
			writeError(w, http.StatusInsufficientStorage, err.Error())
			return
		}
	}
	// Its files came first (hubUpload): the notes name this runner's copies.
	atts, fix, err := sv.hubAttachments(req.Attachments)
	if err != nil {
		writeErr(w, err)
		return
	}
	notes := make([]string, len(req.Feedback))
	for i, f := range req.Feedback {
		notes[i] = fix(f)
	}
	req.Feedback = notes
	if cur, ok := sv.hubTodo(req.HubTodoID); ok && (req.Fresh || cur.ProjectID != p.ID) {
		// Start over: the old copy goes, with its worktree.
		if err := sv.removeTodo(r.Context(), cur.ID); err != nil {
			writeErr(w, err)
			return
		}
	}
	if cur, ok := sv.hubTodo(req.HubTodoID); ok {
		var done *dispatch.TaskState // finished here: not started again
		t, err := sv.data.UpdateTodo(cur.ID, func(x *model.Todo) error {
			done = nil
			if x.Status == model.StatusMerged && !req.TakesDone {
				return fmt.Errorf("%w: %s", errBadRequest, dispatch.AlreadyMerged)
			}
			if finished(x.Status, req.Rejoin) {
				s := dispatch.StateOf(*x)
				done = &s
				return errSkipUpdate
			}
			x.Model = req.Model                   // the hub's task may have changed model since
			x.SharedWork = x.SharedWork || shared // its session is in the shared config folder once it ran there
			if n := len(req.Feedback); n > x.HubNotes {
				x.Feedback = append(x.Feedback, req.Feedback[x.HubNotes:]...)
				x.HubNotes = n
			}
			for _, a := range atts {
				if !slices.ContainsFunc(x.Attachments, func(b model.Attachment) bool { return b.ID == a.ID }) {
					x.Attachments = append(x.Attachments, a)
				}
			}
			switch x.Status {
			case model.StatusReview, model.StatusFailed, model.StatusDraft:
				x.Status, x.Error, x.Paused = model.StatusQueued, "", false
				x.AutoFixes, x.AutoMerge, x.SentBack = 0, false, ""
			}
			if x.Status == model.StatusQueued {
				x.StartNow = req.StartNow
			}
			return nil
		})
		if done != nil {
			slog.Info("runner: the hub sent a task finished here; told it where it stands", "task", req.HubTodoID, "status", done.Status)
			writeJSON(w, http.StatusOK, dispatch.Accepted{ID: cur.ID, Resumed: true, Done: done})
			return
		}
		if err != nil {
			writeErr(w, err)
			return
		}
		s.Todos.Kick()
		writeJSON(w, http.StatusOK, dispatch.Accepted{ID: t.ID, Resumed: true})
		return
	}
	// The task it builds on is this runner's copy of the hub's, if it ran
	// here; otherwise it starts fresh.
	var buildsOn string
	if parent, ok := sv.hubTodo(req.BuildsOn); ok && parent.ProjectID == p.ID {
		buildsOn = parent.ID
	}
	// Queued from the start: a report caught between creating a draft and
	// queueing it would tell the hub the task was canceled here.
	t, err := sv.data.CreateQueuedTodo(model.Todo{
		ProjectID: p.ID, Title: req.Title, Prompt: req.Prompt, Model: req.Model, Feedback: req.Feedback,
		HubTodoID: req.HubTodoID, HubNotes: len(req.Feedback), StartNow: req.StartNow, BuildsOn: buildsOn,
		SharedWork:  shared,
		Attachments: atts,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Todos.Kick()
	writeJSON(w, http.StatusCreated, dispatch.Accepted{ID: t.ID})
}

// errSkipUpdate leaves a todo as it is (see hubTask).
var errSkipUpdate = errors.New("nothing to change")

// finished reports whether a hub's task in status s here is done with, so
// the hub sending it again doesn't start it: merged or merging, or, when
// the hub only sends it back to rejoin a run (dispatch.Task.Rejoin), also
// in review, failed or canceled. Otherwise the hub sends a task in review
// or failed back to Claude.
func finished(s model.TodoStatus, rejoin bool) bool {
	switch s {
	case model.StatusMerged, model.StatusMerging:
		return true
	case model.StatusReview, model.StatusFailed, model.StatusDraft:
		return rejoin
	}
	return false
}

// hubTaskFor is the task from the hub that r names ({htid}), with the
// service it is in; else it answers r (403 for a caller that isn't the
// hub, 404 for a task the hub didn't send) and returns false.
func (s *Server) hubTaskFor(w http.ResponseWriter, r *http.Request) (*service, model.Todo, bool) {
	sv, ok := s.fromHub(w, r)
	if !ok {
		return nil, model.Todo{}, false
	}
	t, ok := sv.hubTodo(r.PathValue("htid"))
	if !ok {
		writeErr(w, fmt.Errorf("%w: no such task from the hub", store.ErrNotFound))
		return nil, model.Todo{}, false
	}
	return sv, t, true
}

// hubApprove handles POST /api/hub/tasks/{htid}/approve.
func (s *Server) hubApprove(w http.ResponseWriter, r *http.Request) {
	sv, t, ok := s.hubTaskFor(w, r)
	if !ok {
		return
	}
	t, err := sv.approve(t.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, dispatch.StateOf(t))
}

// hubMergePR handles POST /api/hub/tasks/{htid}/merge-pr. The hub checked
// who may merge; the merge runs with this runner's gh, in its clone.
func (s *Server) hubMergePR(w http.ResponseWriter, r *http.Request) {
	sv, t, ok := s.hubTaskFor(w, r)
	if !ok {
		return
	}
	var req dispatch.MergePR
	if err := decodeOptional(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	res, err := sv.mergePR(r.Context(), t, req.Method)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// hubRevert handles POST /api/hub/tasks/{htid}/revert.
func (s *Server) hubRevert(w http.ResponseWriter, r *http.Request) {
	sv, t, ok := s.hubTaskFor(w, r)
	if !ok {
		return
	}
	if err := sv.revert(r.Context(), t); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// hubRunTests handles POST /api/hub/tasks/{htid}/test. Its tests are a
// project command: someone else's run here only on a shared runner, with
// its API key alone.
func (s *Server) hubRunTests(w http.ResponseWriter, r *http.Request) {
	sv, t, ok := s.hubTaskFor(w, r)
	if !ok {
		return
	}
	shared, err := s.requestShared(r, "runs a task's tests")
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	if err := sv.runTests(r.Context(), t, shared); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// hubOpenWorktree handles POST /api/hub/tasks/{htid}/open.
func (s *Server) hubOpenWorktree(w http.ResponseWriter, r *http.Request) {
	_, t, ok := s.hubTaskFor(w, r)
	if !ok {
		return
	}
	if err := s.openWorktree(t); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// hubCancel handles POST /api/hub/tasks/{htid}/cancel.
func (s *Server) hubCancel(w http.ResponseWriter, r *http.Request) {
	sv, t, ok := s.hubTaskFor(w, r)
	if !ok {
		return
	}
	if _, _, err := sv.cancel(t.ID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// hubForget handles DELETE /api/hub/tasks/{htid}: the hub forgot the task.
// It stops and stays as the runner's own; deleting it there rejected it, so
// its pull request closes.
func (s *Server) hubForget(w http.ResponseWriter, r *http.Request) {
	sv, t, ok := s.hubTaskFor(w, r)
	if !ok {
		return
	}
	s.Todos.Cancel(t.ID)
	s.rejectPR(t)
	if _, err := sv.data.UpdateTodo(t.ID, func(x *model.Todo) error { x.HubTodoID = ""; return nil }); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// hubDiff handles GET /api/hub/tasks/{htid}/diff.
func (s *Server) hubDiff(w http.ResponseWriter, r *http.Request) {
	sv, t, ok := s.hubTaskFor(w, r)
	if !ok {
		return
	}
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
}

// hubFiles handles GET /api/hub/tasks/{htid}/files: the files the task
// changes, one per line, for the hub's active_work.
func (s *Server) hubFiles(w http.ResponseWriter, r *http.Request) {
	sv, t, ok := s.hubTaskFor(w, r)
	if !ok {
		return
	}
	files, err := sv.workFiles(r.Context(), t)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeText(w, strings.Join(files, "\n"))
}

// hubLog handles GET /api/hub/tasks/{htid}/log.
func (s *Server) hubLog(w http.ResponseWriter, r *http.Request) {
	sv, t, ok := s.hubTaskFor(w, r)
	if !ok {
		return
	}
	log, err := sv.log(t.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeText(w, log)
}

// hubEvents handles GET /api/hub/tasks/{htid}/events.
func (s *Server) hubEvents(w http.ResponseWriter, r *http.Request) {
	_, t, ok := s.hubTaskFor(w, r)
	if !ok {
		return
	}
	evs, err := s.Todos.Log.Events(t.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, evs)
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
		writeError(w, http.StatusServiceUnavailable, "this runner doesn't run scans")
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
	p, ok := sv.linkedProject(req.Project, "a scan")
	if !ok {
		send(scan.Message{Error: errNotLinked.Error()})
		return
	}
	if err := req.Job.Check(); err != nil {
		send(scan.Message{Error: err.Error()})
		return
	}
	var forUsers []int64
	if req.Job.For != 0 {
		forUsers = []int64{req.Job.For}
	}
	shared, err := s.refuseOthers(forUsers, "scan "+req.Job.Template.Name)
	if err != nil {
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
	if shared {
		job.Shared = s.Shared.Dir
	}
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
	p, ok := sv.hubLinked(w, r.PathValue("hpid"), "a GitHub call")
	if !ok {
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
	if rl := s.Git.RateLimit(); rl != nil && req.Op != dispatch.GitHubRerun && req.Op != dispatch.GitHubFailedLog {
		// The hub's build polls wait out this computer's rate limit (it
		// reads it from the answer); a re-run a person asked for tries.
		writeErr(w, rl)
		return
	}
	switch req.Op {
	case dispatch.GitHubRuns:
		out.Runs, err = s.Git.Runs(ctx, p.ID, p.Path, req.Ref, limit)
	case dispatch.GitHubCommitRuns:
		out.Runs, err = s.Git.RunsForCommit(ctx, p.ID, p.Path, req.Ref, limit)
	case dispatch.GitHubPushRuns:
		out.Runs, err = s.Git.PushRuns(ctx, p.ID, p.Path, limit)
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
