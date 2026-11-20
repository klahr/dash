package model

import (
	"strings"
	"testing"
	"time"
)

func TestEnvironmentDefaultRoles(t *testing.T) {
	prod, staging := Environment{Name: "production"}, Environment{Name: "staging"}
	if prod.WhoDeploys() != RoleAdmin || prod.WhoApproves() != RoleAdmin || prod.WhoChangesSettings() != RoleAdmin {
		t.Errorf("production: %s %s %s", prod.WhoDeploys(), prod.WhoApproves(), prod.WhoChangesSettings())
	}
	if staging.WhoDeploys() != RoleMember || staging.WhoApproves() != "" || staging.WhoChangesSettings() != RoleAdmin {
		t.Errorf("staging: %s %q %s", staging.WhoDeploys(), staging.WhoApproves(), staging.WhoChangesSettings())
	}
	prod.ApproveRole = RoleNone
	if prod.WhoApproves() != "" || prod.WithDefaults().ApproveRole != RoleNone {
		t.Errorf("production without approval: %q", prod.WhoApproves())
	}
	envs, err := CleanEnvironments([]Environment{{Name: "staging", DeployCommand: "x", DeployRole: " Admin "}, {Name: "production", DeployCommand: "x", RequireLiveOn: "Staging"}})
	if err != nil {
		t.Fatal(err)
	}
	if e := envs[0]; e.DeployRole != RoleAdmin || e.ApproveRole != RoleNone || e.SettingsRole != RoleAdmin {
		t.Errorf("staging cleaned: %+v", e)
	}
	if e := envs[1]; e.RequireLiveOn != "staging" || e.ApproveRole != RoleAdmin {
		t.Errorf("production cleaned: %+v", e)
	}
}

func TestFreezes(t *testing.T) {
	e := Environment{Name: "production", DeployCommand: "x", TimeZone: "Europe/Stockholm",
		Freezes: []Freeze{{From: "Friday  15:00", To: "mon 0:00", Note: "weekend"}, {From: "2026-12-23 12:00", To: "2027-01-02 00:00", Note: "holidays"}}}
	envs, err := CleanEnvironments([]Environment{e})
	if err != nil {
		t.Fatal(err)
	}
	e = envs[0]
	if e.Freezes[0].From != "fri 15:00" || e.Freezes[0].To != "mon 00:00" {
		t.Fatalf("cleaned: %+v", e.Freezes)
	}
	loc, _ := time.LoadLocation("Europe/Stockholm")
	at := func(s string) time.Time {
		x, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	for _, c := range []struct {
		now, until string
	}{
		{"2026-11-20 14:59", ""},                 // Friday, before
		{"2026-11-20 15:00", "2026-11-23 00:00"}, // Friday, at the start
		{"2026-11-22 23:59", "2026-11-23 00:00"}, // Sunday night
		{"2026-11-23 00:00", ""},                 // Monday: over
		{"2026-11-25 10:00", ""},                 // Wednesday
		{"2026-12-28 10:00", "2027-01-02 00:00"}, // the holidays (a Monday)
		{"2027-01-02 00:00", ""},                 // after them (a Saturday: the weekend's)
	} {
		_, until, on := e.FrozenAt(at(c.now))
		if c.now == "2027-01-02 00:00" {
			if !on || !until.Equal(at("2027-01-04 00:00")) {
				t.Errorf("%s: on %v until %v", c.now, on, until)
			}
			continue
		}
		if on != (c.until != "") || on && !until.Equal(at(c.until)) {
			t.Errorf("%s: on %v until %v, want %q", c.now, on, until, c.until)
		}
	}
	// A window across the week's end, read in UTC by default.
	u := Environment{Name: "staging", Freezes: []Freeze{{From: "sat 22:00", To: "sun 02:00"}}}
	if _, _, on := u.FrozenAt(time.Date(2026, 11, 22, 1, 0, 0, 0, time.UTC)); !on {
		t.Error("sunday 01:00 UTC is in sat 22:00 → sun 02:00")
	}
	if why := e.FreezeReason(at("2026-11-21 12:00")); !strings.Contains(why, "production is frozen until Mon 23 Nov 00:00 (Europe/Stockholm): weekend") {
		t.Errorf("reason: %q", why)
	}
	for _, bad := range []Freeze{{From: "fri 15:00", To: "fri 15:00"}, {From: "fri 25:00", To: "mon 00:00"}, {From: "2026-12-24 00:00", To: "2026-12-23 00:00"}, {From: "soon", To: "later"}} {
		if _, err := CleanEnvironments([]Environment{{Name: "a", DeployCommand: "x", Freezes: []Freeze{bad}}}); err == nil {
			t.Errorf("%+v: no error", bad)
		}
	}
}

func TestDiffEnvironments(t *testing.T) {
	old := []Environment{{Name: "production", DeployCommand: "./ship"}, {Name: "staging", DeployCommand: "./ship staging"}}
	// The same with its default roles spelled out: no change.
	same := []Environment{old[0].WithDefaults(), old[1].WithDefaults()}
	if d := DiffEnvironments(old, same); len(d) != 0 {
		t.Errorf("defaults spelled out: %+v", d)
	}
	next := []Environment{
		{Name: "production", DeployCommand: "./ship", RunsOn: []string{"prod", "eu"}, ApproveRole: RoleNone,
			Freezes: []Freeze{{From: "fri 15:00", To: "mon 00:00", Note: "weekend"}}},
		{Name: "preview", DeployCommand: "./preview"},
	}
	d := DiffEnvironments(old, next)
	if len(d) != 3 || d[0].Environment != "production" || d[0].Action != EnvChanged || d[1].Action != EnvAdded || d[2].Environment != "staging" || d[2].Action != EnvRemoved {
		t.Fatalf("changes: %+v", d)
	}
	want := []EnvFieldChange{{Field: "runs_on", To: "prod, eu"}, {Field: "approve_role", From: "admin", To: "none"}, {Field: "freezes", To: "fri 15:00 → mon 00:00 (weekend)"}}
	if len(d[0].Diff) != len(want) {
		t.Fatalf("production's diff: %+v", d[0].Diff)
	}
	for i := range want {
		if d[0].Diff[i] != want[i] {
			t.Errorf("diff %d: %+v, want %+v", i, d[0].Diff[i], want[i])
		}
	}
	if d[2].Diff[0] != (EnvFieldChange{Field: "deploy_command", From: "./ship staging"}) {
		t.Errorf("removed: %+v", d[2].Diff)
	}
}
