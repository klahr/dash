package dispatch

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/tunnel"
)

const mergedSHA = "0123456789abcdef0123456789abcdef01234567"

// rejoining sets up a task the hub lost touch with on runner laptop: it was
// failed as interrupted, then laptop reported it still running, so it is
// queued to go back to laptop. desk is another runner of the project.
func rejoining(t *testing.T) (*fixture, model.Project, model.Todo) {
	t.Helper()
	fx := newFixture(t)
	fx.fleet.set(runnerOf("laptop", fx.orgA, true))
	fx.fleet.set(runnerOf("desk", fx.orgA, true))
	p := fx.project(t, fx.orgA, "laptop", "desk")
	td := fx.todo(t, p, func(x *model.Todo) { x.Status, x.Runner, x.RemoteID = model.StatusInProgress, "laptop", "r1" })

	fx.d.interrupt("laptop", "went offline mid-task (asleep or disconnected)", true)
	if got := fx.get(t, td.ID); got.Status != model.StatusFailed {
		t.Fatalf("after losing touch: %s %q", got.Status, got.Error)
	}
	report(t, fx.d, "laptop", TaskState{HubTodoID: td.ID, ID: "r1", Status: model.StatusInProgress})
	got := fx.get(t, td.ID)
	if got.Status != model.StatusQueued || !got.Rejoin || got.Runner != "laptop" || got.RemoteID != "r1" {
		t.Fatalf("after the runner reported in: %+v", got)
	}
	return fx, p, got
}

func (fx *fixture) taskSends() int {
	n := 0
	for _, c := range fx.fleet.callsTo("laptop") {
		if c == "laptop POST /api/hub/tasks" {
			n++
		}
	}
	return n
}

// The whole sequence that left tasks queued for good: interrupted, queued
// to go back to its runner, merged there while the hub ignored (or missed)
// the runner's news, then sent again. The runner answers that it merged
// it: the task ends up merged, with its commit, and isn't sent again.
func TestTaskMergedOnItsRunnerWhileQueuedEndsMerged(t *testing.T) {
	t.Parallel()

	fx, p, td := rejoining(t)
	var sent Task
	fx.fleet.answer = func(_ string, task Task, a *Accepted) error {
		sent = task
		a.ID, a.Resumed = "r1", true
		a.Done = &TaskState{HubTodoID: task.HubTodoID, ID: "r1", Status: model.StatusMerged, MergedCommit: mergedSHA,
			Summary: "did it", Branch: "dash/todo-r1"}
		return nil
	}

	_, _, err := fx.d.Dispatch(context.Background(), p, td)
	if !errors.Is(err, queue.ErrSettled) {
		t.Fatalf("Dispatch: %v, want queue.ErrSettled", err)
	}
	if !sent.Rejoin || !sent.TakesDone || sent.Fresh {
		t.Errorf("sent %+v, want Rejoin and TakesDone, not Fresh", sent)
	}
	got := fx.get(t, td.ID)
	if got.Status != model.StatusMerged || got.MergedCommit != mergedSHA || got.Waiting != "" || got.Rejoin ||
		got.Runner != "laptop" || got.Summary != "did it" || got.FinishedAt == nil {
		t.Fatalf("after the runner answered: %+v", got)
	}

	// Its later reports change nothing, and the engine has nothing queued
	// to send.
	report(t, fx.d, "laptop", TaskState{HubTodoID: td.ID, ID: "r1", Status: model.StatusMerged, MergedCommit: mergedSHA})
	if got := fx.get(t, td.ID); got.Status != model.StatusMerged {
		t.Errorf("after a later report: %s", got.Status)
	}
	if q := fx.st.ListTodos(p.ID, model.StatusQueued); len(q) != 0 {
		t.Errorf("still queued: %+v", q)
	}
	if n := fx.taskSends(); n != 1 {
		t.Errorf("sent %d times, want 1", n)
	}
}

