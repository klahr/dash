package scan

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/usage"
)

// Scheduler starts scans whose schedule is due. It runs in the server
// process; a slot missed while Dash was down runs once at startup, not once
// per missed slot, and a scan still running is never started again. A scan
// set to run early (model.EarlyRun) may start before its cron time, once
// per cron period, when the Claude usage has room to spare.
type Scheduler struct {
	Store  *store.Store
	Runner *Runner
	// Usage, if set, holds scheduled scans back while the subscription's
	// usage pauses the task queue; they run once it lifts. If it also has
	// a Snapshot (a *usage.Tracker), early runs weigh its usage.
	Usage interface{ CanStart() (bool, string) }
	// Spare, on a hub, lists the Claude accounts of the runners that could
	// take a scan of p for the hub's user by now, with each one's usage: an
	// early run weighs those, not Usage's, and runs on the account it found
	// room on (Runner.StartOn). Nil: the scans run on this Dash's account.
	Spare func(p model.Project, by int64) []Spare
	// Hold, if set, is why an org's scheduled scans don't run now, or ""
	// (billing: an unpaid org's).
	Hold  func(org int64) string
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
	// Early is "Run early if there's spare usage" (nil: never set, off).
	Early *model.EarlyRun `json:"early,omitempty"`
}

// Effective combines a template and the user's setting. Every schedule is
// off until a user turns it on in Dash: a built-in's is a suggestion, and a
// repository template's must be approved, because anyone who can merge to
// the repository can change it. Turning a repository schedule on approves
// the template's expression as it is then; if the template's changes later,
// the approved one keeps running until the new one is approved. A scan
// written in Dash is the user's own: its schedule runs until turned off.
func Effective(t Template, set model.ScanSetting, hasSetting bool) Schedule {
	s := Schedule{Cron: t.Schedule, Default: t.Schedule}
	if !hasSetting {
		set = model.ScanSetting{}
	}
	trusted := t.Trusted()
	approved := trusted // a built-in's or Dash scan's own expression needs no approval
	switch {
	case set.Schedule != "":
		s.Cron, s.Custom, approved = set.Schedule, true, true // the user's own
	case !trusted && set.ApprovedSchedule != "":
		s.Cron, approved = set.ApprovedSchedule, true
	}
	if !trusted && set.Schedule == "" && t.Schedule != "" && t.Schedule != set.ApprovedSchedule {
		s.Requested = t.Schedule
	}
	s.Enabled = t.Origin == SourceDash
	if set.Enabled != nil {
		s.Enabled, s.Custom = *set.Enabled, true
	}
	if s.Cron == "" || !approved {
		s.Enabled = false
	}
	if set.Early != nil {
		e := *set.Early
		s.Early = &e
	}
	return s
}

