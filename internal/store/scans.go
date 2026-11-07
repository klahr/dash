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

// ---- Scans ----

// KeepScanRuns is how many runs of each scan are kept.
const KeepScanRuns = 30

func scanKey(projectID, scan string) string { return projectID + "/" + scan }

// CreateScanRun records a new run; it gets an id and is announced.
func (s *Store) CreateScanRun(r model.ScanRun) (model.ScanRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Projects[r.ProjectID]; !ok {
		return r, ErrNotFound
	}
	r.ID = NewID()
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now()
	}
	if s.data.ScanRuns == nil {
		s.data.ScanRuns = map[string]*model.ScanRun{}
	}
	cp := cloneRun(r)
	s.data.ScanRuns[r.ID] = &cp
	pruned := s.pruneScanRunsLocked(r.ProjectID, r.Scan)
	err := s.writeLocked(func(tx *sql.Tx) error {
		if err := putScanRun(tx, &cp); err != nil {
			return err
		}
		for _, id := range pruned {
			if err := deleteRow(tx, "scan_runs", id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return r, err
	}
	s.emit(model.EvScanRun, r.ProjectID, "", r)
	return r, nil
}

// UpdateScanRun applies fn to a copy of the run and saves it. Progress
// updates (save=false) are only announced, not written to disk.
func (s *Store) UpdateScanRun(id string, save bool, fn func(*model.ScanRun)) (model.ScanRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.data.ScanRuns[id]
	if !ok {
		return model.ScanRun{}, ErrNotFound
	}
	cp := cloneRun(*old)
	fn(&cp)
	cp.ID, cp.ProjectID, cp.Scan = old.ID, old.ProjectID, old.Scan
	s.data.ScanRuns[id] = &cp
	if save {
		if err := s.writeLocked(func(tx *sql.Tx) error { return putScanRun(tx, &cp) }); err != nil {
			return cp, err
		}
	}
	s.emit(model.EvScanRun, cp.ProjectID, "", cp)
	return cloneRun(cp), nil
}

func (s *Store) GetScanRun(id string) (model.ScanRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.data.ScanRuns[id]
	if !ok {
		return model.ScanRun{}, ErrNotFound
	}
	return cloneRun(*r), nil
}

// ListScanRuns returns a project's runs, newest first; scan "" means all scans.
func (s *Store) ListScanRuns(projectID, scan string) []model.ScanRun {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scanRunsLocked(projectID, scan)
}

func (s *Store) scanRunsLocked(projectID, scan string) []model.ScanRun {
	out := []model.ScanRun{}
	for _, r := range s.data.ScanRuns {
		if r.ProjectID == projectID && (scan == "" || r.Scan == scan) {
			out = append(out, cloneRun(*r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// LastScanRun is the newest run of a scan for which keep is true.
func (s *Store) LastScanRun(projectID, scan string, keep func(model.ScanRun) bool) (model.ScanRun, bool) {
	for _, r := range s.ListScanRuns(projectID, scan) {
		if keep == nil || keep(r) {
			return r, true
		}
	}
	return model.ScanRun{}, false
}

// pruneScanRunsLocked forgets all but the newest KeepScanRuns runs of a scan
// and returns the ids it forgot.
func (s *Store) pruneScanRunsLocked(projectID, scan string) []string {
	var pruned []string
	runs := s.scanRunsLocked(projectID, scan)
	for _, r := range runs[min(len(runs), KeepScanRuns):] {
		if r.Status != model.ScanRunning {
			delete(s.data.ScanRuns, r.ID)
			pruned = append(pruned, r.ID)
		}
	}
	return pruned
}

// ScanSetting is what the user changed about a scan: its schedule and what
// they approved (ok=false: nothing).
func (s *Store) ScanSetting(projectID, scan string) (model.ScanSetting, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.data.ScanSettings[scanKey(projectID, scan)]
	if !ok {
		return model.ScanSetting{ProjectID: projectID, Scan: scan}, false
	}
	return cloneSetting(*st), true
}

// SetScanSetting changes a scan's settings with fn.
func (s *Store) SetScanSetting(projectID, scan string, fn func(*model.ScanSetting)) (model.ScanSetting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Projects[projectID]; !ok {
		return model.ScanSetting{}, ErrNotFound
	}
	k := scanKey(projectID, scan)
	cp := model.ScanSetting{ProjectID: projectID, Scan: scan}
	if old, ok := s.data.ScanSettings[k]; ok {
		cp = cloneSetting(*old)
	}
	fn(&cp)
	cp.ProjectID, cp.Scan, cp.UpdatedAt = projectID, scan, time.Now()
	if s.Now != nil {
		cp.UpdatedAt = s.Now()
	}
	if s.data.ScanSettings == nil {
		s.data.ScanSettings = map[string]*model.ScanSetting{}
	}
	s.data.ScanSettings[k] = &cp
	if err := s.writeLocked(func(tx *sql.Tx) error { return putScanSetting(tx, &cp) }); err != nil {
		return cp, err
	}
	s.emit(model.EvScanSettings, projectID, "", cp)
	return cp, nil
}

func cloneRun(r model.ScanRun) model.ScanRun {
	r.NewIssues = slices.Clone(r.NewIssues)
	r.ReopenedIssues = slices.Clone(r.ReopenedIssues)
	if r.FinishedAt != nil {
		t := *r.FinishedAt
		r.FinishedAt = &t
	}
	return r
}

func cloneSetting(st model.ScanSetting) model.ScanSetting {
	if st.Enabled != nil {
		b := *st.Enabled
		st.Enabled = &b
	}
	st.ApprovedTools = slices.Clone(st.ApprovedTools)
	return st
}

// ErrScanExists is returned when a project already has a Dash scan of that name.
var ErrScanExists = errors.New("a Dash scan with that name already exists")

// ScanTemplates are a project's Dash scans, by name.
func (s *Store) ScanTemplates(projectID string) []model.ScanTemplate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []model.ScanTemplate{}
	for _, t := range s.data.ScanTemplates {
		if t.ProjectID == projectID {
			out = append(out, cloneTemplate(*t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ScanTemplate is the project's Dash scan called name.
func (s *Store) ScanTemplate(projectID, name string) (model.ScanTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.data.ScanTemplates[scanKey(projectID, name)]
	if !ok {
		return model.ScanTemplate{}, ErrNotFound
	}
	return cloneTemplate(*t), nil
}

// SaveScanTemplate adds a Dash scan (create=true; ErrScanExists if the name
// is taken) or replaces one (ErrNotFound if there is none). The caller
// validates it first (scan.Custom); the store only keeps it.
func (s *Store) SaveScanTemplate(t model.ScanTemplate, create bool) (model.ScanTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Projects[t.ProjectID]; !ok {
		return t, ErrNotFound
	}
	if t.Name == "" || strings.ContainsAny(t.Name, "/\\") {
		return t, fmt.Errorf("invalid scan name %q", t.Name)
	}
	k := scanKey(t.ProjectID, t.Name)
	old, exists := s.data.ScanTemplates[k]
	switch {
	case create && exists:
		return t, fmt.Errorf("%w: %q", ErrScanExists, t.Name)
	case !create && !exists:
		return t, ErrNotFound
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	t.CreatedAt, t.UpdatedAt = now, now
	if exists {
		t.CreatedAt = old.CreatedAt
	}
	if s.data.ScanTemplates == nil {
		s.data.ScanTemplates = map[string]*model.ScanTemplate{}
	}
	cp := cloneTemplate(t)
	s.data.ScanTemplates[k] = &cp
	if err := s.writeLocked(func(tx *sql.Tx) error { return putScanTemplate(tx, &cp) }); err != nil {
		return t, err
	}
	s.emit(model.EvScanTemplate, t.ProjectID, "", map[string]any{"name": t.Name})
	return cloneTemplate(t), nil
}

// DeleteScanTemplate forgets a Dash scan. Its runs and the issues it filed
// (source "scan:<name>") are kept; so are its settings when keepSettings is
// set, e.g. because a repository scan of the same name uses them.
func (s *Store) DeleteScanTemplate(projectID, name string, keepSettings bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := scanKey(projectID, name)
	if _, ok := s.data.ScanTemplates[k]; !ok {
		return ErrNotFound
	}
	delete(s.data.ScanTemplates, k)
	if !keepSettings {
		delete(s.data.ScanSettings, k)
	}
	err := s.writeLocked(func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM scan_templates WHERE project_id = ? AND name = ?", projectID, name); err != nil {
			return err
		}
		if keepSettings {
			return nil
		}
		_, err := tx.Exec("DELETE FROM scan_settings WHERE project_id = ? AND scan = ?", projectID, name)
		return err
	})
	if err != nil {
		return err
	}
	s.emit(model.EvScanTemplate, projectID, "", map[string]any{"name": name, "deleted": true})
	return nil
}

func cloneTemplate(t model.ScanTemplate) model.ScanTemplate {
	t.Tools = slices.Clone(t.Tools)
	return t
}
