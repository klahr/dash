package store

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

func TestNotifySettingsRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	acc := s.Accounts()
	if err := acc.Bootstrap("admin", "s3cret-password"); err != nil {
		t.Fatal(err)
	}
	u, _ := acc.EnvUser()
	def, _ := acc.DefaultOrg()
	other, _ := acc.CreateOrg(u.ID, "Other")
	p, _ := s.CreateProject(model.Project{Name: "shop", Path: t.TempDir(), OrgID: def.ID})
	q, _ := s.CreateProject(model.Project{Name: "elsewhere", Path: t.TempDir(), OrgID: other.ID})
	me := s.ForOrg(def.ID).AsUser(u.ID)
	hook, err := me.CreateWebhook(model.Webhook{URL: "https://hooks.slack.com/services/x", Format: model.WebhookSlack, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	theirs, _ := s.ForOrg(other.ID).AsUser(u.ID).CreateWebhook(model.Webhook{URL: "https://ntfy.sh/x", Format: model.WebhookNtfy, Enabled: true})

	// Nothing saved: the defaults, summary off.
	set, st, err := me.NotifySettings()
	if err != nil || set.On() || !st.NextAt.IsZero() || !slices.Equal(set.Push, model.DefaultPush) {
		t.Fatalf("defaults %+v %+v %v", set, st, err)
	}
	if evs, _ := s.PushEventsFor(u.ID, def.ID); !slices.Equal(evs, model.DefaultPush) {
		t.Fatalf("default pushes %v", evs)
	}

	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	set.Summary, set.Time, set.TimeZone = model.SummaryDaily, "08:00", "Europe/Stockholm"
	set.AllProjects, set.Projects = false, []string{p.ID}
	set.Webhooks = []string{hook.ID}
	set.Include.MinSeverity = model.SevHigh
	set.Push = []model.NotifyEvent{model.NotifyIssue, model.NotifyFailed}

	// Another org's project or webhook is refused.
	bad := set
	bad.Projects = []string{q.ID}
	if _, _, err := me.SaveNotifySettings(bad, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org's project: %v", err)
	}
	bad = set
	bad.Webhooks = []string{theirs.ID}
	if _, _, err := me.SaveNotifySettings(bad, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org's webhook: %v", err)
	}

	saved, st, err := me.SaveNotifySettings(set, now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 18, 7, 0, 0, 0, time.UTC) // 08:00 CET
	if !st.NextAt.Equal(want) {
		t.Fatalf("next %v, want %v", st.NextAt, want)
	}
	s.Close()
	s = reopen(t, s, path)
	defer s.Close()
	got, st2, err := s.ForOrg(def.ID).AsUser(u.ID).NotifySettings()
	if err != nil || !st2.NextAt.Equal(want) {
		t.Fatalf("reopened %+v %v", st2, err)
	}
	sameJSON(t, "settings", got, saved)
	if evs, _ := s.PushEventsFor(u.ID, def.ID); !slices.Equal(evs, set.Push) {
		t.Fatalf("pushes %v", evs)
	}
	// The other org keeps its own (default) settings.
	if o, _, _ := s.NotifySettings(u.ID, other.ID); o.On() {
		t.Fatal("settings leaked to another org")
	}

	// Due at its time, claimed once.
	if due, _ := s.SummariesDue(want.Add(-time.Second)); len(due) != 0 {
		t.Fatalf("due early: %+v", due)
	}
	due, err := s.SummariesDue(want)
	if err != nil || len(due) != 1 || due[0].UserID != u.ID || due[0].OrgID != def.ID || due[0].Settings.Include.MinSeverity != model.SevHigh {
		t.Fatalf("due %+v %v", due, err)
	}
	next := want.Add(24 * time.Hour)
	if ok, err := s.ClaimSummary(u.ID, def.ID, want, want, next); !ok || err != nil {
		t.Fatalf("claim %v %v", ok, err)
	}
	if ok, _ := s.ClaimSummary(u.ID, def.ID, want, want, next); ok {
		t.Fatal("claimed twice")
	}
	if due, _ := s.SummariesDue(want.Add(time.Minute)); len(due) != 0 {
		t.Fatalf("still due after the claim: %+v", due)
	}
	if err := s.MarkSummarySent(u.ID, def.ID, want); err != nil {
		t.Fatal(err)
	}
	if _, st, _ := s.NotifySettings(u.ID, def.ID); !st.Through.Equal(want) || !st.LastSent.Equal(want) || !st.NextAt.Equal(next) {
		t.Fatalf("state %+v", st)
	}

	// Deleting the webhook takes it off the destinations.
	if err := s.DeleteWebhook(hook.ID); err != nil {
		t.Fatal(err)
	}
	if set, _, _ := s.NotifySettings(u.ID, def.ID); len(set.Webhooks) != 0 {
		t.Fatalf("deleted webhook still a destination: %v", set.Webhooks)
	}

	// The unsubscribe link turns it off.
	if was, err := s.TurnOffSummary(u.ID, def.ID, now); !was || err != nil {
		t.Fatalf("turn off: %v %v", was, err)
	}
	if set, st, _ := s.NotifySettings(u.ID, def.ID); set.On() || !st.NextAt.IsZero() {
		t.Fatalf("still on: %+v %+v", set, st)
	}
}

func TestActivity(t *testing.T) {
	t.Parallel()
	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t0 := time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)
	for i, a := range []model.Activity{
		{ProjectID: "p1", At: t0.Add(-time.Hour), Kind: model.SummaryReview, Ref: "t1", Title: "old"},
		{ProjectID: "p1", At: t0.Add(time.Hour), Kind: model.SummaryIssue, Ref: "i1", Title: "SQL injection", Severity: model.SevHigh},
		{ProjectID: "p2", At: t0.Add(time.Hour), Kind: model.SummaryFailed, Ref: "t2", Title: "elsewhere"},
		{ProjectID: "p1", At: t0.Add(2 * time.Hour), Kind: model.SummaryGoal, Ref: "g1", Title: "Mine", User: 7},
	} {
		if err := s.RecordActivity(a); err != nil {
			t.Fatal(i, err)
		}
	}
	got, err := s.ListActivity([]string{"p1"}, t0, t0.Add(2*time.Hour))
	if err != nil || len(got) != 2 || got[0].Ref != "i1" || got[0].Severity != model.SevHigh || got[1].User != 7 {
		t.Fatalf("activity %+v %v", got, err)
	}
	if err := s.PruneActivity(t0); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListActivity([]string{"p1", "p2"}, t0.Add(-48*time.Hour), t0.Add(48*time.Hour)); len(got) != 3 {
		t.Fatalf("after pruning %+v", got)
	}
}
