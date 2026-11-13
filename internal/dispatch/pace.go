package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/disk"
	"github.com/klahr/dash/internal/fsutil"
	"github.com/klahr/dash/internal/tunnel"
	"github.com/klahr/dash/internal/usage"
)

// Following the weekly pace on the hub. Runners logged in to one Claude
// account share its usage limits, so the hub paces them as one budget: the
// "Follow weekly pace" setting is the account's, and while it is on, the
// hub works out from the account's weekly usage (the freshest any of its
// runners reported) whether the group is ahead of the pace line (it sends
// none of them new tasks), on it (each its usual number at once) or behind
// it (more at once, spread over the group, up to the setting's boost times
// the group's usual). Runners are grouped by the account's email, not its
// Claude organization: every seat of a Team or Enterprise plan has its own
// limits. A runner that hasn't said which account it is on is paced alone
// (its setting is off). Runners then don't pace themselves: the hub tells
// each how it paces it in its answer to the runner's report (Reply), and
// an older hub, which doesn't, leaves runners pacing themselves as before.

// Pacer keeps the accounts' pace settings and the hysteresis of their
// pacing.
type Pacer struct {
	// Runners lists the hub's runners (tunnel.Hub.ListBare).
	Runners func() []tunnel.Runner

	now func() time.Time

	mu       sync.Mutex
	path     string
	settings map[string]usage.PaceSettings // account email → its setting
	last     map[string]usage.Pace         // group key → its last evaluation
}

// GroupPace is how the hub paces a group of runners on one account.
type GroupPace struct {
	usage.Pace // the group's: Parallel and Slots are the whole group's
	// Slots is how many tasks each of the group's runners may have at once
	// now (runner id → count).
	Slots map[string]int `json:"runner_slots"`
	// Stop is why none of them may start a task now, whatever the pace: the
	// account's freshest usage has a window at its limit.
	Stop string `json:"stop,omitempty"`
	// Usage is the account's freshest usage report (nil: none yet).
	Usage *usage.Snapshot `json:"-"`
}

// Reply is the hub's answer to a runner's report.
type Reply struct {
	// Pace is how the hub paces the runner: Slots is how many of the hub's
	// tasks it may have at once now. The runner doesn't pace itself.
	Pace *usage.Pace `json:"pace,omitempty"`
	// Disk are the runner's disk thresholds and Go build cache limit, as set
	// on the hub (Runners → the runner → Disk).
	Disk *disk.Settings `json:"disk,omitempty"`
}

