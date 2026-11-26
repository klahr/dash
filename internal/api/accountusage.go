package api

import (
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/tunnel"
	"github.com/klahr/dash/internal/usage"
)

// Usage per Claude account. Runners logged in to one Claude account share
// its usage (the Runners sheet says "Shares its Claude account, and its
// usage, with …"), so the dashboard's Claude usage sheet and the wall show
// one set of meters per account, each from the newest report of the
// account's online runners, rather than whichever runner reported last.

// usageGroup is the online runners on one Claude account and the newest
// usage report of any of them.
type usageGroup struct {
	account string          // "" for a runner that hasn't said which account it is on
	runners []tunnel.Runner // by name
	usage   *usage.Snapshot // the newest report
	active  bool            // one of them may start tasks (online, not paused or updating)
}

// usageGroups groups the online runners of runners that reported their
// usage by Claude account, as the hub paces them (dispatch.Group): a runner
// that hasn't said which account it is on is a group of its own. Sorted by
// account, then by the first runner's name.
func usageGroups(runners []tunnel.Runner) []usageGroup {
	var out []usageGroup
	idx := map[string]int{}
	for _, r := range runners {
		if !r.Online {
			continue
		}
		key := "runner:" + r.ID
		if a := r.Account(); a != "" {
			key = "account:" + a
		}
		i, ok := idx[key]
		if !ok {
			i = len(out)
			idx[key] = i
			out = append(out, usageGroup{account: r.Account()})
		}
		g := &out[i]
		g.runners = append(g.runners, r)
		g.active = g.active || (!r.Paused && r.Update == nil)
		if r.Reported != nil && r.Reported.Usage != nil && r.Reported.Usage.Known &&
			(g.usage == nil || r.Reported.Usage.UpdatedAt.After(g.usage.UpdatedAt)) {
			g.usage = r.Reported.Usage
		}
	}
	out = slices.DeleteFunc(out, func(g usageGroup) bool { return g.usage == nil })
	for i := range out {
		slices.SortFunc(out[i].runners, func(a, b tunnel.Runner) int { return strings.Compare(a.Name, b.Name) })
	}
	slices.SortFunc(out, func(a, b usageGroup) int {
		if c := strings.Compare(a.account, b.account); c != 0 {
			return c
		}
		return strings.Compare(a.runners[0].Name, b.runners[0].Name)
	})
	return out
}

// names are g's runners' names, as name gives them.
func (g usageGroup) names(name func(tunnel.Runner) string) []string {
	out := make([]string, 0, len(g.runners))
	for _, r := range g.runners {
		out = append(out, name(r))
	}
	return out
}

// accountUsage is one Claude account's usage in GET /api/usage.
type accountUsage struct {
	usage.Snapshot
	// Account is the account's email ("" for a runner that hasn't said).
	Account string `json:"account,omitempty"`
	// Label names the account: its email, else its runner's name.
	Label string `json:"label"`
	// Runners are the caller's runners on it that reported, by name.
	Runners []string `json:"runners,omitempty"`
	// Active: one of them may start tasks now.
	Active bool `json:"active"`
}

// usageView is GET /api/usage: the usage as it always was (on a hub, the
// caller's account's, see usageFor), and on a hub each of the caller's
// Claude accounts on its own, in Accounts. With more than one, the queue
// is paused (Paused, Reason) only while every account is (combinePause).
type usageView struct {
	usage.Snapshot
	Accounts []accountUsage `json:"accounts,omitempty"`
}

// waits is one account's pause, for combinePause.
type waits struct {
	active bool // it could run tasks at all
	snap   usage.Snapshot
}

// combinePause sets s's pause from the accounts' when there are several: the
// queue waits on limits only while every account that could run tasks
// (active; all of them when none could) is paused. A manual pause stays as
// it is. Pacing is set when one of them waits only for the pace, so a task
// started now (model.Todo.StartNow) runs there.
func combinePause(s *usage.Snapshot, accts []waits) {
	if s.ManualPause || len(accts) < 2 {
		return
	}
	if slices.ContainsFunc(accts, func(a waits) bool { return a.active }) {
		accts = slices.DeleteFunc(slices.Clone(accts), func(a waits) bool { return !a.active })
	}
	s.Paused, s.Pacing, s.Reason, s.Until = false, false, "", time.Time{}
	if slices.ContainsFunc(accts, func(a waits) bool { return !a.snap.Paused }) {
		return
	}
	var reasons []string
	for _, a := range accts {
		s.Pacing = s.Pacing || a.snap.Pacing
		if r := a.snap.Reason; r != "" && !slices.Contains(reasons, r) {
			reasons = append(reasons, r)
		}
		if u := a.snap.Until; !u.IsZero() && (s.Until.IsZero() || u.Before(s.Until)) {
			s.Until = u // when the first of them may start again
		}
	}
	s.Paused = true
	s.Reason = "Every Claude account waits"
	if len(reasons) > 0 {
		s.Reason += ": " + strings.Join(reasons, "; ")
	}
}

// accountSnapshot is the usage of Claude account acct as c sees it on the
// hub: u, its newest report (or the hub's pacer's, if fresher), with the
// queue's manual pause and the hub's pace of the account, reevaluated as of
// now (the runner's verdict may be stale: its windows may have reset since).
func (s *Server) accountSnapshot(c Caller, acct string, u *usage.Snapshot, manual bool, reason string, now time.Time) usage.Snapshot {
	hubPace := usage.Pace{Hub: true, Account: acct}
	if acct != "" && s.Dispatch != nil && s.Dispatch.Pacer != nil {
		if pace := s.Dispatch.Pacer.Account(acct); pace != nil {
			if pace.Usage != nil && (u == nil || pace.Usage.UpdatedAt.After(u.UpdatedAt)) {
				u = pace.Usage // the account's freshest figures
			}
			hubPace = pace.Pace
			hubPace.Runners = s.accountRunners(c, acct)
		}
	}
	var snap usage.Snapshot
	if u != nil {
		snap = *u
	}
	snap.ManualPause = manual
	if manual {
		snap.Paused, snap.Reason, snap.Until = true, reason, time.Time{}
	}
	return snap.WithPace(hubPace, now)
}

// callerAccounts is the usage of each Claude account c's online runners in
// c's org are on (usageGroups), for the dashboard: labelled with the
// account's email, as these are c's own runners.
func (s *Server) callerAccounts(c Caller, manual bool, reason string, now time.Time) []accountUsage {
	var out []accountUsage
	for _, g := range usageGroups(s.claudeRunners(c)) {
		a := accountUsage{Snapshot: s.accountSnapshot(c, g.account, g.usage, manual, reason, now), Account: g.account, Label: g.account,
			Runners: g.names(func(r tunnel.Runner) string { return r.Name }), Active: g.active}
		if a.Label == "" {
			a.Label = strings.Join(a.Runners, ", ")
		}
		out = append(out, a)
	}
	return out
}
