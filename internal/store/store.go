// Package store keeps Dash state in memory and persists it to SQLite.
//
// Reads are served from the in-memory copy under an RWMutex; every mutation
// changes memory and writes the rows it touched in one transaction while
// holding the write lock. If the write fails, memory is reloaded from the
// database so the two never disagree.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/metrics"
	"github.com/klahr/dash/internal/model"
)

var ErrNotFound = errors.New("not found")

// ErrReviewing is returned when a todo Claude is reviewing would change status.
var ErrReviewing = errors.New("task is being reviewed; wait until the review finishes")

// ErrBadDependency is returned when a todo's DependsOn names itself, a todo
// that doesn't exist or is in another project, or would make a cycle.
var ErrBadDependency = errors.New("invalid dependency")

// CleanPath expands a leading ~ and makes path absolute, so a project path
// typed as "~/src/app" doesn't resolve against the server's working directory.
func CleanPath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[1:])
		}
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// Publisher receives an event after every successful mutation.
type Publisher interface{ Publish(model.Event) }

type snapshot struct {
	Projects map[string]*model.Project `json:"projects"`
	Todos    map[string]*model.Todo    `json:"todos"`
	// Chat is the general chat of a state.json from before users; it is
	// imported as the first user's (see Accounts.Bootstrap).
	Chat []model.ChatMessage `json:"chat,omitempty"`
	// MainChats are the general chats, one per user and org, keyed by
	// mainChatOwner.
	MainChats map[string][]model.ChatMessage `json:"-"`
	// TodoChats are conversations about one todo, keyed by todo id. Kept
	// apart from Todo so they aren't broadcast with every todo update.
	TodoChats map[string][]model.ChatMessage `json:"todo_chats,omitempty"`
	// ProjectChats are conversations about one project, keyed by project id.
	ProjectChats map[string][]model.ChatMessage `json:"project_chats,omitempty"`
	// Issues are each project's known bugs, vulnerabilities and other
	// problems, keyed by issue id.
	Issues map[string]*model.Issue `json:"issues,omitempty"`
	// issueIDs indexes Issues by issueKey(project id, fingerprint) → issue
	// id, so an upsert finds the issue it updates without scanning them all.
	// nil until built (see Store.issueIndexLocked).
	issueIDs map[string]string
	// ScanRuns are the recent runs of each project's scans, keyed by run id.
	ScanRuns map[string]*model.ScanRun `json:"scan_runs,omitempty"`
	// ScanSettings are the schedule changes and approvals users made, keyed by
	// "<project id>/<scan name>".
	ScanSettings map[string]*model.ScanSetting `json:"scan_settings,omitempty"`
	// ScanTemplates are the custom scans users wrote in Dash, keyed by
	// "<project id>/<scan name>".
	ScanTemplates map[string]*model.ScanTemplate `json:"scan_templates,omitempty"`
}

type Store struct {
	mu   sync.RWMutex
	db   *sql.DB
	pub  Publisher
	data snapshot
	// orgs mirrors each project's org (project id → int64) outside mu, so
	// the event hub can look it up while a mutation holding mu publishes.
	orgs sync.Map
	// Now, if set, is the clock of scan settings' UpdatedAt, which scan
	// schedules count from (tests); default time.Now.
	Now func() time.Time
	// SlugReserved, if set, reports whether a slug is reserved for org
	// subdomains (default: DefaultReservedSlugs). Set it before serving.
	SlugReserved func(slug string) bool
}

