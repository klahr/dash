package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/checklist"
	"github.com/klahr/dash/internal/disk"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/queue"
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
	// Undo queues a task for Claude to undo merged task t by hand, as its
	// runner couldn't revert it without conflicts in files (optional).
	Undo func(t model.Todo, files []string)
	// Pacer paces the runners per Claude account (optional: without it,
	// each runner paces itself, as its own setting says).
	Pacer *Pacer

	mu     sync.Mutex
	timers map[string]*time.Timer // runner id → its offline grace

	originMu  sync.Mutex
	origin    map[[2]string]string                 // {hub project id, branch} → the last sha passed on
	checkouts map[string]map[string]model.Checkout // hub project id → runner id → its checkout behind origin
	fanouts   sync.WaitGroup                       // OriginMoved calls in flight (tests)

	permMu sync.Mutex
	perms  map[string]map[string]perm.Request // runner id → request id → its pending request, with the hub's ids
	// answered are requests the hub answered lately: a report the runner
	// built before the answer reached it doesn't bring them back.
	answered map[string]time.Time

	diskMu  sync.Mutex
	diskLow map[string]bool // runner id → its disk space is low
}

// answeredFor is how long the hub ignores a request it answered.
const answeredFor = time.Minute

// WaitError is why no runner can take a task now; the task stays queued.
type WaitError struct{ Reason string }

func (e *WaitError) Error() string { return e.Reason }

