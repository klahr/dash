package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tunnel"
	"github.com/klahr/dash/internal/usage"
)

// startNow marks td to start now, as the board's Start now does.
func (fx *fixture) startNow(t *testing.T, td model.Todo) model.Todo {
	t.Helper()
	td, err := fx.st.UpdateTodo(td.ID, func(x *model.Todo) error { x.StartNow = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	return td
}

// The hub paces an account that is ahead: a task with Start now still gets
// one of its runners, and the task it sends says so; the others wait, and
// PaceWait says they wait for the pace.
func TestStartNowPastTheHubsPace(t *testing.T) {
	t.Parallel()

	fx := pacedFixture(t)
	fx.fleet.set(onAccount("desk", fx.orgA, "ann@example.com", gap(2*time.Hour), time.Now()))
	if err := fx.d.Pacer.Set("ann@example.com", usage.PaceSettings{Follow: true}); err != nil {
		t.Fatal(err)
	}
	p := fx.project(t, fx.orgA, "desk")
	plain := fx.todo(t, p, nil)
	var w *WaitError
	if _, _, err := fx.d.Dispatch(context.Background(), p, plain); !errors.As(err, &w) || !strings.Contains(w.Reason, "to stay on pace") {
		t.Fatalf("unforced: %v, want a wait for the pace", err)
	}
	if why := fx.d.PaceWait(p, plain); !strings.Contains(why, "desk is waiting") {
		t.Errorf("PaceWait = %q, want desk's pace", why)
	}

	forced := fx.startNow(t, fx.todo(t, p, nil))
	rid, _, err := fx.d.Dispatch(context.Background(), p, forced)
	if err != nil || rid != "desk" {
		t.Fatalf("forced: %q %v, want desk", rid, err)
	}
	if n := len(fx.fleet.tasks); n != 1 || !fx.fleet.tasks[0].StartNow {
		t.Errorf("tasks sent: %+v, want one with start_now", fx.fleet.tasks)
	}
}

// A hard limit on the account stops a task with Start now too.
func TestStartNowStopsAtTheAccountsLimit(t *testing.T) {
	t.Parallel()

	fx := pacedFixture(t)
	now := time.Now()
	r := onAccount("desk", fx.orgA, "ann@example.com", gap(2*time.Hour), now)
	r.Reported.Usage.Windows = append(r.Reported.Usage.Windows, usage.Window{Name: "five_hour", Utilization: 1, ResetsAt: now.Add(time.Hour), Status: "rejected"})
	fx.fleet.set(r)
	if err := fx.d.Pacer.Set("ann@example.com", usage.PaceSettings{Follow: true}); err != nil {
		t.Fatal(err)
	}
	p := fx.project(t, fx.orgA, "desk")
	td := fx.startNow(t, fx.todo(t, p, nil))
	var w *WaitError
	if _, _, err := fx.d.Dispatch(context.Background(), p, td); !errors.As(err, &w) || !strings.Contains(w.Reason, "5-hour limit reached") {
		t.Fatalf("err = %v, want the 5-hour stop", err)
	}
	if why := fx.d.PaceWait(p, td); why != "" {
		t.Errorf("PaceWait = %q at a hard limit", why)
	}
}

// A runner that paces itself reports its queue paused: Start now passes a
// pause for the pace only, not one for a limit or by its owner, nor one
// from an older runner that doesn't say it is only the pace; and not a
// runner whose lanes are full.
func TestStartNowAndARunnersOwnPause(t *testing.T) {
	t.Parallel()

	pace := &usage.Snapshot{Paused: true, Pacing: true, Reason: "Waiting 1h 20m to stay on pace"}
	var old usage.Snapshot // an older runner's report: no "pacing"
	if err := json.Unmarshal([]byte(`{"known":true,"paused":true,"reason":"Waiting 1h 20m to stay on pace"}`), &old); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		usage *usage.Snapshot
		busy  bool
		takes bool
	}{
		{"pacing", pace, false, true},
		{"pacing, lanes full", pace, true, false},
		{"limit", &usage.Snapshot{Paused: true, Reason: "5-hour limit reached"}, false, false},
		{"paused by its owner", &usage.Snapshot{Paused: true, ManualPause: true, Pacing: true, Reason: "Paused by you"}, false, false},
		{"older runner", &old, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			fx := newFixture(t)
			r := runnerOf("laptop", fx.orgA, true)
			r.Parallel = 1
			r.Reported = &tunnel.Reported{Parallel: 1, Usage: c.usage, At: time.Now()}
			fx.fleet.set(r)
			p := fx.project(t, fx.orgA, "laptop")
			if c.busy {
				fx.todo(t, p, func(x *model.Todo) { x.Status, x.Runner = model.StatusInProgress, "laptop" })
			}
			if _, _, err := fx.d.Dispatch(context.Background(), p, fx.todo(t, p, nil)); err == nil {
				t.Fatal("an unforced task was sent to a paused runner")
			}
			rid, _, err := fx.d.Dispatch(context.Background(), p, fx.startNow(t, fx.todo(t, p, nil)))
			if c.takes && (err != nil || rid != "laptop") {
				t.Fatalf("forced: %q %v, want laptop", rid, err)
			}
			if !c.takes && err == nil {
				t.Fatalf("forced: sent to %q, want it to wait", rid)
			}
		})
	}
}
