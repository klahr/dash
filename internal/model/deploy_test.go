package model

import (
	"strings"
	"testing"
	"time"
)

// deployIn is a deploy of commit that went through statuses, in order.
func deployIn(id, commit string, statuses ...DeployStatus) Deploy {
	d := Deploy{ID: id, Commit: commit}
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	for _, s := range append([]DeployStatus{DeployQueued}, statuses...) {
		d.SetStatus(s, at)
		at = at.Add(time.Minute)
	}
	return d
}

// The status a deploy ends in: live once its checks pass; failed, and
// deployed, when they don't; superseded or rolled back once replaced.
func TestDeployStatusTransitions(t *testing.T) {
	d := deployIn("a", "c1", DeployBuilding, DeployDeploying, DeployVerifying, DeployLive)
	if d.Status != DeployLive || d.StartedAt == nil || d.FinishedAt == nil || !d.WentLive() {
		t.Fatalf("live: %+v", d)
	}
	finished := *d.FinishedAt
	d.SetStatus(DeployRolledBack, finished.Add(time.Hour))
	if d.Status != DeployRolledBack || !d.FinishedAt.Equal(finished) || !d.WentLive() {
		t.Errorf("rolled back keeps when it finished: %+v", d)
	}

	f := deployIn("b", "c2", DeployDeploying, DeployVerifying, DeployFailed)
	f.Deployed, f.Checks = true, []DeployCheck{{Name: CheckHealth, OK: true}, {Name: CheckVerify, Detail: "exit 1"}}
	if !f.ChecksFailed() || f.WentLive() {
		t.Errorf("failed its checks: %+v", f)
	}
	f.Deployed = false
	if f.ChecksFailed() {
		t.Error("a deploy whose command failed didn't fail its checks")
	}
}

// What a rollback replaces, and what it goes back to: the last good deploy
// before it (went live, not rolled back), of another commit.
func TestRollbackSelection(t *testing.T) {
	live := func(id, c string) Deploy { return deployIn(id, c, DeployDeploying, DeployLive) }
	replaced := func(id, c string, s DeployStatus) Deploy {
		return deployIn(id, c, DeployDeploying, DeployLive, s)
	}
	failedChecks := deployIn("f", "c4", DeployDeploying, DeployVerifying, DeployFailed)
	failedChecks.Deployed = true
	failedScript := deployIn("s", "c5", DeployDeploying, DeployFailed)
	cancelled := failedChecks
	cancelled.ID, cancelled.Cancelled = "x", true

	for _, tc := range []struct {
		name     string
		deploys  []Deploy // newest first
		from, to string   // "": none
	}{
		{"the live one, to the one it superseded",
			[]Deploy{live("l", "c3"), replaced("p", "c2", DeploySuperseded), replaced("o", "c1", DeploySuperseded)}, "l", "p"},
		{"skips the rolled back and the same commit",
			[]Deploy{live("l", "c3"), replaced("r", "c2", DeployRolledBack), replaced("same", "c3", DeploySuperseded), replaced("o", "c1", DeploySuperseded)}, "l", "o"},
		{"skips a commit rolled back, though an older deploy of it was good",
			[]Deploy{live("rb", "c1"), replaced("r", "c2", DeployRolledBack), replaced("p", "c2", DeploySuperseded), replaced("o", "c1", DeploySuperseded), replaced("z", "c0", DeploySuperseded)}, "rb", "z"},
		{"skips deploys that never went live",
			[]Deploy{live("l", "c3"), failedScript, replaced("o", "c1", DeploySuperseded)}, "l", "o"},
		{"a deploy that failed its checks, to the one still live",
			[]Deploy{failedChecks, live("l", "c3")}, "f", "l"},
		{"one whose script failed isn't what runs: the live one is",
			[]Deploy{failedScript, live("l", "c3"), replaced("o", "c1", DeploySuperseded)}, "l", "o"},
		{"a cancelled deploy isn't rolled back",
			[]Deploy{cancelled, live("l", "c3"), replaced("o", "c1", DeploySuperseded)}, "l", "o"},
		{"nothing older to go back to",
			[]Deploy{live("l", "c3")}, "l", ""},
		{"nothing deployed", []Deploy{failedScript}, "", ""},
	} {
		from, ok := RollbackFrom(tc.deploys)
		if !ok {
			if tc.from != "" {
				t.Errorf("%s: no rollback, want of %s", tc.name, tc.from)
			}
			continue
		}
		if from.ID != tc.from {
			t.Errorf("%s: rollback of %s, want %s", tc.name, from.ID, tc.from)
		}
		to, ok := RollbackTarget(tc.deploys, from)
		if got := map[bool]string{true: to.ID}[ok]; got != tc.to {
			t.Errorf("%s: rollback to %q, want %q", tc.name, got, tc.to)
		}
	}
}

