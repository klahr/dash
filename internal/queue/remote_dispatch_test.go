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
}

func (f *fakeRemote) Dispatch(_ context.Context, p model.Project, t model.Todo) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, t.ID)
	if f.err != nil {
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
	e, st, rem, exe := remoteEngine(t)
	shared, _ := st.CreateProject(model.Project{Name: "shared", RemoteURL: "https://x/y", Runners: []string{"laptop"}})
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
	e, st, rem, _ := remoteEngine(t)
	rem.err = errors.New("waiting for a runner: laptop is offline")
	shared, _ := st.CreateProject(model.Project{Name: "shared", RemoteURL: "https://x/y", Runners: []string{"laptop"}})
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

// A hub that doesn't run tasks itself leaves tasks of unlinked projects
// queued, saying so.
func TestHubWithoutLocalTasks(t *testing.T) {
	e, st, _, exe := remoteEngine(t)
	e.LocalTasks = func() bool { return false }
	local, _ := st.CreateProject(model.Project{Name: "local"})
	td := queued(t, st, local)

	e.dispatch(context.Background())
	got, _ := st.GetTodo(td.ID)
	if got.Status != model.StatusQueued || got.Waiting != NoLocalTasks || len(exe.Calls()) != 0 {
		t.Errorf("local task on a runners-only hub: %s %q, %d runs", got.Status, got.Waiting, len(exe.Calls()))
	}
}

// A task running on a runner can't be paused from the hub.
func TestPauseRefusesRunnerTasks(t *testing.T) {
	e, st, _, _ := remoteEngine(t)
	shared, _ := st.CreateProject(model.Project{Name: "shared", RemoteURL: "https://x/y", Runners: []string{"laptop"}})
	td := queued(t, st, shared)
	e.dispatch(context.Background())
	if _, err := e.Pause(td.ID); !errors.Is(err, ErrOnRunner) {
		t.Errorf("pause = %v, want ErrOnRunner", err)
	}
}

// A task that ran on a runner of a project since unlinked starts over on
// the hub, as the hub's own.
func TestUnlinkedProjectTaskStartsOverHere(t *testing.T) {
	e, st, _, exe := remoteEngine(t)
	p, _ := st.CreateProject(model.Project{Name: "was-shared", Path: t.TempDir(), DefaultBranch: "main"})
	td := queued(t, st, p)
	_, _ = st.UpdateTodo(td.ID, func(x *model.Todo) error {
		x.Runner, x.RemoteID, x.Resumable, x.Worktree = "laptop", "r1", true, &model.Worktree{Branch: "dash/todo-r1"}
		return nil
	})
	e.dispatch(context.Background())
	got, _ := st.GetTodo(td.ID)
	if got.Status == model.StatusQueued || got.OnRunner() || got.RemoteID != "" || got.Resumable {
		t.Errorf("after dispatch: %+v", got)
	}
	// Its worker fails here (the folder isn't a repository); wait for it.
	for deadline := time.Now().Add(10 * time.Second); ; {
		e.mu.Lock()
		n := len(e.running)
		e.mu.Unlock()
		if n == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = exe
}
