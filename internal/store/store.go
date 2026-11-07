// Package store keeps Dash state in memory and snapshots it to a JSON file.
// It is deliberately simple; swap for SQLite behind the same methods later.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

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
	Chat     []model.ChatMessage       `json:"chat,omitempty"`
	// TodoChats are conversations about one todo, keyed by todo id. Kept
	// apart from Todo so they aren't broadcast with every todo update.
	TodoChats map[string][]model.ChatMessage `json:"todo_chats,omitempty"`
	// ProjectChats are conversations about one project, keyed by project id.
	ProjectChats map[string][]model.ChatMessage `json:"project_chats,omitempty"`
	// Issues are each project's known bugs, vulnerabilities and other
	// problems, keyed by issue id.
	Issues map[string]*model.Issue `json:"issues,omitempty"`
	// ScanRuns are the recent runs of each project's scans, keyed by run id.
	ScanRuns map[string]*model.ScanRun `json:"scan_runs,omitempty"`
	// ScanSettings are the schedule changes users made, keyed by
	// "<project id>/<scan name>".
	ScanSettings map[string]*model.ScanSetting `json:"scan_settings,omitempty"`
}

type Store struct {
	mu   sync.RWMutex
	path string
	pub  Publisher
	data snapshot
}

func NewID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Open loads state from path (if present). Todos that were in progress when
// Dash stopped are put back in the queue; their worktrees are reused.
func Open(path string, pub Publisher) (*Store, error) {
	s := &Store{path: path, pub: pub, data: snapshot{
		Projects: map[string]*model.Project{},
		Todos:    map[string]*model.Todo{},
	}}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, err
		}
	}
	for _, p := range s.data.Projects {
		p.Path = CleanPath(p.Path)
	}
	for _, t := range s.data.Todos {
		if t.Status == model.StatusInProgress {
			t.Status = model.StatusQueued
		}
		t.Reviewing = false // the chat turn died with the old process
		// Merging todos stay merging: their jobs are lost with the old
		// process, but MergeQueue.Resume queues them again.
	}
	for _, r := range s.data.ScanRuns {
		if r.Status == model.ScanRunning { // the agent died with the old process
			now := time.Now()
			r.Status, r.Error, r.FinishedAt, r.Activity = model.ScanFailed, "Dash stopped during the scan", &now, ""
		}
	}
	return s, s.saveLocked()
}

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) emit(t model.EventType, projectID, todoID string, data any) {
	if s.pub != nil {
		s.pub.Publish(model.Event{Type: t, ProjectID: projectID, TodoID: todoID, Data: data, Time: time.Now()})
	}
}

// ---- Projects ----

func (s *Store) CreateProject(p model.Project) (model.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		p.ID = NewID()
	}
	p.CreatedAt = time.Now()
	s.data.Projects[p.ID] = &p
	if err := s.saveLocked(); err != nil {
		return p, err
	}
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
	cp.ID = id
	s.data.Projects[id] = &cp
	if err := s.saveLocked(); err != nil {
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
	if _, ok := s.data.Projects[id]; !ok {
		return nil, ErrNotFound
	}
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
	delete(s.data.Projects, id)
	delete(s.data.ProjectChats, id)
	if err := s.saveLocked(); err != nil {
		return todos, err
	}
	s.emit(model.EvProjectDeleted, id, "", nil)
	return todos, nil
}

// ToolAllowed reports whether tool is on the project's always-allowed list.
func (s *Store) ToolAllowed(projectID, tool string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.data.Projects[projectID]
	return ok && contains(p.AllowedTools, tool)
}

// AllowTool adds tool to the project's always-allowed list.
func (s *Store) AllowTool(projectID, tool string) error {
	_, err := s.UpdateProject(projectID, func(p *model.Project) error {
		if !contains(p.AllowedTools, tool) {
			p.AllowedTools = append(append([]string(nil), p.AllowedTools...), tool)
		}
		return nil
	})
	return err
}

// ---- Todos ----

func (s *Store) CreateTodo(t model.Todo) (model.Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Projects[t.ProjectID]; !ok {
		return t, ErrNotFound
	}
	t.ID = NewID()
	t.Status = model.StatusDraft
	t.CreatedAt = time.Now()
	t.UpdatedAt = t.CreatedAt
	t.DependsOn = cleanIDs(t.DependsOn)
	if err := s.checkDepsLocked(t); err != nil {
		return t, err
	}
	s.data.Todos[t.ID] = &t
	if err := s.saveLocked(); err != nil {
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

// ListTodos returns a project's todos in queue order (priority, then age).
// An empty status list means all statuses.
func (s *Store) ListTodos(projectID string, statuses ...model.TodoStatus) []model.Todo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []model.Todo
	for _, t := range s.data.Todos {
		if projectID != "" && t.ProjectID != projectID {
			continue
		}
		if len(statuses) > 0 && !contains(statuses, t.Status) {
			continue
		}
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
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
	if err := s.saveLocked(); err != nil {
		return cp, err
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
			t.Paused = false
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
	if err := s.saveLocked(); err != nil {
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

func (s *Store) Chat() []model.ChatMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.ChatMessage{}, s.data.Chat...)
}

// SetChat replaces the saved conversation (nil clears it).
func (s *Store) SetChat(msgs []model.ChatMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Chat = append([]model.ChatMessage(nil), msgs...)
	return s.saveLocked()
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
	if _, ok := s.data.Todos[todoID]; !ok {
		return ErrNotFound
	}
	if len(msgs) == 0 {
		delete(s.data.TodoChats, todoID)
	} else {
		if s.data.TodoChats == nil {
			s.data.TodoChats = map[string][]model.ChatMessage{}
		}
		s.data.TodoChats[todoID] = append([]model.ChatMessage(nil), msgs...)
	}
	return s.saveLocked()
}

// AppendTodoChat adds a message to the conversation about a todo.
func (s *Store) AppendTodoChat(todoID string, msg model.ChatMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Todos[todoID]; !ok {
		return ErrNotFound
	}
	if s.data.TodoChats == nil {
		s.data.TodoChats = map[string][]model.ChatMessage{}
	}
	s.data.TodoChats[todoID] = append(s.data.TodoChats[todoID], msg)
	return s.saveLocked()
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
	if _, ok := s.data.Projects[projectID]; !ok {
		return ErrNotFound
	}
	if len(msgs) == 0 {
		delete(s.data.ProjectChats, projectID)
	} else {
		if s.data.ProjectChats == nil {
			s.data.ProjectChats = map[string][]model.ChatMessage{}
		}
		s.data.ProjectChats[projectID] = append([]model.ChatMessage(nil), msgs...)
	}
	return s.saveLocked()
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
		cp.PR = &pr
	}
	if t.Revert != nil {
		r := *t.Revert
		cp.Revert = &r
	}
	return cp
}

func contains[T comparable](xs []T, x T) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
