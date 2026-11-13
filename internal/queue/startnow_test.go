package queue

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/usage"
)

// pacedEngine is a local engine (no runners) whose queue follows the weekly
// pace and is far ahead of it, so it waits; its project has no repository,
// so a task that starts fails at once, which is all these tests need.
func pacedEngine(t *testing.T) (*TodoEngine, *store.Store, *usage.Tracker, model.Project) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dash.db"), nopPub{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tr := usage.New(nil)
	observe(tr, map[string]any{
		"seven_day": map[string]any{"utilization": 0.9, "resetsAt": time.Now().Add(usage.WeekSpan / 2).Unix(), "status": "allowed"},
	})
	if err := tr.SetPace(usage.PaceSettings{Follow: true}); err != nil {
		t.Fatal(err)
	}
	e := NewTodoEngine(&TodoEngine{Store: st, Git: gitops.New(nopPub{}), Exec: &fakeExec{block: true}, Pub: nopPub{},
		Log: &TodoLog{Pub: nopPub{}, Root: t.TempDir()}, WorktreeRoot: t.TempDir(), MaxParallel: 1, Usage: tr})
	ctx, cancel := context.WithCancel(context.Background())
	e.runCtx = ctx
	t.Cleanup(func() {
		cancel()
		e.Wait(10 * time.Second)
	})
	p, err := st.CreateProject(model.Project{Name: "p", Path: filepath.Join(t.TempDir(), "missing"), DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if s := tr.Snapshot(); !s.PaceOnly() {
		t.Fatalf("the queue should wait only for the pace: %+v", s)
	}
	return e, st, tr, p
}

func observe(tr *usage.Tracker, windows map[string]any) {
	raw, _ := json.Marshal(map[string]any{"status": "allowed", "unifiedWindows": windows})
	tr.Observe(raw)
}

// started reports whether dispatch took todo id out of the queue.
func started(t *testing.T, st *store.Store, id string) model.Todo {
	t.Helper()
	got, err := st.GetTodo(id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// While the queue waits for the pace, a task with Start now starts and
// uses the flag up; the others keep waiting.
func TestStartNowStartsPastThePace(t *testing.T) {
	parallel(t)

	e, st, _, p := pacedEngine(t)
	plain := queued(t, st, p)
	e.dispatch(context.Background())
	if got := started(t, st, plain.ID); got.Status != model.StatusQueued {
		t.Fatalf("an unforced task started while pacing: %s", got.Status)
	}

	forced := queued(t, st, p)
	if _, err := e.StartNow(forced.ID); err != nil {
		t.Fatal(err)
	}
	e.dispatch(context.Background())
	got := started(t, st, forced.ID)
	if got.Status == model.StatusQueued || got.StartNow || !got.StartedNow {
		t.Fatalf("forced task: status %s, start_now %v, started_now %v; want started with the flag used up", got.Status, got.StartNow, got.StartedNow)
	}
	if log, _ := e.Log.Read(forced.ID); !strings.Contains(log, "started now, despite the weekly pace") {
		t.Errorf("the log doesn't say it started despite the pace:\n%s", log)
	}
	if got := started(t, st, plain.ID); got.Status != model.StatusQueued {
		t.Errorf("the unforced task started along: %s", got.Status)
	}

	// Started again later (a retry), it follows the pace like any other.
	e.Wait(10 * time.Second)
	waitStatus(t, st, forced.ID, model.StatusFailed)
	if _, err := st.SetStatus(forced.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	e.dispatch(context.Background())
	if got := started(t, st, forced.ID); got.Status != model.StatusQueued {
		t.Errorf("a later run ignored the pace: %s", got.Status)
	}
}

func waitStatus(t *testing.T, st *store.Store, id string, want model.TodoStatus) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if got, _ := st.GetTodo(id); got.Status == want {
			return
		}
	}
	got, _ := st.GetTodo(id)
	t.Fatalf("todo %s is %s, want %s", id, got.Status, want)
}

// Start now skips only the pace: a usage limit, a paused queue, unmerged
// dependencies and full lanes still hold the task back.
func TestStartNowStillWaitsForTheRest(t *testing.T) {
	parallel(t)

	cases := map[string]func(t *testing.T, e *TodoEngine, st *store.Store, tr *usage.Tracker, p model.Project, td *model.Todo){
		"5-hour limit": func(t *testing.T, e *TodoEngine, st *store.Store, tr *usage.Tracker, p model.Project, td *model.Todo) {
			observe(tr, map[string]any{"five_hour": map[string]any{"utilization": 1, "resetsAt": time.Now().Add(time.Hour).Unix(), "status": "rejected"}})
		},
		"weekly limit": func(t *testing.T, e *TodoEngine, st *store.Store, tr *usage.Tracker, p model.Project, td *model.Todo) {
			observe(tr, map[string]any{"seven_day": map[string]any{"utilization": 1, "resetsAt": time.Now().Add(usage.WeekSpan / 2).Unix(), "status": "rejected"}})
		},
		"paused queue": func(t *testing.T, e *TodoEngine, st *store.Store, tr *usage.Tracker, p model.Project, td *model.Todo) {
			tr.SetManualPause(true)
		},
		"dependencies": func(t *testing.T, e *TodoEngine, st *store.Store, tr *usage.Tracker, p model.Project, td *model.Todo) {
			dep, err := st.CreateTodo(model.Todo{ProjectID: p.ID, Title: "first"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.UpdateTodo(td.ID, func(x *model.Todo) error { x.DependsOn = []string{dep.ID}; return nil }); err != nil {
				t.Fatal(err)
			}
		},
		"full lanes": func(t *testing.T, e *TodoEngine, st *store.Store, tr *usage.Tracker, p model.Project, td *model.Todo) {
			e.mu.Lock()
			for i := range e.EffectiveParallel() {
				e.running["busy-"+string(rune('a'+i))] = func() {}
			}
			e.mu.Unlock()
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			e, st, tr, p := pacedEngine(t)
			td := queued(t, st, p)
			setup(t, e, st, tr, p, &td)
			if _, err := e.StartNow(td.ID); err != nil {
				t.Fatal(err)
			}
			e.dispatch(context.Background())
			got := started(t, st, td.ID)
			if got.Status != model.StatusQueued || !got.StartNow {
				t.Fatalf("status %s, start_now %v: want it still queued with Start now kept", got.Status, got.StartNow)
			}
		})
	}
}

// Only a queued task can start now; a paused one is resumed by it.
func TestStartNowNeedsAQueuedTask(t *testing.T) {
	parallel(t)

	e, st, tr, p := pacedEngine(t)
	tr.SetManualPause(true) // nothing starts during the test
	draft, err := st.CreateTodo(model.Todo{ProjectID: p.ID, Title: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.StartNow(draft.ID); !errors.Is(err, ErrNotQueued) {
		t.Errorf("a draft: err = %v, want ErrNotQueued", err)
	}
	td := queued(t, st, p)
	if _, err := e.Pause(td.ID); err != nil {
		t.Fatal(err)
	}
	got, err := e.StartNow(td.ID)
	if err != nil || got.Paused || !got.StartNow {
		t.Fatalf("a paused task: %+v %v", got, err)
	}
	// Moved back to drafts, it forgets Start now.
	if got, _ = st.SetStatus(td.ID, model.StatusDraft, ""); got.StartNow {
		t.Error("a draft kept Start now")
	}
}
