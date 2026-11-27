package store

import (
	"errors"
	"slices"

	"github.com/klahr/dash/internal/model"
)

// OrgStore is the store as one organization sees it: only its projects, and
// the todos, scans and chats that belong to them. Records of other
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
	// user, if set, is the person working in the org: it reaches their
	// personal records there too (personal webhooks).
	user int64
}

// ForOrg returns the store as org sees it.
func (s *Store) ForOrg(org int64) *OrgStore { return &OrgStore{s: s, org: org} }

// AsUser returns the org's view for user, who also reaches their own
// personal records in it.
func (o *OrgStore) AsUser(user int64) *OrgStore { return &OrgStore{s: o.s, org: o.org, user: user} }

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
	if p.AddedBy == 0 && o.user > 0 {
		p.AddedBy = o.user // Dash's own tasks in it are theirs (see DashWorkFor)
	}
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

// UpdateTodo changes one of the org's todos. A change by a user (AsUser)
// that starts a run of it makes them its RunBy (see startsRun).
func (o *OrgStore) UpdateTodo(id string, fn func(*model.Todo) error) (model.Todo, error) {
	if !o.ownsTodo(id) {
		return model.Todo{}, ErrNotFound
	}
	return o.s.UpdateTodo(id, func(x *model.Todo) error {
		before := *x
		if err := fn(x); err != nil {
			return err
		}
		if o.user != 0 && startsRun(before, *x) {
			x.RunBy = o.user
		}
		return nil
	})
}

// startsRun reports whether a change of a todo from before to after starts
// a run of it, for its RunBy: it is queued (or resumed while queued), sent
// feedback, or what it gives Claude changes: its title or prompt (both in
// Claude's prompt), its attachments, its web-editor edits, or the task it
// builds on (whose Claude session it forks). Who does that is who the run
// is for, so a member can't put their instructions in another member's
// task and have it run on that member's own runner.
func startsRun(before, after model.Todo) bool {
	if after.Status == model.StatusQueued && (before.Status != model.StatusQueued || (before.Paused && !after.Paused)) {
		return true
	}
	return len(after.Feedback) > len(before.Feedback) ||
		after.Prompt != before.Prompt ||
		after.Title != before.Title ||
		after.BuildsOn != before.BuildsOn ||
		!slices.Equal(after.Attachments, before.Attachments) ||
		!slices.Equal(after.Edits, before.Edits)
}

func (o *OrgStore) SetStatus(id string, to model.TodoStatus, errMsg string) (model.Todo, error) {
	return o.UpdateTodo(id, statusFn(to, errMsg))
}

// DeleteTodo is Store.DeleteTodo for the org's tasks.
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

// ProjectChat is user's own chat about a project in the org.
func (o *OrgStore) ProjectChat(projectID string, user int64) ([]model.ChatMessage, error) {
	if !o.ownsProject(projectID) {
		return nil, ErrNotFound
	}
	return o.s.ProjectChat(projectID, user), nil
}

// AppendProjectChat adds msgs to user's own chat about a project in the org.
func (o *OrgStore) AppendProjectChat(projectID string, user int64, msgs ...model.ChatMessage) error {
	if !o.ownsProject(projectID) {
		return ErrNotFound
	}
	return o.s.AppendProjectChat(projectID, user, msgs...)
}

// RewindProjectChat takes the last turn out of user's own chat about a
// project in the org (see Store.RewindProjectChat).
func (o *OrgStore) RewindProjectChat(projectID string, user int64, n, keep int) (bool, error) {
	if !o.ownsProject(projectID) {
		return false, ErrNotFound
	}
	return o.s.RewindProjectChat(projectID, user, n, keep)
}

// ownsChat reports whether user's general chat (project "") or their chat
// about project is in the org.
func (o *OrgStore) ownsChat(user int64, project string) bool {
	if project == "" {
		return o.org != 0 && user != 0
	}
	return o.ownsProject(project)
}

// PutChatAside is Store.PutChatAside in the org.
func (o *OrgStore) PutChatAside(user int64, project string) error {
	if !o.ownsChat(user, project) {
		return ErrNotFound
	}
	return o.s.PutChatAside(o.org, user, project)
}

// PastChats is Store.PastChats in the org.
func (o *OrgStore) PastChats(user int64, project string) ([]model.PastChat, error) {
	if !o.ownsChat(user, project) {
		return nil, ErrNotFound
	}
	return o.s.PastChats(o.org, user, project)
}

// PastChatCount is Store.PastChatCount in the org.
func (o *OrgStore) PastChatCount(user int64, project string) (int, error) {
	if !o.ownsChat(user, project) {
		return 0, ErrNotFound
	}
	return o.s.PastChatCount(o.org, user, project)
}

// OpenPastChat is Store.OpenPastChat in the org.
func (o *OrgStore) OpenPastChat(user int64, project, id string) ([]model.ChatMessage, error) {
	if !o.ownsChat(user, project) {
		return nil, ErrNotFound
	}
	return o.s.OpenPastChat(o.org, user, project, id)
}

// DeletePastChat is Store.DeletePastChat in the org.
func (o *OrgStore) DeletePastChat(user int64, project, id string) error {
	if !o.ownsChat(user, project) {
		return ErrNotFound
	}
	return o.s.DeletePastChat(o.org, user, project, id)
}

// RestorePastChat is Store.RestorePastChat in the org.
func (o *OrgStore) RestorePastChat(user int64, project, id string) error {
	if !o.ownsChat(user, project) {
		return ErrNotFound
	}
	return o.s.RestorePastChat(o.org, user, project, id)
}

// ---- findings: reported issues, as draft tasks ----

// ReportFinding is Store.ReportFinding, for one of the org's projects.
func (o *OrgStore) ReportFinding(in model.Issue) (FindingOutcome, error) {
	if !o.ownsProject(in.ProjectID) {
		return FindingOutcome{}, ErrNotFound
	}
	return o.s.ReportFinding(in)
}

// OpenFindings are the org's tasks made from reported issues that aren't
// merged, in the projects keep accepts (nil keeps all), most severe first.
func (o *OrgStore) OpenFindings(keep func(projectID string) bool) []model.Todo {
	ts := o.s.listTodos(func(p string) bool { return (keep == nil || keep(p)) && o.ownsProject(p) })
	out := []model.Todo{}
	for _, t := range ts {
		if t.Finding != nil && t.Status != model.StatusMerged {
			out = append(out, t)
		}
	}
	slices.SortStableFunc(out, func(a, b model.Todo) int { return a.Finding.Severity().Rank() - b.Finding.Severity().Rank() })
	return out
}

// MergeTodos is Store.MergeTodos for the org's drafts.
func (o *OrgStore) MergeTodos(ids []string) (model.Todo, error) {
	for _, id := range ids {
		if !o.ownsTodo(id) {
			return model.Todo{}, ErrNotFound
		}
	}
	return o.s.MergeTodos(ids)
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
