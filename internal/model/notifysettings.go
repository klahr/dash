package model

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	// Users' time zones must load wherever the hub runs (Windows has no
	// zoneinfo of its own).
	_ "time/tzdata"
)

// Summary frequencies: how often a user's summary goes out.
const (
	SummaryOff    = "off"
	SummaryDaily  = "daily"
	SummaryWeekly = "weekly"
)

// Summary item kinds: what a summary can tell about, each ticked on or off
// in SummaryInclude.
const (
	SummaryIssue  = "issue"  // a scan or Claude found a new issue
	SummaryReview = "review" // a task became ready for review
	SummaryFailed = "failed" // a task failed
	SummaryMerged = "merged" // a task's work landed
	SummaryGoal   = "goal"   // one of the user's goals waits for them
	SummaryBuild  = "build"  // GitHub Actions started failing on the default branch
)

// SummaryKinds are the item kinds, in the order a summary lists them.
var SummaryKinds = []string{SummaryIssue, SummaryReview, SummaryFailed, SummaryMerged, SummaryGoal, SummaryBuild}

// SummaryInclude is what goes in a summary.
type SummaryInclude struct {
	Issues bool `json:"issues"`
	// MinSeverity leaves out new issues less severe than it.
	MinSeverity Severity `json:"min_severity"`
	Review      bool     `json:"review"`
	Failed      bool     `json:"failed"`
	Merged      bool     `json:"merged"`
	Goals       bool     `json:"goals"`
	Builds      bool     `json:"builds"`
}

// Wants reports whether an item of kind (with severity sev, for issues)
// goes in the summary.
func (in SummaryInclude) Wants(kind string, sev Severity) bool {
	switch kind {
	case SummaryIssue:
		return in.Issues && sev.Rank() <= in.MinSeverity.Rank()
	case SummaryReview:
		return in.Review
	case SummaryFailed:
		return in.Failed
	case SummaryMerged:
		return in.Merged
	case SummaryGoal:
		return in.Goals
	case SummaryBuild:
		return in.Builds
	}
	return false
}

// NotifySettings are one user's notification settings in one org: their
// summary (how often, when, of which projects, where it goes, what is in
// it) and which events push to their browsers.
type NotifySettings struct {
	Summary string `json:"summary"` // SummaryOff, SummaryDaily or SummaryWeekly
	// Time is when the summary goes out, "HH:MM" in TimeZone.
	Time string `json:"time"`
	// Weekday is the day a weekly summary goes out (0 = Sunday).
	Weekday  time.Weekday `json:"weekday"`
	TimeZone string       `json:"time_zone"` // IANA name, e.g. "Europe/Stockholm"
	// AllProjects covers every project of the org, new ones too; else
	// Projects, by id.
	AllProjects bool     `json:"all_projects"`
	Projects    []string `json:"projects"`
	// Email sends the summary to the user's address; Webhooks to these of
	// their personal webhooks in the org, by id.
	Email    bool           `json:"email"`
	Webhooks []string       `json:"webhooks"`
	Include  SummaryInclude `json:"include"`
	// Push are the events pushed to the user's browsers.
	Push []NotifyEvent `json:"push"`
}

// PushChoices are the events a user may have pushed to their browsers.
var PushChoices = []NotifyEvent{NotifyReview, NotifyFailed, NotifyPermission, NotifyGoal, NotifyBuild, NotifyDisk, NotifyIssue}

// DefaultPush are the events pushed to a user who hasn't chosen: all
// but new issues (which can be many).
var DefaultPush = []NotifyEvent{NotifyReview, NotifyFailed, NotifyPermission, NotifyGoal, NotifyBuild, NotifyDisk}

// DefaultNotifySettings are a user's settings until they save theirs:
// no summary, every push but new issues.
func DefaultNotifySettings() NotifySettings {
	return NotifySettings{
		Summary: SummaryOff, Time: "08:00", Weekday: time.Monday, TimeZone: "UTC",
		AllProjects: true, Projects: []string{}, Email: true, Webhooks: []string{},
		Include: SummaryInclude{Issues: true, MinSeverity: SevMedium, Review: true, Failed: true, Merged: true, Goals: true, Builds: true},
		Push:    slices.Clone(DefaultPush),
	}
}

// On reports whether the summary is sent at all.
func (s NotifySettings) On() bool { return s.Summary == SummaryDaily || s.Summary == SummaryWeekly }

// Period is how long a summary covers: a day or a week.
func (s NotifySettings) Period() time.Duration {
	if s.Summary == SummaryWeekly {
		return 7 * 24 * time.Hour
	}
	return 24 * time.Hour
}

// Location is the settings' time zone (UTC if it can't be loaded).
func (s NotifySettings) Location() *time.Location {
	if loc, err := time.LoadLocation(s.TimeZone); err == nil {
		return loc
	}
	return time.UTC
}

