package store

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
)

// ---- Issues ----

// ErrBadIssue is returned for an issue without a title or with an unknown
// type or severity.
var ErrBadIssue = errors.New("invalid issue")

// UpsertIssue adds an issue to its project, or updates the one with the same
// fingerprint: a dismissed issue stays dismissed (and unchanged), a resolved
// one is reopened, an open one takes the new details. The fingerprint is
// computed unless in has one.
func (s *Store) UpsertIssue(in model.Issue) (model.Issue, model.UpsertResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is, r, err := s.upsertIssueLocked(in)
	if err != nil {
		return is, r, err
	}
	if r == model.UpsertKnown || r == model.UpsertDismissed {
		return is, r, nil
	}
	if err := s.writeLocked(func(tx *sql.Tx) error { return putIssue(tx, &is) }); err != nil {
		return is, r, err
	}
	s.emit(model.EvIssueUpdated, is.ProjectID, "", is)
	return is, r, nil
}

// UpsertIssues adds a batch of issues to a project (e.g. a scan's findings)
// with UpsertIssue's rules, saving once, and counts the outcomes. Issues
// without a Source get source.
func (s *Store) UpsertIssues(projectID, source string, in []model.Issue) (model.UpsertCounts, error) {
	counts, _, err := s.UpsertIssueBatch(projectID, source, in)
	return counts, err
}

// UpsertOutcome is what UpsertIssueBatch did with one issue.
type UpsertOutcome struct {
	Issue  model.Issue
	Result model.UpsertResult
}

// UpsertIssueBatch is UpsertIssues that also reports each issue's outcome,
// in order. The batch is all or nothing: if one issue is invalid, none is
// added.
func (s *Store) UpsertIssueBatch(projectID, source string, in []model.Issue) (model.UpsertCounts, []UpsertOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var counts model.UpsertCounts
	if _, ok := s.data.Projects[projectID]; !ok {
		return counts, nil, ErrNotFound
	}
	// Check them all first so a bad one doesn't leave half a batch behind.
	for _, is := range in {
		if err := is.Normalize(); err != nil {
			return counts, nil, fmt.Errorf("%w: %w", ErrBadIssue, err)
		}
	}
	var changed []model.Issue
	out := make([]UpsertOutcome, 0, len(in))
	for _, is := range in {
		is.ProjectID = projectID
		if is.Source == "" {
			is.Source = source
		}
		res, r, err := s.upsertIssueLocked(is)
		if err != nil {
			return counts, nil, err
		}
		counts.Add(r)
		out = append(out, UpsertOutcome{Issue: res, Result: r})
		if r == model.UpsertNew || r == model.UpsertUpdated || r == model.UpsertReopened {
			changed = append(changed, res)
		}
	}
	if len(changed) == 0 {
		return counts, out, nil
	}
	if err := s.writeLocked(func(tx *sql.Tx) error { return putIssues(tx, changed) }); err != nil {
		return counts, out, err
	}
	for _, is := range changed {
		s.emit(model.EvIssueUpdated, is.ProjectID, "", is)
	}
	return counts, out, nil
}

func (s *Store) upsertIssueLocked(in model.Issue) (model.Issue, model.UpsertResult, error) {
	if _, ok := s.data.Projects[in.ProjectID]; !ok {
		return in, "", ErrNotFound
	}
	if err := in.Normalize(); err != nil {
		return in, "", fmt.Errorf("%w: %w", ErrBadIssue, err)
	}
	in.Fingerprint = strings.TrimSpace(in.Fingerprint)
	if in.Fingerprint == "" {
		in.Fingerprint = in.ComputeFingerprint()
	}
	now := time.Now()
	old := s.findIssueLocked(in.ProjectID, in.Fingerprint)
	if old == nil {
		in.ID = NewID()
		in.Status = model.IssueOpen
		in.TodoIDs, in.DismissReason = nil, ""
		in.CreatedAt, in.UpdatedAt = now, now
		if s.data.Issues == nil {
			s.data.Issues = map[string]*model.Issue{}
		}
		s.data.Issues[in.ID] = &in
		s.issueIndexLocked()[issueKey(in.ProjectID, in.Fingerprint)] = in.ID
		return in, model.UpsertNew, nil
	}
	if old.Status == model.IssueDismissed {
		return cloneIssue(*old), model.UpsertDismissed, nil
	}
	cp := cloneIssue(*old)
	// A report refreshes what it knows; blank fields keep what was there
	// (e.g. details the user wrote in).
	cp.Title, cp.Type, cp.Severity = in.Title, in.Type, in.Severity
	if in.File != "" {
		cp.File, cp.Line = in.File, in.Line
	}
	if in.Details != "" {
		cp.Details = in.Details
	}
	if in.Fix != "" {
		cp.Fix = in.Fix
	}
	if in.Rule != "" {
		cp.Rule = in.Rule
	}
	if in.ScanKind != "" {
		cp.ScanKind = in.ScanKind
	}
	res := model.UpsertUpdated
	if cp.Status == model.IssueResolved {
		cp.Status, res = model.IssueOpen, model.UpsertReopened // it came back
	}
	if issueEqual(cp, *old) {
		return cp, model.UpsertKnown, nil
	}
	cp.UpdatedAt = now
	s.data.Issues[cp.ID] = &cp
	return cp, res, nil
}

