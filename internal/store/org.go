package store

import (
	"errors"

	"github.com/klahr/dash/internal/model"
)

// OrgStore is the store as one organization sees it: only its projects, and
// the todos, issues, scans and chats that belong to them. Records of other
// orgs don't exist here: reading or changing one is ErrNotFound, and lists
// leave them out. The HTTP API and the MCP tools reach records only through
// an OrgStore (Store.ForOrg), so a handler can't read across orgs by
// forgetting a check.
//
// A record's org is its project's, and a project never changes org, so the
// checks below hold for as long as the record exists.
type OrgStore struct {
	s   *Store
	org int64
}

// ForOrg returns the store as org sees it.
func (s *Store) ForOrg(org int64) *OrgStore { return &OrgStore{s: s, org: org} }

// OrgID is the org this view belongs to.
func (o *OrgStore) OrgID() int64 { return o.org }

// ownsProject reports whether projectID exists and belongs to the org.
func (o *OrgStore) ownsProject(projectID string) bool {
	org, ok := o.s.ProjectOrg(projectID)
	return ok && o.org != 0 && org == o.org
}

// ownsTodo reports whether todoID exists and belongs to the org.
func (o *OrgStore) ownsTodo(todoID string) bool {
	org, ok := o.s.TodoOrg(todoID)
	return ok && o.org != 0 && org == o.org
}

func (o *OrgStore) ownsIssue(id string) bool {
	o.s.mu.RLock()
	is, ok := o.s.data.Issues[id]
	o.s.mu.RUnlock()
	return ok && o.ownsProject(is.ProjectID)
}

func (o *OrgStore) ownsScanRun(id string) bool {
	o.s.mu.RLock()
	r, ok := o.s.data.ScanRuns[id]
	o.s.mu.RUnlock()
	return ok && o.ownsProject(r.ProjectID)
}

// OwnsProject reports whether projectID is one of the org's projects, for
// callers that act on it through another service (git, processes, previews).
func (o *OrgStore) OwnsProject(projectID string) bool { return o.ownsProject(projectID) }

// ---- projects ----

func (o *OrgStore) ListProjects() []model.Project {
	var out []model.Project
	for _, p := range o.s.ListProjects() {
		if p.OrgID == o.org && o.org != 0 {
			out = append(out, p)
		}
	}
	return out
}

func (o *OrgStore) GetProject(id string) (model.Project, error) {
	if !o.ownsProject(id) {
		return model.Project{}, ErrNotFound
	}
	return o.s.GetProject(id)
}

// CreateProject adds a project to the org.
func (o *OrgStore) CreateProject(p model.Project) (model.Project, error) {
	if o.org == 0 {
		return p, errors.New("no organization to create the project in")
	}
	p.OrgID = o.org
	return o.s.CreateProject(p)
}

func (o *OrgStore) UpdateProject(id string, fn func(*model.Project) error) (model.Project, error) {
	if !o.ownsProject(id) {
		return model.Project{}, ErrNotFound
	}
	return o.s.UpdateProject(id, fn)
}

// ---- todos ----

// ListTodos is Store.ListTodos within the org; projectID "" means all of
// the org's projects.
func (o *OrgStore) ListTodos(projectID string, statuses ...model.TodoStatus) []model.Todo {
	if projectID != "" {
		if !o.ownsProject(projectID) {
			return nil
		}
		return o.s.ListTodos(projectID, statuses...)
	}
	return o.s.listTodos(o.ownsProject, statuses...)
}

func (o *OrgStore) GetTodo(id string) (model.Todo, error) {
	if !o.ownsTodo(id) {
		return model.Todo{}, ErrNotFound
	}
	return o.s.GetTodo(id)
}

// CreateTodo adds a draft to one of the org's projects. Its dependencies
// must be in the same project (the store checks), hence in the org too.
func (o *OrgStore) CreateTodo(t model.Todo) (model.Todo, error) {
	if !o.ownsProject(t.ProjectID) {
		return t, ErrNotFound
	}
	return o.s.CreateTodo(t)
}

