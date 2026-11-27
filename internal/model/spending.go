package model

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Spending are a project's spending caps, in US dollars (docs/spending.md).
// A cap of 0 is no cap of that kind; all of them 0 is off.
type Spending struct {
	// TaskUSD caps what one task may cost: a run that reaches it is
	// stopped and the task fails (Todo.CapUSD raises it for one task).
	TaskUSD float64 `json:"task_usd,omitempty"`
	// DayUSD and MonthUSD cap what the project may cost in a day and in a
	// calendar month: once reached, the queue starts none of its tasks
	// until the next day or month, or until the cap is raised. Running
	// tasks finish.
	DayUSD   float64 `json:"day_usd,omitempty"`
	MonthUSD float64 `json:"month_usd,omitempty"`
	// Zone is the IANA time zone days and months start in ("": the time
	// zone of the computer Dash runs on).
	Zone string `json:"zone,omitempty"`
	// Warned are the periods already warned about at 80%, e.g.
	// "day:2026-11-27" and "month:2026-11", so each warns once. Set by
	// the queue, not by the API.
	Warned []string `json:"warned,omitempty"`
}

// MaxSpendUSD bounds a spending cap.
const MaxSpendUSD = 1_000_000

// SpendWarnShare is the share of a cap that warns.
const SpendWarnShare = 0.8

// On reports whether any cap is set.
func (s *Spending) On() bool { return s != nil && (s.TaskUSD > 0 || s.DayUSD > 0 || s.MonthUSD > 0) }

// Clone is a copy of s that shares nothing with it.
func (s *Spending) Clone() *Spending {
	if s == nil {
		return nil
	}
	c := *s
	c.Warned = append([]string(nil), s.Warned...)
	return &c
}

// Location is where s's days and months start.
func (s *Spending) Location() *time.Location {
	if s != nil && s.Zone != "" {
		if loc, err := time.LoadLocation(s.Zone); err == nil {
			return loc
		}
	}
	return time.Local
}

// Check reports what is wrong with s's caps, or nil.
func (s Spending) Check() error {
	for _, c := range []struct {
		name string
		v    float64
	}{{"task", s.TaskUSD}, {"day", s.DayUSD}, {"month", s.MonthUSD}} {
		if math.IsNaN(c.v) || c.v < 0 || c.v > MaxSpendUSD {
			return fmt.Errorf("the %s cap must be between $0 (no cap) and $%d", c.name, MaxSpendUSD)
		}
	}
	if s.Zone != "" {
		if _, err := time.LoadLocation(s.Zone); err != nil {
			return errors.New("unknown time zone " + s.Zone + ": use an IANA name such as Europe/Stockholm")
		}
	}
	return nil
}

// Periods of a project cap (SpendHold.Period, SpendWarning.Kind).
const (
	SpendTask  = "task"
	SpendDay   = "day"
	SpendMonth = "month"
)

// SpendHold says why a project's queued tasks don't start: it reached its
// Period's cap. Until is when that period ends.
type SpendHold struct {
	Period   string    `json:"period"` // SpendDay or SpendMonth
	CapUSD   float64   `json:"cap_usd"`
	SpentUSD float64   `json:"spent_usd"`
	Until    time.Time `json:"until"`
}

// Note is the hold as a queued task's waiting reason.
func (h SpendHold) Note() string {
	what := "daily"
	if h.Period == SpendMonth {
		what = "monthly"
	}
	return fmt.Sprintf("the project reached its %s spending cap ($%s): its tasks start again at the next %s, or when the cap is raised in Project settings → Spending",
		what, Dollars(h.CapUSD), h.Period)
}

// SpendWarning is EvSpendWarning's data: a task (TodoID) or a project
// reached SpendWarnShare of a cap.
type SpendWarning struct {
	ProjectID string  `json:"project_id"`
	Kind      string  `json:"kind"` // SpendTask, SpendDay or SpendMonth
	CapUSD    float64 `json:"cap_usd"`
	SpentUSD  float64 `json:"spent_usd"`
	TodoID    string  `json:"todo_id,omitempty"`
	OrgID     int64   `json:"-"`
}

// Message is the warning in words.
func (w SpendWarning) Message() string {
	what := map[string]string{SpendTask: "this task's cap", SpendDay: "the project's daily cap", SpendMonth: "the project's monthly cap"}[w.Kind]
	return fmt.Sprintf("$%s spent: %d%% of %s ($%s)", Dollars(w.SpentUSD), int(math.Round(100*w.SpentUSD/w.CapUSD)), what, Dollars(w.CapUSD))
}

// Dollars writes an amount without cents when it has none: "5", "2.50".
func Dollars(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.2f", v)
}

// TaskCap is the spending cap of task t in project p (0: none): its own,
// else the project's.
func TaskCap(p Project, t Todo) float64 {
	if t.CapUSD > 0 {
		return t.CapUSD
	}
	if p.Spending != nil {
		return p.Spending.TaskUSD
	}
	return 0
}

// TaskCapError is the error of a run stopped at its task's cap.
func TaskCapError(capUSD float64) string {
	return "Stopped at the $" + Dollars(capUSD) + " task cap"
}

// RaiseTaskCap raises the cap of task x, stopped at its cap, once: by
// another of the project's task caps (projectCap) on top of what it cost
// so far, or by as much again as the cap it hit when the project has none
// now. It reports whether it raised it. An update for store.UpdateTodo.
func RaiseTaskCap(x *Todo, projectCap float64) bool {
	if x.CapHit <= 0 {
		return false
	}
	step := projectCap
	if step <= 0 {
		step = x.CapHit
	}
	x.CapUSD = math.Max(x.CostUSD, x.CapHit) + step
	x.CapHit = 0
	return true
}