func (s *Store) findIssueLocked(projectID, fingerprint string) *model.Issue {
	id, ok := s.issueIndexLocked()[issueKey(projectID, fingerprint)]
	if !ok {
		return nil
	}
	if is, ok := s.data.Issues[id]; ok && is.ProjectID == projectID && is.Fingerprint == fingerprint {
		return is
	}
	return nil
}

// issueKey is an issue's key in snapshot.issueIDs.
func issueKey(projectID, fingerprint string) string {
	return projectID + "\x00" + fingerprint
}

// issueIndexLocked returns the (project, fingerprint) → id index of the
// issues, building it if memory was (re)loaded since. The caller holds the
// write lock.
func (s *Store) issueIndexLocked() map[string]string {
	if s.data.issueIDs == nil {
		idx := make(map[string]string, len(s.data.Issues))
		for id, is := range s.data.Issues {
			idx[issueKey(is.ProjectID, is.Fingerprint)] = id
		}
		s.data.issueIDs = idx
	}
	return s.data.issueIDs
}

// unindexIssueLocked forgets is's (project, fingerprint) key, which it no
// longer has (deleted, or given another fingerprint). Should an older issue
// share that key, it takes the key over.
func (s *Store) unindexIssueLocked(is *model.Issue) {
	idx := s.issueIndexLocked()
	k := issueKey(is.ProjectID, is.Fingerprint)
	if idx[k] != is.ID {
		return
	}
	delete(idx, k)
	for id, o := range s.data.Issues {
		if id != is.ID && o.ProjectID == is.ProjectID && o.Fingerprint == is.Fingerprint {
			idx[k] = id
			return
		}
	}
}

func (s *Store) GetIssue(id string) (model.Issue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	is, ok := s.data.Issues[id]
	if !ok {
		return model.Issue{}, ErrNotFound
	}
	return cloneIssue(*is), nil
}

// ListIssues returns a project's issues, most severe first, then newest.
func (s *Store) ListIssues(projectID string) []model.Issue {
	return s.listIssues(func(is *model.Issue) bool { return projectID == "" || is.ProjectID == projectID })
}

