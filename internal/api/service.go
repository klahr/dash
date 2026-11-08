package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/runner"
	"github.com/klahr/dash/internal/store"
)

// Data is all of the store the API can reach: one org's records at a time
// (ForOrg), plus which org a record is in. Handlers get their org's view from
// the caller (Server.service), so a query across orgs can't be written by
// accident; the background engines (queue, scans, CI, previews) keep the
// whole store.
type Data interface {
	ForOrg(org int64) *store.OrgStore
	ProjectOrg(projectID string) (int64, bool)
	TodoOrg(todoID string) (int64, bool)
	Accounts() *store.Accounts
}

// Caller is who a request or tool call is made for.
type Caller struct {
	User int64 // 0: another Dash (Machine), or Dash itself (a task's tools)
	Org  int64 // the org whose records they work with
	// OrgFolder is its folder: new projects go in <projects root>/<folder>/.
	// It was the org's slug when the org was made, and stays when the slug
	// is renamed.
	OrgFolder string
	// PlatformAdmin runs the Dash instance: other computers, the queue
	// pause, projects anywhere on disk.
	PlatformAdmin bool
	// Machine: the hub calling over this runner's tunnel. It acts for this Dash's default org (a runner keeps all its
	// projects there) and checked its own user's rights.
	Machine bool
}

var (
	errLogin     = errors.New("login required")
	errNoOrg     = errors.New("you aren't a member of any organization")
	errAdminOnly = errors.New("only platform admins can do that")
)

// caller is who r was made by: its session's user in their active org, or
// another Dash.
func (s *Server) caller(r *http.Request) (Caller, error) {
	if id, ok := auth.From(r.Context()); ok {
		if id.Org == nil {
			return Caller{}, errNoOrg
		}
		return Caller{User: id.User.ID, Org: id.Org.ID, OrgFolder: folderOf(id.Org.Org), PlatformAdmin: id.User.PlatformAdmin}, nil
	}
	if auth.FromMachine(r.Context()) && s.Store != nil {
		org, err := s.Store.Accounts().DefaultOrg()
		if err != nil {
			return Caller{}, fmt.Errorf("this Dash has no default organization: %w", err)
		}
		return Caller{Org: org.ID, OrgFolder: folderOf(org), PlatformAdmin: true, Machine: true}, nil
	}
	return Caller{}, errLogin
}

// folderOf is o's project folder (its slug, for an org loaded without one).
func folderOf(o store.Org) string {
	if o.Folder != "" {
		return o.Folder
	}
	return o.Slug
}

// service is the API as one caller may use it: their org's records, and
// the engines' actions on those records.
type service struct {
	s    *Server
	c    Caller
	data *store.OrgStore
}

// as returns the service for c.
func (s *Server) as(c Caller) *service {
	return &service{s: s, c: c, data: s.Store.ForOrg(c.Org)}
}