func (p *Pacer) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// Keep loads the accounts' settings from path and saves changes there.
func (p *Pacer) Keep(path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.path = path
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f struct {
		Accounts map[string]usage.PaceSettings `json:"accounts"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	p.settings = map[string]usage.PaceSettings{}
	for k, s := range f.Accounts {
		p.settings[strings.ToLower(k)] = cleanSettings(s)
	}
	return nil
}

func cleanSettings(s usage.PaceSettings) usage.PaceSettings {
	return usage.PaceSettings{Follow: s.Follow, MaxBoost: min(max(s.MaxBoost, 0), usage.MaxBoostCap)}
}

// ValidBoost reports whether b is a boost an account may be set to (0: the
// default).
func ValidBoost(b float64) bool { return b == 0 || (b >= 1 && b <= usage.MaxBoostCap) }

// Set changes account's settings and saves them.
func (p *Pacer) Set(account string, s usage.PaceSettings) error {
	account = strings.ToLower(strings.TrimSpace(account))
	if account == "" {
		return errors.New("no Claude account to pace")
	}
	if !ValidBoost(s.MaxBoost) {
		return fmt.Errorf("max_boost must be between 1 and %g", usage.MaxBoostCap)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.settings == nil {
		p.settings = map[string]usage.PaceSettings{}
	}
	s = cleanSettings(s)
	if !s.Follow && s.MaxBoost == 0 {
		delete(p.settings, account)
	} else {
		p.settings[account] = s
	}
	if !s.Follow {
		delete(p.last, groupKey(account, ""))
	}
	if p.path == "" {
		return nil
	}
	b, _ := json.MarshalIndent(map[string]any{"accounts": p.settings}, "", "  ")
	return fsutil.WriteFileAtomic(p.path, b, 0o600)
}

// Settings is account's setting.
func (p *Pacer) Settings(account string) usage.PaceSettings {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.settings[strings.ToLower(account)]
}

func groupKey(account, runnerID string) string {
	if account != "" {
		return "account:" + account
	}
	return "runner:" + runnerID
}

// Group is the runners paced together with r: those on its account, or r
// alone when it hasn't said which it is on. all is every runner of the hub.
func Group(all []tunnel.Runner, r tunnel.Runner) []tunnel.Runner {
	acct := r.Account()
	if acct == "" {
		return []tunnel.Runner{r}
	}
	var out []tunnel.Runner
	for _, x := range all {
		if x.Account() == acct {
			out = append(out, x)
		}
	}
	if !slices.ContainsFunc(out, func(x tunnel.Runner) bool { return x.ID == r.ID }) {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b tunnel.Runner) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// For is how the hub paces r's group now.
func (p *Pacer) For(r tunnel.Runner) GroupPace {
	var all []tunnel.Runner
	if p.Runners != nil {
		all = p.Runners()
	}
	return p.Evaluate(Group(all, r))
}

// Account is how the hub paces the runners on account now (nil: none of
// the hub's runners is on it).
func (p *Pacer) Account(account string) *GroupPace {
	account = strings.ToLower(account)
	if account == "" || p.Runners == nil {
		return nil
	}
	all := p.Runners()
	i := slices.IndexFunc(all, func(r tunnel.Runner) bool { return r.Account() == account })
	if i < 0 {
		return nil
	}
	g := p.Evaluate(Group(all, all[i]))
	return &g
}

// active reports whether the hub may send r tasks at all (not while it
// is updating, see tunnel.Update).
func active(r tunnel.Runner) bool { return r.Online && !r.Paused && r.Update == nil }

// Evaluate paces group, runners on one account (or one runner on none).
func (p *Pacer) Evaluate(group []tunnel.Runner) GroupPace {
	now := p.clock()
	g := GroupPace{Slots: map[string]int{}}
	if len(group) == 0 {
		return g
	}
	acct := group[0].Account()
	key := groupKey(acct, group[0].ID)

	base := 0
	var names []string
	for _, r := range group {
		g.Slots[r.ID] = r.Limit()
		names = append(names, r.Name)
		if active(r) {
			base += r.Limit()
		}
		if u := reportedUsage(r); u != nil && (g.Usage == nil || u.UpdatedAt.After(g.Usage.UpdatedAt)) {
			g.Usage = u
		}
	}
	var week *usage.Window
	if g.Usage != nil {
		week = usage.WeekOf(g.Usage.Windows, now)
		// The hard stops, from the freshest figures: a runner that reported
		// earlier may not know yet that the account reached a limit.
		hard := *g.Usage
		hard.ManualPause, hard.Pace = false, usage.Pace{}
		if h := hard.Reevaluate(now); h.Paused {
			g.Stop = h.Reason
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	var set usage.PaceSettings
	if acct != "" {
		set = p.settings[acct]
	}
	pace := usage.Pace{Follow: set.Follow, MaxBoost: set.MaxBoost, Parallel: base, MaxParallel: usage.BoostCap(base, set.MaxBoost)}
	if prev, ok := p.last[key]; ok && set.Follow {
		// Carry the state over, and the extra tasks at once, even if the
		// group's usual changed since (a runner came or went).
		pace.State = prev.State
		pace.Slots = base + max(0, prev.Slots-prev.Parallel)
	}
	pace = pace.Evaluate(week, now)
	if set.Follow {
		if p.last == nil {
			p.last = map[string]usage.Pace{}
		}
		p.last[key] = pace
	} else {
		delete(p.last, key)
	}
	pace.Hub, pace.Account, pace.Runners = true, acct, names
	g.Pace = pace
	spread(g.Slots, group, pace.Slots-base)
	return g
}

// spread gives extra more tasks at once to group's active runners, in
// proportion to their usual: each next one to the runner with the fewest
// extra relative to its limit.
func spread(slots map[string]int, group []tunnel.Runner, extra int) {
	got := map[string]int{}
	for range max(extra, 0) {
		best := -1
		for i, r := range group {
			if !active(r) {
				continue
			}
			// got/limit smallest; the earlier name on a tie.
			if best < 0 || got[r.ID]*group[best].Limit() < got[group[best].ID]*r.Limit() {
				best = i
			}
		}
		if best < 0 {
			return
		}
		got[group[best].ID]++
		slots[group[best].ID]++
	}
}

// reportedUsage is r's last usage report, if it knows its usage.
func reportedUsage(r tunnel.Runner) *usage.Snapshot {
	if r.Reported == nil || r.Reported.Usage == nil || !r.Reported.Usage.Known {
		return nil
	}
	return r.Reported.Usage
}

// RunnerPace is the pace the hub tells runner r in its Reply: the group's,
// with Slots and Parallel r's own.
func (g GroupPace) RunnerPace(r tunnel.Runner) usage.Pace {
	p := g.Pace
	p.Runners = nil
	p.Parallel, p.Slots = r.Limit(), g.Slots[r.ID]
	p.MaxParallel = 0
	return p
}
