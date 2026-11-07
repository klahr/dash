package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/store"
)

// ---------------------------------------------------------------- projects

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	// Active lets the sidebar show which projects have Claude running or work
	// waiting for review; events keep it current from there.
	type item struct {
		model.Project
		Active map[string]model.TodoStatus `json:"active,omitempty"`
	}
	ps := s.Store.ListProjects()
	out := make([]item, len(ps))
	for i, p := range ps {
		out[i].Project = p
		for _, t := range s.Store.ListTodos(p.ID, model.StatusInProgress, model.StatusReview) {
			if out[i].Active == nil {
				out[i].Active = map[string]model.TodoStatus{}
			}
			out[i].Active[t.ID] = t.Status
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

var slugRe = regexp.MustCompile(`[^a-z0-9-]+`)

var colorRe = regexp.MustCompile(`^#[0-9a-f]{6}$`)

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string   `json:"name"`
		Path          string   `json:"path"` // optional: adopt an existing directory/repo
		DefaultBranch string   `json:"default_branch"`
		RunCommand    string   `json:"run_command"`
		TestCommand   string   `json:"test_command"`
		DeployCommand string   `json:"deploy_command"`
		SetupCommand  string   `json:"setup_command"`
		CopyFiles     []string `json:"copy_files"`
		MaxAutoFixes  *int     `json:"max_auto_fixes"`
		AutoPush      bool     `json:"auto_push"`
		MergeMode     string   `json:"merge_mode"`
		MaxRunMinutes int      `json:"max_run_minutes"`
		MaxTurns      int      `json:"max_turns"`
		// GitHub names a repository (REPO or OWNER/REPO): with Clone it is
		// cloned (it must exist); without, it is created and pushed (it must not).
		GitHub       string `json:"github"`
		Clone        bool   `json:"clone"`
		GitHubPublic bool   `json:"github_public"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := checkMergeMode(req.MergeMode, ""); err != nil && req.MergeMode != model.MergePR {
		writeErr(w, err)
		return
	}
	maxFixes := model.DefaultMaxAutoFixes
	if req.MaxAutoFixes != nil {
		maxFixes = max(0, *req.MaxAutoFixes)
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErr(w, fmt.Errorf("%w: name is required", errBadRequest))
		return
	}
	if req.Path != "" {
		req.Path = store.CleanPath(req.Path)
	} else {
		req.Path = filepath.Join(s.ProjectRoot, strings.Trim(slugRe.ReplaceAllString(strings.ToLower(req.Name), "-"), "-"))
	}
	if req.DefaultBranch == "" {
		req.DefaultBranch = "main"
	}
	id := store.NewID()
	ctx := r.Context()
	req.GitHub = strings.TrimSpace(req.GitHub)
	createRemote := false
	if req.GitHub != "" {
		gh, exists, err := s.Git.LookupGitHubRepo(ctx, req.GitHub)
		switch {
		case err != nil:
			writeErr(w, err)
			return
		case exists && !req.Clone:
			writeErr(w, fmt.Errorf("%w: %s already exists on GitHub; clone it instead", errBadRequest, gh.Name))
			return
		case !exists && req.Clone:
			writeErr(w, fmt.Errorf("%w: %s not found on GitHub", errBadRequest, req.GitHub))
			return
		case exists:
			if err := s.Git.CloneGitHubRepo(ctx, id, gh.Name, req.Path); err != nil {
				writeErr(w, errors.Join(errBadRequest, err))
				return
			}
		default:
			if s.Git.RemoteURL(ctx, id, req.Path) != "" {
				writeErr(w, fmt.Errorf("%w: %s already has an origin remote", errBadRequest, req.Path))
				return
			}
			createRemote = true
		}
	}
	if err := s.Git.InitRepo(ctx, id, req.Path, req.DefaultBranch); err != nil {
		writeErr(w, err)
		return
	}
	if createRemote {
		if _, err := s.Git.CreateGitHubRepo(ctx, id, req.Path, req.GitHub, !req.GitHubPublic); err != nil {
			writeErr(w, err)
			return
		}
	}
	// An adopted repo keeps whatever branch it has checked out.
	if b, err := s.Git.CurrentBranch(ctx, id, req.Path); err == nil && b != "HEAD" {
		req.DefaultBranch = b
	}
	// A detached HEAD leaves the requested name, which may not exist (master repos).
	if !s.Git.HasBranch(ctx, id, req.Path, req.DefaultBranch) {
		if b := s.Git.MainBranch(ctx, id, req.Path); b != "" {
			req.DefaultBranch = b
		}
	}
	p, err := s.Store.CreateProject(model.Project{
		ID: id, Name: req.Name, Path: req.Path, DefaultBranch: req.DefaultBranch,
		RemoteURL: s.Git.RemoteURL(ctx, id, req.Path), AutoPush: req.AutoPush,
		RunCommand: req.RunCommand, TestCommand: req.TestCommand, DeployCommand: req.DeployCommand,
		SetupCommand: req.SetupCommand, CopyFiles: cleanList(req.CopyFiles), MaxAutoFixes: maxFixes,
		MaxRunMinutes: max(0, req.MaxRunMinutes), MaxTurns: max(0, req.MaxTurns),
	})
	if err == nil && req.MergeMode == model.MergePR {
		// Only possible when the adopted repo already has a GitHub remote.
		if err = checkMergeMode(req.MergeMode, p.RemoteURL); err == nil {
			p, err = s.Store.UpdateProject(p.ID, func(p *model.Project) error { p.MergeMode = model.MergePR; return nil })
		}
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          *string   `json:"name"`
		RunCommand    *string   `json:"run_command"`
		TestCommand   *string   `json:"test_command"`
		DeployCommand *string   `json:"deploy_command"`
		SetupCommand  *string   `json:"setup_command"`
		CopyFiles     *[]string `json:"copy_files"`
		AllowedTools  *[]string `json:"allowed_tools"`
		ChatMode      *string   `json:"chat_permission_mode"`
		ReviewPrompt  *string   `json:"review_prompt"`
		MaxAutoFixes  *int      `json:"max_auto_fixes"`
		AutoPush      *bool     `json:"auto_push"`
		AutoApprove   *bool     `json:"auto_approve"`
		MergeMode     *string   `json:"merge_mode"`
		InvestigateCI *bool     `json:"investigate_ci"`
		MaxRunMinutes *int      `json:"max_run_minutes"`
		MaxTurns      *int      `json:"max_turns"`
		Color         *string   `json:"color"`
		Icon          *string   `json:"icon"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.ChatMode != nil {
		switch *req.ChatMode {
		case "", model.ChatModeAuto, model.ChatModeAcceptEdits:
		default:
			writeErr(w, fmt.Errorf("%w: chat_permission_mode must be \"\", %q or %q", errBadRequest, model.ChatModeAuto, model.ChatModeAcceptEdits))
			return
		}
	}
	if req.Color != nil {
		*req.Color = strings.ToLower(strings.TrimSpace(*req.Color))
		if *req.Color != "" && !colorRe.MatchString(*req.Color) {
			writeErr(w, fmt.Errorf("%w: color must look like #3a7bd5", errBadRequest))
			return
		}
	}
	if req.Icon != nil && *req.Icon != "" {
		id, name, _ := strings.Cut(*req.Icon, "/")
		if a, err := s.findUpload(id); err != nil || a.Name != name || !strings.HasPrefix(a.Type, "image/") {
			writeErr(w, fmt.Errorf("%w: icon must be an uploaded image", errBadRequest))
			return
		}
	}
	remote := ""
	if cur, err := s.Store.GetProject(r.PathValue("pid")); err == nil {
		remote = s.Git.RemoteURL(r.Context(), cur.ID, cur.Path) // pick up an origin added since
	}
	p, err := s.Store.UpdateProject(r.PathValue("pid"), func(p *model.Project) error {
		p.RemoteURL = remote
		set(&p.Name, req.Name)
		set(&p.RunCommand, req.RunCommand)
		set(&p.TestCommand, req.TestCommand)
		set(&p.DeployCommand, req.DeployCommand)
		set(&p.SetupCommand, req.SetupCommand)
		set(&p.AutoPush, req.AutoPush)
		set(&p.AutoApprove, req.AutoApprove)
		set(&p.Color, req.Color)
		set(&p.Icon, req.Icon)
		set(&p.ChatPermissionMode, req.ChatMode)
		set(&p.ReviewPrompt, req.ReviewPrompt)
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
			p.MergeMode = *req.MergeMode
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
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

// lookupGitHubRepo tells the new-project form whether a repository exists,
// so it can offer to clone it instead of creating it.
func (s *Server) lookupGitHubRepo(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeErr(w, fmt.Errorf("%w: name is required", errBadRequest))
		return
	}
	repo, ok, err := s.Git.LookupGitHubRepo(r.Context(), name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"exists": ok, "name": repo.Name, "url": repo.URL})
}

func (s *Server) createGitHubRepo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Private bool   `json:"private"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.Store.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if req.Name == "" {
		req.Name = filepath.Base(p.Path)
	}
	url, err := s.Git.CreateGitHubRepo(r.Context(), p.ID, p.Path, req.Name, req.Private)
	if err != nil {
		writeErr(w, err)
		return
	}
	p, err = s.Store.UpdateProject(p.ID, func(p *model.Project) error { p.RemoteURL = url; return nil })
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
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
	if err := s.Auth.ConfirmPassword(r, req.Password); err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.Store.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if queue.Busy(s.Store.ListTodos(p.ID)) {
		writeErr(w, queue.ErrProjectBusy)
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
	if s.Procs != nil {
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
	p, err := s.Store.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if url := s.Git.RemoteURL(r.Context(), p.ID, p.Path); url != p.RemoteURL {
		p, _ = s.Store.UpdateProject(p.ID, func(x *model.Project) error { x.RemoteURL = url; return nil })
	}
	if p.RemoteURL == "" {
		writeErr(w, fmt.Errorf("%w: project has no remote; create one via /github first", errBadRequest))
		return
	}
	if err := s.Merges.Remote.With(r.Context(), p.ID, func() error {
		return s.Git.Push(r.Context(), p.ID, p.Path, p.DefaultBranch)
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"pushed": p.DefaultBranch})
}

// remoteStatus reports how the default branch compares with origin's and
// marks the project as selected, so it's fetched every minute. With fetch it
// also fetches now, in the background: the result arrives as a
// remote.updated event; ?force=1 fetches even while backing off after failures.
func (s *Server) remoteStatus(fetch bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := s.Store.GetProject(r.PathValue("pid"))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.Merges.Remote.Select(r.Context(), p, fetch, r.URL.Query().Get("force") == "1"))
	}
}

// unpushed counts the default branch's commits (merged todos) origin doesn't have yet.
func (s *Server) unpushed(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	n, todos := 0, []string{}
	if p.RemoteURL != "" {
		n, _ = s.Git.Unpushed(r.Context(), p.ID, p.Path, p.DefaultBranch)
		if ids, err := s.Git.UnpushedTodos(r.Context(), p.ID, p.Path, p.DefaultBranch); err == nil && ids != nil {
			todos = ids
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": n, "todos": todos})
}

func (s *Server) gitGraph(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	graph, err := s.Git.Graph(r.Context(), p.ID, p.Path, p.DefaultBranch, 500)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"branch": p.DefaultBranch, "graph": graph})
}

// ---------------------------------------------------------------- todos

func (s *Server) listTodos(w http.ResponseWriter, r *http.Request) {
	var statuses []model.TodoStatus
	if st := r.URL.Query().Get("status"); st != "" {
		for _, x := range strings.Split(st, ",") {
			statuses = append(statuses, model.TodoStatus(x))
		}
	}
	writeJSON(w, http.StatusOK, nonNil(s.Store.ListTodos(r.PathValue("pid"), statuses...)))
}

func (s *Server) createTodo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title       string   `json:"title"`
		Prompt      string   `json:"prompt"`
		Priority    int      `json:"priority"`
		Enqueue     bool     `json:"enqueue"`     // skip Draft
		Attachments []string `json:"attachments"` // upload ids
		DependsOn   []string `json:"depends_on"`  // todo ids that must be merged first
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if strings.TrimSpace(req.Title) == "" && strings.TrimSpace(req.Prompt) == "" {
		writeErr(w, fmt.Errorf("%w: title or prompt is required", errBadRequest))
		return
	}
	atts, err := s.attachments(req.Attachments)
	if err != nil {
		writeErr(w, err)
		return
	}
	t, err := s.addTodo(model.Todo{
		ProjectID: r.PathValue("pid"), Title: req.Title, Prompt: req.Prompt, Priority: req.Priority,
		Attachments: atts, DependsOn: req.DependsOn,
	}, req.Enqueue)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// addTodo creates a draft, or queues it when enqueue is set, and names it if untitled.
func (s *Server) addTodo(t model.Todo, enqueue bool) (model.Todo, error) {
	t, err := s.Store.CreateTodo(t)
	if err == nil && enqueue {
		t, err = s.Store.SetStatus(t.ID, model.StatusQueued, "")
		s.Todos.Kick()
	}
	if err != nil {
		return t, err
	}
	if strings.TrimSpace(t.Title) == "" {
		s.autoTitle(t.ID, t.Prompt)
	}
	return t, nil
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
	if s.Refiner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cleaning up instructions is not available"})
		return
	}
	out, err := s.Refiner.Refine(r.Context(), req.Prompt)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"prompt": out})
}

// autoTitle names a todo in the background; a title typed meanwhile wins.
func (s *Server) autoTitle(id, prompt string) {
	if s.Titler == nil {
		return
	}
	go func() {
		title, err := s.Titler.Title(context.Background(), prompt)
		if err != nil {
			slog.Debug("auto title", "todo", id, "err", err)
			return
		}
		_, _ = s.Store.UpdateTodo(id, func(t *model.Todo) error {
			if strings.TrimSpace(t.Title) == "" {
				t.Title = title
			}
			return nil
		})
	}()
}

func (s *Server) retestTodo(w http.ResponseWriter, r *http.Request) {
	if err := s.Todos.RunTests(r.PathValue("tid")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, err)
			return
		}
		writeErr(w, fmt.Errorf("%w: %v", errBadRequest, err))
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// duplicateTodo copies a todo's instructions into a new draft.
func (s *Server) duplicateTodo(w http.ResponseWriter, r *http.Request) {
	src, err := s.Store.GetTodo(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	title := src.Title
	if title != "" {
		title += " (copy)"
	}
	t, err := s.Store.CreateTodo(model.Todo{ProjectID: src.ProjectID, Title: title, Prompt: src.Prompt,
		Priority: src.Priority, Attachments: src.Attachments, Edits: src.Edits, DependsOn: src.DependsOn})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// openTodo opens the todo's worktree in the configured editor on the Dash machine.
func (s *Server) openTodo(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.GetTodo(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if s.Editor == "" {
		writeErr(w, fmt.Errorf("%w: no editor configured; start dash with -editor (e.g. -editor code)", errBadRequest))
		return
	}
	if t.Worktree == nil {
		writeErr(w, fmt.Errorf("%w: this task has no worktree", errBadRequest))
		return
	}
	args := append(strings.Fields(s.Editor), t.Worktree.Path)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // outlives the request
	if err := cmd.Start(); err != nil {
		writeErr(w, err)
		return
	}
	go func() { _ = cmd.Wait() }()
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) getTodo(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.GetTodo(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) updateTodo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title       *string   `json:"title"`
		Prompt      *string   `json:"prompt"`
		Priority    *int      `json:"priority"`
		Attachments *[]string `json:"attachments"` // upload ids; replaces the list
		DependsOn   *[]string `json:"depends_on"`  // todo ids; replaces the list
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	var atts []model.Attachment
	if req.Attachments != nil {
		var err error
		if atts, err = s.attachments(*req.Attachments); err != nil {
			writeErr(w, err)
			return
		}
	}
	t, err := s.Store.UpdateTodo(r.PathValue("tid"), func(t *model.Todo) error {
		editable := t.Status == model.StatusDraft || t.Status == model.StatusQueued || t.Status == model.StatusFailed
		if (req.Prompt != nil || req.Attachments != nil) && !editable {
			return fmt.Errorf("%w: instructions can only be edited while draft, queued or failed", errBadRequest)
		}
		if req.Attachments != nil {
			t.Attachments = atts
		}
		set(&t.Title, req.Title)
		set(&t.Prompt, req.Prompt)
		set(&t.Priority, req.Priority)
		set(&t.DependsOn, req.DependsOn) // validated by the store
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Todos.Kick() // priority or dependencies may have changed
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) deleteTodo(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.GetTodo(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	switch t.Status {
	case model.StatusInProgress, model.StatusMerging:
		writeErr(w, model.ErrInvalidTransition{From: t.Status, To: "deleted"})
		return
	}
	if t.Reviewing {
		writeErr(w, store.ErrReviewing)
		return
	}
	if t.Worktree != nil && t.Status != model.StatusMerged {
		if p, err := s.Store.GetProject(t.ProjectID); err == nil {
			_ = s.Git.RemoveWorktree(r.Context(), p.ID, p.Path, *t.Worktree)
		}
	}
	if _, err := s.Store.DeleteTodo(t.ID); err != nil {
		writeErr(w, err)
		return
	}
	if s.Perms != nil {
		s.Perms.Forget(t.ID)
	}
	s.Todos.Log.Remove(t.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) enqueueTodo(w http.ResponseWriter, r *http.Request) {
	t, err := s.enqueue(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) enqueue(id string) (model.Todo, error) {
	t, err := s.Store.SetStatus(id, model.StatusQueued, "")
	if err == nil {
		s.Todos.Kick()
	}
	return t, err
}

func (s *Server) cancelTodo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("tid")
	if s.Todos.Cancel(id) { // worker will mark it failed("canceled")
		w.WriteHeader(http.StatusAccepted)
		return
	}
	t, err := s.Store.SetStatus(id, model.StatusDraft, "")
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) pauseTodo(w http.ResponseWriter, r *http.Request) {
	t, err := s.Todos.Pause(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) resumeTodo(w http.ResponseWriter, r *http.Request) {
	t, err := s.Todos.Resume(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) todoDiff(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.GetTodo(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if t.Worktree == nil {
		writeErr(w, fmt.Errorf("%w: task has no worktree yet", errBadRequest))
		return
	}
	p, err := s.Store.GetProject(t.ProjectID)
	if err != nil {
		writeErr(w, err)
		return
	}
	diff, err := s.todoChanges(r.Context(), p, t)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(diff))
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
	return s.Git.Diff(ctx, p.ID, p.Path, p.DefaultBranch, t.Worktree.Branch)
}

func (s *Server) todoLog(w http.ResponseWriter, r *http.Request) {
	log, err := s.Todos.Log.Read(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(log))
}

// todoEvents is the todo's worker events, oldest first, for its timeline.
func (s *Server) todoEvents(w http.ResponseWriter, r *http.Request) {
	evs, err := s.Todos.Log.Events(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, evs)
}

func (s *Server) approveTodo(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.UpdateTodo(r.PathValue("tid"), func(t *model.Todo) error {
		if t.Status != model.StatusReview && t.Status != model.StatusFailed {
			return fmt.Errorf("%w: only tasks in review (or a failed merge) can be approved", errBadRequest)
		}
		if t.Worktree == nil {
			return fmt.Errorf("%w: this task has no work left to merge; retry it instead", errBadRequest)
		}
		t.Status, t.Error, t.AutoFixes = model.StatusMerging, "", 0
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.Merges.Enqueue(model.MergeJob{TodoID: t.ID, ProjectID: t.ProjectID}); err != nil {
		_, _ = s.Store.SetStatus(t.ID, model.StatusReview, "")
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
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	atts, err := s.attachments(req.Attachments)
	if err != nil {
		writeErr(w, err)
		return
	}
	t, err := s.Store.UpdateTodo(r.PathValue("tid"), func(t *model.Todo) error {
		fb := strings.TrimSpace(req.Feedback)
		if len(atts) > 0 {
			if fb == "" {
				fb = "See the attached files."
			}
			fb += "\n\n" + agent.AttachmentList(atts)
			t.Attachments = append(t.Attachments, atts...)
		}
		if fb != "" {
			t.Feedback = append(t.Feedback, fb)
		}
		t.Status, t.Error = model.StatusQueued, ""
		t.AutoFixes, t.AutoMerge, t.SentBack = 0, false, "" // a human is involved again: fresh budget, review afterwards
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Todos.Kick()
	writeJSON(w, http.StatusOK, t)
}

// revertTodo undoes a merged todo on the merge worker; the todo updates
// when it's done (or an "Undo:" todo appears if Claude has to do it by hand).
func (s *Server) revertTodo(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.GetTodo(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	switch {
	case t.Status != model.StatusMerged:
		writeErr(w, fmt.Errorf("%w: only merged tasks can be reverted", errBadRequest))
		return
	case t.Revert != nil:
		writeErr(w, fmt.Errorf("%w: this task was already reverted", errBadRequest))
		return
	}
	if err := s.Merges.Enqueue(model.MergeJob{TodoID: t.ID, ProjectID: t.ProjectID, Revert: true}); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) cleanupProject(w http.ResponseWriter, r *http.Request) {
	rep, err := s.Merges.Cleanup(r.Context(), r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": rep, "summary": rep.String()})
}

func (s *Server) getCI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.CI.Status(r.PathValue("pid")))
}

func (s *Server) refreshCI(w http.ResponseWriter, r *http.Request) {
	st, err := s.CI.Refresh(r.Context(), r.PathValue("pid"))
	if err != nil && st.ProjectID == "" {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st) // a gh failure is reported in st.Error
}

// investigateCI asks Claude to look into the failing build now.
func (s *Server) investigateCI(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) getQueues(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	writeJSON(w, http.StatusOK, map[string]any{
		"todo":          nonNil(s.Store.ListTodos(pid, model.StatusQueued)),
		"in_progress":   nonNil(s.Store.ListTodos(pid, model.StatusInProgress)),
		"review":        nonNil(s.Store.ListTodos(pid, model.StatusReview)),
		"merge":         nonNil(s.Store.ListTodos(pid, model.StatusMerging)),
		"merge_pending": s.Merges.Pending(),
	})
}

// ---------------------------------------------------------------- processes

func (s *Server) listProcesses(w http.ResponseWriter, r *http.Request) {
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
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.Store.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	dir := p.Path
	if req.TodoID != "" {
		t, err := s.Store.GetTodo(req.TodoID)
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
	if req.Command == "" {
		req.Command = map[string]string{"run": p.RunCommand, "test": p.TestCommand, "deploy": p.DeployCommand}[req.Preset]
	}
	if req.Command == "" && req.Kind != model.ProcPTY {
		writeErr(w, fmt.Errorf("%w: no command given and project has no %q command configured", errBadRequest, req.Preset))
		return
	}

	var proc model.Process
	switch req.Kind {
	case model.ProcWeb:
		proc, err = s.Procs.StartWeb(p.ID, dir, req.Command)
	case model.ProcTask, "":
		proc, err = s.Procs.StartTask(p.ID, dir, req.Command)
	case model.ProcPTY:
		proc, err = s.Procs.StartPTY(p.ID, dir, req.Command, req.Cols, req.Rows)
	default:
		err = fmt.Errorf("%w: unknown kind %q", errBadRequest, req.Kind)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, proc)
}

func (s *Server) stopProcess(w http.ResponseWriter, r *http.Request) {
	if err := s.Procs.Stop(r.PathValue("procid")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// ---------------------------------------------------------------- permissions & usage

func (s *Server) listPermissions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Perms.Pending(r.URL.Query().Get("project")))
}

func (s *Server) decidePermission(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Decision string `json:"decision"` // allow | always | deny
		Message  string `json:"message"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.Perms.Decide(r.PathValue("id"), req.Decision, req.Message); err != nil {
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
	writeJSON(w, http.StatusOK, s.Usage.Snapshot())
}

func (s *Server) pauseQueue(paused bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.Usage.SetManualPause(paused)
		if !paused {
			s.Todos.Kick()
		}
		writeJSON(w, http.StatusOK, s.Usage.Snapshot())
	}
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
