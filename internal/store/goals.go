package store

import (
	"database/sql"
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
)

// ---- Goals ----

// CreateGoal adds a goal to its project, planning. g must be cleaned
// (model.CleanGoal) by the caller.
func (s *Store) CreateGoal(g model.Goal) (model.Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Projects[g.ProjectID]; !ok {
		return g, ErrNotFound
	}
	g.ID = NewID()
	g.Status = model.GoalPlanning
	g.CreatedAt = time.Now()
	g.UpdatedAt = g.CreatedAt
	g = cloneGoal(g)
	if s.data.Goals == nil {
		s.data.Goals = map[string]*model.Goal{}
	}
	s.data.Goals[g.ID] = &g
	if err := s.writeLocked(func(tx *sql.Tx) error { return putGoal(tx, &g) }); err != nil {
		return g, err
	}
	s.emitGoal(g)
	return cloneGoal(g), nil
}

// GetGoal returns a goal with its log.
func (s *Store) GetGoal(id string) (model.Goal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.data.Goals[id]
	if !ok {
		return model.Goal{}, ErrNotFound
	}
	return cloneGoal(*g), nil
}

// ListGoals returns a project's goals ("" for all), newest first, with
// their logs.
func (s *Store) ListGoals(projectID string) []model.Goal {
	return s.listGoals(func(p string) bool { return projectID == "" || p == projectID })
}

func (s *Store) listGoals(keep func(projectID string) bool) []model.Goal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []model.Goal{}
	for _, g := range s.data.Goals {
		if keep(g.ProjectID) {
			out = append(out, cloneGoal(*g))
		}
	}
	slices.SortFunc(out, func(a, b model.Goal) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// UpdateGoal applies fn to a copy of the goal, then stores and broadcasts
// it. fn's error leaves the goal as it was.
func (s *Store) UpdateGoal(id string, fn func(*model.Goal) error) (model.Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.data.Goals[id]
	if !ok {
		return model.Goal{}, ErrNotFound
	}
	cp := cloneGoal(*g)
	if err := fn(&cp); err != nil {
		return cloneGoal(*g), err
	}
	cp.ID, cp.ProjectID, cp.CreatedAt, cp.UpdatedAt = g.ID, g.ProjectID, g.CreatedAt, time.Now()
	s.data.Goals[id] = &cp
	if err := s.writeLocked(func(tx *sql.Tx) error { return putGoal(tx, &cp) }); err != nil {
		return cp, err
	}
	s.emitGoal(cp)
	return cloneGoal(cp), nil
}

// GoalOrg is the org a goal belongs to: its project's.
func (s *Store) GoalOrg(goalID string) (int64, bool) {
	s.mu.RLock()
	g, ok := s.data.Goals[goalID]
	s.mu.RUnlock()
	if !ok {
		return 0, false
	}
	return s.ProjectOrg(g.ProjectID)
}

// GoalChat is the conversation of a goal's coordinator.
func (s *Store) GoalChat(goalID string) []model.ChatMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.ChatMessage{}, s.data.GoalChats[goalID]...)
}

// AppendGoalChat adds messages to the conversation of a goal's coordinator.
func (s *Store) AppendGoalChat(goalID string, msgs ...model.ChatMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.data.Goals[goalID]
	if !ok {
		return ErrNotFound
	}
	if len(msgs) == 0 {
		return nil
	}
	org := s.projectOrgLocked(g.ProjectID)
	if s.data.GoalChats == nil {
		s.data.GoalChats = map[string][]model.ChatMessage{}
	}
	prev := s.data.GoalChats[goalID]
	next := append(slices.Clip(prev), msgs...)
	s.data.GoalChats[goalID] = next
	return s.writeLocked(func(tx *sql.Tx) error {
		for i := len(prev); i < len(next); i++ {
			if err := putChatMessage(tx, chatGoal, goalID, org, i, next[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

// emitGoal announces a change to a goal, without its log (which only grows;
// clients read it with the goal). The caller holds the lock.
func (s *Store) emitGoal(g model.Goal) {
	g = cloneGoal(g)
	g.Log = nil
	s.emit(model.EvGoalUpdated, g.ProjectID, "", g)
}

// cloneGoal copies a goal deeply enough that changing the copy never
// touches the stored original.
func cloneGoal(g model.Goal) model.Goal {
	cp := g
	cp.Plan = make([]model.GoalPlanItem, len(g.Plan))
	for i, it := range g.Plan {
		it.After = slices.Clone(it.After)
		cp.Plan[i] = it
	}
	if len(cp.Plan) == 0 {
		cp.Plan = nil
	}
	cp.Pending = slices.Clone(g.Pending)
	cp.Log = slices.Clone(g.Log)
	return cp
}

// GoalTodos are the tasks a goal's coordinator made, in queue order.
func (s *Store) GoalTodos(g model.Goal) []model.Todo {
	var out []model.Todo
	for _, t := range s.ListTodos(g.ProjectID) {
		if t.GoalID == g.ID {
			out = append(out, t)
		}
	}
	return out
}

// ---- in one org ----

func (o *OrgStore) ownsGoal(id string) bool {
	org, ok := o.s.GoalOrg(id)
	return ok && o.org != 0 && org == o.org
}

// ListGoals is the goals of one of the org's projects, or of all of them ("").
func (o *OrgStore) ListGoals(projectID string) []model.Goal {
	if projectID != "" && !o.ownsProject(projectID) {
		return []model.Goal{}
	}
	return o.s.listGoals(func(p string) bool { return (projectID == "" || p == projectID) && o.ownsProject(p) })
}

func (o *OrgStore) GetGoal(id string) (model.Goal, error) {
	if !o.ownsGoal(id) {
		return model.Goal{}, ErrNotFound
	}
	return o.s.GetGoal(id)
}

func (o *OrgStore) CreateGoal(g model.Goal) (model.Goal, error) {
	if !o.ownsProject(g.ProjectID) {
		return g, ErrNotFound
	}
	return o.s.CreateGoal(g)
}

func (o *OrgStore) UpdateGoal(id string, fn func(*model.Goal) error) (model.Goal, error) {
	if !o.ownsGoal(id) {
		return model.Goal{}, ErrNotFound
	}
	return o.s.UpdateGoal(id, fn)
}

func (o *OrgStore) GoalChat(id string) ([]model.ChatMessage, error) {
	if !o.ownsGoal(id) {
		return nil, ErrNotFound
	}
	return o.s.GoalChat(id), nil
}

func (o *OrgStore) AppendGoalChat(id string, msgs ...model.ChatMessage) error {
	if !o.ownsGoal(id) {
		return ErrNotFound
	}
	return o.s.AppendGoalChat(id, msgs...)
}

// GoalTodos are the tasks of one of the org's goals.
func (o *OrgStore) GoalTodos(g model.Goal) []model.Todo {
	if !o.ownsGoal(g.ID) {
		return nil
	}
	return o.s.GoalTodos(g)
}
