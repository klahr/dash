package api

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/claudecli"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/tunnel"
	"github.com/klahr/dash/internal/usage"
)

// usageRunner is runner name, online or not, on Claude account email ("":
// it hasn't said), that reported 5-hour usage five at at; limited: the
// window is at its limit.
func usageRunner(name string, online bool, email string, five float64, limited bool, at time.Time) tunnel.Runner {
	c := &claudecli.Status{Version: "2.1.0"}
	if email != "" {
		c.Account = &claudecli.Account{Email: email}
	}
	status := "allowed"
	if limited {
		status = "rejected"
	}
	return tunnel.Runner{ID: "id-" + name, Name: name, Online: online, Reported: &tunnel.Reported{At: at, Claude: c,
		Usage: &usage.Snapshot{Known: true, UpdatedAt: at, Windows: []usage.Window{
			{Name: usage.FiveHourWindow, Utilization: five, ResetsAt: at.Add(2 * time.Hour), Status: status}}}}}
}

// The online runners' reports are grouped by Claude account, the newest
// report of each; a runner that hasn't said which account it is on is one
// of its own, and offline runners don't count.
func TestUsageGroupsByAccount(t *testing.T) {
	parallel(t)
	now := time.Now()
	gs := usageGroups([]tunnel.Runner{
		usageRunner("laptop", true, "alice@example.com", 0.9, false, now.Add(-time.Hour)),
		usageRunner("desk", true, "Alice@example.com", 0.3, false, now),
		usageRunner("tower", true, "bob@example.com", 0.5, false, now),
		usageRunner("away", false, "carol@example.com", 0.1, false, now),
		usageRunner("old", true, "", 0.2, false, now),
	})
	var got []string
	for _, g := range gs {
		got = append(got, g.account+"="+strings.Join(g.names(func(r tunnel.Runner) string { return r.Name }), "+"))
	}
	if want := []string{"=old", "alice@example.com=desk+laptop", "bob@example.com=tower"}; !slices.Equal(got, want) {
		t.Fatalf("groups %q, want %q", got, want)
	}
	if u := gs[1].usage.Windows[0].Utilization; u != 0.3 {
		t.Errorf("alice's usage %v, want desk's newer 0.3", u)
	}
}

// The wall shows one set of meters per account. With a wall token it names
// none by email; and the queue waits on limits only when every account that
// could run tasks is out of usage.
func TestWallUsagePerAccount(t *testing.T) {
	parallel(t)
	s := &Server{Peers: &peer.Hub{Runners: &tunnel.Hub{}}}
	now := time.Now()
	name := func(r tunnel.Runner) string { return r.Name }
	wall := func(runners ...tunnel.Runner) wallData {
		var d wallData
		s.wallQueue(&d, Caller{Org: 1}, runners, name)
		return d
	}

	d := wall(usageRunner("laptop", true, "alice@example.com", 1, true, now),
		usageRunner("tower", true, "bob@example.com", 0.4, false, now))
	if len(d.Usage.Accounts) != 2 || d.Usage.Accounts[0].Label != "laptop" || d.Usage.Accounts[1].Label != "tower" {
		t.Fatalf("accounts %+v, want laptop's and tower's, by runner name", d.Usage.Accounts)
	}
	if !d.Usage.Accounts[0].Paused || d.Usage.Accounts[1].Paused {
		t.Errorf("accounts %+v, want alice's paused, bob's not", d.Usage.Accounts)
	}
	if d.Queue.Paused {
		t.Errorf("queue paused (%s) while bob's account has usage", d.Queue.Reason)
	}

	d = wall(usageRunner("laptop", true, "alice@example.com", 1, true, now),
		usageRunner("tower", true, "bob@example.com", 1, true, now))
	if !d.Queue.Paused || !strings.Contains(d.Queue.Reason, "Every Claude account waits") || !strings.Contains(d.Queue.Reason, "5-hour limit reached") {
		t.Errorf("queue %+v, want paused on every account's limit", d.Queue)
	}

	// An account whose runners are all paused can't run tasks: it doesn't
	// keep the queue running.
	idle := usageRunner("tower", true, "bob@example.com", 0.1, false, now)
	idle.Paused = true
	d = wall(usageRunner("laptop", true, "alice@example.com", 1, true, now), idle)
	if !d.Queue.Paused {
		t.Errorf("queue running on an account whose runners are paused: %+v", d.Queue)
	}

	// One account: as before.
	d = wall(usageRunner("laptop", true, "alice@example.com", 1, true, now))
	if len(d.Usage.Accounts) != 1 || !d.Queue.Paused || d.Queue.Reason != "5-hour limit reached" || !d.Usage.Known {
		t.Errorf("one account: %+v %+v", d.Queue, d.Usage)
	}
}

// With several accounts the dashboard's queue waits only while all of them
// do; a manual pause stays.
func TestCombinePause(t *testing.T) {
	parallel(t)
	limit := usage.Snapshot{Paused: true, Reason: "5-hour limit reached", Until: time.Now().Add(time.Hour)}
	pace := usage.Snapshot{Paused: true, Pacing: true, Reason: "Ahead of the weekly pace"}
	free := usage.Snapshot{}

	s := limit
	combinePause(&s, []waits{{true, limit}, {true, free}})
	if s.Paused {
		t.Errorf("paused with one account free: %+v", s)
	}
	s = free
	combinePause(&s, []waits{{true, limit}, {true, pace}})
	if !s.Paused || !s.Pacing || s.Until.IsZero() {
		t.Errorf("both waiting: %+v, want paused, pacing (start now runs on the paced one)", s)
	}
	s = usage.Snapshot{ManualPause: true, Paused: true, Reason: "Paused by you"}
	combinePause(&s, []waits{{true, free}, {true, free}})
	if !s.Paused || s.Reason != "Paused by you" {
		t.Errorf("manual pause lost: %+v", s)
	}
}