func NewID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Open opens (or creates) the SQLite database at path and loads it. On
// first start, a state.json beside it is imported once. Todos that were in
// progress when Dash stopped are put back in the queue; their worktrees are
// reused.
func Open(path string, pub Publisher) (*Store, error) {
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	s, err := open(db, path, pub)
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func open(db *sql.DB, path string, pub Publisher) (*Store, error) {
	if err := migrate(db, func(from, to int) error {
		dest := PreMigrationBackup(path, from, to, time.Now())
		if err := Backup(context.Background(), path, dest); err != nil {
			return err
		}
		slog.Info("database backed up before migrating", "from", from, "to", to, "backup", dest)
		return nil
	}); err != nil {
		return nil, err
	}
	if legacy := filepath.Join(filepath.Dir(path), LegacyFile); legacy != filepath.Clean(path) {
		if err := importLegacy(db, legacy); err != nil {
			return nil, err
		}
	}
	data, err := load(db)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, pub: pub, data: data}
	s.indexOrgsLocked()
	var projects []*model.Project
	for _, p := range s.data.Projects {
		if clean := CleanPath(p.Path); clean != p.Path {
			p.Path = clean
			projects = append(projects, p)
		}
	}
	var todos []*model.Todo
	for _, t := range s.data.Todos {
		// A todo running on a runner keeps running there; the runner
		// reports on it again when it reconnects.
		if (t.Status == model.StatusInProgress && t.Runner == "") || t.Reviewing {
			if t.Status == model.StatusInProgress && t.Runner == "" {
				t.Status = model.StatusQueued
			}
			t.Reviewing = false // the chat turn died with the old process
			todos = append(todos, t)
		}
		// Merging todos stay merging: their jobs are lost with the old
		// process, but MergeQueue.Resume queues them again.
	}
	var runs []*model.ScanRun
	for _, r := range s.data.ScanRuns {
		if r.Status == model.ScanRunning { // the agent died with the old process (a crash or kill -9)
			now := time.Now()
			r.Status, r.FinishedAt, r.Activity = model.ScanInterrupted, &now, ""
			r.Note = "Dash stopped during the scan"
			runs = append(runs, r)
		}
	}
	err = inTx(db, func(tx *sql.Tx) error {
		for _, p := range projects {
			if err := putProject(tx, p); err != nil {
				return err
			}
		}
		for _, t := range todos {
			if err := putTodo(tx, t); err != nil {
				return err
			}
		}
		for _, r := range runs {
			if err := putScanRun(tx, r); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Close closes the database. The store must not be used afterwards.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}

// writeLocked persists a mutation the caller has made in memory: fn writes
// the rows it changed in one transaction. If that fails, memory is reloaded
// from the database, undoing the mutation, and the error is returned.
func (s *Store) writeLocked(fn func(*sql.Tx) error) error {
	err := inTx(s.db, fn)
	if err == nil {
		return nil
	}
	if data, lerr := load(s.db); lerr == nil {
		s.data = data
		s.indexOrgsLocked()
	}
	return err
}

// emit announces a change to projectID's records, as an event of the
// project's org. The caller holds the lock.
func (s *Store) emit(t model.EventType, projectID, todoID string, data any) {
	var org int64
	if p, ok := s.data.Projects[projectID]; ok {
		org = p.OrgID
	}
	s.emitTo(org, t, projectID, todoID, data)
}

func (s *Store) emitTo(org int64, t model.EventType, projectID, todoID string, data any) {
	if org == 0 {
		org = -1 // a project without an org (no accounts yet): only for subscribers of every org
	}
	if s.pub != nil {
		s.pub.Publish(model.Event{Type: t, ProjectID: projectID, TodoID: todoID, Data: data, Time: time.Now(), OrgID: org})
	}
}

// reload replaces memory with what the database holds, after the account
// code has changed rows beneath the store (Accounts.Bootstrap).
func (s *Store) reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := load(s.db)
	if err != nil {
		return err
	}
	s.data = data
	s.indexOrgsLocked()
	return nil
}

// indexOrgsLocked rebuilds s.orgs from memory.
func (s *Store) indexOrgsLocked() {
	s.orgs.Clear()
	for id, p := range s.data.Projects {
		s.orgs.Store(id, p.OrgID)
	}
}

// defaultOrgLocked is the org a project created without one goes in, or 0
// before there are any orgs.
func (s *Store) defaultOrgLocked() int64 {
	var org sql.NullInt64
	_ = s.db.QueryRow("SELECT default_org_id FROM instance WHERE id = 1").Scan(&org)
	return org.Int64
}

// ProjectOrg is the org a project belongs to. It doesn't take the store's
// lock, so it may be called while publishing an event.
func (s *Store) ProjectOrg(projectID string) (int64, bool) {
	v, ok := s.orgs.Load(projectID)
	if !ok {
		return 0, false
	}
	return v.(int64), true
}

// TodoOrg is the org a todo belongs to: its project's.
func (s *Store) TodoOrg(todoID string) (int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.data.Todos[todoID]
	if !ok {
		return 0, false
	}
	p, ok := s.data.Projects[t.ProjectID]
	if !ok {
		return 0, false
	}
	return p.OrgID, true
}

// ---- Projects ----

func (s *Store) CreateProject(p model.Project) (model.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		p.ID = NewID()
	}
	p.CreatedAt = time.Now()
	if p.OrgID == 0 {
		p.OrgID = s.defaultOrgLocked()
	}
	s.data.Projects[p.ID] = &p
	if err := s.writeLocked(func(tx *sql.Tx) error { return putProject(tx, &p) }); err != nil {
		return p, err
	}
	s.orgs.Store(p.ID, p.OrgID)
	s.emit(model.EvProjectUpdated, p.ID, "", p)
	return p, nil
}

func (s *Store) GetProject(id string) (model.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.data.Projects[id]
	if !ok {
		return model.Project{}, ErrNotFound
	}
	return *p, nil
}

func (s *Store) ListProjects() []model.Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Project, 0, len(s.data.Projects))
	for _, p := range s.data.Projects {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (s *Store) UpdateProject(id string, fn func(*model.Project) error) (model.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.data.Projects[id]
	if !ok {
		return model.Project{}, ErrNotFound
	}
	cp := *p
	if err := fn(&cp); err != nil {
		return *p, err
	}
	cp.ID, cp.OrgID = id, p.OrgID // a project never changes org
	s.data.Projects[id] = &cp
	if err := s.writeLocked(func(tx *sql.Tx) error { return putProject(tx, &cp) }); err != nil {
		return cp, err
	}
	s.emit(model.EvProjectUpdated, id, "", cp)
	return cp, nil
}

// DeleteProject forgets a project with its todos and chats, and returns the
// todos it removed. Files on disk are the caller's business.
func (s *Store) DeleteProject(id string) ([]model.Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	proj, ok := s.data.Projects[id]
	if !ok {
		return nil, ErrNotFound
	}
	org := proj.OrgID
	var todos []model.Todo
	for tid, t := range s.data.Todos {
		if t.ProjectID == id {
			todos = append(todos, *t)
			delete(s.data.Todos, tid)
			delete(s.data.TodoChats, tid)
		}
	}
	for iid, is := range s.data.Issues {
		if is.ProjectID == id {
			delete(s.data.Issues, iid)
			if s.data.issueIDs != nil {
				delete(s.data.issueIDs, issueKey(is.ProjectID, is.Fingerprint))
			}
		}
	}
	for rid, r := range s.data.ScanRuns {
		if r.ProjectID == id {
			delete(s.data.ScanRuns, rid)
		}
	}
	for k, st := range s.data.ScanSettings {
		if st.ProjectID == id {
			delete(s.data.ScanSettings, k)
		}
	}
	for k, t := range s.data.ScanTemplates {
		if t.ProjectID == id {
			delete(s.data.ScanTemplates, k)
		}
	}
	delete(s.data.Projects, id)
	delete(s.data.ProjectChats, id)
	err := s.writeLocked(func(tx *sql.Tx) error {
		for _, t := range todos {
			if err := setChat(tx, chatTodo, t.ID, org, nil); err != nil {
				return err
			}
		}
		for _, table := range []string{"todos", "issues", "scan_runs", "scan_settings", "scan_templates", "webhooks",
			"tracker_projects", "tracker_links", "project_events"} {
			if _, err := tx.Exec("DELETE FROM "+table+" WHERE project_id = ?", id); err != nil {
				return err
			}
		}
		if err := setChat(tx, chatProject, id, org, nil); err != nil {
			return err
		}
		return deleteRow(tx, "projects", id)
	})
	if err != nil {
		return todos, err
	}
	s.orgs.Delete(id)
	s.emitTo(org, model.EvProjectDeleted, id, "", nil)
	return todos, nil
}

// ToolAllowed reports whether tool is on the project's always-allowed list.
func (s *Store) ToolAllowed(projectID, tool string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.data.Projects[projectID]
	return ok && slices.Contains(p.AllowedTools, tool)
}

// AllowTool adds tool to the project's always-allowed list.
func (s *Store) AllowTool(projectID, tool string) error {
	_, err := s.UpdateProject(projectID, func(p *model.Project) error {
		if !slices.Contains(p.AllowedTools, tool) {
			p.AllowedTools = append(append([]string(nil), p.AllowedTools...), tool)
		}
		return nil
	})
	return err
}

// ---- Todos ----

func (s *Store) CreateTodo(t model.Todo) (model.Todo, error) {
	return s.createTodo(t, model.StatusDraft)
}

// CreateQueuedTodo is CreateTodo for a todo that is queued from the start,
// never seen as a draft: a hub reads a draft task on its runner as one
// canceled there.
func (s *Store) CreateQueuedTodo(t model.Todo) (model.Todo, error) {
	return s.createTodo(t, model.StatusQueued)
}

func (s *Store) createTodo(t model.Todo, status model.TodoStatus) (model.Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Projects[t.ProjectID]; !ok {
		return t, ErrNotFound
	}
	t.ID = NewID()
	t.Status = status
	t.CreatedAt = time.Now()
	t.UpdatedAt = t.CreatedAt
	t.DependsOn = cleanIDs(t.DependsOn)
	if err := s.checkDepsLocked(t); err != nil {
		return t, err
	}
	s.data.Todos[t.ID] = &t
	if err := s.writeLocked(func(tx *sql.Tx) error { return putTodo(tx, &t) }); err != nil {
		return t, err
	}
	s.emit(model.EvTodoUpdated, t.ProjectID, t.ID, t)
	return t, nil
}

func (s *Store) GetTodo(id string) (model.Todo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.data.Todos[id]
	if !ok {
		return model.Todo{}, ErrNotFound
	}
	return *t, nil
}

// ListTodos returns a project's todos in queue order (model.QueueBefore:
// prioritised ones by priority then age, then unprioritised ones by age).
// An empty status list means all statuses.
func (s *Store) ListTodos(projectID string, statuses ...model.TodoStatus) []model.Todo {
	return s.listTodos(func(p string) bool { return projectID == "" || p == projectID }, statuses...)
}

// listTodos is ListTodos for the todos whose project keep accepts: it
// filters before copying and sorting, so a narrow list stays cheap however
// many todos the store holds.
func (s *Store) listTodos(keep func(projectID string) bool, statuses ...model.TodoStatus) []model.Todo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []model.Todo
	for _, t := range s.data.Todos {
		if !keep(t.ProjectID) {
			continue
		}
		if len(statuses) > 0 && !slices.Contains(statuses, t.Status) {
			continue
		}
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return model.QueueBefore(out[i], out[j]) })
	return out
}