// CreateQueuedTodo is CreateTodo for a todo queued from the start (see
// Store.CreateQueuedTodo).
func (o *OrgStore) CreateQueuedTodo(t model.Todo) (model.Todo, error) {
	if !o.ownsProject(t.ProjectID) {
		return t, ErrNotFound
	}
	return o.s.CreateQueuedTodo(t)
}

func (o *OrgStore) UpdateTodo(id string, fn func(*model.Todo) error) (model.Todo, error) {
	if !o.ownsTodo(id) {
		return model.Todo{}, ErrNotFound
	}
	return o.s.UpdateTodo(id, fn)
}

func (o *OrgStore) SetStatus(id string, to model.TodoStatus, errMsg string) (model.Todo, error) {
	if !o.ownsTodo(id) {
		return model.Todo{}, ErrNotFound
	}
	return o.s.SetStatus(id, to, errMsg)
}

func (o *OrgStore) DeleteTodo(id string) (model.Todo, error) {
	if !o.ownsTodo(id) {
		return model.Todo{}, ErrNotFound
	}
	return o.s.DeleteTodo(id)
}

// ---- chats ----

// Chat is user's general chat in the org.
func (o *OrgStore) Chat(user int64) []model.ChatMessage {
	if o.org == 0 || user == 0 {
		return []model.ChatMessage{}
	}
	return o.s.Chat(o.org, user)
}

// SetChat replaces user's general chat in the org.
func (o *OrgStore) SetChat(user int64, msgs []model.ChatMessage) error {
	if o.org == 0 || user == 0 {
		return ErrNotFound
	}
	return o.s.SetChat(o.org, user, msgs)
}

func (o *OrgStore) TodoChat(todoID string) ([]model.ChatMessage, error) {
	if !o.ownsTodo(todoID) {
		return nil, ErrNotFound
	}
	return o.s.TodoChat(todoID), nil
}

func (o *OrgStore) SetTodoChat(todoID string, msgs []model.ChatMessage) error {
	if !o.ownsTodo(todoID) {
		return ErrNotFound
	}
	return o.s.SetTodoChat(todoID, msgs)
}

func (o *OrgStore) ProjectChat(projectID string) ([]model.ChatMessage, error) {
	if !o.ownsProject(projectID) {
		return nil, ErrNotFound
	}
	return o.s.ProjectChat(projectID), nil
}

func (o *OrgStore) SetProjectChat(projectID string, msgs []model.ChatMessage) error {
	if !o.ownsProject(projectID) {
		return ErrNotFound
	}
	return o.s.SetProjectChat(projectID, msgs)
}

// ---- issues ----

func (o *OrgStore) GetIssue(id string) (model.Issue, error) {
	if !o.ownsIssue(id) {
		return model.Issue{}, ErrNotFound
	}
	return o.s.GetIssue(id)
}

// ListIssues is a project's issues; "" means all of the org's.
func (o *OrgStore) ListIssues(projectID string) []model.Issue {
	if projectID != "" && !o.ownsProject(projectID) {
		return []model.Issue{}
	}
	return o.s.listIssues(func(is *model.Issue) bool {
		return (projectID == "" || is.ProjectID == projectID) && o.ownsProject(is.ProjectID)
	})
}

// OpenIssues is the open issues of the org's projects that keep accepts
// (nil keeps all), most severe first, then newest.
func (o *OrgStore) OpenIssues(keep func(projectID string) bool) []model.Issue {
	return o.s.listIssues(func(is *model.Issue) bool {
		return is.Status == model.IssueOpen && (keep == nil || keep(is.ProjectID)) && o.ownsProject(is.ProjectID)
	})
}

func (o *OrgStore) UpsertIssue(in model.Issue) (model.Issue, model.UpsertResult, error) {
	if !o.ownsProject(in.ProjectID) {
		return in, "", ErrNotFound
	}
	return o.s.UpsertIssue(in)
}

