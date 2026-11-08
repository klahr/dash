package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// Fleet is the hub's runners (a *tunnel.Hub).
type Fleet interface {
	Get(id string) (tunnel.Runner, bool)
	Call(ctx context.Context, id, method, path string, in, out any) error
}

// DefaultGrace is how long a runner may be gone before the tasks it was
// running are failed: long enough for a network blip or a hub restart.
const DefaultGrace = 2 * time.Minute

// InterruptedPrefix starts the error of a task whose runner went away
// while it ran.
const InterruptedPrefix = "interrupted: "

// Dispatcher is the hub's side: it implements queue.Remote for the todo
// engine, applies runners' reports, and fails a runner's running tasks when
// it stays offline or is revoked.
type Dispatcher struct {
	Store *store.Store
	Fleet Fleet
	// Grace is how long a runner may be offline before its running tasks
	// fail (DefaultGrace if 0).
	Grace time.Duration
	// Kick tells the todo engine to look for work (optional).
	Kick func()
	// Note adds a line to a task's log (optional).
	Note func(t model.Todo, line string)
	// Pub streams a running task's output, as its runner reports it, to
	// the dashboards (optional).
	Pub interface{ Publish(model.Event) }

	mu     sync.Mutex
	timers map[string]*time.Timer // runner id → its offline grace
}

// WaitError is why no runner can take a task now; the task stays queued.
type WaitError struct{ Reason string }

func (e *WaitError) Error() string { return e.Reason }

// Dispatch sends t, a queued task of p, to one of p's runners and returns
// the runner's id and its id for the task. A *WaitError means none can take
// it now.
func (d *Dispatcher) Dispatch(ctx context.Context, p model.Project, t model.Todo) (runnerID, remoteID string, err error) {
	r, reason := d.pick(p, t)
	if r == nil {
		return "", "", &WaitError{Reason: reason}
	}
	if err := d.link(ctx, *r, p); err != nil {
		return "", "", &WaitError{Reason: "waiting for a runner: " + err.Error()}
	}
	task := Task{HubTodoID: t.ID, HubProjectID: p.ID, Title: t.Title, Prompt: t.Prompt, Feedback: t.Feedback,
		Fresh: t.Runner != r.ID}
	var acc Accepted
	if err := d.Fleet.Call(ctx, r.ID, http.MethodPost, "/api/hub/tasks", task, &acc); err != nil {
		return "", "", &WaitError{Reason: "waiting for a runner: " + err.Error()}
	}
	if acc.ID == "" {
		return "", "", &WaitError{Reason: "waiting for a runner: " + r.Name + " didn't take the task"}
	}
	if d.Note != nil {
		verb := "sent to runner "
		if acc.Resumed {
			verb = "continuing on runner "
		}
		d.Note(t, "→ "+verb+r.Name)
	}
	return r.ID, acc.ID, nil
}

// pick chooses the runner for t among p's, or says why there is none.
func (d *Dispatcher) pick(p model.Project, t model.Todo) (*tunnel.Runner, string) {
	var cands []tunnel.Runner
	var why []string
	for _, id := range p.Runners {
		r, ok := d.Fleet.Get(id)
		if !ok || r.OrgID() != p.OrgID {
			continue // revoked, or (never) another org's
		}
		switch busy := d.Busy(r.ID); {
		case !r.Online:
			why = append(why, r.Name+" is offline")
		case r.Paused:
			why = append(why, r.Name+" is paused")
		case r.Reported != nil && r.Reported.Usage != nil && r.Reported.Usage.Paused:
			msg := r.Name + " has paused its queue"
			if r.Reported.Usage.Reason != "" {
				msg += " (" + r.Reported.Usage.Reason + ")"
			}
			why = append(why, msg)
		case busy >= r.Limit():
			why = append(why, fmt.Sprintf("%s is busy (%d of %d tasks)", r.Name, busy, r.Limit()))
		default:
			cands = append(cands, r)
		}
	}
	if len(cands) == 0 {
		if len(why) == 0 {
			return nil, "no runner: the runners linked to this project were revoked; link another in the project's settings"
		}
		return nil, waitReason(why)
	}
	// The runner that has the task's worktree and session continues it.
	if i := slices.IndexFunc(cands, func(r tunnel.Runner) bool { return r.ID == t.Runner }); i >= 0 {
		return &cands[i], ""
	}
	best := slices.MinFunc(cands, func(a, b tunnel.Runner) int {
		return d.Busy(a.ID)*b.Limit() - d.Busy(b.ID)*a.Limit() // the least loaded, relative to its limit
	})
	return &best, ""
}

// Busy is how many tasks runner id is working on for the hub.
func (d *Dispatcher) Busy(id string) int {
	n := 0
	for _, t := range d.Store.ListTodos("", model.StatusInProgress) {
		if t.Runner == id {
			n++
		}
	}
	return n
}

var errSkip = errors.New("nothing to change")