// UpdateTodo applies fn to a copy of the todo. If fn changes Status the
// transition is validated. The todo is persisted and broadcast.
func (s *Store) UpdateTodo(id string, fn func(*model.Todo) error) (model.Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.data.Todos[id]
	if !ok {
		return model.Todo{}, ErrNotFound
	}
	cp := clone(*t)
	if err := fn(&cp); err != nil {
		return *t, err
	}
	if cp.Status != t.Status && t.Reviewing {
		return *t, ErrReviewing
	}
	if cp.Status != t.Status && !model.CanTransition(t.Status, cp.Status) {
		return *t, model.ErrInvalidTransition{From: t.Status, To: cp.Status}
	}
	if !slices.Equal(cp.DependsOn, t.DependsOn) {
		cp.DependsOn = cleanIDs(cp.DependsOn)
		cp.ID, cp.ProjectID = t.ID, t.ProjectID
		if err := s.checkDepsLocked(cp); err != nil {
			return *t, err
		}
	}
	cp.ID, cp.ProjectID, cp.UpdatedAt = t.ID, t.ProjectID, time.Now()
	s.data.Todos[id] = &cp
	var resolved []model.Issue
	if cp.Status == model.StatusMerged && t.Status != model.StatusMerged {
		resolved = s.resolveIssuesLocked(id)
	}
	err := s.writeLocked(func(tx *sql.Tx) error {
		if err := putTodo(tx, &cp); err != nil {
			return err
		}
		return putIssues(tx, resolved)
	})
	if err != nil {
		return cp, err
	}
	if cp.Status == model.StatusFailed && t.Status != model.StatusFailed {
		metrics.TasksFailed.Inc()
	}
	s.emit(model.EvTodoUpdated, cp.ProjectID, cp.ID, cp)
	s.emitIssues(resolved)
	return cp, nil
}

