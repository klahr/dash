// Package usage tracks the Claude subscription's rate-limit windows, as
// reported by Claude Code's rate_limit_event, and decides whether the todo
// queue may start more work.
package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/klahr/dash/internal/fsutil"
	"github.com/klahr/dash/internal/model"
)

type Publisher interface{ Publish(model.Event) }

// Window is one rate-limit window, e.g. "five_hour" or "seven_day".
type Window struct {
	Name        string    `json:"name"`
	Utilization float64   `json:"utilization"` // 0..1
	ResetsAt    time.Time `json:"resets_at"`
	Status      string    `json:"status"` // "allowed", "allowed_warning" (near the limit) or "rejected"
}

// Snapshot is what the UI shows.
type Snapshot struct {
	Known       bool      `json:"known"` // false until Claude Code has reported once
	Status      string    `json:"status"`
	Windows     []Window  `json:"windows"`
	UpdatedAt   time.Time `json:"updated_at"`
	PauseAt     float64   `json:"pause_at"`     // utilization at which the queue pauses
	ManualPause bool      `json:"manual_pause"` // paused by the user
	Paused      bool      `json:"paused"`       // the queue won't start new todos
	Reason      string    `json:"reason,omitempty"`
	Until       time.Time `json:"until,omitempty"`  // automatic pause lifts at this time
	Pacing      bool      `json:"pacing,omitempty"` // paused only to stay on the weekly pace
	Pace        Pace      `json:"pace"`             // following the weekly pace (see pace.go)
}

type Tracker struct {
	mu      sync.Mutex
	pub     Publisher
	pauseAt float64
	manual  bool
	known   bool
	status  string
	windows map[string]Window
	updated time.Time
	now     func() time.Time
	pace    Pace   // its setting, and its last evaluation (for hysteresis)
	path    string // where the pace setting is kept (see Keep)
}

// New returns a tracker that pauses the queue once any window reaches
// pauseAt (e.g. 0.9). pauseAt <= 0 or >= 1 only pauses when Claude Code
// reports a window as limited.
func New(pub Publisher, pauseAt float64) *Tracker {
	return &Tracker{pub: pub, pauseAt: pauseAt, windows: map[string]Window{}, now: time.Now}
}

type rawInfo struct {
	Status         string   `json:"status"`
	ResetsAt       int64    `json:"resetsAt"`
	RateLimitType  string   `json:"rateLimitType"`
	Utilization    *float64 `json:"utilization"`
	UnifiedWindows map[string]struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    int64   `json:"resetsAt"`
		Status      string  `json:"status"`
	} `json:"unifiedWindows"`
}

// Observe records one rate_limit_info payload from Claude Code.
func (t *Tracker) Observe(raw json.RawMessage) {
	var info rawInfo
	if json.Unmarshal(raw, &info) != nil {
		return
	}
	t.mu.Lock()
	t.known = true
	t.status = info.Status
	t.updated = t.now()
	for name, w := range info.UnifiedWindows {
		t.windows[name] = Window{Name: name, Utilization: w.Utilization, ResetsAt: time.Unix(w.ResetsAt, 0), Status: w.Status}
	}
	// Older payloads carry only the top-level window.
	if len(info.UnifiedWindows) == 0 && info.RateLimitType != "" {
		w := t.windows[info.RateLimitType]
		w.Name, w.Status, w.ResetsAt = info.RateLimitType, info.Status, time.Unix(info.ResetsAt, 0)
		if info.Utilization != nil {
			w.Utilization = *info.Utilization
		}
		t.windows[info.RateLimitType] = w
	}
	t.mu.Unlock()
	t.publish()
}

func (t *Tracker) SetManualPause(paused bool) {
	t.mu.Lock()
	t.manual = paused
	t.mu.Unlock()
	t.publish()
}

// SetParallel tells the tracker how many tasks the queue runs at once on
// pace (its MaxParallel).
func (t *Tracker) SetParallel(n int) {
	t.mu.Lock()
	t.pace.Parallel = n
	t.mu.Unlock()
}

// PaceSettings are the user's "Follow weekly pace" settings.
type PaceSettings struct {
	Follow      bool `json:"follow"`
	MaxParallel int  `json:"max_parallel"` // most tasks at once when behind; 0 = twice the usual
}

// MaxPaceParallel bounds PaceSettings.MaxParallel.
const MaxPaceParallel = 64

