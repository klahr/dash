package queue

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

// spendPub records the spending warnings the engine sends.
type spendPub struct {
	mu    sync.Mutex
	warns []model.SpendWarning
}

func (p *spendPub) Publish(ev model.Event) {
	if w, ok := ev.Data.(model.SpendWarning); ok && ev.Type == model.EvSpendWarning {
		p.mu.Lock()
		p.warns = append(p.warns, w)
		p.mu.Unlock()
	}
}

func (p *spendPub) kinds() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var ks []string
	for _, w := range p.warns {
		ks = append(ks, w.Kind)
	}
	return strings.Join(ks, ",")
}

// costExec streams costs to the run's OnCost, as Claude Code's messages
// do, then blocks until stopped when stopAfter is set; otherwise it
// finishes, reporting the last cost.
type costExec struct {
	fakeExec
	tt        *testing.T
	costs     [][]float64 // the costs each run reports, run by run
	stopAfter bool        // the first run never finishes on its own
}

func (c *costExec) Run(ctx context.Context, req agent.ExecRequest, emit func(model.RunEvent)) (agent.ExecResult, error) {
	c.mu.Lock()
	c.calls = append(c.calls, req)
	n := len(c.calls)
	c.mu.Unlock()
	costs := c.costs[min(n, len(c.costs))-1]
	for _, usd := range costs {
		req.OnCost(usd)
	}
	last := costs[len(costs)-1]
	res := agent.ExecResult{SessionID: "sess", Turns: 1, Stop: "success", Model: "claude-test"}
	if c.stopAfter && n == 1 {
		<-ctx.Done()
		res.CostUSD, res.Estimated = last, true // stopped before its result
		return res, ctx.Err()
	}
	write(c.tt, req.Dir, "f"+strconv.Itoa(n)+".txt", "done\n")
	res.CostUSD = last
	return res, nil
}

