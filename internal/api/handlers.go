package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/release"
	"github.com/klahr/dash/internal/runner"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
	"github.com/klahr/dash/internal/usage"
)

// ---------------------------------------------------------------- projects

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	// Active lets the sidebar show which projects have Claude running or work
	// waiting for review; events keep it current from there.
	type item struct {
		model.Project
		Active map[string]model.TodoStatus `json:"active,omitempty"`
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	ps := sv.data.ListProjects()
	if auth.FromHub(r.Context()) {
		// The hub has its own copy of the projects it linked to this runner.
		ps = slices.DeleteFunc(ps, func(p model.Project) bool { return p.LinkedFromHub() })
	}
	out := make([]item, len(ps))
	idx := make(map[string]int, len(ps))
	for i, p := range ps {
		out[i].Project = p
		idx[p.ID] = i
	}
	// One pass over the store for all projects, not one per project.
	for _, t := range sv.data.ListTodos("", model.StatusInProgress, model.StatusReview) {
		i, ok := idx[t.ProjectID]
		if !ok {
			continue
		}
		if out[i].Active == nil {
			out[i].Active = map[string]model.TodoStatus{}
		}
		out[i].Active[t.ID] = t.Status
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.project(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, p)
}

var colorRe = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// projectRequest is the body of POST /api/projects.
type projectRequest struct {
	Name           string   `json:"name"`
	Path           string   `json:"path"` // optional: adopt an existing directory/repo
	DefaultBranch  string   `json:"default_branch"`
	RunCommand     string   `json:"run_command"`
	TestCommand    string   `json:"test_command"`
	DeployCommand  string   `json:"deploy_command"`
	SetupCommand   string   `json:"setup_command"`
	PreviewKind    string   `json:"preview_kind"`    // "web" (default) | "terminal"
	PreviewCommand string   `json:"preview_command"` // terminal previews: runs in the worktree
	CopyFiles      []string `json:"copy_files"`
	MaxAutoFixes   *int     `json:"max_auto_fixes"`
	AutoPush       bool     `json:"auto_push"`
	MergeMode      string   `json:"merge_mode"`
	MaxRunMinutes  int      `json:"max_run_minutes"`
	MaxTurns       int      `json:"max_turns"`
	// GitHub names a repository (REPO or OWNER/REPO): with Clone it is
	// cloned (it must exist); without, it is created and pushed (it must not).
	GitHub       string `json:"github"`
	Clone        bool   `json:"clone"`
	GitHubPublic bool   `json:"github_public"`
	// Existing adds a repository already cloned at Path: it must be one
	// (and not a project yet), and the name defaults to its folder's.
	Existing bool `json:"existing"`
	// LocalOnly makes a new repository with no remote, in a new folder
	// (no Path, GitHub or Existing): deleting the project deletes it.
	LocalOnly bool `json:"local_only"`
	// On the hub only, which keeps no projects on its own disk: a
	// shared project is the remote (RemoteURL, or GitHub's OWNER/REPO)
	// and the runners it is linked to, which clone it.
	RemoteURL string   `json:"remote_url"`
	Runners   []string `json:"runners"`
	// CloneURL is the URL the runners clone as it is (see
	// model.Project.CloneURL); empty, they pick it from RemoteURL.
	CloneURL string `json:"clone_url"`
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var req projectRequest
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	kind, err := checkPreviewKind(req.PreviewKind)
	if err != nil {
		writeErr(w, err)
		return
	}
	req.PreviewKind, req.PreviewCommand = kind, strings.TrimSpace(req.PreviewCommand)
	// The value is checked before anything is cloned or created; whether
	// pull requests are possible once the remote is known.
	if err := checkMergeMode(req.MergeMode, ""); err != nil && req.MergeMode != model.MergePR {
		writeErr(w, err)
		return
	}
	if req.DefaultBranch == "" {
		req.DefaultBranch = "main"
	}
	if s.hub() {
		p, err := sv.createHubProject(req)
		if err != nil {
			writeErr(w, err)
			return
		}
		s.Todos.Kick()
		writeJSON(w, http.StatusCreated, p)
		return
	}
	path, err := sv.prepareProjectPath(&req)
	if err != nil {
		writeErr(w, err)
		return
	}
	req.Path = path
	id := store.NewID()
	ctx := r.Context()
	if err := s.setupGitHubRemote(ctx, id, &req); err != nil {
		writeErr(w, err)
		return
	}
	req.DefaultBranch = s.adoptedBranch(ctx, id, req.Path, req.DefaultBranch)
	// Asking for pull requests is only possible when the repo already has
	// a remote; without asking, GitHub projects get them (DefaultMergeMode).
	np, err := newProjectFromRequest(req, model.Project{
		ID: id, Path: req.Path, RemoteURL: s.Git.RemoteURL(ctx, id, req.Path),
		AutoPush: req.AutoPush, LocalOnly: req.LocalOnly,
	}, s.Git.HasGh())
	if err != nil {
		writeErr(w, err)
		return
	}
	p, err := sv.data.CreateProject(np)
	if err != nil {
		writeErr(w, err)
		return
	}
	if req.Existing || req.Clone {
		p = sv.adoptFavicon(p)
	}
	writeJSON(w, http.StatusCreated, p)
}

// newProjectFromRequest is the project req asks for: base (its ID, remote
// and the fields only one kind of project has) with req's settings, their
// defaults filled in, and its merge mode checked against base's remote. gh
// is whether this Dash can open pull requests (see DefaultMergeMode).
func newProjectFromRequest(req projectRequest, base model.Project, gh bool) (model.Project, error) {
	if err := checkMergeMode(req.MergeMode, base.RemoteURL); err != nil {
		return model.Project{}, err
	}
	np := base
	np.Name = strings.TrimSpace(req.Name)
	np.DefaultBranch = cmp.Or(req.DefaultBranch, "main")
	np.RunCommand, np.TestCommand, np.DeployCommand, np.SetupCommand = req.RunCommand, req.TestCommand, req.DeployCommand, req.SetupCommand
	np.PreviewKind, np.PreviewCommand = req.PreviewKind, req.PreviewCommand
	np.CopyFiles = cleanList(req.CopyFiles)
	np.MaxAutoFixes = model.DefaultMaxAutoFixes
	if req.MaxAutoFixes != nil {
		np.MaxAutoFixes = max(0, *req.MaxAutoFixes)
	}
	np.MaxRunMinutes, np.MaxTurns = max(0, req.MaxRunMinutes), max(0, req.MaxTurns)
	np.TagFormat = release.DefaultFormat
	np.MergeMode = np.DefaultMergeMode(gh)
	if req.MergeMode != "" && req.MergeMode != np.MergeMode {
		np.MergeMode, np.MergeModeSet = req.MergeMode, true
	}
	return np, nil
}

// createHubProject creates a project on the hub, which keeps none on its
// own disk: the remote and the runners that clone it.
func (sv *service) createHubProject(req projectRequest) (model.Project, error) {
	remote := strings.TrimSpace(req.RemoteURL)
	if gh := strings.Trim(strings.TrimSpace(req.GitHub), "/"); remote == "" && strings.Count(gh, "/") == 1 {
		remote = "https://github.com/" + strings.TrimSuffix(gh, ".git") + ".git"
	}
	if req.Path != "" || req.Existing || remote == "" || len(req.Runners) == 0 {
		return model.Project{}, errHubHasNoProjects
	}
	if err := gitops.CheckRemote(remote); err != nil {
		return model.Project{}, errors.Join(errBadRequest, err)
	}
	cloneURL, err := checkCloneURL(req.CloneURL)
	if err != nil {
		return model.Project{}, err
	}
	if strings.TrimSpace(req.Name) == "" {
		return model.Project{}, fmt.Errorf("%w: name is required", errBadRequest)
	}
	base := model.Project{ID: store.NewID(), OrgID: sv.c.Org, RemoteURL: remote, CloneURL: cloneURL}
	if base.Runners, err = sv.linkRunners(base, req.Runners); err != nil {
		return model.Project{}, err
	}
	np, err := newProjectFromRequest(req, base, false)
	if err != nil {
		return model.Project{}, err
	}
	return sv.data.CreateProject(np)
}

// prepareProjectPath checks where a new local project goes and returns its
// folder: req.Path, an existing clone, or a new folder named after it. It
// fills in req.Name from an existing clone's folder.
func (sv *service) prepareProjectPath(req *projectRequest) (string, error) {
	req.Path = strings.TrimSpace(req.Path)
	if req.LocalOnly && (req.Path != "" || req.Existing || strings.TrimSpace(req.GitHub) != "") {
		return "", fmt.Errorf("%w: a local-only project is a new repository in a new folder: no path, existing clone or GitHub", errBadRequest)
	}
	if req.Existing {
		if req.Path == "" {
			return "", fmt.Errorf("%w: the path of the existing clone is required", errBadRequest)
		}
		if strings.TrimSpace(req.GitHub) != "" {
			return "", fmt.Errorf("%w: an existing clone keeps its own remote; leave GitHub empty", errBadRequest)
		}
		if strings.TrimSpace(req.Name) == "" {
			req.Name = filepath.Base(store.CleanPath(req.Path))
		}
	}
	if strings.TrimSpace(req.Name) == "" {
		return "", fmt.Errorf("%w: name is required", errBadRequest)
	}
	newFolder := req.Path == ""
	path, err := sv.projectPath(req.Name, req.Path)
	if err != nil {
		return "", err
	}
	if newFolder && !emptyOrMissing(path) {
		// A folder named after the project is made for it: never one that
		// holds something else (another project's clone, say), which
		// deleting this project with its folder would take along.
		return "", fmt.Errorf("%w: %s already exists; pick another name, or add it as an existing clone", errBadRequest, path)
	}
	if req.Existing {
		if err := sv.checkExistingRepo(path); err != nil {
			return "", err
		}
	}
	return path, nil
}

// setupGitHubRemote makes req.Path a repository: cloned from GitHub (with
// Clone), or initialized and, given a GitHub name, pushed to a new
// repository there.
func (s *Server) setupGitHubRemote(ctx context.Context, id string, req *projectRequest) error {
	req.GitHub = strings.TrimSpace(req.GitHub)
	createRemote := false
	if req.GitHub != "" {
		gh, exists, err := s.Git.LookupGitHubRepo(ctx, req.GitHub)
		switch {
		case err != nil:
			return err
		case exists && !req.Clone:
			return fmt.Errorf("%w: %s already exists on GitHub; clone it instead", errBadRequest, gh.Name)
		case !exists && req.Clone:
			return fmt.Errorf("%w: %s not found on GitHub", errBadRequest, req.GitHub)
		case exists:
			if err := s.Git.CloneGitHubRepo(ctx, id, gh.Name, req.Path); err != nil {
				return errors.Join(errBadRequest, err)
			}
		default:
			if s.Git.RemoteURL(ctx, id, req.Path) != "" {
				return fmt.Errorf("%w: %s already has an origin remote", errBadRequest, req.Path)
			}
			createRemote = true
		}
	}
	if err := s.Git.InitRepo(ctx, id, req.Path, req.DefaultBranch); err != nil {
		return err
	}
	if createRemote {
		if _, err := s.Git.CreateGitHubRepo(ctx, id, req.Path, req.GitHub, !req.GitHubPublic); err != nil {
			return err
		}
	}
	return nil
}

// checkPreviewKind validates a project's preview_kind and returns it as
// stored: "" for web previews (the default), model.PreviewTerminal.
// Terminal previews need no run_command; web ones do, when started.
func checkPreviewKind(k string) (string, error) {
	switch normPreviewKind(k) {
	case model.PreviewWeb:
		return "", nil
	case model.PreviewTerminal:
		return model.PreviewTerminal, nil
	}
	return "", fmt.Errorf("%w: preview_kind must be %q or %q", errBadRequest, model.PreviewWeb, model.PreviewTerminal)
}

// normPreviewKind is k with "" meaning web.
func normPreviewKind(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	if k == "" {
		return model.PreviewWeb
	}
	return k
}

// maxTaskInstructions caps a project's extra instructions for Claude: they
// go on every task's command line.
const maxTaskInstructions = 32 << 10

// projectUpdate is a change to a project's settings; nil fields stay.
type projectUpdate struct {
	Name          *string `json:"name"`
	RunCommand    *string `json:"run_command"`
	TestCommand   *string `json:"test_command"`
	DeployCommand *string `json:"deploy_command"`
	SetupCommand  *string `json:"setup_command"`
	// PreviewKind is "web" (run_command, proxied) or "terminal"
	// (preview_command, or a shell, in a terminal in the worktree).
	PreviewKind    *string   `json:"preview_kind"`
	PreviewCommand *string   `json:"preview_command"`
	CopyFiles      *[]string `json:"copy_files"`
	AllowedTools   *[]string `json:"allowed_tools"`
	ChatMode       *string   `json:"chat_permission_mode"`
	ReviewPrompt   *string   `json:"review_prompt"`
	Instructions   *string   `json:"task_instructions"`
	MaxAutoFixes   *int      `json:"max_auto_fixes"`
	AutoPush       *bool     `json:"auto_push"`
	AutoApprove    *bool     `json:"auto_approve"`
	MergeMode      *string   `json:"merge_mode"`
	InvestigateCI  *bool     `json:"investigate_ci"`
	MaxRunMinutes  *int      `json:"max_run_minutes"`
	MaxTurns       *int      `json:"max_turns"`
	Color          *string   `json:"color"`
	Icon           *string   `json:"icon"`
	TagFormat      *string   `json:"tag_format"`
	// CloneURL is the hub's project's model.Project.CloneURL ("" clears
	// it). Runners that cloned the project already keep their origin.
	CloneURL *string `json:"clone_url"`
	// Runners links the project to these runners (ids; replaces the list,
	// [] unlinks them all).
	Runners *[]string `json:"runners"`
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req projectUpdate
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, err := sv.updateProject(r.Context(), r.PathValue("pid"), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// updateProject changes a project's settings, for PATCH /api/projects/{pid}
// and the update_project tool.
func (sv *service) updateProject(ctx context.Context, id string, req projectUpdate) (model.Project, error) {
	if req.ChatMode != nil {
		switch *req.ChatMode {
		case "", model.ChatModeAuto, model.ChatModeAcceptEdits:
		default:
			return model.Project{}, fmt.Errorf("%w: chat_permission_mode must be \"\", %q or %q", errBadRequest, model.ChatModeAuto, model.ChatModeAcceptEdits)
		}
	}
	if req.PreviewKind != nil {
		k, err := checkPreviewKind(*req.PreviewKind)
		if err != nil {
			return model.Project{}, err
		}
		req.PreviewKind = &k
	}
	if req.Instructions != nil && len(*req.Instructions) > maxTaskInstructions {
		return model.Project{}, fmt.Errorf("%w: task_instructions is longer than %d KiB", errBadRequest, maxTaskInstructions>>10)
	}
	if req.Color != nil {
		*req.Color = strings.ToLower(strings.TrimSpace(*req.Color))
		if *req.Color != "" && !colorRe.MatchString(*req.Color) {
			return model.Project{}, fmt.Errorf("%w: color must look like #3a7bd5", errBadRequest)
		}
	}
	if req.Icon != nil && *req.Icon != "" {
		uid, name, _ := strings.Cut(*req.Icon, "/")
		if a, err := sv.findUpload(uid); err != nil || a.Name != name || !strings.HasPrefix(a.Type, "image/") {
			return model.Project{}, fmt.Errorf("%w: icon must be an uploaded image", errBadRequest)
		}
	}
	if req.TagFormat != nil {
		f, err := sv.s.checkTagFormat(ctx, strings.TrimSpace(*req.TagFormat))
		if err != nil {
			return model.Project{}, err
		}
		*req.TagFormat = f.String()
	}
	if req.CloneURL != nil {
		if !sv.s.hub() {
			return model.Project{}, fmt.Errorf("%w: clone_url is a hub's project's setting", errBadRequest)
		}
		u, err := checkCloneURL(*req.CloneURL)
		if err != nil {
			return model.Project{}, err
		}
		req.CloneURL = &u
	}
	cur, err := sv.data.GetProject(id)
	if err != nil {
		return cur, err
	}
	remote, gh := cur.RemoteURL, false
	if !sv.s.hub() {
		// Pick up an origin added since (the hub has no checkout to look in).
		remote = sv.s.Git.RemoteURL(ctx, cur.ID, cur.Path)
		if remote == "" && cur.OnRunners() {
			remote = cur.RemoteURL // the checkout may be gone; the runners have theirs
		}
		gh = sv.s.Git.HasGh()
	}
	runners := cur.Runners
	if req.Runners != nil {
		withRemote := cur
		withRemote.RemoteURL = remote
		if runners, err = sv.linkRunners(withRemote, *req.Runners); err != nil {
			return cur, err
		}
	}
	var gone []string
	for _, r := range cur.Runners {
		if !slices.Contains(runners, r) {
			gone = append(gone, r)
		}
	}
	if err := sv.checkUnlink(cur, gone); err != nil {
		return cur, err
	}
	p, err := sv.data.UpdateProject(id, func(p *model.Project) error {
		p.RemoteURL = remote
		set(&p.Name, req.Name)
		set(&p.RunCommand, req.RunCommand)
		set(&p.TestCommand, req.TestCommand)
		set(&p.DeployCommand, req.DeployCommand)
		set(&p.SetupCommand, req.SetupCommand)
		set(&p.PreviewKind, req.PreviewKind)
		if req.PreviewCommand != nil {
			p.PreviewCommand = strings.TrimSpace(*req.PreviewCommand)
		}
		set(&p.AutoPush, req.AutoPush)
		set(&p.AutoApprove, req.AutoApprove)
		set(&p.Color, req.Color)
		set(&p.Icon, req.Icon)
		set(&p.ChatPermissionMode, req.ChatMode)
		set(&p.ReviewPrompt, req.ReviewPrompt)
		if req.Instructions != nil {
			p.TaskInstructions = strings.TrimSpace(*req.Instructions)
		}
		set(&p.TagFormat, req.TagFormat)
		set(&p.CloneURL, req.CloneURL)
		if req.CopyFiles != nil {
			p.CopyFiles = cleanList(*req.CopyFiles)
		}
		if req.AllowedTools != nil {
			p.AllowedTools = cleanList(*req.AllowedTools)
		}
		if req.MaxAutoFixes != nil {
			p.MaxAutoFixes = max(0, *req.MaxAutoFixes)
		}
		if req.MaxRunMinutes != nil {
			p.MaxRunMinutes = max(0, *req.MaxRunMinutes)
		}
		if req.MaxTurns != nil {
			p.MaxTurns = max(0, *req.MaxTurns)
		}
		if req.InvestigateCI != nil {
			v := *req.InvestigateCI
			p.InvestigateCI = &v
		}
		if req.MergeMode != nil {
			if err := checkMergeMode(*req.MergeMode, p.RemoteURL); err != nil {
				return err
			}
			mode := *req.MergeMode
			if mode == "" {
				mode = model.MergeLocal
			}
			if cur := p.MergeMode; mode != cur && (cur != "" || mode != model.MergeLocal) {
				p.MergeModeSet = true // the user's choice, kept from now on
			}
			p.MergeMode = mode
		}
		p.Runners = runners
		if !p.MergeModeSet && p.HubProjectID == "" {
			// The default follows the remote and the runners; a runner's
			// clone lands the way its hub says.
			p.MergeMode = p.DefaultMergeMode(gh)
		}
		if len(runners) > 0 {
			// The runners push to origin themselves (see
			// model.Project.LandsOnOrigin); the hub doesn't push.
			p.AutoPush = false
		}
		return nil
	})
	if err != nil {
		return p, err
	}
	if len(gone) > 0 && sv.s.Dispatch != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			sv.s.Dispatch.Unlink(ctx, p.ID, gone)
		}()
	}
	if req.Runners != nil {
		sv.s.Todos.Kick() // queued tasks may go elsewhere now
	}
	return p, nil
}

func checkMergeMode(mode, remote string) error {
	switch mode {
	case "", model.MergeLocal:
		return nil
	case model.MergePR:
		if remote == "" {
			return fmt.Errorf("%w: pull requests need a GitHub repository; create or connect one first", errBadRequest)
		}
		return nil
	}
	return fmt.Errorf("%w: merge_mode must be \"local\" or \"pr\"", errBadRequest)
}

// deleteProject removes a project from Dash after the user re-enters the
// password. Optionally its folder goes too, and its GitHub repository; the
// GitHub one first, so a gh failure leaves everything else as it was.
func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password     string `json:"password"`
		DeleteFiles  bool   `json:"delete_files"`
		DeleteGitHub bool   `json:"delete_github"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := s.Auth.ConfirmPassword(r, req.Password); err != nil {
		writeErr(w, err)
		return
	}
	p, err := sv.data.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if queue.Busy(sv.data.ListTodos(p.ID)) {
		writeErr(w, queue.ErrProjectBusy)
		return
	}
	// A local-only project's folder is the project: it goes with it.
	req.DeleteFiles = req.DeleteFiles || p.LocalOnly
	if s.hub() && (req.DeleteFiles || req.DeleteGitHub) {
		// The hub keeps no files of its projects, and runs no gh.
		writeErr(w, fmt.Errorf("%w: the hub only forgets the project: its clones stay on the runners, and its GitHub repository on GitHub", errBadRequest))
		return
	}
	if req.DeleteGitHub {
		remote := s.Git.RemoteURL(r.Context(), p.ID, p.Path)
		if remote == "" {
			remote = p.RemoteURL
		}
		if remote == "" {
			writeErr(w, fmt.Errorf("%w: the project has no GitHub repository", errBadRequest))
			return
		}
		if err := s.Git.DeleteGitHubRepo(r.Context(), p.ID, p.Path, remote); err != nil {
			if ce := (*gitops.CmdError)(nil); errors.As(err, &ce) && strings.Contains(ce.Output, "delete_repo") {
				err = fmt.Errorf("%w (run: gh auth refresh -h github.com -s delete_repo)", err)
			}
			writeErr(w, err)
			return
		}
	}
	if s.Procs != nil && !s.hub() {
		for _, proc := range s.Procs.List(p.ID) {
			_ = s.Procs.Stop(proc.ID)
		}
	}
	todos, err := s.Merges.DeleteProject(r.Context(), p.ID, req.DeleteFiles)
	for _, t := range todos {
		if s.Perms != nil {
			s.Perms.Forget(t.ID)
		}
		s.Todos.Log.Remove(t.ID)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pushProject(w http.ResponseWriter, r *http.Request) {
	sv, p, ok := s.project(w, r)
	if !ok {
		return
	}
	if url := s.Git.RemoteURL(r.Context(), p.ID, p.Path); url != p.RemoteURL {
		p, _ = sv.data.UpdateProject(p.ID, func(x *model.Project) error { x.RemoteURL = url; return nil })
	}
	if p.RemoteURL == "" {
		writeErr(w, fmt.Errorf("%w: project has no remote; create one via /github first", errBadRequest))
		return
	}
	if err := s.Merges.PushDefault(r.Context(), p); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"pushed": p.Base()})
}

// remoteStatus reports how the default branch compares with origin's and
// marks the project as selected, so it's fetched every minute. With fetch it
// also fetches now, in the background: the result arrives as a
// remote.updated event; ?force=1 fetches even while backing off after failures.
func (s *Server) remoteStatus(fetch bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, p, ok := s.project(w, r)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, s.Merges.Remote.Select(r.Context(), p, fetch, r.URL.Query().Get("force") == "1"))
	}
}

// pullRemote handles POST /api/projects/{pid}/remote/pull: fetch origin and
// fast-forward the default branch, and the project's folder with it, only
// if that is a fast-forward of a clean checkout; otherwise 409 says why and
// nothing changes. On a hub, ?runner= names the runner whose checkout to
// pull (see viaRunner).
func (s *Server) pullRemote(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.project(w, r)
	if !ok {
		return
	}
	moved, err := s.Merges.Remote.Pull(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"moved": moved, "remote": s.Merges.Remote.Status(p)})
}

// listCheckouts handles GET /api/projects/{pid}/checkouts: the checkouts of
// the project that are behind origin. On a hub, the runners' as they last
// reported them; elsewhere this Dash's own, if it is.
func (s *Server) listCheckouts(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.project(w, r)
	if !ok {
		return
	}
	out := []model.Checkout{}
	switch {
	case s.Dispatch != nil:
		out = s.Dispatch.Checkouts(p.ID)
	case s.Merges != nil && s.Merges.Remote != nil:
		if st := s.Merges.Remote.Status(p); st.Enabled && st.Behind > 0 {
			out = append(out, model.Checkout{Branch: st.Branch, Behind: st.Behind, Ahead: st.Ahead, Stuck: st.Stuck, DashOwned: p.DashOwnsCheckout()})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// unpushed counts the default branch's commits (merged todos) origin doesn't have yet.
func (s *Server) unpushed(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.project(w, r)
	if !ok {
		return
	}
	n, todos := 0, []string{}
	if p.RemoteURL != "" {
		n, _ = s.Git.Unpushed(r.Context(), p.ID, p.Path, p.Base())
		if ids, err := s.Git.UnpushedTodos(r.Context(), p.ID, p.Path, p.Base()); err == nil && ids != nil {
			todos = ids
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": n, "todos": todos})
}

func (s *Server) gitGraph(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.project(w, r)
	if !ok {
		return
	}
	commits, err := s.Git.Graph(r.Context(), p.ID, p.Path, p.Base(), 500)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.fetchTagsSoon(p)
	writeJSON(w, http.StatusOK, map[string]any{"branch": p.Base(), "commits": commits})
}

// ---------------------------------------------------------------- todos

func (s *Server) listTodos(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var statuses []model.TodoStatus
	if st := r.URL.Query().Get("status"); st != "" {
		for _, x := range strings.Split(st, ",") {
			statuses = append(statuses, model.TodoStatus(x))
		}
	}
	if _, err := sv.data.GetProject(r.PathValue("pid")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(sv.data.ListTodos(r.PathValue("pid"), statuses...)))
}

func (s *Server) createTodo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title       string   `json:"title"`
		Prompt      string   `json:"prompt"`
		Priority    *int     `json:"priority"`    // null or absent: unprioritised
		Enqueue     bool     `json:"enqueue"`     // skip Draft
		Attachments []string `json:"attachments"` // upload ids
		DependsOn   []string `json:"depends_on"`  // todo ids that must be merged first
		BuildsOn    string   `json:"builds_on"`   // todo id whose Claude session the first run continues
		Model       string   `json:"model"`       // claude --model; empty: the default
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if strings.TrimSpace(req.Title) == "" && strings.TrimSpace(req.Prompt) == "" {
		writeErr(w, fmt.Errorf("%w: title or prompt is required", errBadRequest))
		return
	}
	atts, err := sv.attachments(req.Attachments)
	if err != nil {
		writeErr(w, err)
		return
	}
	t, err := sv.addTodo(model.Todo{
		ProjectID: r.PathValue("pid"), Title: req.Title, Prompt: req.Prompt, Priority: req.Priority,
		Attachments: atts, DependsOn: req.DependsOn, BuildsOn: req.BuildsOn, Model: req.Model,
	}, req.Enqueue)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// refinePrompt rewrites instructions (often pasted from a chat answer) into a
// cleaner prompt; the form shows the result and nothing is stored.
func (s *Server) refinePrompt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt string `json:"prompt"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeErr(w, fmt.Errorf("%w: prompt is required", errBadRequest))
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	out, err := sv.refine(r.Context(), req.Prompt)
	if errors.Is(err, errNoRefiner) || errors.Is(err, errNoClaudeRunner) || errors.Is(err, tunnel.ErrOffline) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"prompt": out})
}

