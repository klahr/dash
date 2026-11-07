package dispatch

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/klahr/dash/internal/model"
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
	// Send posts the report (tunnel.Report with the runner's credentials).
	Send func(ctx context.Context, r Report) error
	// Every is the heartbeat (default 30s).
	Every time.Duration
	// Keep is how long finished tasks are still reported (default a day),
	// so the hub hears of what finished while it couldn't be reached.
	Keep time.Duration

	wake chan struct{}
}

// ErrStop ends Run: Send returns it when the hub revoked the runner.
var ErrStop = errors.New("stop reporting")

func (r *Reporter) init() {
	if r.wake == nil {
		r.wake = make(chan struct{}, 1)
	}
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
	case model.EvUsage:
		r.Nudge()
	case model.EvTodoUpdated:
		if t, ok := ev.Data.(model.Todo); ok && t.HubTodoID != "" {
			r.Nudge()
		}
	}
}

// Build is the report as of now.
func (r *Reporter) Build(now time.Time) Report {
	keep := r.Keep
	if keep <= 0 {
		keep = 24 * time.Hour
	}
	rep := Report{Parallel: r.Parallel}
	if r.Usage != nil {
		u := r.Usage.Snapshot()
		rep.Usage = &u
	}
	for _, t := range r.Store.ListTodos("") {
		if t.HubTodoID == "" {
			continue
		}
		done := t.Status == model.StatusMerged || t.Status == model.StatusFailed || t.Status == model.StatusReview
		if done && now.Sub(t.UpdatedAt) > keep {
			continue
		}
		rep.Tasks = append(rep.Tasks, StateOf(t))
	}
	return rep
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
		err := r.Send(sctx, r.Build(time.Now()))
		cancel()
		if errors.Is(err, ErrStop) {
			return
		}
		if err != nil && ctx.Err() == nil {
			slog.Debug("runner: report to hub", "err", err) // the heartbeat tries again
		}
	}
}