func TestTaskCapStopsTheRunAndRetryRaisesItOnce(t *testing.T) {
	exe := &costExec{tt: t, costs: [][]float64{{0.5, 0.85, 1.2, 1.5}, {0.4}}, stopAfter: true}
	h := newHarness(t, exe, model.Project{Name: "p", Spending: &model.Spending{TaskUSD: 1}})
	pub := &spendPub{}
	h.engine.Pub = pub // before any task: read only by the workers

	td := h.add("costly")
	td = h.waitFor(td.ID, model.StatusFailed)
	if td.Error != "Stopped at the $1 task cap" || td.CapHit != 1 {
		t.Fatalf("stopped at the cap: error %q, cap hit %v", td.Error, td.CapHit)
	}
	if td.CostUSD != 1.5 || len(td.RunStats) != 1 || !td.RunStats[0].Estimated {
		t.Errorf("the stopped run's estimated cost counts: cost %v, runs %+v", td.CostUSD, td.RunStats)
	}
	if got := pub.kinds(); got != "task" {
		t.Errorf("warnings: %q, want one for the task at 80%%", got)
	}
	if td.CapWarned != 1 {
		t.Errorf("cap warned: %v", td.CapWarned)
	}

	// Retry: the cap goes up once, by another task cap's worth.
	td, err := h.st.UpdateTodo(td.ID, func(x *model.Todo) error {
		if !model.RaiseTaskCap(x, 1) {
			t.Error("a task stopped at its cap should be raised")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if td.CapUSD != 2.5 || td.CapHit != 0 {
		t.Fatalf("raised cap %v (hit %v), want 2.5", td.CapUSD, td.CapHit)
	}
	if model.RaiseTaskCap(&td, 1) {
		t.Error("a second raise without hitting the cap again")
	}
	if _, err := h.st.SetStatus(td.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	h.engine.Kick()
	td = h.waitFor(td.ID, model.StatusReview)
	if td.CostUSD != 1.9 && (td.CostUSD < 1.899 || td.CostUSD > 1.901) {
		t.Errorf("cost after the retry: %v", td.CostUSD)
	}
	if got := pub.kinds(); got != "task" {
		t.Errorf("warnings after the retry: %q (1.9 is under 80%% of 2.5)", got)
	}
}

func TestTaskAtItsCapDoesntRunAgain(t *testing.T) {
	exe := &costExec{tt: t, costs: [][]float64{{0.1}}}
	h := newHarness(t, exe, model.Project{Name: "p", Spending: &model.Spending{TaskUSD: 2}})
	td, err := h.st.CreateTodo(model.Todo{ProjectID: h.proj.ID, Title: "spent", Prompt: "do it", CostUSD: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SetStatus(td.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	h.engine.Kick()
	td = h.waitFor(td.ID, model.StatusFailed)
	if td.Error != "Stopped at the $2 task cap" || td.CapHit != 2 || len(exe.Calls()) != 0 {
		t.Fatalf("error %q, cap hit %v, runs %d", td.Error, td.CapHit, len(exe.Calls()))
	}
}

// clock is a settable clock for the engine's spending checks.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// spentAt adds a finished task to h's project that cost usd at at.
func spentAt(t *testing.T, h *harness, usd float64, at time.Time) {
	t.Helper()
	td, err := h.st.CreateTodo(model.Todo{ProjectID: h.proj.ID, Title: "earlier", Prompt: "earlier"})
	if err == nil {
		_, err = h.st.UpdateTodo(td.ID, func(x *model.Todo) error {
			x.CostUSD, x.RunStats = usd, []model.TodoRun{{At: at, Kind: model.RunFirst, CostUSD: usd}}
			return nil
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	// Totals are kept a few seconds; a run that ends here drops them, as
	// this cost, added behind the engine's back, must.
	h.engine.spentChanged(h.proj.ID)
}

func spendHarness(t *testing.T, s *model.Spending, start time.Time) (*harness, *clock, *spendPub) {
	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		write(t, req.Dir, "f"+strconv.Itoa(n)+".txt", "done\n")
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p", Spending: s})
	c := &clock{t: start}
	pub := &spendPub{}
	h.engine.spend.mu.Lock()
	h.engine.spend.now = c.now
	h.engine.spend.mu.Unlock()
	h.engine.Pub = pub
	return h, c, pub
}

// waitWaiting waits for queued task id to wait for why.
func (h *harness) waitWaiting(id, why string) model.Todo {
	h.t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		td, _ := h.st.GetTodo(id)
		if td.Status == model.StatusQueued && td.Waiting == why {
			return td
		}
		h.engine.Kick()
		time.Sleep(50 * time.Millisecond)
	}
	td, _ := h.st.GetTodo(id)
	h.t.Fatalf("todo %s: %s, waiting %q; want queued, waiting %q", id, td.Status, td.Waiting, why)
	return td
}

func TestProjectDayCapHoldsTheQueueUntilTheNextDay(t *testing.T) {
	start := time.Date(2026, 11, 27, 22, 0, 0, 0, time.UTC)
	h, c, pub := spendHarness(t, &model.Spending{DayUSD: 1, Zone: "UTC"}, start)
	spentAt(t, h, 1.2, start.Add(-time.Hour))
	// Yesterday's spend doesn't count today.
	spentAt(t, h, 5, start.Add(-30*time.Hour))

	td := h.add("held")
	hold := model.SpendHold{Period: model.SpendDay, CapUSD: 1}
	td = h.waitWaiting(td.ID, hold.Note())
	p, _ := h.st.GetProject(h.proj.ID)
	if p.SpendHold == nil || p.SpendHold.Period != model.SpendDay || p.SpendHold.SpentUSD < 1.2 {
		t.Fatalf("the project's hold: %+v", p.SpendHold)
	}
	if got := pub.kinds(); got != "day" {
		t.Errorf("warnings: %q, want one for the day", got)
	}

	// The next day starts: the queue starts it again.
	c.set(p.SpendHold.Until.Add(time.Minute))
	h.engine.Kick()
	h.waitFor(td.ID, model.StatusReview)
	waitHold(t, h, false)
	if got := pub.kinds(); got != "day" {
		t.Errorf("warnings after the day rolled over: %q", got)
	}
}

func TestRaisingTheCapRestartsTheQueue(t *testing.T) {
	start := time.Now()
	h, _, pub := spendHarness(t, &model.Spending{DayUSD: 10, MonthUSD: 2}, start)
	spentAt(t, h, 2.5, start)

	td := h.add("held")
	hold := model.SpendHold{Period: model.SpendMonth, CapUSD: 2}
	h.waitWaiting(td.ID, hold.Note())
	if got := pub.kinds(); got != "month" {
		t.Errorf("warnings: %q, want the month's (2.5 is under 80%% of the day's 10)", got)
	}
	if _, err := h.st.UpdateProject(h.proj.ID, func(x *model.Project) error {
		x.Spending = &model.Spending{DayUSD: 10, MonthUSD: 20, Warned: x.Spending.Warned}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.engine.SpendingChanged(h.proj.ID)
	h.waitFor(td.ID, model.StatusReview)
	waitHold(t, h, false)
}

func TestTurningCapsOffClearsTheHold(t *testing.T) {
	start := time.Now()
	h, _, _ := spendHarness(t, &model.Spending{DayUSD: 1}, start)
	spentAt(t, h, 3, start)
	td := h.add("held")
	h.waitWaiting(td.ID, model.SpendHold{Period: model.SpendDay, CapUSD: 1}.Note())
	if _, err := h.st.UpdateProject(h.proj.ID, func(x *model.Project) error { x.Spending = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	h.engine.SpendingChanged(h.proj.ID)
	h.waitFor(td.ID, model.StatusReview)
	waitHold(t, h, false)
}

// waitHold waits for h's project to be held (or not).
func waitHold(t *testing.T, h *harness, held bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if p, _ := h.st.GetProject(h.proj.ID); (p.SpendHold != nil) == held {
			return
		}
		h.engine.Kick()
		time.Sleep(50 * time.Millisecond)
	}
	p, _ := h.st.GetProject(h.proj.ID)
	t.Fatalf("project hold %+v, want held %v", p.SpendHold, held)
}
