package queue

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/spend"
)

// Spending caps (model.Spending, docs/spending.md): a task's cap stops its
// run as its cost reaches it (capWatcher); a project's daily and monthly
// caps hold its queued tasks back once reached (projectSpend), until the
// next day or month or until the cap is raised. Each cap warns once per
// period at 80% (EvSpendWarning, which notifies).

// spendState is what the engine knows of its projects' spending.
type spendState struct {
	mu     sync.Mutex
	totals map[string]spendTotals // project id → what it spent, as last added up
	// gen counts each project's cost changes (spentChanged): totals added
	// up across one aren't kept, as they may miss it.
	gen       map[string]uint64
	lastSweep time.Time
	// now, if set, is the clock (tests); else time.Now.
	now func() time.Time
}

type spendTotals struct {
	at time.Time
	t  spend.Totals
}

// spendFresh is how long added-up totals are used before they are added
// up again; a run that ends here drops its project's at once.
const spendFresh = 10 * time.Second

// spendSweep is how often every project with caps is checked, queued tasks
// or not: a hold ends (or a warning is due) without one.
const spendSweep = 30 * time.Second

func (e *TodoEngine) spendNow() time.Time {
	e.spend.mu.Lock()
	now := e.spend.now
	e.spend.mu.Unlock()
	if now != nil {
		return now()
	}
	return time.Now()
}

// spentChanged drops project id's totals, after one of its runs recorded
// its cost.
func (e *TodoEngine) spentChanged(projectID string) {
	e.spend.mu.Lock()
	delete(e.spend.totals, projectID)
	if e.spend.gen == nil {
		e.spend.gen = map[string]uint64{}
	}
	e.spend.gen[projectID]++
	e.spend.mu.Unlock()
}

// SpendingChanged is told that project id's caps changed (or its costs):
// they are checked again at once, and held tasks may start.
func (e *TodoEngine) SpendingChanged(projectID string) {
	e.spentChanged(projectID)
	if e.Store == nil {
		return
	}
	if p, err := e.Store.GetProject(projectID); err == nil {
		e.projectSpend(p, true)
	}
	e.Kick()
}

// spentTotals is what project p spent in the current day and month.
func (e *TodoEngine) spentTotals(p model.Project, now time.Time) spend.Totals {
	loc := p.Spending.Location()
	e.spend.mu.Lock()
	c, ok := e.spend.totals[p.ID]
	gen := e.spend.gen[p.ID]
	e.spend.mu.Unlock()
	// A cached total is good while it is fresh and of the same day.
	if ok && now.Sub(c.at) < spendFresh && !now.Before(c.at) && now.Before(c.t.DayEnd) && c.t.DayStart.Location() == loc {
		return c.t
	}
	t := spend.TotalsAt(spend.Entries(e.Store.ListTodos(p.ID), e.Store.ListScanRuns(p.ID, "")), now, loc)
	e.spend.mu.Lock()
	if e.spend.totals == nil {
		e.spend.totals = map[string]spendTotals{}
	}
	if e.spend.gen[p.ID] == gen { // no cost recorded meanwhile
		e.spend.totals[p.ID] = spendTotals{at: now, t: t}
	}
	e.spend.mu.Unlock()
	return t
}

// projectSpend is the cap that holds project p's tasks back now, or nil.
// It records the hold on the project (model.Project.SpendHold, which the
// board shows) and warns of caps reached 80% of. force adds up its spend
// afresh.
func (e *TodoEngine) projectSpend(p model.Project, force bool) *model.SpendHold {
	s := p.Spending
	if !(s != nil && (s.DayUSD > 0 || s.MonthUSD > 0)) {
		if p.SpendHold != nil || (s != nil && len(s.Warned) > 0) {
			e.saveSpend(p.ID, nil, nil, nil)
		}
		return nil
	}
	if force {
		e.spentChanged(p.ID)
	}
	now := e.spendNow()
	t := e.spentTotals(p, now)
	hold := spend.Hold(s, t)
	warns := spend.Warnings(s, t)
	warned := spend.KeepWarned(s.Warned, t)
	for _, w := range warns {
		warned = append(warned, w.Key)
	}
	if !sameHold(p.SpendHold, hold) || !slices.Equal(warned, s.Warned) {
		e.saveSpend(p.ID, hold, warned, s)
	}
	for _, w := range warns {
		e.publishSpend(model.SpendWarning{ProjectID: p.ID, OrgID: p.OrgID, Kind: w.Kind, CapUSD: w.CapUSD, SpentUSD: w.SpentUSD})
	}
	return hold
}

// sameHold reports whether a and b hold for the same cap and period (what
// was spent may differ: the hold shows it as of when it began).
func sameHold(a, b *model.SpendHold) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Period == b.Period && a.CapUSD == b.CapUSD && a.Until.Equal(b.Until)
}