// service returns the service for r's caller, or answers r and returns false.
func (s *Server) service(w http.ResponseWriter, r *http.Request) (*service, bool) {
	c, err := s.caller(r)
	if err != nil {
		code := http.StatusUnauthorized
		if errors.Is(err, errNoOrg) {
			code = http.StatusForbidden
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return nil, false
	}
	return s.as(c), true
}

// adminOnly answers r with 403 and returns false unless its caller is a
// platform admin.
func (s *Server) adminOnly(w http.ResponseWriter, r *http.Request) bool {
	c, err := s.caller(r)
	if err == nil && c.PlatformAdmin {
		return true
	}
	if err == nil {
		err = errAdminOnly
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
	return false
}

// ---- tasks: what the REST API and the MCP tools share ----

// addTodo creates a draft, or queues it when enqueue is set, and names it if untitled.
func (sv *service) addTodo(t model.Todo, enqueue bool) (model.Todo, error) {
	t.CreatedBy = sv.c.User
	if err := sv.sendable(t.ProjectID, len(t.Attachments) > 0 || len(t.Edits) > 0); err != nil {
		return t, err
	}
	t, err := sv.data.CreateTodo(t)
	if err == nil && enqueue {
		t, err = sv.data.SetStatus(t.ID, model.StatusQueued, "")
		sv.s.Todos.Kick()
	}
	if err != nil {
		return t, err
	}
	if strings.TrimSpace(t.Title) == "" {
		sv.autoTitle(t.ID, t.Prompt)
	}
	return t, nil
}

// autoTitle names a todo in the background; a title typed meanwhile wins.
func (sv *service) autoTitle(id, prompt string) {
	if sv.s.Titler == nil {
		return
	}
	go func() {
		title := sv.title(context.Background(), prompt)
		if title == "" {
			return
		}
		_, _ = sv.data.UpdateTodo(id, func(t *model.Todo) error {
			if strings.TrimSpace(t.Title) == "" {
				t.Title = title
			}
			return nil
		})
	}()
}

func (sv *service) enqueue(id string) (model.Todo, error) {
	t, err := sv.data.SetStatus(id, model.StatusQueued, "")
	if err == nil {
		sv.s.Todos.Kick()
	}
	return t, err
}

// cancel stops a running task (accepted=true: the worker marks it failed)
// or moves a queued one back to draft.
func (sv *service) cancel(id string) (t model.Todo, accepted bool, err error) {
	cur, err := sv.data.GetTodo(id)
	if err != nil {
		return t, false, err
	}
	if cur.OnRunner() && cur.Status == model.StatusInProgress {
		// The runner stops it and reports it failed ("canceled").
		d, err := sv.runnerTask(cur)
		if err != nil {
			return t, false, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := d.Cancel(ctx, cur); err != nil {
			return t, false, fmt.Errorf("%w (if it stays away, the task fails by itself and can be queued again)", err)
		}
		return t, true, nil
	}
	if sv.s.Todos.Cancel(id) {
		return t, true, nil
	}
	t, err = sv.data.SetStatus(id, model.StatusDraft, "")
	return t, false, err
}

// editTodo applies u (and atts, when not nil) to a task, for PATCH
// /api/todos/{tid} and the board chat's update_todo. The prompt and
// attachments can only change while it is a draft, queued or failed.
func (sv *service) editTodo(id string, u mcptools.TodoUpdate, atts *[]model.Attachment) (model.Todo, error) {
	t, err := sv.data.UpdateTodo(id, func(t *model.Todo) error {
		editable := t.Status == model.StatusDraft || t.Status == model.StatusQueued || t.Status == model.StatusFailed
		if (u.Prompt != nil || atts != nil) && !editable {
			return fmt.Errorf("%w: instructions can only be edited while draft, queued or failed", errBadRequest)
		}
		set(&t.Attachments, atts)
		set(&t.Title, u.Title)
		set(&t.Prompt, u.Prompt)
		set(&t.Priority, u.Priority)
		set(&t.DependsOn, u.DependsOn) // validated by the store
		return nil
	})
	if err != nil {
		return t, err
	}
	sv.s.Todos.Kick() // priority or dependencies may have changed
	return t, nil
}

// removeTodo deletes a task and its worktree, unless it is running, merging
// or being reviewed. For DELETE /api/todos/{tid} and the delete_todo tools.
func (sv *service) removeTodo(ctx context.Context, id string) error {
	t, err := sv.data.GetTodo(id)
	if err != nil {
		return err
	}
	switch t.Status {
	case model.StatusInProgress, model.StatusMerging:
		return model.ErrInvalidTransition{From: t.Status, To: "deleted"}
	}
	if t.Reviewing {
		return store.ErrReviewing
	}
	if t.OnRunner() && sv.s.Dispatch != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			sv.s.Dispatch.Forget(ctx, t)
		}()
	} else if t.Worktree != nil && t.Status != model.StatusMerged && !sv.s.RunnerOnly {
		if p, err := sv.data.GetProject(t.ProjectID); err == nil {
			_ = sv.s.Git.RemoveWorktree(ctx, p.ID, p.Path, *t.Worktree)
		}
	}
	if _, err := sv.data.DeleteTodo(t.ID); err != nil {
		return err
	}
	if sv.s.Perms != nil {
		sv.s.Perms.Forget(t.ID)
	}
	sv.s.Todos.Log.Remove(t.ID)
	return nil
}

// approve sends a task in review (or a failed merge) to the merge queue.
func (sv *service) approve(id string) (model.Todo, error) {
	cur, err := sv.data.GetTodo(id)
	if err != nil {
		return cur, err
	}
	if cur.OnRunner() {
		return sv.approveOnRunner(context.Background(), cur)
	}
	t, err := sv.data.UpdateTodo(id, func(t *model.Todo) error {
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
		return t, err
	}
	if err := sv.s.Merges.Enqueue(model.MergeJob{TodoID: t.ID, ProjectID: t.ProjectID}); err != nil {
		_, _ = sv.data.SetStatus(t.ID, model.StatusReview, "")
		return t, err
	}
	return t, nil
}

// rework sends a reviewed (or failed) todo back to the queue with feedback;
// the next run reuses its worktree so Claude builds on its changes.
func (sv *service) rework(id, feedback string, atts []model.Attachment) (model.Todo, error) {
	if cur, err := sv.data.GetTodo(id); err == nil {
		if err := sv.sendable(cur.ProjectID, len(atts) > 0); err != nil {
			return cur, err
		}
	}
	t, err := sv.data.UpdateTodo(id, func(t *model.Todo) error {
		fb := strings.TrimSpace(feedback)
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
		return t, err
	}
	sv.s.Todos.Kick()
	return t, nil
}

// diff is the patch a task has made.
func (sv *service) diff(ctx context.Context, id string) (string, error) {
	t, err := sv.data.GetTodo(id)
	if err != nil {
		return "", err
	}
	if t.OnRunner() {
		b, err := sv.fetchFromRunner(ctx, t, "diff")
		return string(b), err
	}
	if t.Worktree == nil {
		return "", fmt.Errorf("%w: task has no worktree yet", errBadRequest)
	}
	p, err := sv.data.GetProject(t.ProjectID)
	if err != nil {
		return "", err
	}
	return sv.s.todoChanges(ctx, p, t)
}

// log is the task's Claude run log.
func (sv *service) log(id string) (string, error) {
	t, err := sv.data.GetTodo(id)
	if err != nil {
		return "", err
	}
	if t.OnRunner() {
		b, err := sv.fetchFromRunner(context.Background(), t, "log")
		if err != nil {
			// What the hub logged itself (sent, interrupted), and why the rest is missing.
			local, _ := sv.s.Todos.Log.Read(id)
			return local + "\n(" + err.Error() + ")\n", nil
		}
		return string(b), nil
	}
	return sv.s.Todos.Log.Read(id)
}

// ---- projects on disk, processes ----

// projectsRoot is the folder the caller's projects go in: the projects root
// for another Dash, else the org's own folder inside it.
func (sv *service) projectsRoot() (string, error) {
	if sv.c.Machine {
		return sv.s.ProjectRoot, nil
	}
	org := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(sv.c.OrgFolder), "-"), "-")
	if org == "" {
		return "", fmt.Errorf("%w: your organization has no folder for projects", errBadRequest)
	}
	return filepath.Join(sv.s.ProjectRoot, org), nil
}

// projectPath is where a new project named name goes. Platform admins may
// give any path; everyone else gets, or must give, a folder inside their
// org's own folder, <projects root>/<org folder>/, so one org can't adopt
// another's checkout (or any other directory on the server). Another Dash
// (a hub adding a project on its runner) keeps to the projects root, which
// the runner guard checks.
func (sv *service) projectPath(name, path string) (string, error) {
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	root, err := sv.projectsRoot()
	if err != nil {
		return "", err
	}
	if path == "" {
		if slug == "" {
			return "", fmt.Errorf("%w: the name needs a letter or digit to make a folder from", errBadRequest)
		}
		return filepath.Join(root, slug), nil
	}
	path = store.CleanPath(path)
	if sv.c.PlatformAdmin {
		return path, nil
	}
	if sv.s.ProjectRoot == "" || !within(root, path) || filepath.Clean(path) == filepath.Clean(root) {
		return "", fmt.Errorf("%w: projects in your organization go in a folder inside %s", errBadRequest, root)
	}
	return path, nil
}

// process is one of the processes of the caller's org's projects.
func (sv *service) process(id string) (model.Process, error) {
	if sv.s.Procs == nil {
		return model.Process{}, runner.ErrNotFound
	}
	p, err := sv.s.Procs.Get(id)
	if err != nil {
		return p, err
	}
	if !sv.data.OwnsProject(p.ProjectID) {
		return model.Process{}, runner.ErrNotFound
	}
	return p, nil
}
