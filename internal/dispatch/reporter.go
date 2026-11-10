package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/klahr/dash/internal/claudecli"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/usage"
)

// Reporter is the runner's side: it tells the hub, on POST /runner/report,
// where the hub's tasks are and how much of its owner's Claude subscription
// is used, whenever one of those changes, when the tunnel comes up, and
// every Every as a heartbeat.
type Reporter struct {
	Store    *store.Store
	Usage    *usage.Tracker // optional
	Parallel int            // the runner's -parallel
	// Terminals: the runner opens terminals for its owner on the hub (it
	// runs with -hub-commands).
	Terminals bool
	// ChatCommands: its chats run "!" commands for its owner.
	ChatCommands bool
	// Claude reports the runner's Claude Code CLI (optional).
	Claude *claudecli.Monitor
	// Send posts the report (tunnel.Report with the runner's credentials).
	Send func(ctx context.Context, r Report) error
	// Every is the heartbeat (default 30s).
	Every time.Duration
	// Keep is how long finished tasks are still reported (default a day),
	// so the hub hears of what finished while it couldn't be reached.
	Keep time.Duration
	// Checkouts lists the clones of the hub's projects whose checkout is
	// behind origin, for every report (optional).
	Checkouts func() []CheckoutState
	// Perms lists the permission requests waiting on the runner (its
	// perm.Broker's Pending); those of the hub's tasks go in every report.
	Perms func() []perm.Request

	once sync.Once
	wake chan struct{}

	mu     sync.Mutex
	live   map[string]*liveOutput // runner todo id → Claude's output since the last report
	procs  map[string]bool        // hub project ids whose processes changed since the last report
	lists  map[string]bool        // hub project ids whose TODO.md changed since the last report
	pushes []Push                 // not yet reported
}

// maxPushes bounds the pushes kept while the hub can't be reached: only the
// latest per branch matters, and older ones are dropped first.
const maxPushes = 100

// Pushed reports, soon, that this runner pushed a branch of a hub's project.
func (r *Reporter) Pushed(p Push) {
	if p.HubProjectID == "" || p.SHA == "" {
		return
	}
	r.mu.Lock()
	r.pushes = append(r.pushes, p)
	if n := len(r.pushes) - maxPushes; n > 0 {
		r.pushes = append([]Push(nil), r.pushes[n:]...)
	}
	r.mu.Unlock()
	r.Nudge()
}

// takePushes hands over the pushes not reported yet.
func (r *Reporter) takePushes() []Push {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.pushes
	r.pushes = nil
	return p
}

// unsent puts back pushes a report failed to deliver, before newer ones.
func (r *Reporter) unsent(p []Push) {
	if len(p) == 0 {
		return
	}
	r.mu.Lock()
	r.pushes = append(append([]Push(nil), p...), r.pushes...)
	if n := len(r.pushes) - maxPushes; n > 0 {
		r.pushes = append([]Push(nil), r.pushes[n:]...)
	}
	r.mu.Unlock()
}

// liveOutput is what a hub's task printed since the last report: the hub
// streams it to its dashboards like a task of its own.
type liveOutput struct {
	line   string
	events []model.RunEvent
}

// Live output kept per task between reports: older events are dropped (the
// hub reads the whole log from the runner when someone opens the task).
const (
	maxLiveEvents = 100
	maxLiveText   = 4000
)

// ErrStop ends Run: Send returns it when the hub revoked the runner.
var ErrStop = errors.New("stop reporting")

func (r *Reporter) init() {
	r.once.Do(func() { r.wake = make(chan struct{}, 1) })
}

// Nudge asks for a report soon.
func (r *Reporter) Nudge() {
	r.init()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Observe is fed the runner's events: changes to the hub's tasks and to
// usage are reported.
func (r *Reporter) Observe(ev model.Event) {
	switch ev.Type {
	case model.EvUsage, model.EvRemote:
		r.Nudge()
	case model.EvTodoUpdated:
		if t, ok := ev.Data.(model.Todo); ok && t.HubTodoID != "" {
			r.Nudge()
		}
	case model.EvTodoOutput, model.EvTodoEvent:
		r.observeOutput(ev)
	case model.EvProcUpdated:
		r.observeProc(ev)
	case model.EvChecklist:
		r.observeChecklist(ev)
	case model.EvPermission:
		if ev.TodoID == "" || r.Store == nil {
			return
		}
		if t, err := r.Store.GetTodo(ev.TodoID); err == nil && t.HubTodoID != "" {
			r.Nudge() // the hub's board shows it, or stops showing it
		}
	}
}

// observeProc keeps, for the next report, that a process of the clone of a
// hub's project started or ended.
func (r *Reporter) observeProc(ev model.Event) { r.observeProject(ev, &r.procs) }

// observeChecklist keeps, for the next report, that the TODO.md of the
// clone of a hub's project changed (committed here, or brought in).
func (r *Reporter) observeChecklist(ev model.Event) { r.observeProject(ev, &r.lists) }

// observeProject adds the hub project of ev's project to set, and asks for
// a report soon.
func (r *Reporter) observeProject(ev model.Event, set *map[string]bool) {
	if ev.ProjectID == "" || r.Store == nil {
		return
	}
	p, err := r.Store.GetProject(ev.ProjectID)
	if err != nil || p.HubProjectID == "" {
		return
	}
	r.mu.Lock()
	if *set == nil {
		*set = map[string]bool{}
	}
	(*set)[p.HubProjectID] = true
	r.mu.Unlock()
	r.Nudge()
}

// observeOutput keeps a hub task's progress (todo.output and todo.event
// events of the runner's task log) for the next report.
func (r *Reporter) observeOutput(ev model.Event) {
	if ev.TodoID == "" || r.Store == nil {
		return
	}
	if t, err := r.Store.GetTodo(ev.TodoID); err != nil || t.HubTodoID == "" {
		return
	}
	r.mu.Lock()
	if r.live == nil {
		r.live = map[string]*liveOutput{}
	}
	l := r.live[ev.TodoID]
	if l == nil {
		l = &liveOutput{}
		r.live[ev.TodoID] = l
	}
	switch d := ev.Data.(type) {
	case string:
		l.line = d
	case model.RunEvent:
		if len(d.Text) > maxLiveText {
			d.Text = d.Text[:maxLiveText] + "…"
		}
		if len(d.Input) > maxLiveText {
			d.Input = nil
		}
		l.events = append(l.events, d)
		if n := len(l.events) - maxLiveEvents; n > 0 {
			l.events = append([]model.RunEvent(nil), l.events[n:]...)
		}
	}
	r.mu.Unlock()
	r.Nudge()
}

// takeLive hands over the output kept since the last report.
func (r *Reporter) takeLive() map[string]*liveOutput {
	r.mu.Lock()
	defer r.mu.Unlock()
	l := r.live
	r.live = nil
	return l
}

// takeProcs hands over the hub projects whose processes changed since the
// last report.
func (r *Reporter) takeProcs() []string { return r.take(&r.procs) }

// takeChecklists hands over the hub projects whose TODO.md changed since
// the last report.
func (r *Reporter) takeChecklists() []string { return r.take(&r.lists) }

func (r *Reporter) take(set *map[string]bool) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(*set))
	for id := range *set {
		out = append(out, id)
	}
	*set = nil
	sort.Strings(out)
	return out
}