// SetStatus is a convenience wrapper around UpdateTodo.
func (s *Store) SetStatus(id string, to model.TodoStatus, errMsg string) (model.Todo, error) {
	return s.UpdateTodo(id, func(t *model.Todo) error {
		t.Status = to
		t.Error = errMsg
		if to != model.StatusQueued {
			t.Paused, t.Waiting = false, ""
		}
		now := time.Now()
		switch to {
		case model.StatusInProgress:
			t.StartedAt, t.FinishedAt = &now, nil
		case model.StatusReview, model.StatusFailed, model.StatusMerged:
			t.FinishedAt = &now
		}
		return nil
	})
}

func (s *Store) DeleteTodo(id string) (model.Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.data.Todos[id]
	if !ok {
		return model.Todo{}, ErrNotFound
	}
	delete(s.data.Todos, id)
	delete(s.data.TodoChats, id)
	unlinked := s.unlinkIssuesLocked(id)
	// Forget the deleted todo in the dependencies of others.
	var changed []*model.Todo
	for oid, o := range s.data.Todos {
		if i := slices.Index(o.DependsOn, id); i >= 0 {
			cp := clone(*o)
			cp.DependsOn = slices.Delete(cp.DependsOn, i, i+1)
			cp.UpdatedAt = time.Now()
			s.data.Todos[oid] = &cp
			changed = append(changed, &cp)
		}
	}
	err := s.writeLocked(func(tx *sql.Tx) error {
		if err := deleteRow(tx, "todos", id); err != nil {
			return err
		}
		if err := setChat(tx, chatTodo, id, 0, nil); err != nil {
			return err
		}
		for _, o := range changed {
			if err := putTodo(tx, o); err != nil {
				return err
			}
		}
		return putIssues(tx, unlinked)
	})
	if err != nil {
		return *t, err
	}
	s.emit(model.EvTodoDeleted, t.ProjectID, id, nil)
	for _, o := range changed {
		s.emit(model.EvTodoUpdated, o.ProjectID, o.ID, *o)
	}
	s.emitIssues(unlinked)
	return *t, nil
}