// clock is Time's hour and minute.
func (s NotifySettings) clock() (int, int, bool) {
	if len(s.Time) != 5 || s.Time[2] != ':' {
		return 0, 0, false
	}
	hh, err1 := strconv.Atoi(s.Time[:2])
	mm, err2 := strconv.Atoi(s.Time[3:])
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 || s.Time[0] == '+' || s.Time[3] == '+' || s.Time[0] == '-' || s.Time[3] == '-' {
		return 0, 0, false
	}
	return hh, mm, true
}

// Next is when the summary goes out next, strictly after after: Time on
// the next day (or Weekday) in TimeZone, as the wall clock there reads it,
// whatever daylight saving time does. A time that doesn't exist that day
// (skipped when the clocks go forward) is the moment after the gap. The
// zero time when the summary is off.
func (s NotifySettings) Next(after time.Time) time.Time {
	if !s.On() {
		return time.Time{}
	}
	hh, mm, ok := s.clock()
	if !ok {
		return time.Time{}
	}
	loc := s.Location()
	local := after.In(loc)
	for d := 0; d <= 8; d++ {
		day := time.Date(local.Year(), local.Month(), local.Day()+d, 0, 0, 0, 0, loc)
		if s.Summary == SummaryWeekly && day.Weekday() != s.Weekday {
			continue
		}
		at := time.Date(day.Year(), day.Month(), day.Day(), hh, mm, 0, 0, loc)
		if l := at.In(loc); l.Hour() != hh || l.Minute() != mm {
			// In a gap: time.Date may land before it; move past it.
			_, before := at.Zone()
			_, later := at.Add(12 * time.Hour).Zone()
			if later > before {
				at = at.Add(time.Duration(later-before) * time.Second)
			}
		}
		if at.After(after) {
			return at
		}
	}
	return time.Time{}
}

// Normalize checks s and fills in what is missing. It doesn't check that
// projects and webhooks exist: the caller does.
func (s *NotifySettings) Normalize() error {
	def := DefaultNotifySettings()
	s.Summary = strings.TrimSpace(s.Summary)
	switch s.Summary {
	case "":
		s.Summary = SummaryOff
	case SummaryOff, SummaryDaily, SummaryWeekly:
	default:
		return fmt.Errorf("summary must be off, daily or weekly, not %q", s.Summary)
	}
	if s.Time == "" {
		s.Time = def.Time
	}
	if _, _, ok := s.clock(); !ok {
		return fmt.Errorf("summary time must be HH:MM, not %q", s.Time)
	}
	if s.Weekday < time.Sunday || s.Weekday > time.Saturday {
		return errors.New("summary weekday must be 0 (Sunday) to 6 (Saturday)")
	}
	s.TimeZone = strings.TrimSpace(s.TimeZone)
	if s.TimeZone == "" {
		s.TimeZone = "UTC"
	}
	if _, err := time.LoadLocation(s.TimeZone); err != nil || strings.EqualFold(s.TimeZone, "local") {
		return fmt.Errorf("unknown time zone %q", s.TimeZone)
	}
	s.Projects = dedupe(s.Projects)
	s.Webhooks = dedupe(s.Webhooks)
	if s.Include.MinSeverity == "" {
		s.Include.MinSeverity = SevMedium
	}
	if s.Include.MinSeverity.Rank() > 4 {
		return fmt.Errorf("unknown severity %q", s.Include.MinSeverity)
	}
	if s.Push == nil {
		s.Push = []NotifyEvent{}
	}
	var push []NotifyEvent
	for _, e := range s.Push {
		if !slices.Contains(PushChoices, e) {
			return fmt.Errorf("%q can't be pushed", e)
		}
		if !slices.Contains(push, e) {
			push = append(push, e)
		}
	}
	if push == nil {
		push = []NotifyEvent{}
	}
	s.Push = push
	if s.On() {
		if !s.Email && len(s.Webhooks) == 0 {
			return errors.New("choose where the summary goes: email or a webhook")
		}
		if !s.AllProjects && len(s.Projects) == 0 {
			return errors.New("choose the projects the summary covers")
		}
	}
	return nil
}

// dedupe trims ids and drops empty and repeated ones (never nil).
func dedupe(ids []string) []string {
	out := []string{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// Activity is one moment a summary may tell about, as the notifier saw it.
type Activity struct {
	ID        int64     `json:"id"`
	ProjectID string    `json:"project_id"`
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"`  // one of SummaryKinds
	Ref       string    `json:"ref"`   // the task, goal or issue id (a build: the commit)
	Title     string    `json:"title"` // its title then
	URL       string    `json:"url"`   // where it opens
	Severity  Severity  `json:"severity,omitempty"`
	// User is who a goal's item is for (its owner); 0 = everyone.
	User int64 `json:"user,omitempty"`
}

// NotifyState is where a user's summary stands: when it goes out next,
// up to when the last one covered, and when one was last sent.
type NotifyState struct {
	NextAt   time.Time `json:"next_at,omitzero"`
	Through  time.Time `json:"through,omitzero"`
	LastSent time.Time `json:"last_sent,omitzero"`
}

// UserNotify is one user's settings in one org, with their summary's state.
type UserNotify struct {
	UserID   int64
	OrgID    int64
	Settings NotifySettings
	State    NotifyState
}
