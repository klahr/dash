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
	manual  bool
	known   bool
	status  string
	windows map[string]Window
	updated time.Time
	now     func() time.Time
	pace    Pace   // its setting, and its last evaluation (for hysteresis)
	path    string // where the pace setting is kept (see Keep)
	hub     *Pace  // how the hub paces this runner (nil: it paces itself)
}

// New returns a tracker that pauses the queue while Claude Code reports a
// window as limited.
func New(pub Publisher) *Tracker {
	return &Tracker{pub: pub, windows: map[string]Window{}, now: time.Now}
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

// SetHubPace tells the tracker how the hub paces this runner (see
// Pace.Hub), as the hub answered its last report; nil: the hub doesn't
// (an older hub), and the tracker paces the queue itself, as set with
// SetPace. It reports whether that changed what the queue may run.
func (t *Tracker) SetHubPace(p *Pace) bool {
	t.mu.Lock()
	prev := t.hub
	if p != nil {
		cp := *p
		cp.Hub = true
		p = &cp
	}
	t.hub = p
	changed := (prev == nil) != (p == nil) || (p != nil && (prev.Slots != p.Slots || prev.State != p.State || prev.Follow != p.Follow))
	t.mu.Unlock()
	if changed {
		t.publish()
	}
	return changed
}

// PaceSettings are the user's "Follow weekly pace" settings.
type PaceSettings struct {
	Follow      bool `json:"follow"`
	MaxParallel int  `json:"max_parallel"` // most tasks at once when behind; 0 = twice the usual
	// MaxBoost is the hub's cap for an account: most tasks at once when
	// behind, as a multiple of its runners' usual (0 = DefaultBoost).
	MaxBoost float64 `json:"max_boost,omitempty"`
	// Account is the Claude account the hub's setting is for (optional:
	// the caller's own).
	Account string `json:"account,omitempty"`
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
	pace := t.pace
	if t.hub != nil {
		// The hub decides; the queue may run as many as it sends.
		pace = *t.hub
		pace.Parallel = t.pace.Parallel
		pace.Slots = max(pace.Slots, t.pace.Parallel)
	}
	s := Snapshot{Known: t.known, Status: t.status, UpdatedAt: t.updated, ManualPause: t.manual, Pace: pace}
	for _, w := range t.windows {
		s.Windows = append(s.Windows, w)
	}
	sort.Slice(s.Windows, func(i, j int) bool { return s.Windows[i].ResetsAt.Before(s.Windows[j].ResetsAt) })

	if t.manual {
		s.Paused, s.Reason = true, "Paused by you"
	}
	s = s.Reevaluate(now)
	if t.hub == nil {
		t.pace = s.Pace // its state carries over to the next evaluation
	}
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
	s.Pace = s.Pace.next(WeekOf(s.Windows, now), now)
	if s.ManualPause {
		return s
	}
	s.Paused, s.Reason, s.Until, s.Pacing = false, "", time.Time{}, false
	for _, w := range s.Windows {
		if !w.ResetsAt.After(now) {
			continue // window already reset; its numbers are stale
		}
		if Limited(w.Status) {
			s.Paused, s.Reason = true, label(w.Name)+" limit reached"
			if w.ResetsAt.After(s.Until) {
				s.Until = w.ResetsAt
			}
		}
	}
	// Ahead of the weekly pace: wait until the line catches up. The hard
	// stops above win, as their pause lasts until a reset.
	if !s.Paused && s.Pace.holds() {
		s = s.waitForPace(now)
	}
	return s
}

func (s Snapshot) waitForPace(now time.Time) Snapshot {
	s.Paused, s.Reason, s.Until, s.Pacing = true, s.Pace.Reason(), now.Add(s.Pace.Gap()).Truncate(time.Second), true
	return s
}

// WithPace is s, reevaluated as of now, with the hub's pace p of the
// account it is for: waiting for the line, it says so (as a hard stop
// isn't already).
func (s Snapshot) WithPace(p Pace, now time.Time) Snapshot {
	s.Pace = p
	s.Pace.Hub = true
	s = s.Reevaluate(now)
	if !s.Paused && p.Waiting() {
		s = s.waitForPace(now)
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