// An older runner refuses the task with a plain "already merged" error:
// the hub marks it merged all the same, and the runner's next report
// brings the commit.
func TestOlderRunnersAlreadyMergedErrorEndsMerged(t *testing.T) {
	t.Parallel()

	fx, p, td := rejoining(t)
	fx.fleet.answer = func(string, Task, *Accepted) error {
		return &tunnel.CallError{Status: http.StatusBadRequest, Msg: "laptop: bad request: " + AlreadyMerged}
	}
	if _, _, err := fx.d.Dispatch(context.Background(), p, td); !errors.Is(err, queue.ErrSettled) {
		t.Fatalf("Dispatch: %v, want queue.ErrSettled", err)
	}
	if got := fx.get(t, td.ID); got.Status != model.StatusMerged || got.Waiting != "" || got.RemoteID != "r1" {
		t.Fatalf("after the runner refused: %+v", got)
	}
	report(t, fx.d, "laptop", TaskState{HubTodoID: td.ID, ID: "r1", Status: model.StatusMerged, MergedCommit: mergedSHA})
	if got := fx.get(t, td.ID); got.MergedCommit != mergedSHA {
		t.Errorf("commit after the next report: %q", got.MergedCommit)
	}

	// Any other refusal still means "try again later".
	other := fx.todo(t, p, func(x *model.Todo) { x.Runner, x.RemoteID = "laptop", "r2" })
	fx.fleet.answer = func(string, Task, *Accepted) error {
		return &tunnel.CallError{Status: http.StatusBadRequest, Msg: "laptop: bad request: hub_todo_id is required"}
	}
	var we *WaitError
	if _, _, err := fx.d.Dispatch(context.Background(), p, other); !errors.As(err, &we) {
		t.Fatalf("Dispatch: %v, want a WaitError", err)
	}
	if got := fx.get(t, other.ID); got.Status != model.StatusQueued {
		t.Errorf("after another refusal: %s", got.Status)
	}
}

// While a task waits to go back to the runner that kept running it, that
// runner's news that it reached review (and then merged) counts; the same
// news from another runner doesn't.
func TestQueuedTaskFollowsOnlyItsOwnRunnersReports(t *testing.T) {
	t.Parallel()

	fx, _, td := rejoining(t)
	review := TaskState{HubTodoID: td.ID, ID: "r1", Status: model.StatusReview, Summary: "did it", Branch: "dash/todo-r1"}
	merged := TaskState{HubTodoID: td.ID, ID: "r1", Status: model.StatusMerged, MergedCommit: mergedSHA}

	report(t, fx.d, "desk", review)
	report(t, fx.d, "desk", merged)
	if got := fx.get(t, td.ID); got.Status != model.StatusQueued || got.MergedCommit != "" || got.Summary != "" {
		t.Fatalf("another runner's report changed the task: %+v", got)
	}

	report(t, fx.d, "laptop", review)
	got := fx.get(t, td.ID)
	if got.Status != model.StatusReview || got.Summary != "did it" || got.Rejoin {
		t.Fatalf("after its runner's review report: %+v", got)
	}
	report(t, fx.d, "laptop", TaskState{HubTodoID: td.ID, ID: "r1", Status: model.StatusMerging})
	report(t, fx.d, "laptop", merged)
	if got := fx.get(t, td.ID); got.Status != model.StatusMerged || got.MergedCommit != mergedSHA {
		t.Fatalf("after its runner merged it: %+v", got)
	}
}

// A task whose runner had it in review when the hub sent it back to Claude
// is queued without Rejoin: the runner's old news of review or failure
// doesn't undo that (only a merge, which is final, does).
func TestRequeuedTaskIgnoresItsRunnersOldNews(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	fx.fleet.set(runnerOf("laptop", fx.orgA, true))
	p := fx.project(t, fx.orgA, "laptop")
	td := fx.todo(t, p, func(x *model.Todo) { x.Status, x.Runner, x.RemoteID = model.StatusInProgress, "laptop", "r1" })
	report(t, fx.d, "laptop", TaskState{HubTodoID: td.ID, ID: "r1", Status: model.StatusReview})
	if _, err := fx.st.SetStatus(td.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	for _, s := range []model.TodoStatus{model.StatusReview, model.StatusFailed, model.StatusMerging, model.StatusInProgress} {
		report(t, fx.d, "laptop", TaskState{HubTodoID: td.ID, ID: "r1", Status: s})
		if got := fx.get(t, td.ID); got.Status != model.StatusQueued {
			t.Fatalf("old news (%s) moved a requeued task to %s", s, got.Status)
		}
	}
	// Sent again, the runner takes it back to Claude: no Rejoin.
	var sent Task
	fx.fleet.answer = func(_ string, task Task, a *Accepted) error { sent, a.ID, a.Resumed = task, "r1", true; return nil }
	if _, _, err := fx.d.Dispatch(context.Background(), p, fx.get(t, td.ID)); err != nil {
		t.Fatal(err)
	}
	if sent.Rejoin {
		t.Error("a requeued task was sent as Rejoin")
	}
}