// checkDepsLocked validates t.DependsOn against the stored todos: each must
// exist in t's project, not be t, and not (transitively) depend on t.
func (s *Store) checkDepsLocked(t model.Todo) error {
	for _, id := range t.DependsOn {
		if id == t.ID {
			return fmt.Errorf("%w: a task can't depend on itself", ErrBadDependency)
		}
		d, ok := s.data.Todos[id]
		if !ok {
			return fmt.Errorf("%w: no task %q", ErrBadDependency, id)
		}
		if d.ProjectID != t.ProjectID {
			return fmt.Errorf("%w: task %q is in another project", ErrBadDependency, id)
		}
	}
	// A cycle needs a path from one of t's dependencies back to t.
	seen := map[string]bool{}
	var reaches func(id string) bool
	reaches = func(id string) bool {
		if id == t.ID {
			return true
		}
		if seen[id] {
			return false
		}
		seen[id] = true
		if d, ok := s.data.Todos[id]; ok {
			for _, n := range d.DependsOn {
				if reaches(n) {
					return true
				}
			}
		}
		return false
	}
	for _, id := range t.DependsOn {
		if reaches(id) {
			title := s.data.Todos[id].Title
			if title == "" {
				title = id
			}
			return fmt.Errorf("%w: %q already depends on this task, which would make a cycle", ErrBadDependency, title)
		}
	}
	return nil
}

