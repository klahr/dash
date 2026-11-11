package usage

import (
	"fmt"
	"time"
)

// Following the weekly pace: with Pace.Follow on, the queue keeps weekly
// usage close to the pace line the usage meters draw, 0% at the start of the
// weekly window rising evenly to 100% at its reset. Ahead of the line it
// starts no new tasks (running ones finish); behind it, it runs more at once
// to catch up. Only the weekly window paces; the 5-hour window, PauseAt and
// a manual pause stop the queue as before.

const (
	// WeekWindow is the window that paces the queue.
	WeekWindow = "seven_day"
	// WeekSpan is the weekly window's length: it started WeekSpan before it resets.
	WeekSpan = 7 * 24 * time.Hour
	// PaceDeadBand is how far (as window time) usage may run ahead of or
	// behind the line and count as on pace. Ahead by more, the queue waits;
	// once waiting, it waits until the line has caught up (gap <= 0), so one
	// usage sample doesn't flip it between waiting and starting.
	PaceDeadBand = 5 * time.Minute
	// PaceSlotStep is how far behind earns one more task at a time: any lag
	// beyond the dead band earns the first, each PaceSlotStep more another.
	PaceSlotStep = 3 * time.Hour
	// PaceSlotHold is how far the lag must shrink below a slot's threshold
	// before that slot is given up again. The last extra slot is kept until
	// the line is reached.
	PaceSlotHold = 15 * time.Minute
)

// Pacing states.
const (
	PaceOff      = ""            // not following the pace
	PaceUnknown  = "unknown"     // following, but no live weekly figures yet
	PaceWaiting  = "waiting"     // ahead of the line: no new tasks
	PaceOnPace   = "on_pace"     // within the dead band: MaxParallel tasks
	PaceCatching = "catching_up" // behind: up to Slots tasks
)

// Pace is the "Follow weekly pace" setting and where the queue stands with it.
type Pace struct {
	Follow bool `json:"follow"`
	// MaxParallel caps the tasks at once while catching up; 0 = twice Parallel.
	MaxParallel int `json:"max_parallel"`
	Parallel    int `json:"parallel"` // tasks at once on pace (the queue's own limit)

	State string `json:"state,omitempty"`
	// GapS is weekly usage minus the pace line, as seconds of window time
	// (> 0 ahead, < 0 behind).
	GapS  int64 `json:"gap_s"`
	Slots int   `json:"slots"` // tasks the queue may run at once now
}

// Cap is the most tasks at once while catching up.
func (p Pace) Cap() int {
	c := p.MaxParallel
	if c <= 0 {
		c = 2 * p.Parallel
	}
	return max(c, p.Parallel)
}

// Gap is GapS as a duration.
func (p Pace) Gap() time.Duration { return time.Duration(p.GapS) * time.Second }

// Waiting reports whether pacing holds new tasks back.
func (p Pace) Waiting() bool { return p.Follow && p.State == PaceWaiting }

// Reason is why the queue waits, e.g. "Waiting 1h 20m to stay on pace".
func (p Pace) Reason() string {
	return "Waiting " + Duration(p.Gap()) + " to stay on pace"
}

// PaceGap is how far usage is ahead (> 0) or behind (< 0) of the pace line
// at now, as window time, for a window of length span resetting at resetsAt.
func PaceGap(utilization float64, resetsAt, now time.Time, span time.Duration) time.Duration {
	elapsed := float64(now.Sub(resetsAt.Add(-span))) / float64(span)
	elapsed = min(1, max(0, elapsed))
	return time.Duration((utilization - elapsed) * float64(span))
}

// next is p as of now, given the weekly window (nil: none known or live).
// p's State and Slots are the previous evaluation's, for hysteresis.
func (p Pace) next(week *Window, now time.Time) Pace {
	base := max(p.Parallel, 0)
	if !p.Follow {
		p.State, p.GapS, p.Slots = PaceOff, 0, base
		return p
	}
	if week == nil {
		p.State, p.GapS, p.Slots = PaceUnknown, 0, base
		return p
	}
	gap := PaceGap(week.Utilization, week.ResetsAt, now, WeekSpan)
	p.GapS = int64(gap / time.Second)
	prevExtra := 0
	if p.State == PaceCatching {
		prevExtra = max(0, p.Slots-base)
	}
	switch {
	case gap > PaceDeadBand, p.State == PaceWaiting && gap > 0:
		p.State, p.Slots = PaceWaiting, base
		return p
	}
	behind := -gap
	extra := paceExtra(behind)
	if extra < prevExtra {
		// Give a slot up only once the lag is clearly below its threshold,
		// and the last one once the line is reached.
		held := paceExtra(behind + PaceSlotHold)
		if behind <= 0 {
			held = 0
		}
		extra = min(prevExtra, max(extra, held))
	}
	p.Slots = min(base+extra, p.Cap())
	if p.Slots > base {
		p.State = PaceCatching
	} else {
		p.State = PaceOnPace
	}
	return p
}

// paceExtra is how many more tasks at once a lag of behind earns.
func paceExtra(behind time.Duration) int {
	if behind <= PaceDeadBand {
		return 0
	}
	return int((behind - PaceDeadBand + PaceSlotStep - 1) / PaceSlotStep) // ceil
}

// Duration is d in its largest two units, as the usage meters write it:
// "45m", "2h 15m", "3d 6h", "<1m".
func Duration(d time.Duration) string {
	m := int64(max(0, d) / time.Minute)
	if m < 1 {
		return "<1m"
	}
	day, hr, mins := m/1440, (m%1440)/60, m%60
	switch {
	case day > 0 && hr > 0:
		return fmt.Sprintf("%dd %dh", day, hr)
	case day > 0:
		return fmt.Sprintf("%dd", day)
	case hr > 0 && mins > 0:
		return fmt.Sprintf("%dh %dm", hr, mins)
	case hr > 0:
		return fmt.Sprintf("%dh", hr)
	}
	return fmt.Sprintf("%dm", mins)
}