// An environment's checks are checked when it is saved.
func TestCleanEnvironmentChecks(t *testing.T) {
	ok := Environment{Name: "prod", DeployCommand: "./ship", URL: "https://example.com/", HealthPath: " /healthz ", HealthStatus: 200,
		HealthTimeout: 30, VerifyCommand: "\r\n./smoke\r\n", Soak: 60, AutoRollback: true}
	out, err := CleanEnvironments([]Environment{ok})
	if err != nil {
		t.Fatal(err)
	}
	e := out[0]
	if e.HealthPath != "/healthz" || e.VerifyCommand != "./smoke" || e.HealthURL(e.URL) != "https://example.com/healthz" || e.HealthWait() != 30*time.Second {
		t.Errorf("cleaned %+v", e)
	}
	if !strings.Contains(DeployScripts(out), "verify: ./smoke") {
		t.Errorf("the verify command is one of the commands a runner approves: %q", DeployScripts(out))
	}
	e.ClearCommands()
	if e.HasCommands() {
		t.Errorf("cleared %+v", e)
	}
	for _, bad := range []func(*Environment){
		func(e *Environment) { e.URL = "" },               // a path needs a URL
		func(e *Environment) { e.HealthPath = "healthz" }, // not a path
		func(e *Environment) { e.HealthPath = "/a b" },    // a space
		func(e *Environment) { e.HealthStatus = 42 },      // not a status
		func(e *Environment) { e.HealthTimeout = 601 },    // too long
		func(e *Environment) { e.Soak = -1 },              // negative
		func(e *Environment) { e.Soak = MaxSoak + 1 },     // too long
	} {
		x := ok
		bad(&x)
		if _, err := CleanEnvironments([]Environment{x}); err == nil {
			t.Errorf("%+v: want an error", x)
		}
	}
}

// LiveShipped: what an environment runs, of what its deploys shipped,
// from the records alone; a rollback goes back to what was live with the
// deploy it rolled back to.
func TestLiveShipped(t *testing.T) {
	day := func(n int) time.Time { return time.Date(2026, 11, n, 0, 0, 0, 0, time.UTC) }
	ship := func(ids ...string) []DeployShipped {
		var out []DeployShipped
		for _, id := range ids {
			out = append(out, DeployShipped{Kind: LinkTask, ID: id})
		}
		return out
	}
	went := func(id string, at time.Time, s DeployStatus, shipped []DeployShipped) Deploy {
		return Deploy{ID: id, Status: s, Shipped: shipped, Phases: []DeployPhase{{DeployQueued, at.Add(-time.Hour)}, {DeployLive, at}, {s, at.Add(time.Hour)}}}
	}
	d1 := went("d1", day(1), DeploySuperseded, ship("a"))
	d2 := went("d2", day(2), DeployRolledBack, ship("b"))
	rb := went("rb", day(3), DeploySuperseded, nil)
	rb.Rollback, rb.RollbackTo = true, "d1"
	d4 := went("d4", day(4), DeployLive, ship("b", "c"))
	failed := Deploy{ID: "f", Status: DeployFailed, Shipped: ship("x")}

	if got := LiveShipped([]Deploy{failed}); got != nil {
		t.Errorf("nothing live: %v", got)
	}
	rbLive := rb
	rbLive.Status = DeployLive
	got := LiveShipped([]Deploy{rbLive, d2, d1})
	if len(got) != 1 || !got[ShippedKey(LinkTask, "a")].Equal(day(1)) {
		t.Errorf("after the rollback: %v", got)
	}
	got = LiveShipped([]Deploy{failed, d4, rb, d2, d1})
	want := map[string]time.Time{"task:a": day(1), "task:b": day(4), "task:c": day(4)}
	if len(got) != len(want) {
		t.Errorf("after d4: %v", got)
	}
	for k, at := range want {
		if !got[k].Equal(at) {
			t.Errorf("%s: %v, want %v", k, got[k], at)
		}
	}
}
