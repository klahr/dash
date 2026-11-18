package queue

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// fakeRemote stands in for the hub's dispatcher.
type fakeRemote struct {
	mu   sync.Mutex
	err  error // what Dispatch answers; nil: runner "laptop" takes it
	sent []string
	// settle, if set, runs before Dispatch answers err (as the dispatcher
	// records a task its runner finished already).
	settle func(id string)
}

func (f *fakeRemote) Dispatch(_ context.Context, p model.Project, t model.Todo) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, t.ID)
	if f.err != nil {
		if f.settle != nil {
			f.settle(t.ID)
		}
		return "", "", f.err
	}
	return "laptop", "remote-" + t.ID, nil
}

func remoteEngine(t *testing.T) (*TodoEngine, *store.Store, *fakeRemote, *fakeExec) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dash.db"), nopPub{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rem := &fakeRemote{}
	exe := &fakeExec{block: true}
	e := NewTodoEngine(&TodoEngine{Store: st, Git: gitops.New(nopPub{}), Exec: exe, Pub: nopPub{}, Log: &TodoLog{Pub: nopPub{}, Root: t.TempDir()},
		WorktreeRoot: t.TempDir(), MaxParallel: 1, Remote: rem})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.runCtx = ctx
	return e, st, rem, exe
}

func queued(t *testing.T, st *store.Store, p model.Project) model.Todo {
	t.Helper()
	td, err := st.CreateTodo(model.Todo{ProjectID: p.ID, Title: "x", Prompt: "x"})
	if err == nil {
		td, err = st.SetStatus(td.ID, model.StatusQueued, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	return td
}

// Tasks of a project linked to runners go to the dispatcher, never to the
// hub's own Claude, and don't use the hub's parallel slots.
func TestRunnerProjectTasksGoToTheirRunners(t *testing.T) {
	parallel(t)

	e, st, rem, exe := remoteEngine(t)
	shared, _ := st.CreateProject(model.Project{Name: "shared", RemoteURL: "https://x/y", RunsOn: []string{"any"}})
	a, b := queued(t, st, shared), queued(t, st, shared) // more than MaxParallel

	e.dispatch(context.Background())

	for _, id := range []string{a.ID, b.ID} {
		got, _ := st.GetTodo(id)
		if got.Status != model.StatusInProgress || got.Runner != "laptop" || got.RemoteID != "remote-"+id {
			t.Errorf("task on a runner: %+v", got)
		}
	}
	if len(exe.Calls()) != 0 {
		t.Error("the hub ran a runner project's task itself")
	}
	if len(rem.sent) != 2 {
		t.Errorf("sent %d tasks, want 2", len(rem.sent))
	}
}

// With no runner able to take it, a task stays queued and says why; it is
// tried again later.
func TestRunnerProjectTasksWaitWhileRunnersAreOffline(t *testing.T) {
	parallel(t)

	e, st, rem, _ := remoteEngine(t)
	rem.err = errors.New("waiting for a runner: laptop is offline")
	shared, _ := st.CreateProject(model.Project{Name: "shared", RemoteURL: "https://x/y", RunsOn: []string{"any"}})
	td := queued(t, st, shared)

	e.dispatch(context.Background())
	got, _ := st.GetTodo(td.ID)
	if got.Status != model.StatusQueued || got.Waiting != rem.err.Error() || got.Runner != "" {
		t.Fatalf("while offline: %+v", got)
	}

	rem.err = nil
	e.dispatch(context.Background())
	got, _ = st.GetTodo(td.ID)
	if got.Status != model.StatusInProgress || got.Waiting != "" || got.Runner != "laptop" {
		t.Errorf("once back: %+v", got)
	}
}

// The hub runs no task itself: tasks of projects no runner is linked to
// stay queued, saying so.
func TestHubWaitsForARunner(t *testing.T) {
	parallel(t)

	e, st, _, exe := remoteEngine(t)
	local, _ := st.CreateProject(model.Project{Name: "local"})
	td := queued(t, st, local)

	e.dispatch(context.Background())
	got, _ := st.GetTodo(td.ID)
	if got.Status != model.StatusQueued || got.Waiting != NoRunnerLinked || len(exe.Calls()) != 0 {
		t.Errorf("local task on a runners-only hub: %s %q, %d runs", got.Status, got.Waiting, len(exe.Calls()))
	}
}

// A task running on a runner can't be paused from the hub.
func TestPauseRefusesRunnerTasks(t *testing.T) {
	parallel(t)

	e, st, _, _ := remoteEngine(t)
	shared, _ := st.CreateProject(model.Project{Name: "shared", RemoteURL: "https://x/y", RunsOn: []string{"any"}})
	td := queued(t, st, shared)
	e.dispatch(context.Background())
	if _, err := e.Pause(td.ID); !errors.Is(err, ErrOnRunner) {
		t.Errorf("pause = %v, want ErrOnRunner", err)
	}
}

// A task that ran on a runner of a project since unlinked never starts
// over on the hub: it waits for a runner to be linked again.
func TestUnlinkedProjectTaskWaitsForARunner(t *testing.T) {
	parallel(t)

	e, st, rem, exe := remoteEngine(t)
	p, _ := st.CreateProject(model.Project{Name: "was-shared", RemoteURL: "https://x/y", DefaultBranch: "main"})
	td := queued(t, st, p)
	_, _ = st.UpdateTodo(td.ID, func(x *model.Todo) error {
		x.Runner, x.RemoteID, x.Resumable, x.Worktree = "laptop", "r1", true, &model.Worktree{Branch: "dash/todo-r1"}
		return nil
	})
	e.dispatch(context.Background())
	got, _ := st.GetTodo(td.ID)
	if got.Status != model.StatusQueued || got.Waiting != NoRunnerLinked || len(exe.Calls()) != 0 || len(rem.sent) != 0 {
		t.Errorf("after dispatch: %s %q, %d runs here", got.Status, got.Waiting, len(exe.Calls()))
	}
}

// A held org's tasks (billing: no subscription) stay queued, here and for
// its runners, saying why; other orgs' tasks go on, and the held ones start
// once the hold lifts.
func TestHeldOrgTasksWait(t *testing.T) {
	parallel(t)

	e, st, rem, _ := remoteEngine(t)
	held := "waiting for the organization's subscription"
	holding := true
	e.Hold = func(org int64) string {
		if holding && org == 7 {
			return held
		}
		return ""
	}
	unpaid, _ := st.CreateProject(model.Project{Name: "unpaid", OrgID: 7, RemoteURL: "https://x/y", RunsOn: []string{"any"}})
	paid, _ := st.CreateProject(model.Project{Name: "paid", OrgID: 8, RemoteURL: "https://x/z", RunsOn: []string{"any"}})
	a, b := queued(t, st, unpaid), queued(t, st, paid)

	e.dispatch(context.Background())
	if got, _ := st.GetTodo(a.ID); got.Status != model.StatusQueued || got.Waiting != held {
		t.Errorf("held task: %s %q", got.Status, got.Waiting)
	}
	if got, _ := st.GetTodo(b.ID); got.Status != model.StatusInProgress {
		t.Errorf("another org's task: %s %q", got.Status, got.Waiting)
	}
	if len(rem.sent) != 1 || rem.sent[0] != b.ID {
		t.Errorf("sent to runners: %v", rem.sent)
	}

	holding = false
	e.dispatch(context.Background())
	if got, _ := st.GetTodo(a.ID); got.Status != model.StatusInProgress || got.Waiting != "" {
		t.Errorf("once paid: %s %q", got.Status, got.Waiting)
	}
}

// A task the runner had finished already (the Remote recorded it, say as
// merged, and answered ErrSettled) gets no Waiting reason and isn't sent
// again.
func TestSettledTaskIsNotSentAgain(t *testing.T) {
	parallel(t)

	e, st, rem, _ := remoteEngine(t)
	shared, _ := st.CreateProject(model.Project{Name: "shared", RemoteURL: "https://x/y", RunsOn: []string{"any"}})
	td := queued(t, st, shared)
	rem.err = ErrSettled
	rem.settle = func(id string) {
		if _, err := st.SetStatus(id, model.StatusMerged, ""); err != nil {
			t.Error(err)
		}
	}

	e.dispatch(context.Background())
	e.dispatch(context.Background())
	got, _ := st.GetTodo(td.ID)
	if got.Status != model.StatusMerged || got.Waiting != "" {
		t.Fatalf("settled task: %+v", got)
	}
	if len(rem.sent) != 1 {
		t.Errorf("sent %d times, want 1", len(rem.sent))
	}
}

// claimRemote is a Claimer: tasks of project "slow" go to a runner that
// takes its time answering.
type claimRemote struct {
	fakeRemote
	slowProject string
	release     chan struct{}
}

func (c *claimRemote) Claim(p model.Project, t model.Todo) (string, func(context.Context) (string, string, error), error) {
	c.mu.Lock()
	c.sent = append(c.sent, t.ID)
	c.mu.Unlock()
	runner := "fast"
	if p.ID == c.slowProject {
		runner = "slow"
	}
	return runner, func(ctx context.Context) (string, string, error) {
		if runner == "slow" {
			select {
			case <-c.release:
			case <-ctx.Done():
				return "", "", ctx.Err()
			}
		}
		return runner, "remote-" + t.ID, nil
	}, nil
}

// A slow runner holds up only its own tasks: the tasks of other runners
// are sent while it is still answering.
func TestSlowRunnerDoesntHoldUpOthers(t *testing.T) {
	parallel(t)

	e, st, _, _ := remoteEngine(t)
	slow, _ := st.CreateProject(model.Project{Name: "slow", RemoteURL: "https://x/slow", RunsOn: []string{"any"}})
	fast, _ := st.CreateProject(model.Project{Name: "fast", RemoteURL: "https://x/fast", RunsOn: []string{"any"}})
	rem := &claimRemote{slowProject: slow.ID, release: make(chan struct{})}
	e.Remote = rem
	s1, s2, f := queued(t, st, slow), queued(t, st, slow), queued(t, st, fast)

	e.dispatch(context.Background())
	status := func(id string) model.Todo {
		got, _ := st.GetTodo(id)
		return got
	}
	waitFor(t, func() bool { return status(f.ID).Status == model.StatusInProgress })
	if got := status(s1.ID); got.Status != model.StatusQueued {
		t.Fatalf("slow runner's task %s before it answered", got.Status)
	}
	e.dispatch(context.Background()) // a round while they are being sent doesn't send them twice
	close(rem.release)
	waitFor(t, func() bool {
		return status(s1.ID).Status == model.StatusInProgress && status(s2.ID).Status == model.StatusInProgress
	})
	rem.mu.Lock()
	n := len(rem.sent)
	rem.mu.Unlock()
	if n != 3 {
		t.Errorf("claimed %d times, want 3", n)
	}
	if got := status(s1.ID); got.Runner != "slow" || got.RemoteID != "remote-"+s1.ID {
		t.Fatalf("slow runner's task: %+v", got)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A project's MaxParallel caps its tasks in progress at once, on runners
// too: the rest stay queued, saying why, until one finishes.
func TestProjectMaxParallel(t *testing.T) {
	parallel(t)

	e, st, rem, _ := remoteEngine(t)
	shared, _ := st.CreateProject(model.Project{Name: "shared", RemoteURL: "https://x/y", RunsOn: []string{"any"}, MaxParallel: 2})
	queued(t, st, shared)
	queued(t, st, shared)
	queued(t, st, shared)

	e.dispatch(context.Background())
	if len(rem.sent) != 2 {
		t.Fatalf("sent %d tasks, want 2", len(rem.sent))
	}
	// Which two went first depends on the queue order, which falls back to
	// CreatedAt: tasks created in the same clock tick (Windows) tie. So find
	// the one left behind rather than assuming it is the last created.
	first, second := rem.sent[0], rem.sent[1]
	left := st.ListTodos(shared.ID, model.StatusQueued)
	if len(left) != 1 || left[0].ID == first || left[0].ID == second {
		t.Fatalf("queued after the first round: %+v", left)
	}
	third := left[0].ID
	if got, _ := st.GetTodo(third); got.Waiting != ProjectFullNote(2) {
		t.Fatalf("third task: %+v", got)
	}

	if _, err := st.SetStatus(first, model.StatusReview, ""); err != nil {
		t.Fatal(err)
	}
	e.dispatch(context.Background())
	if got, _ := st.GetTodo(third); got.Status != model.StatusInProgress || got.Waiting != "" {
		t.Fatalf("after one finished: %+v", got)
	}
	if got, _ := st.GetTodo(second); got.Status != model.StatusInProgress {
		t.Fatalf("second task: %+v", got)
	}
}
