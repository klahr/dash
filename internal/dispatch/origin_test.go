package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

type recordPub struct {
	mu  sync.Mutex
	evs []model.Event
}

func (p *recordPub) Publish(ev model.Event) {
	p.mu.Lock()
	p.evs = append(p.evs, ev)
	p.mu.Unlock()
}

func (p *recordPub) checkouts() [][]model.Checkout {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out [][]model.Checkout
	for _, ev := range p.evs {
		if ev.Type == model.EvCheckouts {
			out = append(out, ev.Data.([]model.Checkout))
		}
	}
	return out
}

func sendReport(t *testing.T, d *Dispatcher, runner string, rep Report) {
	t.Helper()
	b, _ := json.Marshal(rep)
	if err := d.Report(runner, b); err != nil {
		t.Fatal(err)
	}
	d.fanouts.Wait()
}

const sha1 = "0123456789abcdef0123456789abcdef01234567"
const sha2 = "89abcdef0123456789abcdef0123456789abcdef"

func originCalls(f *fakeFleet, id string) []string {
	var out []string
	for _, c := range f.callsTo(id) {
		if strings.HasSuffix(c, "/origin") {
			out = append(out, c)
		}
	}
	return out
}

// A runner's push goes to the project's other online runners in its org,
// and not back to the runner that pushed.
func TestPushFansOutToOtherRunnersOnly(t *testing.T) {
	fx := newFixture(t)
	for _, id := range []string{"a", "b", "c"} {
		fx.fleet.set(runnerOf(id, fx.orgA, true))
	}
	fx.fleet.set(runnerOf("asleep", fx.orgA, false))
	fx.fleet.set(runnerOf("beta", fx.orgB, true))
	fx.fleet.set(runnerOf("unlinked", fx.orgA, true))
	p := fx.project(t, fx.orgA, "a", "b", "c", "asleep", "beta")

	sendReport(t, fx.d, "a", Report{Pushes: []Push{{HubProjectID: p.ID, Branch: "main", SHA: sha1}}})
	want := "POST /api/hub/projects/" + p.ID + "/origin"
	for _, id := range []string{"b", "c"} {
		if got := originCalls(fx.fleet, id); len(got) != 1 || !strings.HasSuffix(got[0], want) {
			t.Errorf("calls to %s = %q; want %q", id, got, want)
		}
	}
	for _, id := range []string{"a", "asleep", "beta", "unlinked"} {
		if got := originCalls(fx.fleet, id); len(got) != 0 {
			t.Errorf("%s was told: %q", id, got)
		}
	}

	// The same push again (a report resent): nobody is told twice.
	sendReport(t, fx.d, "a", Report{Pushes: []Push{{HubProjectID: p.ID, Branch: "main", SHA: sha1}}})
	if got := originCalls(fx.fleet, "b"); len(got) != 1 {
		t.Errorf("a repeated push was passed on again: %q", got)
	}
	// A new one is; this time b pushed, so a hears of it and b doesn't.
	sendReport(t, fx.d, "b", Report{Pushes: []Push{{HubProjectID: p.ID, Branch: "main", SHA: sha2}}})
	if len(originCalls(fx.fleet, "a")) != 1 || len(originCalls(fx.fleet, "b")) != 1 || len(originCalls(fx.fleet, "c")) != 2 {
		t.Errorf("after b's push: a %q, b %q, c %q", originCalls(fx.fleet, "a"), originCalls(fx.fleet, "b"), originCalls(fx.fleet, "c"))
	}
}

// Pushes of a project the runner isn't linked to, or not a commit, are dropped.
func TestPushFromUnlinkedRunnerIsIgnored(t *testing.T) {
	fx := newFixture(t)
	fx.fleet.set(runnerOf("a", fx.orgA, true))
	fx.fleet.set(runnerOf("b", fx.orgA, true))
	fx.fleet.set(runnerOf("intruder", fx.orgA, true))
	p := fx.project(t, fx.orgA, "a", "b")
	sendReport(t, fx.d, "intruder", Report{Pushes: []Push{{HubProjectID: p.ID, Branch: "main", SHA: sha1}}})
	sendReport(t, fx.d, "a", Report{Pushes: []Push{{HubProjectID: p.ID, Branch: "main", SHA: "not a sha"}}})
	if got := originCalls(fx.fleet, "b"); len(got) != 0 {
		t.Errorf("b was told: %q", got)
	}
}

