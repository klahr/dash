package dispatch

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/claudecli"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tunnel"
	"github.com/klahr/dash/internal/usage"
)

// list is every runner of the fake fleet.
func (f *fakeFleet) list() []tunnel.Runner {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tunnel.Runner
	for _, r := range f.runners {
		out = append(out, r)
	}
	return out
}

// onAccount is runner id of org, online, running 2 at once, logged in to
// Claude account email ("" for none), with weekly usage week (as of at)
// in a window half over.
func onAccount(id string, org int64, email string, week float64, at time.Time) tunnel.Runner {
	r := runnerOf(id, org, true)
	c := &claudecli.Status{Version: "2.1.0"}
	if email != "" {
		c.Account = &claudecli.Account{Email: email, OrgID: "shared-org", Subscription: "max"}
	}
	r.Reported = &tunnel.Reported{Parallel: 2, Claude: c, At: at, Usage: &usage.Snapshot{Known: true, UpdatedAt: at,
		Windows: []usage.Window{{Name: usage.WeekWindow, Utilization: week, ResetsAt: at.Add(usage.WeekSpan / 2)}}}}
	return r
}

// gap is weekly usage that far ahead of the line (behind if < 0), half way
// through the window.
func gap(d time.Duration) float64 { return 0.5 + float64(d)/float64(usage.WeekSpan) }

func pacedFixture(t *testing.T) *fixture {
	t.Helper()
	fx := newFixture(t)
	fx.d.Pacer = &Pacer{Runners: fx.fleet.list}
	return fx
}

// Runners are grouped by their account's email, whatever its case or its
// Claude organization (Team seats share an org, not limits); a runner that
// doesn't say which account it is on is a group of its own.
func TestPaceGroupsByAccountEmail(t *testing.T) {
	t.Parallel()

	now := time.Now()
	a := onAccount("a", 1, "Ann@Example.com", 0.5, now)
	b := onAccount("b", 1, "ann@example.com", 0.5, now)
	c := onAccount("c", 1, "carl@example.com", 0.5, now) // same Claude org as a and b
	old1 := onAccount("old1", 1, "", 0.5, now)
	old2 := onAccount("old2", 1, "", 0.5, now)
	all := []tunnel.Runner{a, b, c, old1, old2}
	ids := func(rs []tunnel.Runner) (out []string) {
		for _, r := range rs {
			out = append(out, r.ID)
		}
		return out
	}
	if got := ids(Group(all, a)); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("a's group: %v", got)
	}
	if got := ids(Group(all, c)); !slices.Equal(got, []string{"c"}) {
		t.Errorf("c's group: %v", got)
	}
	if got := ids(Group(all, old1)); !slices.Equal(got, []string{"old1"}) {
		t.Errorf("a runner without an account: %v", got)
	}
}

// A runner without account info isn't paced by the hub: no account, no
// setting, its usual limit.
func TestPaceRunnerWithoutAccount(t *testing.T) {
	t.Parallel()

	fx := pacedFixture(t)
	fx.fleet.set(onAccount("old", fx.orgA, "", gap(48*time.Hour), time.Now())) // far ahead
	if err := fx.d.Pacer.Set("", usage.PaceSettings{Follow: true}); err == nil {
		t.Error("an empty account was paced")
	}
	r, _ := fx.fleet.Get("old")
	g := fx.d.Pacer.For(r)
	if g.Follow || g.Account != "" || g.Slots["old"] != 2 || g.Waiting() {
		t.Fatalf("pace of a runner without an account: %+v", g)
	}
	p := fx.project(t, fx.orgA, "old")
	if _, _, err := fx.d.Dispatch(context.Background(), p, fx.todo(t, p, nil)); err != nil {
		t.Fatalf("not paced, so it takes the task: %v", err)
	}
	if rep, ok := fx.d.Reply("old").(Reply); !ok || rep.Pace == nil || !rep.Pace.Hub || rep.Pace.Follow {
		t.Errorf("reply: %+v", fx.d.Reply("old"))
	}
}