// cleanIDs trims ids and drops empty ones and duplicates.
func cleanIDs(ids []string) []string {
	var out []string
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// ---- Chat ----

// mainChatOwner is the owner_id of a user's general chat in an org.
func mainChatOwner(org, user int64) string { return fmt.Sprintf("%d:%d", org, user) }

// Chat is user's general chat in org.
func (s *Store) Chat(org, user int64) []model.ChatMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.ChatMessage{}, s.data.MainChats[mainChatOwner(org, user)]...)
}

// SetChat replaces user's general chat in org (nil clears it).
func (s *Store) SetChat(org, user int64, msgs []model.ChatMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := mainChatOwner(org, user)
	prev := s.data.MainChats[owner]
	if s.data.MainChats == nil {
		s.data.MainChats = map[string][]model.ChatMessage{}
	}
	if len(msgs) == 0 {
		delete(s.data.MainChats, owner)
	} else {
		s.data.MainChats[owner] = append([]model.ChatMessage(nil), msgs...)
	}
	return s.writeLocked(func(tx *sql.Tx) error { return updateChat(tx, chatMain, owner, org, prev, msgs) })
}

// MainChats are every user's general chats, in no order (for cleanup).
func (s *Store) MainChats() [][]model.ChatMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([][]model.ChatMessage, 0, len(s.data.MainChats))
	for _, msgs := range s.data.MainChats {
		out = append(out, append([]model.ChatMessage(nil), msgs...))
	}
	return out
}

// projectOrgLocked is the org of a project (0 if there is none).
func (s *Store) projectOrgLocked(projectID string) int64 {
	if p, ok := s.data.Projects[projectID]; ok {
		return p.OrgID
	}
	return 0
}

func (s *Store) TodoChat(todoID string) []model.ChatMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.ChatMessage{}, s.data.TodoChats[todoID]...)
}

// SetTodoChat replaces the conversation about a todo (nil clears it).
func (s *Store) SetTodoChat(todoID string, msgs []model.ChatMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.data.Todos[todoID]
	if !ok {
		return ErrNotFound
	}
	org := s.projectOrgLocked(t.ProjectID)
	prev := s.data.TodoChats[todoID]
	if len(msgs) == 0 {
		delete(s.data.TodoChats, todoID)
	} else {
		if s.data.TodoChats == nil {
			s.data.TodoChats = map[string][]model.ChatMessage{}
		}
		s.data.TodoChats[todoID] = append([]model.ChatMessage(nil), msgs...)
	}
	return s.writeLocked(func(tx *sql.Tx) error { return updateChat(tx, chatTodo, todoID, org, prev, msgs) })
}

// AppendTodoChat adds a message to the conversation about a todo.
func (s *Store) AppendTodoChat(todoID string, msg model.ChatMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.data.Todos[todoID]
	if !ok {
		return ErrNotFound
	}
	org := s.projectOrgLocked(t.ProjectID)
	if s.data.TodoChats == nil {
		s.data.TodoChats = map[string][]model.ChatMessage{}
	}
	s.data.TodoChats[todoID] = append(s.data.TodoChats[todoID], msg)
	seq := len(s.data.TodoChats[todoID]) - 1
	return s.writeLocked(func(tx *sql.Tx) error { return putChatMessage(tx, chatTodo, todoID, org, seq, msg) })
}

func (s *Store) ProjectChat(projectID string) []model.ChatMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.ChatMessage{}, s.data.ProjectChats[projectID]...)
}