// SetPace changes the pace settings and saves them (see Keep).
func (t *Tracker) SetPace(ps PaceSettings) error {
	if ps.MaxParallel < 0 || ps.MaxParallel > MaxPaceParallel {
		return fmt.Errorf("max_parallel must be between 0 and %d", MaxPaceParallel)
	}
	t.mu.Lock()
	t.pace.Follow, t.pace.MaxParallel = ps.Follow, ps.MaxParallel
	if !ps.Follow {
		t.pace.State = PaceOff
	}
	path := t.path
	t.mu.Unlock()
	var err error
	if path != "" {
		b, _ := json.Marshal(ps)
		err = fsutil.WriteFileAtomic(path, b, 0o600)
	}
	t.publish()
	return err
}

// Keep loads the pace settings from path and saves them there on SetPace.
func (t *Tracker) Keep(path string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.path = path
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var ps PaceSettings
	if err := json.Unmarshal(b, &ps); err != nil {
		return err
	}
	t.pace.Follow, t.pace.MaxParallel = ps.Follow, min(max(ps.MaxParallel, 0), MaxPaceParallel)
	return nil
}

// CanStart reports whether the queue may start another todo, and why not.
func (t *Tracker) CanStart() (bool, string) {
	s := t.Snapshot()
	return !s.Paused, s.Reason
}

func (t *Tracker) Snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	s := Snapshot{Known: t.known, Status: t.status, UpdatedAt: t.updated, PauseAt: t.pauseAt, ManualPause: t.manual, Pace: t.pace}
	for _, w := range t.windows {
		s.Windows = append(s.Windows, w)
	}
	sort.Slice(s.Windows, func(i, j int) bool { return s.Windows[i].ResetsAt.Before(s.Windows[j].ResetsAt) })

	if t.manual {
		s.Paused, s.Reason = true, "Paused by you"
	}
	s = s.Reevaluate(now)
	t.pace = s.Pace // its state carries over to the next evaluation
	return s
}

// Limited reports whether a window's status means Claude Code refuses work.
// Only "rejected" is a limit: "allowed_warning" is Claude Code saying the
// window is getting close, and its utilization tells how close.
func Limited(status string) bool {
	return status == "rejected"
}

// Reevaluate works out again, as of now, whether the windows pause the
// queue: a snapshot reported earlier (say by a runner) may name windows that
// have since reset. A manual pause is kept as it is. Pacing (see Pace)
// starts from s.Pace's state, so its hysteresis carries over.
func (s Snapshot) Reevaluate(now time.Time) Snapshot {
	var week *Window
	for i, w := range s.Windows {
		if w.Name == WeekWindow && w.ResetsAt.After(now) {
			week = &s.Windows[i]
		}
	}
	s.Pace = s.Pace.next(week, now)
	if s.ManualPause {
		return s
	}
	s.Paused, s.Reason, s.Until, s.Pacing = false, "", time.Time{}, false
	for _, w := range s.Windows {
		if !w.ResetsAt.After(now) {
			continue // window already reset; its numbers are stale
		}
		limited := Limited(w.Status)
		high := s.PauseAt > 0 && s.PauseAt < 1 && w.Utilization >= s.PauseAt
		if limited || high {
			s.Paused = true
			s.Reason = fmt.Sprintf("%s usage at %.0f%%", label(w.Name), w.Utilization*100)
			if limited {
				s.Reason = label(w.Name) + " limit reached"
			}
			if w.ResetsAt.After(s.Until) {
				s.Until = w.ResetsAt
			}
		}
	}
	// Ahead of the weekly pace: wait until the line catches up. The hard
	// stops above win, as their pause lasts until a reset.
	if !s.Paused && s.Pace.Waiting() {
		s.Paused, s.Reason, s.Until, s.Pacing = true, s.Pace.Reason(), now.Add(s.Pace.Gap()).Truncate(time.Second), true
	}
	return s
}

func (t *Tracker) publish() {
	if t.pub != nil {
		t.pub.Publish(model.Event{Type: model.EvUsage, Data: t.Snapshot(), Time: time.Now()})
	}
}

func label(name string) string {
	switch name {
	case "five_hour":
		return "5-hour"
	case "seven_day":
		return "Weekly"
	case "seven_day_opus":
		return "Weekly Opus"
	case "seven_day_sonnet":
		return "Weekly Sonnet"
	case "seven_day_overage_included":
		return "Weekly (with extra usage)"
	case "overage":
		return "Extra usage"
	}
	return name
}