// Report applies runner id's report (the body of its POST /runner/report).
// It changes only tasks the hub sent to that runner, in the runner's org.
func (d *Dispatcher) Report(id string, body []byte) error {
	r, ok := d.Fleet.Get(id)
	if !ok {
		return errors.New("unknown runner")
	}
	var rep Report
	if err := json.Unmarshal(body, &rep); err != nil {
		return fmt.Errorf("bad report: %w", err)
	}
	for _, ts := range rep.Tasks {
		d.apply(r, ts)
	}
	if d.Kick != nil {
		d.Kick() // a slot may have come free, or the usage limit lifted
	}
	return nil
}

// hubStatus is the hub's status for a task the runner reports in s: queued
// on the runner still counts as running there.
func hubStatus(s model.TodoStatus) model.TodoStatus {
	switch s {
	case model.StatusQueued, model.StatusInProgress:
		return model.StatusInProgress
	case model.StatusDraft:
		return model.StatusFailed // canceled on the runner
	}
	return s
}

func (d *Dispatcher) apply(r tunnel.Runner, ts TaskState) {
	org, ok := d.Store.ProjectOrg(projectOf(d.Store, ts.HubTodoID))
	if !ok || org != r.OrgID() {
		return
	}
	to := hubStatus(ts.Status)
	var ours model.Todo // the task, if it is this runner's
	_, err := d.Store.UpdateTodo(ts.HubTodoID, func(x *model.Todo) error {
		if x.Runner != r.ID || (x.RemoteID != "" && x.RemoteID != ts.ID) {
			return errSkip // sent elsewhere since, or never sent to this runner
		}
		ours = *x
		switch x.Status {
		case model.StatusQueued, model.StatusDraft, model.StatusMerged:
			return errSkip // waiting to be sent (again), or done
		case model.StatusFailed:
			// Back from the dead only if the hub, not the runner, failed it:
			// the runner kept going while it was out of reach.
			if to == model.StatusInProgress && !strings.HasPrefix(x.Error, InterruptedPrefix) {
				return errSkip
			}
		}
		if x.Status != to && !model.CanTransition(x.Status, to) {
			if to != model.StatusInProgress || !model.CanTransition(x.Status, model.StatusQueued) {
				return errSkip
			}
			// Running again on the runner (back after an interruption, or
			// sent back to Claude there): queued here, the hub sends it to
			// the same runner, which answers that it is on it.
			to = model.StatusQueued
		}
		before := *x
		x.Status = to
		x.Summary, x.CostUSD, x.Runs, x.Checks, x.PR = ts.Summary, ts.CostUSD, ts.Runs, ts.Checks, ts.PR
		x.DiffFiles, x.DiffAdd, x.DiffDel = ts.DiffFiles, ts.DiffAdd, ts.DiffDel
		x.RemoteID = ts.ID
		if ts.Branch != "" {
			name := ts.Branch[strings.LastIndex(ts.Branch, "/")+1:]
			x.Worktree = &model.Worktree{Name: name, Branch: ts.Branch} // no Path: the files are on the runner
		}
		x.Error, x.Resumable = "", false
		if to == model.StatusQueued {
			x.Resumable = true
		}
		if to == model.StatusFailed {
			x.Error, x.Resumable = ts.Error, ts.Session
			if ts.Status == model.StatusDraft {
				x.Error = "canceled on " + r.Name
			}
		}
		if before.Status != x.Status {
			now := time.Now()
			switch to {
			case model.StatusReview, model.StatusFailed, model.StatusMerged:
				x.FinishedAt = &now
			}
		}
		if reflect.DeepEqual(before, *x) {
			return errSkip
		}
		return nil
	})
	if err != nil && !errors.Is(err, errSkip) && !errors.Is(err, store.ErrNotFound) {
		slog.Warn("apply runner report", "runner", r.Name, "todo", ts.HubTodoID, "err", err)
	}
	if ours.ID != "" {
		d.publishOutput(ours, ts)
	}
}

// publishOutput streams what task t printed on its runner to the hub's
// dashboards, as todo.event and todo.output events like a task run here.
func (d *Dispatcher) publishOutput(t model.Todo, ts TaskState) {
	if d.Pub == nil {
		return
	}
	for _, ev := range ts.Events {
		d.Pub.Publish(model.Event{Type: model.EvTodoEvent, ProjectID: t.ProjectID, TodoID: t.ID, Data: ev, Time: ev.At})
	}
	if ts.Output != "" {
		d.Pub.Publish(model.Event{Type: model.EvTodoOutput, ProjectID: t.ProjectID, TodoID: t.ID, Data: ts.Output, Time: time.Now()})
	}
}

func projectOf(st *store.Store, todoID string) string {
	t, err := st.GetTodo(todoID)
	if err != nil {
		return ""
	}
	return t.ProjectID
}

