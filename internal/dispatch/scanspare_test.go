package dispatch

import (
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/scan"
	"github.com/klahr/dash/internal/usage"
)

// An early scan weighs the accounts of the runners that could take it,
// each with its freshest usage and whether it may start work, and is then
// sent to a runner on the account it picked.
func TestScanSpareByAccount(t *testing.T) {
	t.Parallel()

	fx := pacedFixture(t)
	now := time.Now()
	fiveHour := func(util float64, status string) usage.Window {
		return usage.Window{Name: "five_hour", Utilization: util, ResetsAt: now.Add(30 * time.Minute), Status: status}
	}
	ann := onAccount("ann-laptop", fx.orgA, "ann@example.com", 0.5, now)
	ann.Reported.Usage.Windows = append(ann.Reported.Usage.Windows, fiveHour(0.2, "allowed"))
	bob := onAccount("bob-laptop", fx.orgA, "Bob@example.com", 0.5, now)
	bob.Reported.Usage.Windows = append(bob.Reported.Usage.Windows, fiveHour(1, "rejected"))
	anon := onAccount("anon", fx.orgA, "", 0.5, now) // no account: left out
	off := onAccount("off", fx.orgA, "cy@example.com", 0.5, now)
	off.Online = false // can't take it now
	other := onAccount("other", fx.orgA, "dee@example.com", 0.5, now)
	fx.fleet.set(ann)
	fx.fleet.set(bob)
	fx.fleet.set(anon)
	fx.fleet.set(off)
	fx.fleet.set(other) // not one of the project's runners
	p := fx.project(t, fx.orgA, "ann-laptop", "bob-laptop", "anon", "off")

	got := fx.d.ScanSpare(p, 1)
	if len(got) != 2 || got[0].Account != "ann@example.com" || got[1].Account != "bob@example.com" {
		t.Fatalf("accounts %+v", got)
	}
	if got[0].Hold != "" || got[1].Hold == "" {
		t.Fatalf("holds %q %q, want only bob's account held (limit reached)", got[0].Hold, got[1].Hold)
	}
	if ok, why := scan.SpareNow(model.EarlyRun{On: true}, got[0].Usage, now); !ok {
		t.Fatalf("ann's usage has no room: %s", why)
	}
	// Not for a user whose work these runners don't take.
	if got := fx.d.ScanSpare(p, 99); len(got) != 0 {
		t.Fatalf("for another user: %+v", got)
	}

	// Sent to a runner on the account picked, whichever is less busy.
	r, _, _ := fx.d.scanRunner(p, []int64{1}, "ann@example.com")
	if r == nil || r.ID != "ann-laptop" {
		t.Fatalf("runner %+v, want ann-laptop", r)
	}
	if r, why, _ := fx.d.scanRunner(p, []int64{1}, "cy@example.com"); r != nil || why == "" {
		t.Fatalf("on an offline account: %+v %q", r, why)
	}
}
