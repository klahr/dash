package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
	"github.com/klahr/dash/internal/usage"
)

// fakeFleet stands in for the hub's runners: it records the calls made to
// each and accepts every task.
type fakeFleet struct {
	mu      sync.Mutex
	runners map[string]tunnel.Runner
	calls   []string // "runner METHOD path"
}

func (f *fakeFleet) Get(id string) (tunnel.Runner, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runners[id]
	return r, ok
}

func (f *fakeFleet) set(r tunnel.Runner) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runners[r.ID] = r
}

func (f *fakeFleet) remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.runners, id)
}

func (f *fakeFleet) Call(_ context.Context, id, method, path string, in, out any) error {
	f.mu.Lock()
	r, ok := f.runners[id]
	f.calls = append(f.calls, id+" "+method+" "+path)
	f.mu.Unlock()
	if !ok {
		return tunnel.ErrUnknownName
	}
	if !r.Online {
		return tunnel.ErrOffline
	}
	if t, ok := in.(Task); ok {
		if a, ok := out.(*Accepted); ok {
			a.ID = "remote-" + t.HubTodoID
		}
	}
	return nil
}

func (f *fakeFleet) callsTo(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, id+" ") {
			out = append(out, c)
		}
	}
	return out
}

func runnerOf(id string, org int64, online bool) tunnel.Runner {
	return tunnel.Runner{ID: id, Name: id, Online: online, Parallel: 2,
		Owner: &store.RunnerOwner{UserID: 1, Username: "u", Org: store.Org{ID: org}}}
}

type fixture struct {
	st    *store.Store
	fleet *fakeFleet
	d     *Dispatcher
	orgA  int64
	orgB  int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	acc := st.Accounts()
	if err := acc.Bootstrap("admin", "s3cret-password"); err != nil {
		t.Fatal(err)
	}
	u, _ := acc.EnvUser()
	a, _ := acc.DefaultOrg()
	b, err := acc.CreateOrg(u.ID, "Beta")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeFleet{runners: map[string]tunnel.Runner{}}
	return &fixture{st: st, fleet: f, d: &Dispatcher{Store: st, Fleet: f, Grace: 30 * time.Millisecond}, orgA: a.ID, orgB: b.ID}
}