// splitPrompt proposes how to split a large task into smaller todos; the
// form shows the parts to edit and confirm, and nothing is stored.
func (s *Server) splitPrompt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt string `json:"prompt"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeErr(w, fmt.Errorf("%w: prompt is required", errBadRequest))
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	parts, err := sv.split(r.Context(), req.Prompt)
	if errors.Is(err, errNoSplitter) || errors.Is(err, errNoClaudeRunner) || errors.Is(err, tunnel.ErrOffline) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"parts": parts})
}

func (s *Server) retestTodo(w http.ResponseWriter, r *http.Request) {
	sv, t, ok := s.todo(w, r)
	if !ok {
		return
	}
	if err := sv.runTests(r.Context(), t); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// duplicateTodo copies a todo's instructions into a new draft.
func (s *Server) duplicateTodo(w http.ResponseWriter, r *http.Request) {
	sv, src, ok := s.todo(w, r)
	if !ok {
		return
	}
	title := src.Title
	if title != "" {
		title += " (copy)"
	}
	t, err := sv.data.CreateTodo(model.Todo{ProjectID: src.ProjectID, CreatedBy: sv.c.User, Title: title, Prompt: src.Prompt,
		Priority: src.Priority, Attachments: src.Attachments, Edits: src.Edits, DependsOn: src.DependsOn, BuildsOn: src.BuildsOn, Model: src.Model})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// openTodo opens the todo's worktree in the configured editor on the Dash machine.
func (s *Server) openTodo(w http.ResponseWriter, r *http.Request) {
	sv, t, ok := s.todo(w, r)
	if !ok {
		return
	}
	var err error
	if t.OnRunner() {
		// Its worktree is on its runner: the runner's editor opens it there.
		err = sv.onRunner(r.Context(), t, func(ctx context.Context) error { return s.Dispatch.Open(ctx, t) })
	} else if s.hub() {
		err = fmt.Errorf("%w, so it has no worktree to open", errNotRunYet)
	} else {
		err = s.openWorktree(t)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// openWorktree opens t's worktree in -editor, on this computer.
func (s *Server) openWorktree(t model.Todo) error {
	if s.Editor == "" {
		return fmt.Errorf("%w: no editor configured; start dash with -editor (e.g. -editor code)", errBadRequest)
	}
	if t.Worktree == nil || t.Worktree.Path == "" {
		return fmt.Errorf("%w: this task has no worktree", errBadRequest)
	}
	if err := localrun.Check("editor: " + s.Editor); err != nil {
		return err
	}
	args := append(strings.Fields(s.Editor), t.Worktree.Path)
	cmd := exec.Command(args[0], args[1:]...)
	localrun.Detach(cmd) // outlives the request
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func (s *Server) getTodo(w http.ResponseWriter, r *http.Request) {
	_, t, ok := s.todo(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// nullableInt is a JSON field that tells absent (Set false) from null (Set,
// Value nil) and a number (Set, Value).
type nullableInt struct {
	Set   bool
	Value *int
}

func (n *nullableInt) UnmarshalJSON(b []byte) error {
	n.Set, n.Value = true, nil
	if string(b) == "null" {
		return nil
	}
	var v int
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	n.Value = &v
	return nil
}

func (s *Server) updateTodo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title       *string     `json:"title"`
		Prompt      *string     `json:"prompt"`
		Priority    nullableInt `json:"priority"`    // null unsets it
		Attachments *[]string   `json:"attachments"` // upload ids; replaces the list
		DependsOn   *[]string   `json:"depends_on"`  // todo ids; replaces the list
		BuildsOn    *string     `json:"builds_on"`   // a todo id; "" builds on none
		Model       *string     `json:"model"`       // "" goes back to the default
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	var atts *[]model.Attachment
	if req.Attachments != nil {
		a, err := sv.attachments(*req.Attachments)
		if err != nil {
			writeErr(w, err)
			return
		}
		atts = &a
	}
	t, err := sv.editTodo(r.PathValue("tid"), mcptools.TodoUpdate{
		Title: req.Title, Prompt: req.Prompt, DependsOn: req.DependsOn, BuildsOn: req.BuildsOn, Model: req.Model,
		Priority: req.Priority.Value, ClearPriority: req.Priority.Set && req.Priority.Value == nil,
	}, atts)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) deleteTodo(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := sv.removeTodo(r.Context(), r.PathValue("tid")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) enqueueTodo(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	t, err := sv.enqueue(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// startNowTodo lets a queued task start while the queue waits for the
// weekly pace (POST /api/todos/{tid}/start-now).
func (s *Server) startNowTodo(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	t, err := sv.startNow(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) cancelTodo(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	t, accepted, err := sv.cancel(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if accepted { // worker will mark it failed("canceled")
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) pauseTodo(w http.ResponseWriter, r *http.Request) {
	_, _, ok := s.todo(w, r)
	if !ok {
		return
	}
	t, err := s.Todos.Pause(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) resumeTodo(w http.ResponseWriter, r *http.Request) {
	_, _, ok := s.todo(w, r)
	if !ok {
		return
	}
	t, err := s.Todos.Resume(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) todoDiff(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	diff, err := sv.diff(r.Context(), r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeText(w, diff)
}

// todoChanges returns the patch a todo's worktree has made (t.Worktree must be set).
func (s *Server) todoChanges(ctx context.Context, p model.Project, t model.Todo) (string, error) {
	if t.Status == model.StatusMerged {
		// The branch is gone after merging; show the commit that landed it.
		if t.MergedCommit == "" {
			return "", nil
		}
		diff, err := s.Git.Show(ctx, p.ID, p.Path, t.MergedCommit)
		if err != nil {
			return "", nil // e.g. a PR merged on GitHub that this checkout hasn't fetched
		}
		return diff, nil
	}
	return s.Git.Diff(ctx, p.ID, p.Path, p.For(t).Base(), t.Worktree.Branch)
}

func (s *Server) todoLog(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	log, err := sv.log(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeText(w, log)
}

// todoEvents is the todo's worker events, oldest first, for its timeline.
func (s *Server) todoEvents(w http.ResponseWriter, r *http.Request) {
	sv, t, ok := s.todo(w, r)
	if !ok {
		return
	}
	if t.OnRunner() {
		b, err := sv.fetchFromRunner(r.Context(), t, "events")
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
		return
	}
	evs, err := s.Todos.Log.Events(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, evs)
}

func (s *Server) approveTodo(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	t, err := sv.approve(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, t)
}

// reworkTodo sends a reviewed (or failed) todo back to the queue with
// feedback; the next run reuses its worktree so Claude builds on its changes.
func (s *Server) reworkTodo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Feedback    string   `json:"feedback"`
		Attachments []string `json:"attachments"` // upload ids, e.g. a screenshot of what's wrong
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	atts, err := sv.attachments(req.Attachments)
	if err != nil {
		writeErr(w, err)
		return
	}
	t, err := sv.rework(r.PathValue("tid"), req.Feedback, atts)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// revertTodo undoes a merged todo on its project's merge worker; the todo updates
// when it's done (or an "Undo:" todo appears if Claude has to do it by hand).
func (s *Server) revertTodo(w http.ResponseWriter, r *http.Request) {
	sv, t, ok := s.todo(w, r)
	if !ok {
		return
	}
	if err := sv.revert(r.Context(), t); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// errNotRunYet is why the hub can't do something to a task's work: it has
// none before a runner has run it (the hub keeps no worktrees).
var errNotRunYet = fmt.Errorf("%w: this task hasn't run on a runner yet", errBadRequest)

// errHubHasNoProjects is why the hub refuses a project on its own disk.
var errHubHasNoProjects = fmt.Errorf("%w: the hub keeps no projects on its own computer. Create or add the project on a runner "+
	"(pick the runner as its computer), or share a repository by giving its remote_url (or github OWNER/REPO) and the runners to link it to", errBadRequest)

func (s *Server) cleanupProject(w http.ResponseWriter, r *http.Request) {
	_, _, ok := s.project(w, r)
	if !ok {
		return
	}
	rep, err := s.Merges.Cleanup(r.Context(), r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": rep, "summary": rep.String()})
}

func (s *Server) getCI(w http.ResponseWriter, r *http.Request) {
	_, _, ok := s.project(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.CI.Status(r.PathValue("pid")))
}

func (s *Server) refreshCI(w http.ResponseWriter, r *http.Request) {
	_, _, ok := s.project(w, r)
	if !ok {
		return
	}
	st, err := s.CI.Refresh(r.Context(), r.PathValue("pid"))
	if err != nil && st.ProjectID == "" {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st) // a gh failure is reported in st.Error
}

// retryCI re-runs the default branch's newest build on GitHub Actions.
func (s *Server) retryCI(w http.ResponseWriter, r *http.Request) {
	_, _, ok := s.project(w, r)
	if !ok {
		return
	}
	st, err := s.CI.Retry(r.Context(), r.PathValue("pid"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, err)
			return
		}
		writeErr(w, fmt.Errorf("%w: %v", errBadRequest, err))
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// investigateCI asks Claude to look into the failing build now.
func (s *Server) investigateCI(w http.ResponseWriter, r *http.Request) {
	_, _, ok := s.project(w, r)
	if !ok {
		return
	}
	t, err := s.CI.Investigate(r.Context(), r.PathValue("pid"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, err)
			return
		}
		writeErr(w, fmt.Errorf("%w: %v", errBadRequest, err))
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// ---------------------------------------------------------------- processes

func (s *Server) listProcesses(w http.ResponseWriter, r *http.Request) {
	_, _, ok := s.project(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, nonNil(s.Procs.List(r.PathValue("pid"))))
}

// startProcess launches a web server, a one-off task, or a PTY. With todo_id
// it runs inside that todo's worktree, so changes can be tried before merging.
func (s *Server) startProcess(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind    model.ProcessKind `json:"kind"`    // web | task | pty
		Preset  string            `json:"preset"`  // run | test | deploy — uses the project's command
		Command string            `json:"command"` // overrides preset
		TodoID  string            `json:"todo_id"`
		Cols    uint16            `json:"cols"`
		Rows    uint16            `json:"rows"`
	}
	sv, p, ok := s.project(w, r)
	if !ok {
		return
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	dir := p.Path
	if req.TodoID != "" {
		t, err := sv.data.GetTodo(req.TodoID)
		if err != nil {
			writeErr(w, err)
			return
		}
		if t.Worktree == nil || t.ProjectID != p.ID {
			writeErr(w, fmt.Errorf("%w: task has no worktree in this project", errBadRequest))
			return
		}
		dir = t.Worktree.Path
	}
	// The dashboard finds its quick actions and its shell by these (see
	// model.Process): a hub lists this computer's processes with its own
	// project path, which isn't this clone's.
	o := runner.Origin{Main: req.TodoID == ""}
	if req.Command == "" {
		req.Command = map[string]string{"run": p.RunCommand, "test": p.TestCommand, "deploy": p.DeployCommand}[req.Preset]
		if req.Command != "" {
			o.Preset = req.Preset
		}
	}
	if req.Command == "" && req.Kind != model.ProcPTY {
		writeErr(w, fmt.Errorf("%w: no command given and project has no %q command configured", errBadRequest, req.Preset))
		return
	}

	warning := ""
	if dir == p.Path && req.Kind != model.ProcPTY {
		var err error
		if warning, err = s.checkoutWarning(r.Context(), p, req.Preset == "deploy"); err != nil {
			writeErr(w, err)
			return
		}
	}

	var (
		proc model.Process
		err  error
	)
	switch req.Kind {
	case model.ProcWeb:
		proc, err = s.Procs.StartWeb(p.ID, dir, req.Command, o)
	case model.ProcTask, "":
		proc, err = s.Procs.StartTask(p.ID, dir, req.Command, o)
	case model.ProcPTY:
		proc, err = s.Procs.StartPTY(p.ID, dir, req.Command, req.Cols, req.Rows, o)
	default:
		err = fmt.Errorf("%w: unknown kind %q", errBadRequest, req.Kind)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		model.Process
		Warning string `json:"warning,omitempty"`
	}{proc, warning})
}

// checkoutWarning looks at the folder of a project that guards its
// checkout (a runner's checkout of its hub's project, maybe an adopted one
// its owner also works in) before a command runs there: with uncommitted
// changes to tracked files, a deploy is refused (it would ship work nobody
// committed) and anything else runs with the returned warning.
func (s *Server) checkoutWarning(ctx context.Context, p model.Project, refuse bool) (string, error) {
	if !p.GuardsCheckout() {
		return "", nil
	}
	files, err := s.Git.CheckoutChanges(ctx, p.ID, p.Path)
	if err != nil || len(files) == 0 {
		return "", nil
	}
	what := strings.Join(files, ", ")
	if len(files) > 5 {
		what = strings.Join(files[:5], ", ") + fmt.Sprintf(" and %d more", len(files)-5)
	}
	if refuse {
		return "", fmt.Errorf("%w: %s has uncommitted changes (%s); commit or stash them before deploying from it", errBadRequest, p.Path, what)
	}
	return fmt.Sprintf("%s has uncommitted changes (%s): this runs with them, and tasks wait to land until they're committed or stashed", p.Path, what), nil
}

func (s *Server) stopProcess(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if _, err := sv.process(r.PathValue("procid")); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.Procs.Stop(r.PathValue("procid")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// ---------------------------------------------------------------- permissions & usage

// listPermissions is the permission prompts waiting in the caller's org.
func (s *Server) listPermissions(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	project := r.URL.Query().Get("project")
	all := s.Perms.Pending(project)
	if s.Dispatch != nil {
		// The hub's tasks wait on their runners' requests.
		all = append(all, s.Dispatch.Permissions(project)...)
		slices.SortStableFunc(all, func(a, b perm.Request) int { return a.Created.Compare(b.Created) })
	}
	out := []perm.Request{}
	for _, req := range all {
		if sv.data.OwnsProject(req.ProjectID) {
			out = append(out, req)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) decidePermission(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Decision string `json:"decision"` // allow | always | deny
		Message  string `json:"message"`
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	id := r.PathValue("id")
	if _, ok := s.Perms.Get(id); !ok && s.Dispatch != nil {
		// A request of a hub's task, waiting on its runner.
		pr, ok := s.Dispatch.Permission(id)
		if !ok || !sv.data.OwnsProject(pr.ProjectID) {
			writeErr(w, store.ErrNotFound)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := s.Dispatch.DecidePermission(ctx, id, req.Decision, req.Message); err != nil {
			if errors.Is(err, perm.ErrNotFound) {
				err = store.ErrNotFound
			}
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if pr, ok := s.Perms.Get(id); !ok || !sv.data.OwnsProject(pr.ProjectID) {
		writeErr(w, store.ErrNotFound)
		return
	}
	if err := s.Perms.Decide(id, req.Decision, req.Message); err != nil {
		if errors.Is(err, perm.ErrNotFound) {
			writeErr(w, store.ErrNotFound)
			return
		}
		writeErr(w, fmt.Errorf("%w: %v", errBadRequest, err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getUsage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.usageFor(r))
}

// usageFor is the usage the dashboard shows r's caller. The hub doesn't run
// Claude, so it has no usage of its own: it shows the caller's, as their
// runner last reported it, and how the hub paces the caller's Claude
// account.
func (s *Server) usageFor(r *http.Request) usage.Snapshot {
	var snap usage.Snapshot
	if s.Usage != nil {
		snap = s.Usage.Snapshot()
	}
	if !s.hub() {
		return snap
	}
	c, err := s.caller(r)
	if err != nil {
		return snap
	}
	now := time.Now()
	u := s.runnerUsage(c)
	var pace *dispatch.GroupPace
	acct := s.callerAccount(c)
	if acct != "" && s.Dispatch != nil && s.Dispatch.Pacer != nil {
		pace = s.Dispatch.Pacer.Account(acct)
		if pace != nil && pace.Usage != nil && (u == nil || pace.Usage.UpdatedAt.After(u.UpdatedAt)) {
			u = pace.Usage // the account's freshest figures
		}
	}
	manual, reason := snap.ManualPause, snap.Reason
	hubPace := usage.Pace{Hub: true, Account: acct}
	if u != nil {
		snap = *u
	} else {
		snap = usage.Snapshot{}
	}
	snap.ManualPause = manual
	if manual {
		snap.Paused, snap.Reason, snap.Until = true, reason, time.Time{}
	}
	if pace != nil {
		hubPace = pace.Pace
		hubPace.Runners = s.accountRunners(c, acct)
	}
	// The runner's verdict may be stale: its windows may have reset since.
	return snap.WithPace(hubPace, now)
}

// callerAccount is the Claude account c's runners are logged in to (the
// one an online runner reported last), or "".
func (s *Server) callerAccount(c Caller) string {
	var best *tunnel.Runner
	for _, r := range s.claudeRunners(c) {
		if r.Account() == "" {
			continue
		}
		if best == nil || (r.Online && !best.Online) || (r.Online == best.Online && r.Reported.At.After(best.Reported.At)) {
			best = &r
		}
	}
	if best == nil {
		return ""
	}
	return best.Account()
}

// accountRunners are the names of the runners in c's org logged in to
// account (the hub paces them together, with any of other orgs').
func (s *Server) accountRunners(c Caller, account string) []string {
	var out []string
	for _, r := range s.fleet().List() {
		if r.OrgID() == c.Org && r.Account() == account {
			out = append(out, r.Name)
		}
	}
	return out
}

// canPaceAccount reports whether c may change how the hub paces the
// runners on account: whoever may manage one of them in c's org (its
// owner, an org admin).
func (s *Server) canPaceAccount(c Caller, account string) bool {
	fl := s.fleet()
	if fl == nil || account == "" {
		return false
	}
	for _, r := range fl.List() {
		if r.Account() == account && s.canManageRunner(c, r) {
			return true
		}
	}
	return false
}

// runnerUsage is c's Claude usage as one of c's runners last reported it:
// an online runner's before an offline one's, the newest report first.
func (s *Server) runnerUsage(c Caller) *usage.Snapshot {
	var best *tunnel.Runner
	for _, r := range s.claudeRunners(c) {
		if r.Reported == nil || r.Reported.Usage == nil || !r.Reported.Usage.Known {
			continue
		}
		if best == nil || (r.Online && !best.Online) ||
			(r.Online == best.Online && r.Reported.At.After(best.Reported.At)) {
			best = &r
		}
	}
	if best == nil {
		return nil
	}
	return best.Reported.Usage
}

// pauseQueue pauses or resumes the task queue, which every org shares:
// only platform admins may.
func (s *Server) pauseQueue(paused bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.adminOnly(w, r) {
			return
		}
		s.Usage.SetManualPause(paused)
		if !paused {
			s.Todos.Kick()
		}
		writeJSON(w, http.StatusOK, s.Usage.Snapshot())
	}
}

// putQueuePace changes the "Follow weekly pace" settings. On the hub they
// are a Claude account's (see dispatch.Pacer): the caller's own, or the one
// they name, which the owner of a runner on it or an org admin may change.
// Elsewhere they are this Dash's own, for platform admins.
func (s *Server) putQueuePace(w http.ResponseWriter, r *http.Request) {
	if s.hub() {
		s.putAccountPace(w, r)
		return
	}
	if !s.adminOnly(w, r) {
		return
	}
	var req usage.PaceSettings
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if s.Usage == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "this Dash doesn't track Claude usage"})
		return
	}
	if s.Usage.Snapshot().Pace.Hub {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the hub paces this runner, with the others on its Claude account: change it in the hub's dashboard"})
		return
	}
	if req.MaxParallel < 0 || req.MaxParallel > usage.MaxPaceParallel {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("max_parallel must be between 0 and %d", usage.MaxPaceParallel)})
		return
	}
	if err := s.Usage.SetPace(req); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save the pace settings: " + err.Error()})
		return
	}
	s.Todos.Kick()
	writeJSON(w, http.StatusOK, s.Usage.Snapshot())
}

var errNoAccount = errors.New("none of your runners has said which Claude account it is logged in to yet: update Dash on it, and make sure claude is logged in there")

func (s *Server) putAccountPace(w http.ResponseWriter, r *http.Request) {
	c, ok := s.callerOr(w, r)
	if !ok {
		return
	}
	var req usage.PaceSettings
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	acct := strings.ToLower(strings.TrimSpace(req.Account))
	if acct == "" {
		acct = s.callerAccount(c)
	}
	if acct == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": errNoAccount.Error()})
		return
	}
	if !s.canPaceAccount(c, acct) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the owner of a runner logged in to that Claude account, or an admin of the organization, can change its pace"})
		return
	}
	if !dispatch.ValidBoost(req.MaxBoost) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("max_boost must be between 1 and %g", usage.MaxBoostCap)})
		return
	}
	if s.Dispatch == nil || s.Dispatch.Pacer == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "this hub doesn't pace its runners"})
		return
	}
	if err := s.Dispatch.Pacer.Set(acct, usage.PaceSettings{Follow: req.Follow, MaxBoost: req.MaxBoost}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save the pace settings: " + err.Error()})
		return
	}
	s.Todos.Kick()
	writeJSON(w, http.StatusOK, s.usageFor(r))
}

// ---------------------------------------------------------------- utils

func cleanList(xs []string) []string {
	var out []string
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func set[T any](dst *T, src *T) {
	if src != nil {
		*dst = *src
	}
}

func nonNil[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}

// checkCloneURL checks a project's clone_url: "" (none), or a remote a
// runner clones (gitops.CheckRemote) that carries no password or token,
// which would be stored with the project and shown to everyone in it.
func checkCloneURL(u string) (string, error) {
	u = strings.TrimSpace(u)
	if u == "" {
		return "", nil
	}
	if err := gitops.CheckRemote(u); err != nil {
		return "", errors.Join(errBadRequest, err)
	}
	if x, err := url.Parse(u); err == nil && x.User != nil {
		if _, ok := x.User.Password(); ok || (x.Scheme == "https" && x.User.Username() != "") {
			return "", fmt.Errorf("%w: clone_url must not hold credentials: set them up on the runner (an SSH key, or a git credential helper)", errBadRequest)
		}
	}
	return u, nil
}