// listIssues is ListIssues for the issues keep accepts: it filters before
// cloning and sorting.
func (s *Store) listIssues(keep func(*model.Issue) bool) []model.Issue {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []model.Issue{}
	for _, is := range s.data.Issues {
		if keep(is) {
			out = append(out, cloneIssue(*is))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := out[i].Severity.Rank(), out[j].Severity.Rank(); a != b {
			return a < b
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// UpdateIssue applies fn to a copy of the issue, validates it (fields and any
// status change) and saves it. Editing what identifies the issue gives it a
// new fingerprint, unless another issue already has that one.
func (s *Store) UpdateIssue(id string, fn func(*model.Issue) error) (model.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.data.Issues[id]
	if !ok {
		return model.Issue{}, ErrNotFound
	}
	cp := cloneIssue(*old)
	if err := fn(&cp); err != nil {
		return *old, err
	}
	if err := cp.Normalize(); err != nil {
		return *old, fmt.Errorf("%w: %w", ErrBadIssue, err)
	}
	if cp.Status != old.Status && !model.CanTransitionIssue(old.Status, cp.Status) {
		return *old, model.ErrInvalidIssueTransition{From: old.Status, To: cp.Status}
	}
	if cp.Status != model.IssueDismissed {
		cp.DismissReason = ""
	}
	cp.ID, cp.ProjectID, cp.CreatedAt = old.ID, old.ProjectID, old.CreatedAt
	if cp.Type != old.Type || cp.File != old.File || cp.Title != old.Title || cp.Rule != old.Rule {
		if fp := cp.ComputeFingerprint(); s.findIssueLocked(cp.ProjectID, fp) == nil {
			cp.Fingerprint = fp
		}
	}
	cp.UpdatedAt = time.Now()
	s.data.Issues[id] = &cp
	if cp.Fingerprint != old.Fingerprint {
		s.unindexIssueLocked(old)
		s.issueIndexLocked()[issueKey(cp.ProjectID, cp.Fingerprint)] = id
	}
	if err := s.writeLocked(func(tx *sql.Tx) error { return putIssue(tx, &cp) }); err != nil {
		return cp, err
	}
	s.emit(model.EvIssueUpdated, cp.ProjectID, "", cp)
	return cp, nil
}

// SetIssueStatus moves an issue to another status; reason is kept when it
// is dismissed.
func (s *Store) SetIssueStatus(id string, to model.IssueStatus, reason string) (model.Issue, error) {
	return s.UpdateIssue(id, func(is *model.Issue) error {
		is.Status = to
		if to == model.IssueDismissed {
			is.DismissReason = strings.TrimSpace(reason)
		}
		return nil
	})
}

// LinkIssues records that todoID was created to fix the issues, which become
// task_created.
func (s *Store) LinkIssues(ids []string, todoID string) ([]model.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.data.Todos[todoID]
	if !ok {
		return nil, ErrNotFound
	}
	var out []model.Issue
	for _, id := range ids {
		is, ok := s.data.Issues[id]
		if !ok || is.ProjectID != t.ProjectID {
			return nil, ErrNotFound
		}
		if is.Status != model.IssueTaskCreated && !model.CanTransitionIssue(is.Status, model.IssueTaskCreated) {
			return nil, model.ErrInvalidIssueTransition{From: is.Status, To: model.IssueTaskCreated}
		}
	}
	for _, id := range ids {
		cp := cloneIssue(*s.data.Issues[id])
		if !slices.Contains(cp.TodoIDs, todoID) {
			cp.TodoIDs = append(cp.TodoIDs, todoID)
		}
		cp.Status = model.IssueTaskCreated
		cp.UpdatedAt = time.Now()
		s.data.Issues[id] = &cp
		out = append(out, cp)
	}
	if err := s.writeLocked(func(tx *sql.Tx) error { return putIssues(tx, out) }); err != nil {
		return out, err
	}
	for _, is := range out {
		s.emit(model.EvIssueUpdated, is.ProjectID, "", is)
	}
	return out, nil
}

func (s *Store) DeleteIssue(id string) (model.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is, ok := s.data.Issues[id]
	if !ok {
		return model.Issue{}, ErrNotFound
	}
	delete(s.data.Issues, id)
	s.unindexIssueLocked(is)
	if err := s.writeLocked(func(tx *sql.Tx) error { return deleteRow(tx, "issues", id) }); err != nil {
		return *is, err
	}
	s.emit(model.EvIssueDeleted, is.ProjectID, "", map[string]string{"id": id})
	return *is, nil
}

// issuesForTodoLocked changes the issues linked to todoID with fn and returns
// the ones it changed (fn reports whether it did). The caller saves and emits.
func (s *Store) issuesForTodoLocked(todoID string, fn func(*model.Issue) bool) []model.Issue {
	var out []model.Issue
	for id, is := range s.data.Issues {
		if !slices.Contains(is.TodoIDs, todoID) {
			continue
		}
		cp := cloneIssue(*is)
		if fn(&cp) {
			cp.UpdatedAt = time.Now()
			s.data.Issues[id] = &cp
			out = append(out, cp)
		}
	}
	return out
}

// resolveIssuesLocked marks the issues a merged todo was created for resolved.
func (s *Store) resolveIssuesLocked(todoID string) []model.Issue {
	return s.issuesForTodoLocked(todoID, func(is *model.Issue) bool {
		if is.Status != model.IssueOpen && is.Status != model.IssueTaskCreated {
			return false
		}
		is.Status = model.IssueResolved
		return true
	})
}

// unlinkIssuesLocked forgets a deleted todo in the issues created for it;
// those left without a task are open again.
func (s *Store) unlinkIssuesLocked(todoID string) []model.Issue {
	return s.issuesForTodoLocked(todoID, func(is *model.Issue) bool {
		is.TodoIDs = slices.DeleteFunc(is.TodoIDs, func(id string) bool { return id == todoID })
		if len(is.TodoIDs) == 0 && is.Status == model.IssueTaskCreated {
			is.Status = model.IssueOpen
		}
		return true
	})
}

func (s *Store) emitIssues(issues []model.Issue) {
	for _, is := range issues {
		s.emit(model.EvIssueUpdated, is.ProjectID, "", is)
	}
}

func putIssues(tx *sql.Tx, issues []model.Issue) error {
	for i := range issues {
		if err := putIssue(tx, &issues[i]); err != nil {
			return err
		}
	}
	return nil
}

func cloneIssue(is model.Issue) model.Issue {
	is.TodoIDs = slices.Clone(is.TodoIDs)
	return is
}

func issueEqual(a, b model.Issue) bool {
	return a.Title == b.Title && a.Type == b.Type && a.Severity == b.Severity &&
		a.File == b.File && a.Line == b.Line && a.Details == b.Details && a.Fix == b.Fix &&
		a.Rule == b.Rule && a.Source == b.Source && a.ScanKind == b.ScanKind && a.Status == b.Status &&
		a.Fingerprint == b.Fingerprint && a.DismissReason == b.DismissReason &&
		slices.Equal(a.TodoIDs, b.TodoIDs)
}
