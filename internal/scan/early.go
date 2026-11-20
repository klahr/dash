package scan

import (
	"fmt"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/usage"
)

// "Run early if there's spare usage" (model.EarlyRun): a scheduled scan
// still runs once per cron period, but it may start before its cron time,
// shortly before the 5-hour usage window resets while that window has room
// to spare, so usage that would go unused does the scan. Otherwise it runs
// at the cron time, as it would without the option.
const (
	// EarlyLead is how long before the 5-hour window resets the scan may
	// start, at most.
	EarlyLead = time.Hour
	// EarlyCeiling is the 5-hour window's utilization (0..1) the scan
	// starts below.
	EarlyCeiling = 0.70
	// NightFrom and NightUntil are the hours of the night, in this
	// computer's time zone (the one cron schedules use), for "At night
	// only": from 22:00 until 07:00, across midnight.
	NightFrom  = 22
	NightUntil = 7
)

// fiveHour is the usage window an early run waits for the reset of.
const fiveHour = "five_hour"

// Spare is a Claude account's usage, as an early run weighs it.
type Spare struct {
	// Account is, on a hub, the runners' Claude account ("": this Dash's
	// own).
	Account string
	Usage   usage.Snapshot // Known false: no usage reported
	// Hold is why the account may not start work now ("": it may).
	Hold string
}

// EarlyLeadOf and EarlyCeilingOf are e's lead time and ceiling: its
// overrides, or the defaults.
func EarlyLeadOf(e model.EarlyRun) time.Duration {
	if e.LeadMinutes > 0 {
		return time.Duration(e.LeadMinutes) * time.Minute
	}
	return EarlyLead
}

func EarlyCeilingOf(e model.EarlyRun) float64 {
	if e.MaxPercent > 0 {
		return float64(e.MaxPercent) / 100
	}
	return EarlyCeiling
}

// ValidEarly checks an early-run setting from the API.
func ValidEarly(e model.EarlyRun) error {
	switch e.When {
	case "", model.EarlyAnyTime, model.EarlyNight:
	default:
		return fmt.Errorf("when must be %q or %q", model.EarlyAnyTime, model.EarlyNight)
	}
	if e.LeadMinutes < 0 || e.LeadMinutes > 5*60 {
		return fmt.Errorf("lead_minutes must be between 0 and 300")
	}
	if e.MaxPercent < 0 || e.MaxPercent > 100 {
		return fmt.Errorf("max_percent must be between 0 and 100")
	}
	return nil
}

// AtNight reports whether t, in loc, is in the night range.
func AtNight(t time.Time, loc *time.Location) bool {
	h := t.In(loc).Hour()
	return h >= NightFrom || h < NightUntil
}

// SpareNow reports whether u has room for an early run of e at now: the
// 5-hour window resets within the lead time (and, at night only, at night
// in now's time zone) with its utilization below the ceiling. Without
// usage figures, or with stale ones (the window already reset), it hasn't:
// the scan waits for its cron time.
func SpareNow(e model.EarlyRun, u usage.Snapshot, now time.Time) (bool, string) {
	if !u.Known {
		return false, "no usage reported"
	}
	var w *usage.Window
	for i := range u.Windows {
		if u.Windows[i].Name == fiveHour {
			w = &u.Windows[i]
		}
	}
	switch {
	case w == nil || w.ResetsAt.IsZero():
		return false, "no 5-hour window reported"
	case !w.ResetsAt.After(now):
		return false, "5-hour usage is stale"
	case w.ResetsAt.Sub(now) > EarlyLeadOf(e):
		return false, "the 5-hour window resets later"
	case w.Utilization >= EarlyCeilingOf(e):
		return false, fmt.Sprintf("5-hour usage at %.0f%%", w.Utilization*100)
	case e.When == model.EarlyNight && !AtNight(w.ResetsAt, now.Location()):
		return false, "the 5-hour window resets during the day"
	}
	return true, ""
}