func (fx *fixture) project(t *testing.T, org int64, runners ...string) model.Project {
	t.Helper()
	p, err := fx.st.CreateProject(model.Project{Name: "web", OrgID: org, RemoteURL: "https://github.com/x/web", DefaultBranch: "main",
		MergeMode: model.MergePR, Runners: runners})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (fx *fixture) todo(t *testing.T, p model.Project, fn func(*model.Todo)) model.Todo {
	t.Helper()
	td, err := fx.st.CreateTodo(model.Todo{ProjectID: p.ID, Title: "task", Prompt: "do it"})
	if err == nil {
		td, err = fx.st.SetStatus(td.ID, model.StatusQueued, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	if fn != nil {
		if td, err = fx.st.UpdateTodo(td.ID, func(x *model.Todo) error { fn(x); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	return td
}

func (fx *fixture) get(t *testing.T, id string) model.Todo {
	t.Helper()
	td, err := fx.st.GetTodo(id)
	if err != nil {
		t.Fatal(err)
	}
	return td
}

// Tasks go only to the runners linked to their project, and only to those
// in the project's org: never to another org's runner, even when it is
// linked by mistake, nor to an unlinked runner of the same org.
func TestDispatchRoutesOnlyToLinkedRunnersOfTheOrg(t *testing.T) {
	fx := newFixture(t)
	fx.fleet.set(runnerOf("laptop", fx.orgA, true))
	fx.fleet.set(runnerOf("idle", fx.orgA, true))     // same org, not linked
	fx.fleet.set(runnerOf("beta-box", fx.orgB, true)) // another org's
	p := fx.project(t, fx.orgA, "beta-box", "laptop") // beta-box listed first, but isn't the org's
	td := fx.todo(t, p, nil)

	rid, remote, err := fx.d.Dispatch(context.Background(), p, td)
	if err != nil || rid != "laptop" || remote != "remote-"+td.ID {
		t.Fatalf("dispatch = %q %q %v, want laptop", rid, remote, err)
	}
	if got := fx.fleet.callsTo("laptop"); len(got) != 2 || !strings.Contains(got[0], "PUT /api/hub/projects/"+p.ID) || !strings.Contains(got[1], "POST /api/hub/tasks") {
		t.Errorf("calls to laptop %q, want a link then the task", got)
	}
	if n := len(fx.fleet.callsTo("idle")) + len(fx.fleet.callsTo("beta-box")); n != 0 {
		t.Errorf("%d calls to runners not linked in the org", n)
	}
}

// With every linked runner offline the task waits, and says why.
func TestDispatchWaitsWhileRunnersAreOffline(t *testing.T) {
	fx := newFixture(t)
	fx.fleet.set(runnerOf("laptop", fx.orgA, false))
	fx.fleet.set(runnerOf("desk", fx.orgA, false))
	p := fx.project(t, fx.orgA, "laptop", "desk")
	_, _, err := fx.d.Dispatch(context.Background(), p, fx.todo(t, p, nil))
	var w *WaitError
	if !errors.As(err, &w) || !strings.Contains(w.Reason, "laptop is offline") || !strings.Contains(w.Reason, "desk is offline") {
		t.Fatalf("err = %v, want a wait naming both offline runners", err)
	}
	if len(fx.fleet.calls) != 0 {
		t.Errorf("offline runners were called: %q", fx.fleet.calls)
	}

	// Revoked runners leave nothing to wait for.
	fx.fleet.remove("laptop")
	fx.fleet.remove("desk")
	_, _, err = fx.d.Dispatch(context.Background(), p, fx.todo(t, p, nil))
	if !errors.As(err, &w) || !strings.Contains(w.Reason, "revoked") {
		t.Fatalf("err = %v, want a wait saying the runners were revoked", err)
	}
}

// Each runner has its own parallel limit, pause and usage limit.
func TestDispatchRespectsEachRunnersLimits(t *testing.T) {
	fx := newFixture(t)
	r := runnerOf("laptop", fx.orgA, true)
	r.Parallel = 1
	fx.fleet.set(r)
	p := fx.project(t, fx.orgA, "laptop")
	fx.todo(t, p, func(x *model.Todo) { x.Status, x.Runner = model.StatusInProgress, "laptop" })
	_, _, err := fx.d.Dispatch(context.Background(), p, fx.todo(t, p, nil))
	if err == nil || !strings.Contains(err.Error(), "busy (1 of 1") {
		t.Errorf("full runner: %v", err)
	}

	r.Parallel = 2
	r.Paused = true
	fx.fleet.set(r)
	if _, _, err := fx.d.Dispatch(context.Background(), p, fx.todo(t, p, nil)); err == nil || !strings.Contains(err.Error(), "laptop is paused") {
		t.Errorf("paused runner: %v", err)
	}

	r.Paused = false
	r.Reported = &tunnel.Reported{Usage: &usage.Snapshot{Paused: true, Reason: "5-hour limit at 95%"}}
	fx.fleet.set(r)
	if _, _, err := fx.d.Dispatch(context.Background(), p, fx.todo(t, p, nil)); err == nil || !strings.Contains(err.Error(), "5-hour limit") {
		t.Errorf("runner at its usage limit: %v", err)
	}

	// Its own -parallel counts when the hub sets none.
	r.Reported, r.Parallel = &tunnel.Reported{Parallel: 1}, 0
	fx.fleet.set(r)
	if _, _, err := fx.d.Dispatch(context.Background(), p, fx.todo(t, p, nil)); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Errorf("runner at its own limit: %v", err)
	}
}

// The least loaded runner gets new work; a task that ran before goes back
// to the runner that has its session.
func TestDispatchPrefersTheRunnerWithTheSession(t *testing.T) {
	fx := newFixture(t)
	fx.fleet.set(runnerOf("a", fx.orgA, true))
	fx.fleet.set(runnerOf("b", fx.orgA, true))
	p := fx.project(t, fx.orgA, "a", "b")
	fx.todo(t, p, func(x *model.Todo) { x.Status, x.Runner = model.StatusInProgress, "a" })

	if rid, _, _ := fx.d.Dispatch(context.Background(), p, fx.todo(t, p, nil)); rid != "b" {
		t.Errorf("new task went to %q, want the idle b", rid)
	}
	back := fx.todo(t, p, func(x *model.Todo) { x.Runner, x.RemoteID, x.Resumable = "a", "r1", true })
	fx.fleet.calls = nil
	if rid, _, _ := fx.d.Dispatch(context.Background(), p, back); rid != "a" {
		t.Errorf("resumed task went to %q, want a", rid)
	}
}

// A runner that stays offline past the grace has its running tasks failed,
// ready to continue; one back in time keeps them.
func TestOfflineRunnerFailsItsTasksCleanly(t *testing.T) {
	fx := newFixture(t)
	fx.fleet.set(runnerOf("laptop", fx.orgA, true))
	p := fx.project(t, fx.orgA, "laptop")
	running := func() model.Todo {
		return fx.todo(t, p, func(x *model.Todo) { x.Status, x.Runner, x.RemoteID = model.StatusInProgress, "laptop", "r1" })
	}

	blip := running()
	fx.d.State("laptop", false)
	fx.d.State("laptop", true)
	time.Sleep(80 * time.Millisecond)
	if got := fx.get(t, blip.ID); got.Status != model.StatusInProgress {
		t.Fatalf("after a blip: %s %q", got.Status, got.Error)
	}

	fx.fleet.set(runnerOf("laptop", fx.orgA, false))
	fx.d.State("laptop", false)
	deadline := time.Now().Add(5 * time.Second)
	for fx.get(t, blip.ID).Status == model.StatusInProgress && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := fx.get(t, blip.ID)
	if got.Status != model.StatusFailed || !strings.HasPrefix(got.Error, InterruptedPrefix) || !got.Resumable || got.Runner != "laptop" {
		t.Fatalf("after going away: %+v", got)
	}

	// Back online, the runner says it kept going: the task is queued to go
	// back to it, and it says it is on it.
	fx.fleet.set(runnerOf("laptop", fx.orgA, true))
	report(t, fx.d, "laptop", TaskState{HubTodoID: got.ID, ID: "r1", Status: model.StatusInProgress})
	got = fx.get(t, got.ID)
	if got.Status != model.StatusQueued || got.Error != "" || got.Runner != "laptop" {
		t.Fatalf("after the runner reported in: %s %q %q", got.Status, got.Error, got.Runner)
	}
	if rid, _, err := fx.d.Dispatch(context.Background(), p, got); err != nil || rid != "laptop" {
		t.Errorf("sent back to %q (%v), want laptop", rid, err)
	}
}

func report(t *testing.T, d *Dispatcher, runner string, tasks ...TaskState) {
	t.Helper()
	b, _ := json.Marshal(Report{Tasks: tasks})
	if err := d.Report(runner, b); err != nil {
		t.Fatal(err)
	}
}

// A runner's report changes only the tasks the hub sent to that runner, in
// its org: not another runner's, nor another org's.
func TestReportsOnlyChangeTheRunnersOwnTasks(t *testing.T) {
	fx := newFixture(t)
	fx.fleet.set(runnerOf("laptop", fx.orgA, true))
	fx.fleet.set(runnerOf("desk", fx.orgA, true))
	fx.fleet.set(runnerOf("beta-box", fx.orgB, true))
	p := fx.project(t, fx.orgA, "laptop", "desk")
	td := fx.todo(t, p, func(x *model.Todo) { x.Status, x.Runner, x.RemoteID = model.StatusInProgress, "laptop", "r1" })
	done := TaskState{HubTodoID: td.ID, ID: "r1", Status: model.StatusReview, Summary: "did it", Branch: "dash/todo-r1", DiffFiles: 3}

	report(t, fx.d, "desk", done)
	report(t, fx.d, "beta-box", done)
	if got := fx.get(t, td.ID); got.Status != model.StatusInProgress || got.Summary != "" {
		t.Fatalf("another runner changed the task: %+v", got)
	}

	// Even the beta runner's own task can't be changed when it sits in
	// another org's project.
	betaTask := fx.todo(t, p, func(x *model.Todo) { x.Status, x.Runner, x.RemoteID = model.StatusInProgress, "beta-box", "b1" })
	report(t, fx.d, "beta-box", TaskState{HubTodoID: betaTask.ID, ID: "b1", Status: model.StatusReview})
	if got := fx.get(t, betaTask.ID); got.Status != model.StatusInProgress {
		t.Errorf("a runner of another org changed a task: %s", got.Status)
	}

	report(t, fx.d, "laptop", done)
	got := fx.get(t, td.ID)
	if got.Status != model.StatusReview || got.Summary != "did it" || got.DiffFiles != 3 || got.Worktree == nil ||
		got.Worktree.Branch != "dash/todo-r1" || got.Worktree.Path != "" {
		t.Fatalf("after its runner's report: %+v", got)
	}

	// Sent back for rework on the hub (queued, waiting to be sent again):
	// the runner's old news doesn't undo that.
	if _, err := fx.st.SetStatus(td.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	report(t, fx.d, "laptop", done)
	if got := fx.get(t, td.ID); got.Status != model.StatusQueued {
		t.Errorf("stale report moved a queued task to %s", got.Status)
	}
}

// Revoking a runner unlinks it everywhere and fails what it was running
// (to start over elsewhere: its worktree is out of reach).
func TestRevokedRunnerIsUnlinkedAndItsTasksFail(t *testing.T) {
	fx := newFixture(t)
	fx.fleet.set(runnerOf("laptop", fx.orgA, true))
	fx.fleet.set(runnerOf("desk", fx.orgA, true))
	p := fx.project(t, fx.orgA, "laptop", "desk")
	td := fx.todo(t, p, func(x *model.Todo) { x.Status, x.Runner, x.RemoteID = model.StatusInProgress, "laptop", "r1" })

	fx.fleet.remove("laptop")
	fx.d.Revoked("laptop")

	if got, _ := fx.st.GetProject(p.ID); len(got.Runners) != 1 || got.Runners[0] != "desk" {
		t.Errorf("runners after revoking laptop: %v", got.Runners)
	}
	got := fx.get(t, td.ID)
	if got.Status != model.StatusFailed || got.Resumable || got.Runner != "" || !strings.Contains(got.Error, "revoked") {
		t.Errorf("task of the revoked runner: %+v", got)
	}
	// Late news from the revoked runner changes nothing.
	if err := fx.d.Report("laptop", []byte(`{}`)); err == nil {
		t.Error("a revoked runner's report was accepted")
	}
}

// At start, tasks of runners that aren't connected fail after the grace.
func TestStartArmsTheGraceOfOfflineRunners(t *testing.T) {
	fx := newFixture(t)
	fx.fleet.set(runnerOf("laptop", fx.orgA, false))
	p := fx.project(t, fx.orgA, "laptop")
	td := fx.todo(t, p, func(x *model.Todo) { x.Status, x.Runner, x.RemoteID = model.StatusInProgress, "laptop", "r1" })
	fx.d.Start()
	deadline := time.Now().Add(5 * time.Second)
	for fx.get(t, td.ID).Status == model.StatusInProgress && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := fx.get(t, td.ID); got.Status != model.StatusFailed || !got.Resumable {
		t.Errorf("task of a runner that never came back: %s resumable=%v", got.Status, got.Resumable)
	}
}

func TestStateOf(t *testing.T) {
	s := StateOf(model.Todo{ID: "x", HubTodoID: "h", Status: model.StatusFailed, SessionID: "s",
		Worktree: &model.Worktree{Branch: "dash/todo-x"}})
	if !s.Session || s.Branch != "dash/todo-x" || s.HubTodoID != "h" {
		t.Errorf("%+v", s)
	}
	if StateOf(model.Todo{SessionID: "s"}).Session {
		t.Error("a session without a worktree can't be continued")
	}
}