// The hub keeps each runner's checkouts behind origin as last reported,
// and tells the dashboards when they change.
func TestCheckoutsBehindAreKeptPerRunner(t *testing.T) {
	fx := newFixture(t)
	pub := &recordPub{}
	fx.d.Pub = pub
	fx.fleet.set(runnerOf("a", fx.orgA, true))
	fx.fleet.set(runnerOf("b", fx.orgA, true))
	p := fx.project(t, fx.orgA, "a", "b")

	behind := []CheckoutState{{HubProjectID: p.ID, Branch: "main", Behind: 2, Ahead: 1, Stuck: "local main has 1 commit origin doesn't (diverged)"}}
	sendReport(t, fx.d, "a", Report{Checkouts: &behind})
	got := fx.d.Checkouts(p.ID)
	if len(got) != 1 || got[0].Runner != "a" || got[0].Behind != 2 || got[0].Stuck == "" {
		t.Fatalf("checkouts = %+v; want a's", got)
	}
	if n := len(pub.checkouts()); n != 1 {
		t.Fatalf("%d checkouts events; want 1", n)
	}
	// The same report (a heartbeat) changes nothing; one without the field
	// (an older runner) keeps what the hub knows.
	sendReport(t, fx.d, "a", Report{Checkouts: &behind})
	sendReport(t, fx.d, "a", Report{})
	if n := len(pub.checkouts()); n != 1 || len(fx.d.Checkouts(p.ID)) != 1 {
		t.Fatalf("%d events, %+v; want no news", n, fx.d.Checkouts(p.ID))
	}
	// b reporting none doesn't touch a's.
	none := []CheckoutState{}
	sendReport(t, fx.d, "b", Report{Checkouts: &none})
	if len(fx.d.Checkouts(p.ID)) != 1 {
		t.Fatal("b's report dropped a's checkout")
	}
	// a caught up.
	sendReport(t, fx.d, "a", Report{Checkouts: &none})
	if got := fx.d.Checkouts(p.ID); len(got) != 0 {
		t.Fatalf("checkouts = %+v; want none", got)
	}
	evs := pub.checkouts()
	if len(evs) != 2 || len(evs[1]) != 0 {
		t.Fatalf("events = %+v; want a's, then none", evs)
	}
	// A revoked runner's are forgotten.
	sendReport(t, fx.d, "b", Report{Checkouts: &behind})
	fx.d.Revoked("b")
	if got := fx.d.Checkouts(p.ID); len(got) != 0 {
		t.Fatalf("a revoked runner's checkouts stayed: %+v", got)
	}
}

// A push the hub didn't hear of (the report failed) goes in the next report.
func TestReporterResendsPushesAfterAFailedReport(t *testing.T) {
	fx := newFixture(t)
	var mu sync.Mutex
	var sent []Report
	fail := true
	rep := &Reporter{Store: fx.st, Every: time.Hour,
		Checkouts: func() []CheckoutState { return nil },
		Send: func(_ context.Context, r Report) error {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, r)
			if fail {
				fail = false
				return errors.New("hub unreachable")
			}
			return nil
		}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rep.Run(ctx)
	rep.Pushed(Push{HubProjectID: "hp", Branch: "main", SHA: sha1})
	wait := func(n int) []Report {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			out := append([]Report(nil), sent...)
			mu.Unlock()
			if len(out) >= n {
				return out
			}
			if time.Now().After(deadline) {
				t.Fatalf("%d reports sent; want %d", len(out), n)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait(1)
	rep.Nudge()
	got := wait(2)
	for i, r := range got[:2] {
		if len(r.Pushes) != 1 || r.Pushes[0].SHA != sha1 {
			t.Errorf("report %d pushes = %+v; want the push", i+1, r.Pushes)
		}
		if r.Checkouts == nil || len(*r.Checkouts) != 0 {
			t.Errorf("report %d checkouts = %v; want an empty list", i+1, r.Checkouts)
		}
	}
	rep.Nudge()
	if got := wait(3); len(got[2].Pushes) != 0 {
		t.Errorf("a delivered push was sent again: %+v", got[2].Pushes)
	}
}