// SetProjectChat replaces the conversation about a project (nil clears it).
func (s *Store) SetProjectChat(projectID string, msgs []model.ChatMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.data.Projects[projectID]
	if !ok {
		return ErrNotFound
	}
	org := p.OrgID
	prev := s.data.ProjectChats[projectID]
	if len(msgs) == 0 {
		delete(s.data.ProjectChats, projectID)
	} else {
		if s.data.ProjectChats == nil {
			s.data.ProjectChats = map[string][]model.ChatMessage{}
		}
		s.data.ProjectChats[projectID] = append([]model.ChatMessage(nil), msgs...)
	}
	return s.writeLocked(func(tx *sql.Tx) error { return updateChat(tx, chatProject, projectID, org, prev, msgs) })
}

// clone copies a todo deeply enough that changing the copy (including the
// structs and slices it points to) never touches the stored original.
func clone(t model.Todo) model.Todo {
	cp := t
	cp.Feedback = append([]string(nil), t.Feedback...)
	cp.DependsOn = append([]string(nil), t.DependsOn...)
	cp.Attachments = append([]model.Attachment(nil), t.Attachments...)
	if t.Worktree != nil {
		w := *t.Worktree
		cp.Worktree = &w
	}
	if t.Checks != nil {
		c := *t.Checks
		cp.Checks = &c
	}
	if t.PR != nil {
		pr := *t.PR
		pr.Checks = append([]model.PRCheck(nil), t.PR.Checks...)
		cp.PR = &pr
	}
	if t.Revert != nil {
		r := *t.Revert
		cp.Revert = &r
	}
	if t.ExternalRef != nil {
		r := *t.ExternalRef
		cp.ExternalRef = &r
	}
	return cp
}

// BackfillHubCheckouts is a runner's one-time upgrade to finding its hub's
// projects by id (a HubProjectID with HubCheckout set). Before, a project
// was the hub's when it had a HubProjectID and its folder was under
// <projects>/hub/: those (legacy reports true for them) stay the hub's
// where they are, worktrees and task branches and all, marked HubLegacy;
// any other project with a HubProjectID wasn't the hub's under the old
// rule and stays unmarked (and so isn't the hub's; nothing is cleared, so
// a runner started once with another -projects folder loses nothing).
// Projects with HubCheckout set are left alone, so running it again
// changes nothing. It returns the names of the projects it marked.
func (s *Store) BackfillHubCheckouts(legacy func(model.Project) bool) ([]string, error) {
	var marked []string
	for _, p := range s.ListProjects() {
		if p.HubProjectID == "" || p.HubCheckout != "" || !legacy(p) {
			continue
		}
		if _, err := s.UpdateProject(p.ID, func(x *model.Project) error {
			if x.HubProjectID != "" && x.HubCheckout == "" {
				x.HubCheckout = model.HubLegacy
			}
			return nil
		}); err != nil {
			return marked, err
		}
		marked = append(marked, p.Name)
	}
	return marked, nil
}

// DefaultMergeModes gives every project whose user didn't choose how its
// work lands (model.Project.MergeModeSet) the default: pull requests for a
// GitHub repository (when gh, whether this Dash has the GitHub CLI, or the
// project's runners can open them), else merging locally. A runner's clone
// of a hub's project is left alone: it lands the way the hub says. Tasks
// with a pull request already open are followed until it closes, whatever
// the mode. It returns the names of the projects it changed.
func (s *Store) DefaultMergeModes(gh bool) ([]string, error) {
	var names []string
	for _, p := range s.ListProjects() {
		if p.MergeModeSet || p.HubProjectID != "" || p.MergeMode == p.DefaultMergeMode(gh) {
			continue
		}
		changed := false
		if _, err := s.UpdateProject(p.ID, func(x *model.Project) error {
			if x.MergeModeSet || x.HubProjectID != "" {
				return nil
			}
			mode := x.DefaultMergeMode(gh)
			changed = x.MergeMode != mode
			x.MergeMode = mode
			return nil
		}); err != nil {
			return names, err
		}
		if changed {
			names = append(names, p.Name)
		}
	}
	return names, nil
}