// saveSpend records project id's hold and warned periods. was is the
// Spending they were worked out from: caps changed since are left alone.
func (e *TodoEngine) saveSpend(id string, hold *model.SpendHold, warned []string, was *model.Spending) {
	_, err := e.Store.UpdateProject(id, func(x *model.Project) error {
		if was != nil && (x.Spending == nil || x.Spending.DayUSD != was.DayUSD || x.Spending.MonthUSD != was.MonthUSD || x.Spending.Zone != was.Zone) {
			return errSpendMoved
		}
		x.SpendHold = hold
		if x.Spending != nil {
			s := x.Spending.Clone()
			s.Warned = warned
			x.Spending = s
		}
		return nil
	})
	if err != nil && !errors.Is(err, errSpendMoved) {
		slog.Error("save spending hold", "project", id, "err", err)
	}
}

var errSpendMoved = errors.New("the caps changed meanwhile")

// checkSpending checks every project with caps, or held, every spendSweep.
func (e *TodoEngine) checkSpending() {
	now := e.spendNow()
	e.spend.mu.Lock()
	due := now.Sub(e.spend.lastSweep) >= spendSweep || now.Before(e.spend.lastSweep)
	if due {
		e.spend.lastSweep = now
	}
	e.spend.mu.Unlock()
	if !due {
		return
	}
	for _, p := range e.Store.ListProjects() {
		if p.Spending != nil || p.SpendHold != nil {
			e.projectSpend(p, false)
		}
	}
}

func (e *TodoEngine) publishSpend(w model.SpendWarning) {
	if e.Pub == nil {
		return
	}
	e.Pub.Publish(model.Event{Type: model.EvSpendWarning, ProjectID: w.ProjectID, OrgID: w.OrgID, Data: w, Time: time.Now()})
}

// overTaskCap fails a run of task t before it starts when the task cost
// its cap already (a Retry that didn't raise it, or a cap lowered since).
func (e *TodoEngine) overTaskCap(t *model.Todo, p model.Project) error {
	c := model.TaskCap(p, *t)
	if c <= 0 || t.CostUSD < c {
		return nil
	}
	if nt, err := e.Store.UpdateTodo(t.ID, func(x *model.Todo) error { x.CapHit = c; return nil }); err == nil {
		*t = nt
	}
	return errors.New(model.TaskCapError(c))
}

// capWatcher stops one run of a task as the task's cost reaches its cap,
// and warns at 80% of it. seen is told the run's cost so far.
type capWatcher struct {
	e           *TodoEngine
	t           model.Todo
	org         int64
	cap         float64
	base        float64 // what the task cost before this run
	stop        context.CancelFunc
	over        atomic.Bool // the cap stopped the run
	warned      atomic.Bool // this run warned
	warnPending bool        // record found a warning due at the end, for publish
}

func (e *TodoEngine) capWatch(t model.Todo, p model.Project, capUSD float64, stop context.CancelFunc) *capWatcher {
	return &capWatcher{e: e, t: t, org: p.OrgID, cap: capUSD, base: t.CostUSD, stop: stop}
}

func (c *capWatcher) seen(usd float64) {
	if c.cap <= 0 {
		return
	}
	spent := c.base + usd
	if !c.warned.Load() && spend.TaskWarns(c.cap, spent, c.t.CapWarned) {
		c.warned.Store(true)
		c.e.publishSpend(model.SpendWarning{ProjectID: c.t.ProjectID, OrgID: c.org, Kind: model.SpendTask, CapUSD: c.cap, SpentUSD: spent, TodoID: c.t.ID})
	}
	if spent >= c.cap && !c.over.Swap(true) {
		c.e.Log.Line(c.t, "■ "+model.TaskCapError(c.cap)+": stopping Claude")
		c.stop()
	}
}

// record notes on task x (as its run's cost is saved) the cap it warned
// of: by this run as it streamed, or now, the cost reported at the end
// having reached 80% of it.
func (c *capWatcher) record(x *model.Todo) {
	if c.cap <= 0 {
		return
	}
	switch {
	case c.warned.Load():
		x.CapWarned = c.cap
	case spend.TaskWarns(c.cap, x.CostUSD, x.CapWarned):
		x.CapWarned, c.warnPending = c.cap, true
	}
}

// publish sends the warning record found due, for task t as saved.
func (c *capWatcher) publish(t model.Todo) {
	if c.warnPending {
		c.warnPending = false
		c.e.publishSpend(model.SpendWarning{ProjectID: t.ProjectID, OrgID: c.org, Kind: model.SpendTask, CapUSD: c.cap, SpentUSD: t.CostUSD, TodoID: t.ID})
	}
}
