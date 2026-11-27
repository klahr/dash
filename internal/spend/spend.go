package spend

import (
	"slices"
	"time"

	"github.com/klahr/dash/internal/model"
)

// Entry is one cost a project had: a task's run, or a scan's.
type Entry struct {
	At        time.Time
	USD       float64
	Estimated bool
	TodoID    string // "" for a scan
}

// Entries are the costs of a project's tasks and scans. A task's runs
// count when they started (Todo.RunStats); a task from before those were
// kept counts once, its whole cost, when it finished. Chats aren't
// counted: Dash keeps their tokens, not their cost or model.
func Entries(todos []model.Todo, scans []model.ScanRun) []Entry {
	var out []Entry
	for _, t := range todos {
		if len(t.RunStats) == 0 {
			if t.CostUSD == 0 {
				continue
			}
			at := t.CreatedAt
			if t.FinishedAt != nil {
				at = *t.FinishedAt
			} else if t.StartedAt != nil {
				at = *t.StartedAt
			}
			out = append(out, Entry{At: at, USD: t.CostUSD, TodoID: t.ID})
			continue
		}
		for _, r := range t.RunStats {
			if r.CostUSD != 0 {
				out = append(out, Entry{At: r.At, USD: r.CostUSD, Estimated: r.Estimated, TodoID: t.ID})
			}
		}
	}
	for _, s := range scans {
		if s.CostUSD != 0 {
			out = append(out, Entry{At: s.StartedAt, USD: s.CostUSD})
		}
	}
	return out
}

// DayStart and MonthStart are the first moment of the day and of the
// month t falls in, in loc.
func DayStart(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

func MonthStart(t time.Time, loc *time.Location) time.Time {
	y, m, _ := t.In(loc).Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, loc)
}

// Totals is what a project spent in the day and the month now falls in.
type Totals struct {
	Day, Month             float64
	DayStart, MonthStart   time.Time
	DayEnd, MonthEnd       time.Time
	DayKey, MonthKey       string // "day:2026-11-27", "month:2026-11": Spending.Warned's keys
	DayEstimated, MonthEst bool   // some of it is estimated
}

// TotalsAt adds up es for the day and month now falls in, in loc.
func TotalsAt(es []Entry, now time.Time, loc *time.Location) Totals {
	t := Totals{DayStart: DayStart(now, loc), MonthStart: MonthStart(now, loc)}
	t.DayEnd, t.MonthEnd = t.DayStart.AddDate(0, 0, 1), t.MonthStart.AddDate(0, 1, 0)
	t.DayKey = model.SpendDay + ":" + t.DayStart.Format(time.DateOnly)
	t.MonthKey = model.SpendMonth + ":" + t.MonthStart.Format("2006-01")
	for _, e := range es {
		if e.At.Before(t.MonthStart) || !e.At.Before(t.MonthEnd) {
			continue
		}
		t.Month += e.USD
		t.MonthEst = t.MonthEst || e.Estimated
		if !e.At.Before(t.DayStart) && e.At.Before(t.DayEnd) {
			t.Day += e.USD
			t.DayEstimated = t.DayEstimated || e.Estimated
		}
	}
	return t
}

// Hold is the cap of s that holds the project's tasks back now, given
// what it spent (t), or nil. The monthly cap goes first: it holds longer.
func Hold(s *model.Spending, t Totals) *model.SpendHold {
	if s == nil {
		return nil
	}
	if s.MonthUSD > 0 && t.Month >= s.MonthUSD {
		return &model.SpendHold{Period: model.SpendMonth, CapUSD: s.MonthUSD, SpentUSD: t.Month, Until: t.MonthEnd}
	}
	if s.DayUSD > 0 && t.Day >= s.DayUSD {
		return &model.SpendHold{Period: model.SpendDay, CapUSD: s.DayUSD, SpentUSD: t.Day, Until: t.DayEnd}
	}
	return nil
}

// Warning is a project cap reached SpendWarnShare of, not yet warned about.
type Warning struct {
	Key              string // Spending.Warned's key for the period
	Kind             string // model.SpendDay or model.SpendMonth
	CapUSD, SpentUSD float64
}

// Warnings are the project caps of s that t reached SpendWarnShare of in
// periods not warned about yet (s.Warned).
func Warnings(s *model.Spending, t Totals) []Warning {
	if s == nil {
		return nil
	}
	var out []Warning
	check := func(kind, key string, capUSD, spent float64) {
		if capUSD > 0 && spent >= capUSD*model.SpendWarnShare && !slices.Contains(s.Warned, key) {
			out = append(out, Warning{Key: key, Kind: kind, CapUSD: capUSD, SpentUSD: spent})
		}
	}
	check(model.SpendDay, t.DayKey, s.DayUSD, t.Day)
	check(model.SpendMonth, t.MonthKey, s.MonthUSD, t.Month)
	return out
}

// KeepWarned is warned without the keys of periods other than t's: the
// list stays short, and a period that comes back can't (they don't).
func KeepWarned(warned []string, t Totals) []string {
	var out []string
	for _, k := range warned {
		if k == t.DayKey || k == t.MonthKey {
			out = append(out, k)
		}
	}
	return out
}

// TaskWarns reports whether a task that cost spent warns of its cap
// capUSD: it reached SpendWarnShare of it, and that cap hasn't warned yet.
func TaskWarns(capUSD, spent, warned float64) bool {
	return capUSD > 0 && spent >= capUSD*model.SpendWarnShare && warned != capUSD
}

// Day is one day's cost in a trend.
type Day struct {
	Date      string  `json:"date"` // YYYY-MM-DD
	USD       float64 `json:"cost_usd"`
	Estimated bool    `json:"estimated,omitempty"`
}

// Trend is the cost of each of the days days up to and including now's,
// oldest first, in loc.
func Trend(es []Entry, now time.Time, loc *time.Location, days int) []Day {
	end := DayStart(now, loc).AddDate(0, 0, 1)
	start := end.AddDate(0, 0, -days)
	out := make([]Day, days)
	for i := range out {
		out[i].Date = start.AddDate(0, 0, i).Format(time.DateOnly)
	}
	for _, e := range es {
		if e.At.Before(start) || !e.At.Before(end) {
			continue
		}
		d := DayStart(e.At, loc).Format(time.DateOnly)
		for i := range out {
			if out[i].Date == d {
				out[i].USD += e.USD
				out[i].Estimated = out[i].Estimated || e.Estimated
				break
			}
		}
	}
	return out
}

// TaskEstimated reports whether any of t's cost is estimated.
func TaskEstimated(t model.Todo) bool {
	for _, r := range t.RunStats {
		if r.Estimated && r.CostUSD != 0 {
			return true
		}
	}
	return false
}
