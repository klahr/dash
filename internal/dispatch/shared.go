package dispatch

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tunnel"
)

// StopOthersWork sends back to the queue the tasks runner id is running
// that are no longer its work (tunnel.Runner.WorksFor): other members'
// tasks on a runner that no longer takes them (its setting was turned off,
// or it now reports a subscription login). Each waits for another runner,
// saying why, and the runner is told to stop it at once, so it runs no
// further step of it. A shared runner that hasn't said yet how Claude signs
// in (it just restarted) keeps its tasks until it has.
func (d *Dispatcher) StopOthersWork(id string) {
	r, ok := d.Fleet.Get(id)
	if !ok || (r.Shared && r.Auth() == nil) {
		return
	}
	for _, t := range d.Store.ListTodos("", model.StatusInProgress) {
		if t.Runner != id {
			continue
		}
		ok, why := r.WorksFor(t.WorkFor())
		if ok {
			continue
		}
		wait := "waiting: " + why + ", so it stopped this task; it waits for another runner"
		nt, err := d.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
			if x.Status != model.StatusInProgress || x.Runner != id {
				return errSkip
			}
			x.Status, x.Waiting, x.Error = model.StatusQueued, wait, ""
			x.Runner, x.RemoteID, x.Resumable, x.Rejoin = "", "", false, false
			x.Worktree, x.Checks, x.PR = nil, nil, nil
			return nil
		})
		if err != nil {
			continue
		}
		slog.Warn("runner: stopped another member's task it no longer takes", "runner", r.Name, "todo", t.ID, "why", why)
		if d.Note != nil {
			d.Note(nt, "⏸ "+wait)
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := d.Cancel(ctx, t); err != nil { // t still names the runner
				slog.Info("tell runner to stop a task it no longer takes", "runner", r.Name, "todo", t.ID, "err", err)
			}
		}()
	}
	if d.Kick != nil {
		d.Kick()
	}
}

// sharer is a Fleet that settles runners' Take other members' tasks (a
// *tunnel.Hub).
type sharer interface {
	MigrateShared(id string, othersWork func(tunnel.Runner) bool) (settled, on bool, err error)
	Unsettled() []string
}

// MigrateShared is the move to runners that do only their owner's work,
// when the hub starts: it logs the runners registered before, whose
// setting Take other members' tasks is settled once each says how Claude
// signs in (migrateShared, from its reports).
func (d *Dispatcher) MigrateShared() {
	sh, ok := d.Fleet.(sharer)
	if !ok {
		return
	}
	for _, id := range sh.Unsettled() {
		if r, ok := d.Fleet.Get(id); ok {
			slog.Info("runner: takes only its owner's tasks until it reports how Claude signs in; with an API key and no subscription login it keeps taking other members' tasks",
				"runner", r.Name)
		}
	}
}

// migrateShared settles, once, whether runner r, registered before runners
// did only their owner's work, takes other members' tasks: on if it runs
// Claude with an API key alone and other members' tasks went to it, off
// otherwise (their tasks then wait, saying why). See tunnel.Hub.MigrateShared.
func (d *Dispatcher) migrateShared(r tunnel.Runner) {
	sh, ok := d.Fleet.(sharer)
	if !ok || r.Auth() == nil {
		return
	}
	settled, on, err := sh.MigrateShared(r.ID, d.othersWork)
	switch {
	case err != nil:
		slog.Error("runner: settle Take other members' tasks", "runner", r.Name, "err", err)
	case !settled:
	case on:
		slog.Info("runner: now takes other members' tasks (Take other members' tasks on): it runs Claude with an API key and other members' tasks ran on it",
			"runner", r.Name)
	case d.othersWork(r):
		slog.Warn("runner: now takes only its owner's tasks: other members' tasks wait for one of their own runners, or a shared one with an API key",
			"runner", r.Name, "signs_in_with", r.Auth().Plan(), "why_not_shared", r.Auth().NotShared())
	default:
		slog.Info("runner: takes only its owner's tasks", "runner", r.Name)
	}
}

// othersWork reports whether tasks of other members than r's owner, in
// projects r works on, are on the hub: work that went to it before runners
// did only their owner's.
func (d *Dispatcher) othersWork(r tunnel.Runner) bool {
	owner := r.OwnerID()
	for _, p := range d.Store.ListProjects() {
		if !r.Serves(p) {
			continue
		}
		for _, t := range d.Store.ListTodos(p.ID) {
			if slices.ContainsFunc(t.WorkFor(), func(u int64) bool { return u != owner }) {
				return true
			}
		}
	}
	return false
}