// Build is the report as of now, with the tasks' output since the last
// Build.
func (r *Reporter) Build(now time.Time) Report {
	keep := r.Keep
	if keep <= 0 {
		keep = 24 * time.Hour
	}
	rep := Report{Parallel: r.Parallel, Terminals: r.Terminals, ChatCommands: r.ChatCommands}
	if r.Usage != nil {
		u := r.Usage.Snapshot()
		rep.Usage = &u
	}
	if r.Claude != nil {
		c := r.Claude.Status()
		rep.Claude = &c
	}
	live := r.takeLive()
	rep.Pushes = r.takePushes()
	if r.Checkouts != nil {
		c := r.Checkouts()
		if c == nil {
			c = []CheckoutState{}
		}
		rep.Checkouts = &c
	}
	if procs := r.takeProcs(); len(procs) > 0 {
		rep.Procs = procs
	}
	if lists := r.takeChecklists(); len(lists) > 0 {
		rep.Checklists = lists
	}
	todos := r.Store.ListTodos("")
	if r.Perms != nil {
		rep.Permissions = hubPermissions(r.Perms(), todos)
	}
	for _, t := range todos {
		if t.HubTodoID == "" {
			continue
		}
		done := t.Status == model.StatusMerged || t.Status == model.StatusFailed || t.Status == model.StatusReview
		if done && now.Sub(t.UpdatedAt) > keep {
			continue
		}
		s := StateOf(t)
		if l := live[t.ID]; l != nil {
			s.Output, s.Events = l.line, l.events
		}
		rep.Tasks = append(rep.Tasks, s)
	}
	return rep
}

// Bounds on the permission requests in a report, which the hub takes up to
// 4 MiB of: a request's input is cut down past maxPermInput.
const (
	maxPerms        = 50
	maxPermInput    = 64 << 10
	maxPermInputStr = 4000
)

// hubPermissions are the requests of reqs that the hub's tasks (in todos)
// wait on, with the hub's task ids, oldest first.
func hubPermissions(reqs []perm.Request, todos []model.Todo) []Permission {
	hub := map[string]string{}
	for _, t := range todos {
		if t.HubTodoID != "" {
			hub[t.ID] = t.HubTodoID
		}
	}
	var out []Permission
	for _, q := range reqs {
		htid := hub[q.TodoID]
		if htid == "" || q.Decision != "" {
			continue
		}
		out = append(out, Permission{ID: q.ID, HubTodoID: htid, Tool: q.Tool, Input: shortInput(q.Input), Created: q.Created})
		if len(out) == maxPerms {
			break
		}
	}
	return out
}

// shortInput is a tool call's input, its long strings cut when it is
// bigger than maxPermInput (an Edit of a large file): the board shows the
// command or path, not whole files.
func shortInput(in json.RawMessage) json.RawMessage {
	if len(in) <= maxPermInput {
		return in
	}
	var m map[string]any
	if json.Unmarshal(in, &m) != nil {
		return json.RawMessage("{}")
	}
	for k, v := range m {
		switch x := v.(type) {
		case string:
			if len(x) > maxPermInputStr {
				m[k] = x[:maxPermInputStr] + "…"
			}
		case map[string]any, []any:
			m[k] = "…"
		}
	}
	b, err := json.Marshal(m)
	if err != nil || len(b) > maxPermInput {
		return json.RawMessage("{}")
	}
	return b
}

// Run reports until ctx ends or Send returns ErrStop.
func (r *Reporter) Run(ctx context.Context) {
	r.init()
	every := r.Every
	if every <= 0 {
		every = 30 * time.Second
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
			// Let a burst of changes settle into one report.
			select {
			case <-ctx.Done():
				return
			case <-time.After(300 * time.Millisecond):
			}
		case <-tick.C:
		}
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		rep := r.Build(time.Now())
		err := r.Send(sctx, rep)
		cancel()
		if errors.Is(err, ErrStop) {
			return
		}
		if err != nil {
			r.unsent(rep.Pushes) // the next report tries again; the hub ignores news it had
		}
		if err != nil && ctx.Err() == nil {
			slog.Debug("runner: report to hub", "err", err) // the heartbeat tries again
		}
	}
}