// State is told when runner id comes online or goes offline. Gone for
// longer than Grace, its running tasks fail, ready to be queued again.
func (d *Dispatcher) State(id string, online bool) {
	d.mu.Lock()
	if d.timers == nil {
		d.timers = map[string]*time.Timer{}
	}
	if t := d.timers[id]; t != nil {
		t.Stop()
		delete(d.timers, id)
	}
	if !online {
		grace := d.Grace
		if grace <= 0 {
			grace = DefaultGrace
		}
		d.timers[id] = time.AfterFunc(grace, func() {
			d.mu.Lock()
			delete(d.timers, id)
			d.mu.Unlock()
			if r, ok := d.Fleet.Get(id); ok && r.Online {
				return
			}
			d.interrupt(id, "went offline mid-task (asleep or disconnected)", true)
		})
	}
	d.mu.Unlock()
	if online && d.Kick != nil {
		d.Kick()
	}
}

// Revoked is told when runner id is revoked: it is unlinked from every
// project and its running tasks fail.
func (d *Dispatcher) Revoked(id string) {
	d.mu.Lock()
	if t := d.timers[id]; t != nil {
		t.Stop()
		delete(d.timers, id)
	}
	d.mu.Unlock()
	for _, p := range d.Store.ListProjects() {
		if !slices.Contains(p.Runners, id) {
			continue
		}
		_, err := d.Store.UpdateProject(p.ID, func(x *model.Project) error {
			x.Runners = slices.DeleteFunc(slices.Clone(x.Runners), func(r string) bool { return r == id })
			return nil
		})
		if err != nil {
			slog.Error("unlink revoked runner", "project", p.ID, "err", err)
		}
	}
	d.interrupt(id, "its runner was revoked", false)
	if d.Kick != nil {
		d.Kick()
	}
}

// interrupt fails the tasks runner id is running.
func (d *Dispatcher) interrupt(id, why string, resumable bool) {
	name := id
	if r, ok := d.Fleet.Get(id); ok {
		name = r.Name
	}
	msg := InterruptedPrefix + why
	if resumable {
		msg = InterruptedPrefix + name + " " + why
	}
	for _, t := range d.Store.ListTodos("", model.StatusInProgress) {
		if t.Runner != id {
			continue
		}
		nt, err := d.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
			if x.Status != model.StatusInProgress || x.Runner != id {
				return errSkip
			}
			now := time.Now()
			x.Status, x.Error, x.FinishedAt = model.StatusFailed, msg, &now
			x.Resumable = resumable && x.RemoteID != ""
			if !resumable {
				x.Runner, x.RemoteID = "", "" // nothing to continue: start over elsewhere
			}
			return nil
		})
		if err == nil && d.Note != nil {
			d.Note(nt, "✖ "+msg)
		}
	}
}

// Start arms the offline grace of every runner that has tasks running but
// isn't connected (the hub just started): they fail unless it comes back.
func (d *Dispatcher) Start() {
	seen := map[string]bool{}
	for _, t := range d.Store.ListTodos("", model.StatusInProgress) {
		if t.Runner == "" || seen[t.Runner] {
			continue
		}
		seen[t.Runner] = true
		if r, ok := d.Fleet.Get(t.Runner); !ok {
			d.interrupt(t.Runner, "its runner was revoked", false)
		} else if !r.Online {
			d.State(r.ID, false)
		}
	}
}

// ---- what the API does with a task on a runner ----

func taskPath(t model.Todo, rest string) string {
	return "/api/hub/tasks/" + url.PathEscape(t.ID) + rest
}

// Approve asks t's runner to land it (through a pull request).
func (d *Dispatcher) Approve(ctx context.Context, t model.Todo) error {
	return d.Fleet.Call(ctx, t.Runner, http.MethodPost, taskPath(t, "/approve"), struct{}{}, nil)
}

// Cancel asks t's runner to stop it.
func (d *Dispatcher) Cancel(ctx context.Context, t model.Todo) error {
	return d.Fleet.Call(ctx, t.Runner, http.MethodPost, taskPath(t, "/cancel"), struct{}{}, nil)
}

// Forget tells t's runner the hub deleted it (best effort).
func (d *Dispatcher) Forget(ctx context.Context, t model.Todo) {
	if t.Runner == "" {
		return
	}
	if err := d.Fleet.Call(ctx, t.Runner, http.MethodDelete, taskPath(t, ""), nil, nil); err != nil {
		slog.Info("tell runner a task was deleted", "todo", t.ID, "err", err)
	}
}

// Fetch gets t's diff, log or events (what) from its runner.
func (d *Dispatcher) Fetch(ctx context.Context, t model.Todo, what string) ([]byte, error) {
	var b []byte
	err := d.Fleet.Call(ctx, t.Runner, http.MethodGet, taskPath(t, "/"+what), nil, &b)
	return b, err
}

// Unlink tells runners the hub no longer sends them a project's tasks, so
// they refuse any that still come (best effort: the hub stops sending them
// either way; an offline runner keeps its link until told).
func (d *Dispatcher) Unlink(ctx context.Context, projectID string, runners []string) {
	for _, id := range runners {
		if err := d.Fleet.Call(ctx, id, http.MethodDelete, "/api/hub/projects/"+url.PathEscape(projectID), nil, nil); err != nil {
			slog.Info("unlink project on runner", "runner", id, "project", projectID, "err", err)
		}
	}
}
