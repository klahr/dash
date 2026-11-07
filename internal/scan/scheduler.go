package scan

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// Scheduler starts scans whose schedule is due. It runs in the server
// process; a slot missed while Dash was down runs once at startup, not once
// per missed slot, and a scan still running is never started again.
type Scheduler struct {
	Store  *store.Store
	Runner *Runner
	// Usage, if set, holds scheduled scans back while the subscription's
	// usage pauses the task queue; they run once it lifts.
	Usage interface{ CanStart() (bool, string) }
	Every time.Duration // how often schedules are checked; default a minute
	Now   func() time.Time

	mu        sync.Mutex
	firstSeen map[string]time.Time // when a schedule without runs was first seen
	badCron   map[string]string    // schedules already logged as invalid
}

// Schedule is a scan's effective schedule: the user's setting over the
// template's default.
type Schedule struct {
	Cron    string `json:"cron"`              // "" = none
	Default string `json:"default,omitempty"` // the template's
	Enabled bool   `json:"enabled"`
	// Custom says the user changed the expression or turned it on or off.
	Custom bool `json:"custom,omitempty"`
	// Requested is a repository template's schedule the user has not
	// approved: the template's until it is turned on, or the new one when
	// the template's changed since (Cron keeps the approved one).
	Requested string `json:"requested,omitempty"`
}

// Effective combines a template and the user's setting. Every schedule is
// off until a user turns it on in Dash: a built-in's is a suggestion, and a
// repository template's must be approved, because anyone who can merge to
// the repository can change it. Turning a repository schedule on approves
// the template's expression as it is then; if the template's changes later,
// the approved one keeps running until the new one is approved.
func Effective(t Template, set model.ScanSetting, hasSetting bool) Schedule {
	s := Schedule{Cron: t.Schedule, Default: t.Schedule}
	if !hasSetting {
		set = model.ScanSetting{}
	}
	approved := t.Builtin // a built-in's own expression needs no approval
	switch {
	case set.Schedule != "":
		s.Cron, s.Custom, approved = set.Schedule, true, true // the user's own
	case !t.Builtin && set.ApprovedSchedule != "":
		s.Cron, approved = set.ApprovedSchedule, true
	}
	if !t.Builtin && set.Schedule == "" && t.Schedule != "" && t.Schedule != set.ApprovedSchedule {
		s.Requested = t.Schedule
	}
	if set.Enabled != nil {
		s.Enabled, s.Custom = *set.Enabled, true
	}
	if s.Cron == "" || !approved {
		s.Enabled = false
	}
	return s
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Run checks the schedules now (catching up on missed runs) and then every
// s.Every until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	every := s.Every
	if every <= 0 {
		every = time.Minute
	}
	s.Tick()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick()
		}
	}
}

// Due is what a scan's schedule needs at now, and since when it waited.
func (s *Scheduler) due(projectID string, t Template, now time.Time) bool {
	set, has := s.Store.ScanSetting(projectID, t.Name)
	sch := Effective(t, set, has)
	if !sch.Enabled {
		return false
	}
	key := scanKey(projectID, t.Name)
	c, err := ParseCron(sch.Cron)
	if err != nil {
		s.mu.Lock()
		if s.badCron[key] != sch.Cron {
			if s.badCron == nil {
				s.badCron = map[string]string{}
			}
			s.badCron[key] = sch.Cron
			slog.Warn("scan schedule", "project", projectID, "scan", t.Name, "err", err)
		}
		s.mu.Unlock()
		return false
	}
	return Due(c, s.anchor(projectID, t.Name, set, has, now), now)
}

// anchor is when the schedule last fired: the last scheduled run, or the
// user's last change to it, whichever is later. A schedule that never ran
// counts from when it was first seen.
func (s *Scheduler) anchor(projectID, name string, set model.ScanSetting, has bool, now time.Time) time.Time {
	var a time.Time
	if last, ok := s.Store.LastScanRun(projectID, name, func(r model.ScanRun) bool { return r.Trigger == "schedule" }); ok {
		a = last.StartedAt
	}
	if has && set.UpdatedAt.After(a) {
		a = set.UpdatedAt
	}
	if !a.IsZero() {
		return a
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := scanKey(projectID, name)
	if s.firstSeen == nil {
		s.firstSeen = map[string]time.Time{}
	}
	if _, ok := s.firstSeen[key]; !ok {
		s.firstSeen[key] = now
	}
	return s.firstSeen[key]
}

// NextRun is when a scan's schedule fires next (zero: not scheduled).
func (s *Scheduler) NextRun(projectID string, t Template) time.Time {
	set, has := s.Store.ScanSetting(projectID, t.Name)
	sch := Effective(t, set, has)
	if !sch.Enabled {
		return time.Time{}
	}
	c, err := ParseCron(sch.Cron)
	if err != nil {
		return time.Time{}
	}
	now := s.now()
	if Due(c, s.anchor(projectID, t.Name, set, has, now), now) {
		return now // overdue: on the next check
	}
	return c.Next(now)
}

// Tick starts the scans that are due and returns their runs.
func (s *Scheduler) Tick() []model.ScanRun {
	now := s.now()
	var started []model.ScanRun
	for _, p := range s.Store.ListProjects() {
		templates, _ := Load(p.Path)
		for _, t := range templates {
			if !s.due(p.ID, t, now) {
				continue
			}
			if _, busy := s.Runner.Running(p.ID, t.Name); busy {
				continue // the next check starts it once this one is done
			}
			if s.Usage != nil {
				if ok, why := s.Usage.CanStart(); !ok {
					slog.Info("scheduled scan waits", "project", p.Name, "scan", t.Name, "reason", why)
					continue
				}
			}
			run, err := s.Runner.Start(p.ID, t.Name, "schedule")
			if errors.Is(err, ErrRunning) {
				continue
			}
			if err != nil {
				slog.Warn("scheduled scan", "project", p.Name, "scan", t.Name, "err", err)
				continue
			}
			slog.Info("scheduled scan started", "project", p.Name, "scan", t.Name, "run", run.ID)
			started = append(started, run)
		}
	}
	return started
}