// EarlyOn reports whether the schedule runs early when there's spare usage.
func (s Schedule) EarlyOn() bool {
	return s.Enabled && s.Cron != "" && s.Early != nil && s.Early.On
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

// plan is a scan's schedule as the scheduler weighs it at one check.
type plan struct {
	sch    Schedule
	set    model.ScanSetting
	cron   Cron
	anchor time.Time // see anchor
}

// plan reads a scan's schedule; ok is false when it doesn't run on one.
func (s *Scheduler) plan(projectID string, t Template, now time.Time) (plan, bool) {
	set, has := s.Store.ScanSetting(projectID, t.Name)
	sch := Effective(t, set, has)
	if !sch.Enabled {
		return plan{}, false
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
		return plan{}, false
	}
	return plan{sch: sch, set: set, cron: c, anchor: s.anchor(projectID, t.Name, sch, c, set, has, now)}, true
}

// anchor is when the schedule last fired: the last scheduled run, or the
// user's last change to it, whichever is later. A schedule that never ran
// counts from when it was first seen. A restart of an interrupted run
// counts as a scheduled run: it already catches up on a slot missed while
// Dash was down, so the slot doesn't run the scan a second time. With
// early runs on, a run started early or by hand stands for its whole cron
// period: the anchor is the cron time that ends it, so that one is skipped.
// So does a restart of such a run.
func (s *Scheduler) anchor(projectID, name string, sch Schedule, c Cron, set model.ScanSetting, has bool, now time.Time) time.Time {
	var a time.Time
	early := sch.EarlyOn()
	if last, ok := s.Store.LastScanRun(projectID, name, func(r model.ScanRun) bool {
		return r.Trigger == model.TriggerSchedule || r.Trigger == model.TriggerResume ||
			(early && (r.Trigger == model.TriggerEarly || r.Trigger == model.TriggerManual))
	}); ok {
		a = last.StartedAt
		// A restart stands for the run it starts over: an early or manual
		// one still covers the cron period it started in.
		from := last
		if last.Trigger == model.TriggerResume && last.ResumedFrom != "" {
			if orig, err := s.Store.GetScanRun(last.ResumedFrom); err == nil {
				from = orig
			}
		}
		if early && (from.Trigger == model.TriggerEarly || from.Trigger == model.TriggerManual) {
			if end := c.PeriodEnd(from.StartedAt); end.After(a) {
				a = end
			}
		}
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

// NextRun is when a scan's schedule fires next (zero: not scheduled). A
// scan that already ran early in this cron period runs next in the next.
func (s *Scheduler) NextRun(projectID string, t Template) time.Time {
	now := s.now()
	pl, ok := s.plan(projectID, t, now)
	if !ok {
		return time.Time{}
	}
	if Due(pl.cron, pl.anchor, now) {
		return now // overdue: on the next check
	}
	from := now
	if pl.anchor.After(now) {
		from = pl.anchor
	}
	return pl.cron.Next(from)
}

// Tick starts the scans that are due and returns their runs: those whose
// cron time came, and those set to run early that have spare usage now.
func (s *Scheduler) Tick() []model.ScanRun {
	now := s.now()
	var started []model.ScanRun
	for _, p := range s.Store.ListProjects() {
		if s.Hold != nil && s.Hold(p.OrgID) != "" {
			continue
		}
		templates, _, _ := Templates(s.Store, p)
		for _, t := range templates {
			pl, ok := s.plan(p.ID, t, now)
			if !ok {
				continue
			}
			// The cron time wins: an early run is only looked for when
			// it isn't due, so the two never both start.
			due := Due(pl.cron, pl.anchor, now)
			if !due && !pl.sch.EarlyOn() {
				continue
			}
			if _, busy := s.Runner.Running(p.ID, t.Name); busy {
				continue // the next check starts it once this one is done
			}
			if s.Usage != nil {
				if ok, why := s.Usage.CanStart(); !ok {
					if due {
						slog.Info("scheduled scan waits", "project", p.Name, "scan", t.Name, "reason", why)
					}
					continue
				}
			}
			trigger, account := model.TriggerSchedule, ""
			if !due {
				if account, ok = s.early(p, t, pl, now); !ok {
					continue
				}
				trigger = model.TriggerEarly
			}
			run, err := s.Runner.StartOn(p.ID, t.Name, trigger, account)
			if errors.Is(err, ErrRunning) {
				continue
			}
			if err != nil {
				slog.Warn("scheduled scan", "project", p.Name, "scan", t.Name, "err", err)
				continue
			}
			if trigger == model.TriggerEarly {
				slog.Info("scheduled scan started early: spare usage before the 5-hour window resets",
					"project", p.Name, "scan", t.Name, "run", run.ID, "account", account)
			} else {
				slog.Info("scheduled scan started", "project", p.Name, "scan", t.Name, "run", run.ID)
			}
			started = append(started, run)
		}
	}
	return started
}

// early reports whether a scan set to run early may start now, and on a
// hub on which Claude account's runners: it hasn't started in this cron
// period, its last run is at least half a period ago, and the account's
// 5-hour window resets soon with usage to spare (SpareNow).
func (s *Scheduler) early(p model.Project, t Template, pl plan, now time.Time) (string, bool) {
	end := pl.cron.PeriodEnd(now)
	if end.IsZero() || !pl.anchor.Before(end) {
		return "", false // no next cron time, or it already ran in this period
	}
	begin := pl.cron.Prev(end.Add(-time.Minute))
	if begin.IsZero() {
		return "", false
	}
	// Say it ran at its cron time at 06:00: it doesn't run again at 07:00,
	// early for the next period, because usage resets then.
	if last, ok := s.Store.LastScanRun(p.ID, t.Name, nil); ok && now.Sub(last.StartedAt) < end.Sub(begin)/2 {
		return "", false
	}
	e := *pl.sch.Early
	if s.Spare != nil {
		for _, sp := range s.Spare(p, pl.set.ScheduledBy) {
			if sp.Hold != "" {
				continue
			}
			if ok, _ := SpareNow(e, sp.Usage, now); ok {
				return sp.Account, true
			}
		}
		return "", false
	}
	u, ok := s.Usage.(interface{ Snapshot() usage.Snapshot })
	if !ok {
		return "", false
	}
	spare, _ := SpareNow(e, u.Snapshot(), now)
	return "", spare
}