// Dispatch sends t, a queued task of p, to one of p's runners and returns
// the runner's id and its id for the task. A *WaitError means none can take
// it now.
func (d *Dispatcher) Dispatch(ctx context.Context, p model.Project, t model.Todo) (runnerID, remoteID string, err error) {
	var parent *model.Todo // the task t builds on, when it has one
	if t.BuildsOn != "" && d.Store != nil {
		if x, err := d.Store.GetTodo(t.BuildsOn); err == nil {
			parent = &x
		}
	}
	r, reason, full := d.pick(p, t, parent)
	if r == nil {
		// The task log says why a runner that is too full doesn't start
		// it, once per reason, rather than the task failing halfway.
		if full != "" && d.Note != nil && reason != t.Waiting {
			d.Note(t, "⏸ "+full)
		}
		return "", "", &WaitError{Reason: reason}
	}
	if err := d.link(ctx, *r, p); err != nil {
		return "", "", &WaitError{Reason: "waiting for a runner: " + err.Error()}
	}
	ds := r.Disk
	task := Task{HubTodoID: t.ID, HubProjectID: p.ID, Title: t.Title, Prompt: t.Prompt, Model: t.Model, Feedback: t.Feedback,
		Fresh: t.Runner != r.ID, Disk: &ds, StartNow: t.StartNow, Rejoin: t.Rejoin && t.Runner == r.ID, TakesDone: true}
	if parent != nil {
		task.BuildsOn = parent.ID
		if t.Runner == "" && parent.Runner != r.ID && d.Note != nil {
			why := "ran on another runner, which can't take this one now"
			if parent.Runner == "" {
				why = "has no Claude session on a runner"
			}
			d.Note(t, "⚠ the task this builds on "+why+": this one starts a fresh Claude session with the full task")
		}
	}
	var acc Accepted
	if err := d.Fleet.Call(ctx, r.ID, http.MethodPost, "/api/hub/tasks", task, &acc); err != nil {
		reason := "waiting for a runner: " + err.Error()
		var ce *tunnel.CallError
		if errors.As(err, &ce) && ce.Status == http.StatusBadRequest && strings.HasSuffix(ce.Msg, AlreadyMerged) && !task.Fresh {
			// An older runner: it merged the task while the hub had it
			// queued. It doesn't say the commit; its next report may.
			ts := StateOf(t) // the hub's own news of it, as the runner says nothing more
			ts.HubTodoID, ts.ID, ts.Status, ts.Error = t.ID, t.RemoteID, model.StatusMerged, ""
			return "", "", d.settle(*r, t, ts)
		}
		if errors.As(err, &ce) && ce.Status == http.StatusInsufficientStorage && d.Note != nil && reason != t.Waiting {
			d.Note(t, "⏸ "+err.Error()) // it filled up since its last report
		}
		return "", "", &WaitError{Reason: reason}
	}
	if acc.ID == "" {
		return "", "", &WaitError{Reason: "waiting for a runner: " + r.Name + " didn't take the task"}
	}
	if acc.Done != nil && !task.Fresh {
		ts := *acc.Done
		ts.HubTodoID = t.ID
		if ts.ID == "" {
			ts.ID = acc.ID
		}
		return "", "", d.settle(*r, t, ts)
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

// pick chooses the runner for t among p's, or says why there is none (and,
// in full, why runners with too little disk space don't start it). parent
// is the task t builds on (nil: none): a task starting out prefers the
// runner that has its Claude session.
func (d *Dispatcher) pick(p model.Project, t model.Todo, parent *model.Todo) (*tunnel.Runner, string, string) {
	var cands []tunnel.Runner
	var why, full []string
	limits := map[string]int{} // runner id → how many tasks it may have at once now
	limit := func(r tunnel.Runner) int {
		if n, ok := limits[r.ID]; ok {
			return n
		}
		return r.Limit()
	}
	for _, id := range p.Runners {
		r, ok := d.Fleet.Get(id)
		if !ok || r.OrgID() != p.OrgID {
			continue // revoked, or (never) another org's
		}
		var pace GroupPace
		if d.Pacer != nil && active(r) {
			pace = d.Pacer.For(r)
			if n := pace.Slots[r.ID]; n > 0 {
				limits[r.ID] = n
			}
		}
		switch busy := d.Busy(r.ID); {
		case !r.Online:
			why = append(why, r.Name+" is offline")
		case r.Update != nil:
			why = append(why, r.Name+" is updating")
		case r.Paused:
			why = append(why, r.Name+" is paused")
		case starting(r):
			// Its account, and so its pace, isn't known yet: it reports them
			// in a moment.
			why = append(why, r.Name+" is starting up")
		case r.Reported != nil && r.Reported.Usage != nil && r.Reported.Usage.Paused && !(t.StartNow && r.Reported.Usage.PaceOnly()):
			msg := r.Name + " has paused its queue"
			if r.Reported.Usage.Reason != "" {
				msg += " (" + r.Reported.Usage.Reason + ")"
			}
			why = append(why, msg)
		case r.DiskFull:
			msg := tooFull(r)
			why, full = append(why, msg), append(full, msg)
		case pace.Stop != "":
			// Its account's: every runner on it waits.
			why = append(why, r.Name+"'s Claude account is paused ("+pace.Stop+")")
		case pace.Waiting() && !t.StartNow: // Start now: only the pace holds it back
			why = append(why, r.Name+" is "+lowerFirst(pace.Pace.Reason()))
		case busy >= limit(r):
			why = append(why, fmt.Sprintf("%s is busy (%d of %d tasks)", r.Name, busy, limit(r)))
		default:
			cands = append(cands, r)
		}
	}
	if len(cands) == 0 {
		if len(why) == 0 {
			return nil, "no runner: the runners linked to this project were revoked; link another in the project's settings", ""
		}
		return nil, waitReason(why), strings.Join(full, "; ")
	}
	// The runner that has the task's worktree and session continues it.
	if i := slices.IndexFunc(cands, func(r tunnel.Runner) bool { return r.ID == t.Runner }); i >= 0 {
		return &cands[i], "", ""
	}
	if t.Runner == "" && parent != nil && parent.Runner != "" {
		if i := slices.IndexFunc(cands, func(r tunnel.Runner) bool { return r.ID == parent.Runner }); i >= 0 {
			return &cands[i], "", ""
		}
	}
	best := slices.MinFunc(cands, func(a, b tunnel.Runner) int {
		return d.Busy(a.ID)*limit(b) - d.Busy(b.ID)*limit(a) // the least loaded, relative to its limit
	})
	return &best, "", ""
}

// PaceWait is why queued task t of p waits for the weekly pace (the
// runners' reasons), or "" when the pace isn't what holds it back: no
// runner takes it now, and one would if the user started it now (see
// model.Todo.StartNow). t's own StartNow doesn't count.
func (d *Dispatcher) PaceWait(p model.Project, t model.Todo) string {
	t.StartNow = false
	r, reason, _ := d.pick(p, t, nil)
	if r != nil {
		return ""
	}
	t.StartNow = true
	if r, _, _ := d.pick(p, t, nil); r == nil {
		return ""
	}
	return reason
}

// starting reports whether runner r just started and hasn't yet asked
// Claude Code which account it is on, so the hub can't pace it with its
// account (and the hub has no earlier report of it to go by: see
// tunnel.Hub.ServeReport). An older runner, which doesn't report Claude
// Code at all, never is.
func starting(r tunnel.Runner) bool {
	if r.Reported == nil || r.Reported.Claude == nil {
		return false
	}
	c := r.Reported.Claude
	return c.CheckedAt.IsZero() && c.Version == "" && c.Account == nil
}

// tooFull says why runner r, below its minimum free disk space, doesn't
// start a task: "runner X has only 1.2 GB free …".
func tooFull(r tunnel.Runner) string {
	if r.Reported != nil && r.Reported.Disk != nil {
		if v, ok := r.Disk.TooFull(*r.Reported.Disk); ok {
			return disk.FullMessage(r.Name, v, r.Disk.MinFree())
		}
	}
	return "runner " + r.Name + " is low on disk space"
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// Reply is the hub's answer to runner id's report: how it paces it
// (without a Pacer: it paces itself), and its disk settings.
func (d *Dispatcher) Reply(id string) any {
	r, ok := d.Fleet.Get(id)
	if !ok {
		return nil
	}
	s := r.Disk
	reply := Reply{Disk: &s}
	if d.Pacer != nil {
		p := d.Pacer.For(r).RunnerPace(r)
		reply.Pace = &p
	}
	return reply
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

// Working is how many of the hub's tasks runner id is running or merging:
// it updates itself only at 0 (tunnel.Hub.RunUpdates).
func (d *Dispatcher) Working(id string) int {
	n := 0
	for _, t := range d.Store.ListTodos("", model.StatusInProgress, model.StatusMerging) {
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
	for _, p := range rep.Pushes {
		d.pushed(r, p)
	}
	if rep.Checkouts != nil {
		d.setCheckouts(r, *rep.Checkouts)
	}
	for _, pid := range rep.Procs {
		d.procsChanged(r, pid)
	}
	for _, pid := range rep.Checklists {
		d.projectEvent(r, pid, model.EvChecklist, map[string]string{"path": checklist.Name})
	}
	d.setPermissions(r, rep.Permissions)
	if rep.Disk != nil {
		if fresh, ok := d.Fleet.Get(id); ok {
			d.checkDisk(fresh)
		}
	}
	if d.Kick != nil {
		d.Kick() // a slot may have come free, or the usage limit lifted
	}
	return nil
}

// pushed passes runner r's push on to the project's other online runners,
// so they fetch it at once rather than at their next poll. News about a
// project r isn't linked to (or of another org) is dropped, and so is news
// the hub passed on already: runners resend pushes when a report failed.
func (d *Dispatcher) pushed(r tunnel.Runner, push Push) {
	if push.HubProjectID == "" || push.Branch == "" || !gitops.IsSHA(push.SHA) {
		return
	}
	p, err := d.Store.GetProject(push.HubProjectID)
	if err != nil || p.OrgID != r.OrgID() || !slices.Contains(p.Runners, r.ID) {
		return
	}
	key := [2]string{p.ID, push.Branch}
	d.originMu.Lock()
	if d.origin == nil {
		d.origin = map[[2]string]string{}
	}
	if d.origin[key] == push.SHA {
		d.originMu.Unlock()
		return
	}
	d.origin[key] = push.SHA
	d.originMu.Unlock()
	msg := OriginMoved{Branch: push.Branch, SHA: push.SHA}
	path := strings.Replace(OriginMovedPath, "{hpid}", url.PathEscape(p.ID), 1)
	for _, id := range p.Runners {
		if id == r.ID {
			continue // it knows
		}
		o, ok := d.Fleet.Get(id)
		if !ok || !o.Online || o.OrgID() != p.OrgID {
			continue // an offline runner fetches when it comes back
		}
		d.fanouts.Add(1)
		go func() {
			defer d.fanouts.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := d.Fleet.Call(ctx, id, http.MethodPost, path, msg, nil); err != nil {
				slog.Info("tell runner origin moved", "runner", o.Name, "project", p.Name, "err", err)
			}
		}()
	}
}

// setCheckouts replaces what the hub knows of runner r's checkouts behind
// origin, and tells the dashboards of each project whose list changed.
func (d *Dispatcher) setCheckouts(r tunnel.Runner, states []CheckoutState) {
	now := time.Now()
	news := map[string]bool{}
	d.originMu.Lock()
	if d.checkouts == nil {
		d.checkouts = map[string]map[string]model.Checkout{}
	}
	seen := map[string]bool{}
	for _, cs := range states {
		if cs.Behind <= 0 || seen[cs.HubProjectID] {
			continue
		}
		p, err := d.Store.GetProject(cs.HubProjectID)
		if err != nil || p.OrgID != r.OrgID() || !slices.Contains(p.Runners, r.ID) {
			continue
		}
		seen[p.ID] = true
		c := model.Checkout{Runner: r.ID, RunnerName: r.Name, Branch: cs.Branch, Behind: cs.Behind, Ahead: cs.Ahead, Stuck: cs.Stuck,
			DashOwned: cs.DashOwned, Since: now}
		byRunner := d.checkouts[p.ID]
		if byRunner == nil {
			byRunner = map[string]model.Checkout{}
			d.checkouts[p.ID] = byRunner
		}
		if old, ok := byRunner[r.ID]; ok {
			c.Since = old.Since
			if old == c {
				continue
			}
		}
		byRunner[r.ID] = c
		news[p.ID] = true
	}
	for pid, byRunner := range d.checkouts {
		if _, ok := byRunner[r.ID]; ok && !seen[pid] {
			delete(byRunner, r.ID)
			news[pid] = true
		}
	}
	d.originMu.Unlock()
	for pid := range news {
		d.publishCheckouts(pid)
	}
}

// forgetCheckouts drops runner id's checkouts (it was revoked).
func (d *Dispatcher) forgetCheckouts(id string) {
	var news []string
	d.originMu.Lock()
	for pid, byRunner := range d.checkouts {
		if _, ok := byRunner[id]; ok {
			delete(byRunner, id)
			news = append(news, pid)
		}
	}
	d.originMu.Unlock()
	for _, pid := range news {
		d.publishCheckouts(pid)
	}
}

// Checkouts lists the runners whose checkout of the hub's project
// projectID is behind origin, as they last reported, by runner name.
func (d *Dispatcher) Checkouts(projectID string) []model.Checkout {
	d.originMu.Lock()
	defer d.originMu.Unlock()
	out := []model.Checkout{}
	for id, c := range d.checkouts[projectID] {
		if r, ok := d.Fleet.Get(id); ok {
			c.RunnerName = r.Name
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b model.Checkout) int { return strings.Compare(a.RunnerName, b.RunnerName) })
	return out
}

func (d *Dispatcher) publishCheckouts(projectID string) {
	if d.Pub == nil {
		return
	}
	d.Pub.Publish(model.Event{Type: model.EvCheckouts, ProjectID: projectID, Data: d.Checkouts(projectID), Time: time.Now()})
}

// procsChanged tells the dashboards that the processes of project pid on
// runner r changed, so they list them again (the hub lists them from its
// runners, see api's listProcessesOnRunners). Only for a project of r's org
// linked to r.
func (d *Dispatcher) procsChanged(r tunnel.Runner, pid string) {
	d.projectEvent(r, pid, model.EvProcUpdated, nil)
}

// projectEvent publishes an event of type t about hub project pid, which
// runner r reported news of, if r is one of its runners.
func (d *Dispatcher) projectEvent(r tunnel.Runner, pid string, t model.EventType, data any) {
	if d.Pub == nil {
		return
	}
	p, err := d.Store.GetProject(pid)
	if err != nil || p.OrgID != r.OrgID() || !slices.Contains(p.Runners, r.ID) {
		return
	}
	d.Pub.Publish(model.Event{Type: t, ProjectID: p.ID, Data: data, Time: time.Now()})
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

// safeLinks drops the links in ts that aren't pages on GitHub: the runner
// isn't trusted, and the hub's dashboard shows them (see
// model.IsGitHubWebURL).
func safeLinks(ts *TaskState) {
	if ts.PR != nil {
		pr := *ts.PR
		pr.URL = model.GitHubWebURL(pr.URL)
		pr.Checks = slices.Clone(pr.Checks)
		for i := range pr.Checks {
			pr.Checks[i].URL = model.GitHubWebURL(pr.Checks[i].URL)
		}
		ts.PR = &pr
	}
	if ts.Revert != nil {
		rv := *ts.Revert
		rv.PRURL = model.GitHubWebURL(rv.PRURL)
		ts.Revert = &rv
	}
}

// apply applies runner r's report about one of the hub's tasks.
func (d *Dispatcher) apply(r tunnel.Runner, ts TaskState) {
	_ = d.applyTask(r, ts, false)
}

// applyTask applies ts, what runner r says of a hub's task, and returns
// nil if the task changed. answer: ts is r's answer to the hub sending it
// the task again (see settle), not a report that may be stale.
func (d *Dispatcher) applyTask(r tunnel.Runner, ts TaskState, answer bool) error {
	safeLinks(&ts)
	p, err := d.Store.GetProject(projectOf(d.Store, ts.HubTodoID))
	if err != nil || p.OrgID != r.OrgID() {
		return errSkip
	}
	to := hubStatus(ts.Status)
	var ours model.Todo // the task, if it is this runner's
	undo := false       // its revert conflicted just now
	adopted := false    // taken by the runner before the hub recorded it
	nt, err := d.Store.UpdateTodo(ts.HubTodoID, func(x *model.Todo) error {
		adopt := adopts(x, p, r, to)
		adopted, undo = false, false
		if adopt {
			now := time.Now()
			x.Runner, x.Waiting, x.Paused = r.ID, "", false
			x.StartedAt, x.FinishedAt = &now, nil
		}
		if x.Runner != r.ID || (x.RemoteID != "" && x.RemoteID != ts.ID) {
			return errSkip // sent elsewhere since, or never sent to this runner
		}
		ours = *x
		switch x.Status {
		case model.StatusQueued, model.StatusDraft:
			switch {
			case adopt:
				adopted = true
			case x.Status == model.StatusQueued && finishedThere(ts.Status, x.Rejoin || answer):
				// Its runner finished it while the hub had it queued to send
				// back (it lost touch with the runner for a moment).
			default:
				return errSkip // waiting to be sent (again)
			}
		case model.StatusMerged:
			var err error
			undo, err = applyMerged(x, ts)
			return err
		case model.StatusFailed:
			// Back from the dead only if the hub, not the runner, failed it:
			// the runner kept going while it was out of reach.
			if to == model.StatusInProgress && !strings.HasPrefix(x.Error, InterruptedPrefix) {
				return errSkip
			}
		}
		next, ok := nextStatus(*x, to)
		if !ok {
			return errSkip
		}
		before := *x
		copyReport(x, ts, next, r.Name)
		if reflect.DeepEqual(before, *x) {
			return errSkip
		}
		return nil
	})
	if err != nil && !errors.Is(err, errSkip) && !errors.Is(err, store.ErrNotFound) {
		slog.Warn("apply runner report", "runner", r.Name, "todo", ts.HubTodoID, "err", err)
	}
	if adopted && err == nil && d.Note != nil {
		d.Note(nt, "→ running on runner "+r.Name+" (it reported the task before the hub recorded sending it)")
	}
	if ours.ID != "" {
		d.publishOutput(ours, ts)
	}
	if undo && d.Undo != nil {
		if t, err := d.Store.GetTodo(ts.HubTodoID); err == nil {
			d.Undo(t, ts.RevertConflict)
		}
	}
	return err
}

// adopts reports whether runner r, saying task x is now in status to (the
// hub's), takes it over: a task the runner took but the hub never recorded
// as sent (the hub stopped between the two) is the runner's, as sending it
// again would run it twice, on another runner or from scratch on this one.
func adopts(x *model.Todo, p model.Project, r tunnel.Runner, to model.TodoStatus) bool {
	return x.Status == model.StatusQueued && x.Runner == "" && x.RemoteID == "" &&
		to == model.StatusInProgress && slices.Contains(p.Runners, r.ID)
}

// applyMerged applies ts to x, a merged task: it is done, and only a revert
// (or its conflict) may follow. undo: the revert conflicted just now. It
// returns errSkip if x didn't change.
func applyMerged(x *model.Todo, ts TaskState) (undo bool, err error) {
	if ts.Status != model.StatusMerged {
		return false, errSkip
	}
	before := *x
	if x.MergedCommit == "" {
		x.MergedCommit = ts.MergedCommit
	}
	if ts.Revert != nil {
		x.Revert, x.RevertConflict, x.Error = ts.Revert, nil, ""
	}
	if len(ts.RevertConflict) > 0 && len(x.RevertConflict) == 0 {
		x.RevertConflict, undo = ts.RevertConflict, true
	}
	if ts.Error != "" && ts.Revert == nil && !undo && len(x.RevertConflict) == 0 {
		x.Error = ts.Error // e.g. "Revert failed: …"
	}
	if reflect.DeepEqual(before, *x) {
		return undo, errSkip
	}
	return undo, nil
}

// nextStatus returns the status task x moves to when its runner reports
// status to (the hub's), and false if it can't move there.
func nextStatus(x model.Todo, to model.TodoStatus) (model.TodoStatus, bool) {
	if x.Status == to || model.CanTransition(x.Status, to) {
		return to, true
	}
	if to == model.StatusInProgress && model.CanTransition(x.Status, model.StatusQueued) {
		// Running again on the runner (back after an interruption, or sent
		// back to Claude there): queued here, the hub sends it to the same
		// runner, which answers that it is on it.
		return model.StatusQueued, true
	}
	return "", false
}

// copyReport moves x to status to and copies onto it what its runner,
// runnerName, reports in ts.
func copyReport(x *model.Todo, ts TaskState, to model.TodoStatus, runnerName string) {
	from := x.Status
	x.Status = to
	x.Summary, x.CostUSD, x.Runs, x.Checks, x.PR = ts.Summary, ts.CostUSD, ts.Runs, ts.Checks, ts.PR
	x.RunStats = ts.RunStats
	x.DiffFiles, x.DiffAdd, x.DiffDel = ts.DiffFiles, ts.DiffAdd, ts.DiffDel
	x.RemoteID = ts.ID
	if to == model.StatusMerged {
		x.MergedCommit = ts.MergedCommit
	}
	if ts.Branch != "" {
		name := ts.Branch[strings.LastIndex(ts.Branch, "/")+1:]
		x.Worktree = &model.Worktree{Name: name, Branch: ts.Branch} // no Path: the files are on the runner
	}
	x.Error, x.Resumable = "", false
	if to == model.StatusQueued {
		x.Resumable, x.Rejoin = true, true
	} else {
		x.Waiting = "" // e.g. finished on the runner while queued here
	}
	switch to {
	case model.StatusReview:
		x.Error = ts.Error // e.g. why an approved task wasn't merged after all
	case model.StatusFailed:
		x.Error, x.Resumable = ts.Error, ts.Session
		if ts.Status == model.StatusDraft {
			x.Error = "canceled on " + runnerName
		}
	}
	if from != to {
		switch to {
		case model.StatusReview, model.StatusFailed, model.StatusMerged:
			now := time.Now()
			x.FinishedAt = &now
		}
	}
}

// finishedThere reports whether a runner's news that a task the hub has
// queued is in status s (the runner's) moves it on: merged always (it is
// final, and the runner has it), and review, merging, failed or canceled
// when trusted, i.e. the hub queued it only to send it back to the runner
// that kept running it (model.Todo.Rejoin), or the runner says so in
// answer to the hub sending it. Otherwise it is old news about a task sent
// back to Claude or queued again on the hub.
func finishedThere(s model.TodoStatus, trusted bool) bool {
	switch s {
	case model.StatusMerged:
		return true
	case model.StatusReview, model.StatusMerging, model.StatusFailed, model.StatusDraft:
		return trusted
	}
	return false
}

// settle records ts, runner r's answer to the hub sending it queued task t
// again: it has finished t already (merged it, most often). It returns
// queue.ErrSettled once the task moved on, so it isn't sent again.
func (d *Dispatcher) settle(r tunnel.Runner, t model.Todo, ts TaskState) error {
	if err := d.applyTask(r, ts, true); err != nil {
		slog.Warn("record a task its runner finished", "runner", r.Name, "todo", t.ID, "status", ts.Status, "err", err)
		return &WaitError{Reason: "waiting for a runner: " + r.Name + " has this task " + string(ts.Status) + " already, which the hub couldn't record"}
	}
	if d.Note != nil {
		line := "✔ " + r.Name + " had this task " + string(hubStatus(ts.Status)) + " already"
		if ts.Status == model.StatusMerged && ts.MergedCommit != "" {
			line += " (commit " + shortSHA(ts.MergedCommit) + ")"
		}
		if nt, err := d.Store.GetTodo(t.ID); err == nil {
			d.Note(nt, line)
		}
	}
	return queue.ErrSettled
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
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
		// Its requests can't be answered while it is gone; it reports
		// those still waiting when it is back.
		d.setPermissions(tunnel.Runner{ID: id}, nil)
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
	d.forgetDisk(id)
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
	d.forgetCheckouts(id)
	d.setPermissions(tunnel.Runner{ID: id}, nil)
	if d.Kick != nil {
		d.Kick()
	}
}

// interrupt fails the tasks runner id is running or merging. A merge the
// runner finishes after all still counts: its report moves the task on
// (see apply), and approving it again once the runner is back retries it.
func (d *Dispatcher) interrupt(id, why string, resumable bool) {
	name := id
	if r, ok := d.Fleet.Get(id); ok {
		name = r.Name
	}
	msg := InterruptedPrefix + why
	if resumable {
		msg = InterruptedPrefix + name + " " + why
	}
	for _, t := range d.Store.ListTodos("", model.StatusInProgress, model.StatusMerging) {
		if t.Runner != id {
			continue
		}
		nt, err := d.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
			if (x.Status != model.StatusInProgress && x.Status != model.StatusMerging) || x.Runner != id {
				return errSkip
			}
			merging := x.Status == model.StatusMerging
			now := time.Now()
			x.Status, x.Error, x.FinishedAt = model.StatusFailed, msg, &now
			// Claude's work is done in a merging task: there is no session
			// to continue, only the merge to approve again.
			x.Resumable = resumable && x.RemoteID != "" && !merging
			if merging && resumable {
				x.Error += " while merging; approve it again once it is back"
			}
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

// Start arms the offline grace of every runner that has tasks running or
// merging but isn't connected (the hub just started): they fail unless it
// comes back.
func (d *Dispatcher) Start() {
	seen := map[string]bool{}
	for _, t := range d.Store.ListTodos("", model.StatusInProgress, model.StatusMerging) {
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

// ---- permission requests of tasks on runners ----

// permGone is the Decision of a runner's request that the hub stops
// showing without having answered it: it was answered on the runner, its
// task ended, or the runner went away.
const permGone = "gone"

// setPermissions replaces the permission requests runner r reported as
// waiting, keeping those of tasks the hub sent r, in r's org, and tells the
// dashboards of each request that came or went.
func (d *Dispatcher) setPermissions(r tunnel.Runner, ps []Permission) {
	d.permMu.Lock()
	for id, at := range d.answered {
		if time.Since(at) > answeredFor {
			delete(d.answered, id)
		}
	}
	answered := maps.Clone(d.answered)
	d.permMu.Unlock()
	now := map[string]perm.Request{}
	for _, p := range ps {
		if p.ID == "" || len(now) >= 200 {
			continue
		}
		if _, ok := answered[p.ID]; ok {
			continue
		}
		t, err := d.Store.GetTodo(p.HubTodoID)
		if err != nil || t.Runner != r.ID || t.Status != model.StatusInProgress {
			continue
		}
		pr, err := d.Store.GetProject(t.ProjectID)
		if err != nil || pr.OrgID != r.OrgID() {
			continue
		}
		input := p.Input
		if !json.Valid(input) {
			input = json.RawMessage("{}")
		}
		now[p.ID] = perm.Request{ID: p.ID, TodoID: t.ID, ProjectID: t.ProjectID, OrgID: pr.OrgID,
			Tool: p.Tool, Input: input, Created: p.Created}
	}
	d.permMu.Lock()
	if d.perms == nil {
		d.perms = map[string]map[string]perm.Request{}
	}
	was := d.perms[r.ID]
	if len(now) == 0 {
		delete(d.perms, r.ID)
	} else {
		d.perms[r.ID] = now
	}
	d.permMu.Unlock()
	for id, q := range was {
		if _, ok := now[id]; !ok {
			q.Decision = permGone
			d.publishPermission(q)
		}
	}
	for id, q := range now {
		if _, ok := was[id]; !ok {
			d.publishPermission(q)
		}
	}
}

func (d *Dispatcher) publishPermission(q perm.Request) {
	if d.Pub != nil {
		d.Pub.Publish(model.Event{Type: model.EvPermission, ProjectID: q.ProjectID, TodoID: q.TodoID, Data: q, Time: time.Now()})
	}
}

// Permissions lists the permission requests waiting on runners, with the
// hub's task and project ids, optionally for one project, oldest first.
func (d *Dispatcher) Permissions(projectID string) []perm.Request {
	d.permMu.Lock()
	defer d.permMu.Unlock()
	out := []perm.Request{}
	for _, m := range d.perms {
		for _, q := range m {
			if projectID == "" || q.ProjectID == projectID {
				out = append(out, q)
			}
		}
	}
	slices.SortFunc(out, func(a, b perm.Request) int { return a.Created.Compare(b.Created) })
	return out
}

// Permission is the permission request id waiting on a runner.
func (d *Dispatcher) Permission(id string) (perm.Request, bool) {
	q, _, ok := d.permission(id)
	return q, ok
}

func (d *Dispatcher) permission(id string) (perm.Request, string, bool) {
	d.permMu.Lock()
	defer d.permMu.Unlock()
	for rid, m := range d.perms {
		if q, ok := m[id]; ok {
			return q, rid, true
		}
	}
	return perm.Request{}, "", false
}

// DecidePermission answers the permission request id waiting on a runner
// (allow, always or deny), on that runner. It is perm.ErrNotFound when no
// runner waits on id.
func (d *Dispatcher) DecidePermission(ctx context.Context, id, decision, message string) error {
	q, rid, ok := d.permission(id)
	if !ok {
		return perm.ErrNotFound
	}
	path := strings.NewReplacer("{htid}", url.PathEscape(q.TodoID), "{id}", url.PathEscape(id)).Replace(PermissionPath)
	err := d.Fleet.Call(ctx, rid, http.MethodPost, path, PermissionAnswer{Decision: decision, Message: message}, nil)
	var callErr *tunnel.CallError
	if errors.As(err, &callErr) && callErr.Status == http.StatusNotFound {
		err = perm.ErrNotFound // answered or ended meanwhile
	}
	if err != nil && !errors.Is(err, perm.ErrNotFound) {
		return err
	}
	d.permMu.Lock()
	_, still := d.perms[rid][id]
	delete(d.perms[rid], id)
	if d.answered == nil {
		d.answered = map[string]time.Time{}
	}
	d.answered[id] = time.Now()
	d.permMu.Unlock()
	if still {
		q.Decision, q.Message = decision, message
		if decision == "always" {
			q.Decision = "allow"
		}
		if err != nil {
			q.Decision = permGone
		}
		d.publishPermission(q)
	}
	return err
}

// ---- what the API does with a task on a runner ----

func taskPath(t model.Todo, rest string) string {
	return "/api/hub/tasks/" + url.PathEscape(t.ID) + rest
}

// Approve asks t's runner to land it: push it to origin's default branch
// (or open a pull request, in "pr" mode).
func (d *Dispatcher) Approve(ctx context.Context, t model.Todo) error {
	return d.Fleet.Call(ctx, t.Runner, http.MethodPost, taskPath(t, "/approve"), struct{}{}, nil)
}

// Revert asks the runner that landed t to undo it, the way it landed.
func (d *Dispatcher) Revert(ctx context.Context, t model.Todo) error {
	return d.Fleet.Call(ctx, t.Runner, http.MethodPost, taskPath(t, "/revert"), struct{}{}, nil)
}

// Test asks t's runner to run the project's tests again in t's worktree.
func (d *Dispatcher) Test(ctx context.Context, t model.Todo) error {
	return d.Fleet.Call(ctx, t.Runner, http.MethodPost, taskPath(t, "/test"), struct{}{}, nil)
}

// Open asks t's runner to open t's worktree in its editor (-editor there).
func (d *Dispatcher) Open(ctx context.Context, t model.Todo) error {
	return d.Fleet.Call(ctx, t.Runner, http.MethodPost, taskPath(t, "/open"), struct{}{}, nil)
}

// GitHub makes a GitHubCall about the hub's project projectID on runner id.
func (d *Dispatcher) GitHub(ctx context.Context, id, projectID string, call GitHubCall, out *GitHubAnswer) error {
	return d.Fleet.Call(ctx, id, http.MethodPost, "/api/hub/github/"+url.PathEscape(projectID), call, out)
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

// TaskFile reads a file of t's branch on its runner, for editing it in
// review; out gets the runner's JSON answer.
func (d *Dispatcher) TaskFile(ctx context.Context, t model.Todo, path string, out any) error {
	return d.Fleet.Call(ctx, t.Runner, http.MethodGet, taskPath(t, "/file?path="+url.QueryEscape(path)), nil, out)
}

// SaveTaskFile commits an edit (in) to a file on t's branch on its runner;
// out gets the runner's JSON answer.
func (d *Dispatcher) SaveTaskFile(ctx context.Context, t model.Todo, in, out any) error {
	return d.Fleet.Call(ctx, t.Runner, http.MethodPut, taskPath(t, "/file"), in, out)
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

// checkDisk tells (model.EvRunnerDisk, which notifies) when runner r's
// free disk space falls below its threshold, once, and again when it is
// back above it, not on every report.
func (d *Dispatcher) checkDisk(r tunnel.Runner) {
	low := r.DiskLow != ""
	d.diskMu.Lock()
	if d.diskLow == nil {
		d.diskLow = map[string]bool{}
	}
	was := d.diskLow[r.ID]
	d.diskLow[r.ID] = low
	d.diskMu.Unlock()
	if was == low || d.Pub == nil {
		return
	}
	ev := model.RunnerDisk{RunnerID: r.ID, RunnerName: r.Name, OrgID: r.OrgID(), Low: low}
	if low {
		ev.Message = diskMessage(r)
		slog.Warn("runner: disk space low", "runner", r.Name, "message", ev.Message)
	} else {
		slog.Info("runner: disk space ok again", "runner", r.Name)
	}
	for _, p := range d.Store.ListProjects() {
		if p.OrgID == r.OrgID() && slices.Contains(p.Runners, r.ID) {
			ev.Projects = append(ev.Projects, p.ID)
		}
	}
	d.Pub.Publish(model.Event{Type: model.EvRunnerDisk, OrgID: r.OrgID(), Data: ev, Time: time.Now()})
}

// diskMessage is the low-space warning of runner r: how much it has left,
// and what in its cache folder takes the most.
func diskMessage(r tunnel.Runner) string {
	msg := r.DiskLow
	if r.DiskFull {
		msg += "; it starts no tasks until it has " + disk.Size(r.Disk.MinFree())
	}
	if r.Reported == nil || r.Reported.Disk == nil {
		return msg
	}
	st := r.Reported.Disk
	if len(st.Largest) > 0 {
		var fs []string
		for _, f := range st.Largest {
			fs = append(fs, f.Path+" ("+disk.Size(f.Size)+")")
		}
		msg += ". Largest cache folders: " + strings.Join(fs, ", ")
	}
	if c := st.GoCache; c != nil && c.Size > 0 && !slices.ContainsFunc(st.Largest, func(f disk.Folder) bool { return f.Path == c.Path }) {
		msg += ". Go build cache: " + disk.Size(c.Size) + " (go clean -cache empties it)"
	}
	return msg
}

// forgetDisk forgets runner id's disk state (it was revoked).
func (d *Dispatcher) forgetDisk(id string) {
	d.diskMu.Lock()
	delete(d.diskLow, id)
	d.diskMu.Unlock()
}