// Ahead of the line, no runner on the account gets a new task, and the
// task says how long it waits; a runner on another account still does.
func TestPaceAheadHoldsTheWholeAccount(t *testing.T) {
	t.Parallel()

	fx := pacedFixture(t)
	now := time.Now()
	// laptop's report is older and says on pace; desk's is the freshest,
	// 2 hours ahead: the account is ahead.
	fx.fleet.set(onAccount("laptop", fx.orgA, "ann@example.com", gap(0), now.Add(-10*time.Minute)))
	fx.fleet.set(onAccount("desk", fx.orgA, "ann@example.com", gap(2*time.Hour), now))
	fx.fleet.set(onAccount("other", fx.orgA, "bo@example.com", gap(2*time.Hour), now))
	if err := fx.d.Pacer.Set("ANN@example.com", usage.PaceSettings{Follow: true}); err != nil {
		t.Fatal(err)
	}
	p := fx.project(t, fx.orgA, "laptop", "desk")
	_, _, err := fx.d.Dispatch(context.Background(), p, fx.todo(t, p, nil))
	var w *WaitError
	if !errors.As(err, &w) || !strings.Contains(w.Reason, "laptop is waiting 1h 59m to stay on pace") ||
		!strings.Contains(w.Reason, "desk is waiting 1h 59m to stay on pace") {
		t.Fatalf("err = %v, want both runners waiting for the pace", err)
	}
	if rep := fx.d.Reply("laptop").(Reply); rep.Pace.State != usage.PaceWaiting || rep.Pace.Account != "ann@example.com" {
		t.Errorf("laptop is told %+v", rep.Pace)
	}
	// bo's account isn't paced.
	q := fx.project(t, fx.orgA, "other")
	if rid, _, err := fx.d.Dispatch(context.Background(), q, fx.todo(t, q, nil)); err != nil || rid != "other" {
		t.Fatalf("another account: %q %v", rid, err)
	}
	// Turned off, the account runs again.
	if err := fx.d.Pacer.Set("ann@example.com", usage.PaceSettings{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fx.d.Dispatch(context.Background(), p, fx.todo(t, p, nil)); err != nil {
		t.Fatalf("pace off: %v", err)
	}
}

// startN dispatches tasks of p until one waits, and returns how many each
// runner got.
func (fx *fixture) startN(t *testing.T, p model.Project, n int) map[string]int {
	t.Helper()
	got := map[string]int{}
	for range n {
		td := fx.todo(t, p, nil)
		rid, remote, err := fx.d.Dispatch(context.Background(), p, td)
		if err != nil {
			break
		}
		if _, err := fx.st.UpdateTodo(td.ID, func(x *model.Todo) error {
			x.Status, x.Runner, x.RemoteID = model.StatusInProgress, rid, remote
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		got[rid]++
	}
	return got
}

// On the line, each runner runs its usual number; behind it, the account
// runs more at once, spread over its runners, up to the boost times their
// combined usual.
func TestPaceBoostsTheAccountWhenBehind(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name        string
		gap         time.Duration
		boost       float64
		laptop, box int
	}{
		{"on pace", 0, 0, 2, 2},
		{"a little behind", -time.Hour, 0, 2, 3}, // one more, to the first by name
		{"further behind", -(usage.PaceDeadBand + usage.PaceSlotStep + time.Minute), 0, 3, 3}, // two more, one each
		{"far behind", -48 * time.Hour, 0, 4, 4},                                              // capped at 2× the usual 4
		{"far behind, boost 3", -48 * time.Hour, 3, 6, 6},                                     // 3× 4
		{"far behind, boost 1", -48 * time.Hour, 1, 2, 2},                                     // no more than usual
		{"just behind, within the dead band", -usage.PaceDeadBand + time.Minute, 4, 2, 2},     // on pace
		{"ahead, within the dead band", usage.PaceDeadBand - time.Minute, 4, 2, 2},            // on pace
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			fx := pacedFixture(t)
			now := time.Now()
			fx.fleet.set(onAccount("box", fx.orgA, "ann@example.com", gap(c.gap), now))
			fx.fleet.set(onAccount("laptop", fx.orgA, "ann@example.com", gap(c.gap), now))
			if err := fx.d.Pacer.Set("ann@example.com", usage.PaceSettings{Follow: true, MaxBoost: c.boost}); err != nil {
				t.Fatal(err)
			}
			p := fx.project(t, fx.orgA, "laptop", "box")
			got := fx.startN(t, p, 20)
			if got["laptop"]+got["box"] != c.laptop+c.box {
				t.Fatalf("started %v, want %d in all", got, c.laptop+c.box)
			}
			r, _ := fx.fleet.Get("laptop")
			g := fx.d.Pacer.For(r)
			if g.Slots["laptop"] != c.laptop || g.Slots["box"] != c.box || g.Pace.Slots != c.laptop+c.box || g.Parallel != 4 {
				t.Errorf("slots %v (group %d of %d)", g.Slots, g.Pace.Slots, g.Parallel)
			}
		})
	}
}

// A paused or offline runner takes no share of the boost; the others do,
// and each runner's own limit (set on the hub) is its usual.
func TestPaceBoostSkipsPausedRunners(t *testing.T) {
	t.Parallel()

	fx := pacedFixture(t)
	now := time.Now()
	paused := onAccount("paused", fx.orgA, "ann@example.com", gap(-48*time.Hour), now)
	paused.Paused = true
	off := onAccount("off", fx.orgA, "ann@example.com", gap(-48*time.Hour), now)
	off.Online = false
	big := onAccount("big", fx.orgA, "ann@example.com", gap(-48*time.Hour), now)
	big.Parallel = 3
	for _, r := range []tunnel.Runner{paused, off, big} {
		fx.fleet.set(r)
	}
	if err := fx.d.Pacer.Set("ann@example.com", usage.PaceSettings{Follow: true}); err != nil {
		t.Fatal(err)
	}
	g := fx.d.Pacer.For(big)
	if g.Parallel != 3 || g.Slots["big"] != 6 || g.Slots["paused"] != 2 || g.Slots["off"] != 2 {
		t.Fatalf("group %+v slots %v", g.Pace, g.Slots)
	}
	p := fx.project(t, fx.orgA, "paused", "off", "big")
	if got := fx.startN(t, p, 10); got["big"] != 6 || len(got) != 1 {
		t.Fatalf("started %v", got)
	}
}

// The account's freshest usage stops all its runners at a hard limit, even
// one whose own report is older and says nothing is wrong.
func TestPaceHardStopFromTheFreshestReport(t *testing.T) {
	t.Parallel()

	fx := pacedFixture(t)
	now := time.Now()
	stale := onAccount("stale", fx.orgA, "ann@example.com", gap(-48*time.Hour), now.Add(-time.Hour))
	fresh := onAccount("fresh", fx.orgA, "ann@example.com", gap(-48*time.Hour), now)
	fresh.Reported.Usage.Windows = append(fresh.Reported.Usage.Windows, usage.Window{Name: "five_hour", Utilization: 1, ResetsAt: now.Add(time.Hour), Status: "rejected"})
	fx.fleet.set(stale)
	fx.fleet.set(fresh)
	if err := fx.d.Pacer.Set("ann@example.com", usage.PaceSettings{Follow: true}); err != nil {
		t.Fatal(err)
	}
	p := fx.project(t, fx.orgA, "stale")
	_, _, err := fx.d.Dispatch(context.Background(), p, fx.todo(t, p, nil))
	var w *WaitError
	if !errors.As(err, &w) || !strings.Contains(w.Reason, "stale's Claude account is paused (5-hour limit reached)") {
		t.Fatalf("err = %v, want the 5-hour stop", err)
	}
}

// The waiting state holds until the line catches up (hysteresis), for the
// whole group.
func TestPaceHysteresisAcrossTheGroup(t *testing.T) {
	t.Parallel()

	fx := pacedFixture(t)
	now := time.Now()
	fx.d.Pacer.now = func() time.Time { return now }
	set := func(d time.Duration) {
		fx.fleet.set(onAccount("a", fx.orgA, "ann@example.com", gap(d), now))
		fx.fleet.set(onAccount("b", fx.orgA, "ann@example.com", gap(d), now))
	}
	if err := fx.d.Pacer.Set("ann@example.com", usage.PaceSettings{Follow: true}); err != nil {
		t.Fatal(err)
	}
	a := func() GroupPace { r, _ := fx.fleet.Get("a"); return fx.d.Pacer.For(r) }
	set(usage.PaceDeadBand + time.Minute)
	if g := a(); g.State != usage.PaceWaiting {
		t.Fatalf("ahead: %+v", g.Pace)
	}
	set(usage.PaceDeadBand / 2) // back in the dead band, still ahead
	if g := a(); g.State != usage.PaceWaiting {
		t.Fatalf("still ahead should keep waiting: %+v", g.Pace)
	}
	set(0)
	if g := a(); g.State != usage.PaceOnPace || g.Slots["a"] != 2 {
		t.Fatalf("on the line: %+v", g.Pace)
	}
}

func TestPaceSettingsAreKept(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "pace.json")
	p := &Pacer{}
	if err := p.Keep(path); err != nil {
		t.Fatal(err)
	}
	if err := p.Set("Ann@Example.com", usage.PaceSettings{Follow: true, MaxBoost: 3}); err != nil {
		t.Fatal(err)
	}
	if err := p.Set("ann@example.com", usage.PaceSettings{Follow: true, MaxBoost: 0.5}); err == nil {
		t.Error("a boost under 1 was taken")
	}
	again := &Pacer{}
	if err := again.Keep(path); err != nil {
		t.Fatal(err)
	}
	if s := again.Settings("ann@example.com"); !s.Follow || s.MaxBoost != 3 {
		t.Fatalf("restored %+v", s)
	}
}

// A runner reports its own limit: more only while it paces itself (an
// older hub), never the hub's allowance back to the hub.
func TestReporterParallelWithHubPace(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	tr := usage.New(nil)
	tr.SetParallel(2)
	if err := tr.SetPace(usage.PaceSettings{Follow: true}); err != nil {
		t.Fatal(err)
	}
	tr.Observe([]byte(`{"status":"allowed","unifiedWindows":{"seven_day":{"utilization":0.1,"resetsAt":` +
		strconv.FormatInt(time.Now().Add(usage.WeekSpan/2).Unix(), 10) + `,"status":"allowed"}}}`))
	rep := &Reporter{Store: fx.st, Usage: tr, Parallel: 2}
	if r := rep.Build(time.Now()); r.Parallel != 4 {
		t.Fatalf("pacing itself, far behind: parallel %d, want 4", r.Parallel)
	}
	tr.SetHubPace(&usage.Pace{Follow: true, State: usage.PaceCatching, Slots: 6})
	if r := rep.Build(time.Now()); r.Parallel != 2 || !r.Usage.Pace.Hub || r.Usage.Paused {
		t.Fatalf("paced by the hub: parallel %d, pace %+v", r.Parallel, r.Usage.Pace)
	}
}
