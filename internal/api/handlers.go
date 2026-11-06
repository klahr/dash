package api

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/store"
)

// ---------------------------------------------------------------- projects

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.ListProjects())
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
	}
	if err := decode(r, &req); err != nil {
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
	if req.Path == "" {
		req.Path = filepath.Join(s.ProjectRoot, strings.Trim(slugRe.ReplaceAllString(strings.ToLower(req.Name), "-"), "-"))
	}
	if req.DefaultBranch == "" {
		req.DefaultBranch = "main"
	}
	id := store.NewID()
	ctx := r.Context()
	if err := s.Git.InitRepo(ctx, id, req.Path, req.DefaultBranch); err != nil {
		writeErr(w, err)
		return
	}
	// An adopted repo keeps whatever branch it has checked out.
	if b, err := s.Git.CurrentBranch(ctx, id, req.Path); err == nil && b != "HEAD" {
		req.DefaultBranch = b
	}
	p, err := s.Store.CreateProject(model.Project{
		ID: id, Name: req.Name, Path: req.Path, DefaultBranch: req.DefaultBranch,
		RemoteURL: s.Git.RemoteURL(ctx, id, req.Path), AutoPush: req.AutoPush,
		RunCommand: req.RunCommand, TestCommand: req.TestCommand, DeployCommand: req.DeployCommand,
		SetupCommand: req.SetupCommand, CopyFiles: cleanList(req.CopyFiles), MaxAutoFixes: maxFixes,
	})
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
		MaxAutoFixes  *int      `json:"max_auto_fixes"`
		AutoPush      *bool     `json:"auto_push"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.Store.UpdateProject(r.PathValue("pid"), func(p *model.Project) error {
		set(&p.Name, req.Name)
		set(&p.RunCommand, req.RunCommand)
		set(&p.TestCommand, req.TestCommand)
		set(&p.DeployCommand, req.DeployCommand)
		set(&p.SetupCommand, req.SetupCommand)
		set(&p.AutoPush, req.AutoPush)
		if req.CopyFiles != nil {
			p.CopyFiles = cleanList(*req.CopyFiles)
		}
		if req.MaxAutoFixes != nil {
			p.MaxAutoFixes = max(0, *req.MaxAutoFixes)
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
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

func (s *Server) pushProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if p.RemoteURL == "" {
		writeErr(w, fmt.Errorf("%w: project has no remote; create one via /github first", errBadRequest))
		return
	}
	if err := s.Git.Push(r.Context(), p.ID, p.Path, p.DefaultBranch); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"pushed": p.DefaultBranch})
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
		Title    string `json:"title"`
		Prompt   string `json:"prompt"`
		Goal     bool   `json:"goal"`
		Priority int    `json:"priority"`
		Enqueue  bool   `json:"enqueue"` // skip Draft
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeErr(w, fmt.Errorf("%w: prompt is required", errBadRequest))
		return
	}
	t, err := s.Store.CreateTodo(model.Todo{
		ProjectID: r.PathValue("pid"), Title: req.Title, Prompt: req.Prompt, Goal: req.Goal, Priority: req.Priority,
	})
	if err == nil && req.Enqueue {
		t, err = s.Store.SetStatus(t.ID, model.StatusQueued, "")
		s.Todos.Kick()
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
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
		Title    *string `json:"title"`
		Prompt   *string `json:"prompt"`
		Goal     *bool   `json:"goal"`
		Priority *int    `json:"priority"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	t, err := s.Store.UpdateTodo(r.PathValue("tid"), func(t *model.Todo) error {
		if req.Prompt != nil && t.Status != model.StatusDraft && t.Status != model.StatusQueued && t.Status != model.StatusFailed {
			return fmt.Errorf("%w: prompt can only be edited while draft, queued or failed", errBadRequest)
		}
		set(&t.Title, req.Title)
		set(&t.Prompt, req.Prompt)
		set(&t.Goal, req.Goal)
		set(&t.Priority, req.Priority)
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Todos.Kick() // priority may have changed
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
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) enqueueTodo(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.SetStatus(r.PathValue("tid"), model.StatusQueued, "")
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Todos.Kick()
	writeJSON(w, http.StatusOK, t)
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

func (s *Server) todoDiff(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.GetTodo(r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if t.Worktree == nil {
		writeErr(w, fmt.Errorf("%w: todo has no worktree yet", errBadRequest))
		return
	}
	p, err := s.Store.GetProject(t.ProjectID)
	if err != nil {
		writeErr(w, err)
		return
	}
	diff, err := s.Git.Diff(r.Context(), p.ID, p.Path, p.DefaultBranch, t.Worktree.Branch)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(diff))
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

func (s *Server) approveTodo(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.UpdateTodo(r.PathValue("tid"), func(t *model.Todo) error {
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
		Feedback string `json:"feedback"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	t, err := s.Store.UpdateTodo(r.PathValue("tid"), func(t *model.Todo) error {
		if fb := strings.TrimSpace(req.Feedback); fb != "" {
			t.Feedback = append(t.Feedback, fb)
		}
		t.Status, t.Error = model.StatusQueued, ""
		t.AutoFixes, t.AutoMerge = 0, false // a human is involved again: fresh budget, review afterwards
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Todos.Kick()
	writeJSON(w, http.StatusOK, t)
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
			writeErr(w, fmt.Errorf("%w: todo has no worktree in this project", errBadRequest))
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