func (o *OrgStore) UpsertIssues(projectID, source string, in []model.Issue) (model.UpsertCounts, error) {
	if !o.ownsProject(projectID) {
		return model.UpsertCounts{}, ErrNotFound
	}
	return o.s.UpsertIssues(projectID, source, in)
}

func (o *OrgStore) UpdateIssue(id string, fn func(*model.Issue) error) (model.Issue, error) {
	if !o.ownsIssue(id) {
		return model.Issue{}, ErrNotFound
	}
	return o.s.UpdateIssue(id, fn)
}

func (o *OrgStore) SetIssueStatus(id string, to model.IssueStatus, reason string) (model.Issue, error) {
	if !o.ownsIssue(id) {
		return model.Issue{}, ErrNotFound
	}
	return o.s.SetIssueStatus(id, to, reason)
}

// LinkIssues is Store.LinkIssues; the issues must be in the todo's project
// (the store checks), hence in the org.
func (o *OrgStore) LinkIssues(ids []string, todoID string) ([]model.Issue, error) {
	if !o.ownsTodo(todoID) {
		return nil, ErrNotFound
	}
	return o.s.LinkIssues(ids, todoID)
}

func (o *OrgStore) DeleteIssue(id string) (model.Issue, error) {
	if !o.ownsIssue(id) {
		return model.Issue{}, ErrNotFound
	}
	return o.s.DeleteIssue(id)
}

// ---- scans ----

func (o *OrgStore) GetScanRun(id string) (model.ScanRun, error) {
	if !o.ownsScanRun(id) {
		return model.ScanRun{}, ErrNotFound
	}
	return o.s.GetScanRun(id)
}

func (o *OrgStore) ListScanRuns(projectID, scan string) []model.ScanRun {
	if !o.ownsProject(projectID) {
		return []model.ScanRun{}
	}
	return o.s.ListScanRuns(projectID, scan)
}

func (o *OrgStore) LastScanRun(projectID, scan string, keep func(model.ScanRun) bool) (model.ScanRun, bool) {
	if !o.ownsProject(projectID) {
		return model.ScanRun{}, false
	}
	return o.s.LastScanRun(projectID, scan, keep)
}

func (o *OrgStore) ScanSetting(projectID, scan string) (model.ScanSetting, bool) {
	if !o.ownsProject(projectID) {
		return model.ScanSetting{ProjectID: projectID, Scan: scan}, false
	}
	return o.s.ScanSetting(projectID, scan)
}

func (o *OrgStore) SetScanSetting(projectID, scan string, fn func(*model.ScanSetting)) (model.ScanSetting, error) {
	if !o.ownsProject(projectID) {
		return model.ScanSetting{}, ErrNotFound
	}
	return o.s.SetScanSetting(projectID, scan, fn)
}

func (o *OrgStore) ScanTemplates(projectID string) []model.ScanTemplate {
	if !o.ownsProject(projectID) {
		return []model.ScanTemplate{}
	}
	return o.s.ScanTemplates(projectID)
}

func (o *OrgStore) ScanTemplate(projectID, name string) (model.ScanTemplate, error) {
	if !o.ownsProject(projectID) {
		return model.ScanTemplate{}, ErrNotFound
	}
	return o.s.ScanTemplate(projectID, name)
}

func (o *OrgStore) SaveScanTemplate(t model.ScanTemplate, create bool) (model.ScanTemplate, error) {
	if !o.ownsProject(t.ProjectID) {
		return t, ErrNotFound
	}
	return o.s.SaveScanTemplate(t, create)
}

func (o *OrgStore) DeleteScanTemplate(projectID, name string, keepSettings bool) error {
	if !o.ownsProject(projectID) {
		return ErrNotFound
	}
	return o.s.DeleteScanTemplate(projectID, name, keepSettings)
}
