package dispatch

import (
	"testing"
	"time"

	"github.com/klahr/dash/internal/claudecli"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tunnel"
	"github.com/klahr/dash/internal/usage"
)

// usedRunner is runner id of org, online, running 2 at once, on Claude
// account email, whose last report (as of at) has its 5-hour and weekly
// windows at five and week. five < 0: it reported no usage at all.
func usedRunner(id string, org int64, email string, five, week float64, at time.Time) tunnel.Runner {
	r := runnerOf(id, org, true)
	r.Reported = &tunnel.Reported{Parallel: 2, At: at, Claude: &claudecli.Status{Version: "2.1.0",
		Account: &claudecli.Account{Email: email, Subscription: "max"}}}
	if five >= 0 {
		r.Reported.Usage = &usage.Snapshot{Known: true, UpdatedAt: at, Windows: []usage.Window{
			{Name: usage.FiveHourWindow, Utilization: five, ResetsAt: at.Add(2 * time.Hour)},
			{Name: usage.WeekWindow, Utilization: week, ResetsAt: at.Add(72 * time.Hour)},
		}}
	}
	return r
}

// busy gives runner id n tasks in progress in p.
func (fx *fixture) busy(t *testing.T, p model.Project, id string, n int) {
	t.Helper()
	for range n {
		fx.todo(t, p, func(x *model.Todo) { x.Status, x.Runner = model.StatusInProgress, id })
	}
}

// picked is the runner pick chooses for a new task of p ("" for none).
func (fx *fixture) picked(t *testing.T, p model.Project, parent *model.Todo, fn func(*model.Todo)) string {
	t.Helper()
	r, why, _ := fx.d.pick(p, fx.todo(t, p, fn), parent)
	if r == nil {
		t.Fatalf("no runner picked: %s", why)
	}
	return r.ID
}

// A runner on an account with spare usage takes the task, though a runner
// on a busier account has less to do.
func TestPickPrefersTheAccountWithLowUsage(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	now := time.Now()
	fx.fleet.set(usedRunner("fresh", fx.orgA, "alice@example.com", 0.10, 0.20, now))
	fx.fleet.set(usedRunner("spent", fx.orgA, "bob@example.com", 0.80, 0.30, now))
	p := fx.project(t, fx.orgA, "fresh", "spent")
	fx.busy(t, p, "fresh", 1)
	if got := fx.picked(t, p, nil, nil); got != "fresh" {
		t.Errorf("picked %s, want fresh (10%% used) over spent (80%%), though it is busier", got)
	}
}

// Usage counts in steps: 41% and 43% are the same, so the less busy runner
// takes the task.
func TestPickSmallUsageDifferencesDontBeatLoad(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	now := time.Now()
	fx.fleet.set(usedRunner("a", fx.orgA, "alice@example.com", 0.41, 0.10, now))
	fx.fleet.set(usedRunner("b", fx.orgA, "bob@example.com", 0.43, 0.10, now))
	p := fx.project(t, fx.orgA, "a", "b")
	fx.busy(t, p, "a", 1)
	if got := fx.picked(t, p, nil, nil); got != "b" {
		t.Errorf("picked %s, want b, the less busy at about the same usage", got)
	}
}

// A task that continues a session, or builds on a task whose session is on
// a runner, goes there whatever its account's usage.
func TestPickSessionBeatsUsage(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	now := time.Now()
	fx.fleet.set(usedRunner("fresh", fx.orgA, "alice@example.com", 0.05, 0.05, now))
	fx.fleet.set(usedRunner("spent", fx.orgA, "bob@example.com", 0.90, 0.90, now))
	p := fx.project(t, fx.orgA, "fresh", "spent")
	if got := fx.picked(t, p, nil, func(x *model.Todo) { x.Runner, x.RemoteID = "spent", "r1" }); got != "spent" {
		t.Errorf("continuing a session: picked %s, want spent, which has it", got)
	}
	parent := fx.todo(t, p, func(x *model.Todo) { x.Status, x.Runner = model.StatusMerged, "spent" })
	if got := fx.picked(t, p, &parent, func(x *model.Todo) { x.BuildsOn = parent.ID }); got != "spent" {
		t.Errorf("building on a task: picked %s, want spent, which has its session", got)
	}
}

// Runners on one Claude account share its usage, the freshest any of them
// reported: they tie on it, and the less busy one takes the task.
func TestPickSameAccountTiesOnUsage(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	now := time.Now()
	fx.fleet.set(usedRunner("old", fx.orgA, "alice@example.com", 0.90, 0.90, now.Add(-time.Hour)))   // an old report
	fx.fleet.set(usedRunner("new", fx.orgA, "Alice@example.com", 0.10, 0.10, now.Add(-time.Minute))) // the account's freshest
	p := fx.project(t, fx.orgA, "old", "new")
	fx.busy(t, p, "new", 1)
	if got := fx.picked(t, p, nil, nil); got != "old" {
		t.Errorf("picked %s, want old: on the same account as new, and less busy", got)
	}
}

// A runner whose account hasn't reported its usage comes after every one
// that has, however busy those are; among themselves, by load.
func TestPickRunnersWithoutUsageGoLast(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	now := time.Now()
	fx.fleet.set(usedRunner("quiet1", fx.orgA, "carol@example.com", -1, 0, now))
	fx.fleet.set(usedRunner("quiet2", fx.orgA, "dave@example.com", -1, 0, now))
	fx.fleet.set(usedRunner("spent", fx.orgA, "bob@example.com", 0.85, 0.85, now))
	p := fx.project(t, fx.orgA, "quiet1", "quiet2", "spent")
	fx.busy(t, p, "spent", 1)
	fx.busy(t, p, "quiet1", 1)
	if got := fx.picked(t, p, nil, nil); got != "spent" {
		t.Errorf("picked %s, want spent, the only one that reported its usage", got)
	}
	fx.busy(t, p, "spent", 1) // full now
	if got := fx.picked(t, p, nil, nil); got != "quiet2" {
		t.Errorf("picked %s, want quiet2, the less busy of those without a report", got)
	}
}

// The weekly window counts when it is higher than the 5-hour one: an
// account nearly out for the week isn't picked for a fresh 5-hour window.
func TestPickWeeklyUsageCounts(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	now := time.Now()
	fx.fleet.set(usedRunner("weekly", fx.orgA, "alice@example.com", 0.05, 0.90, now))
	fx.fleet.set(usedRunner("even", fx.orgA, "bob@example.com", 0.50, 0.50, now))
	p := fx.project(t, fx.orgA, "weekly", "even")
	fx.busy(t, p, "even", 1)
	if got := fx.picked(t, p, nil, nil); got != "even" {
		t.Errorf("picked %s, want even (50%%) over weekly (90%% of the week)", got)
	}
}
