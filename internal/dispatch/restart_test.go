package dispatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

// merging is a task of p that laptop is merging.
func merging(t *testing.T, fx *fixture, p model.Project) model.Todo {
	t.Helper()
	td := fx.todo(t, p, func(x *model.Todo) { x.Status, x.Runner, x.RemoteID = model.StatusInProgress, "laptop", "r1" })
	td, err := fx.st.SetStatus(td.ID, model.StatusMerging, "")
	if err != nil {
		t.Fatal(err)
	}
	return td
}

func waitStatus(t *testing.T, fx *fixture, id string, not model.TodoStatus) model.Todo {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for fx.get(t, id).Status == not && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return fx.get(t, id)
}

// A task merging on a runner that never comes back after a hub restart
// doesn't stay merging forever: it fails, saying to approve it again, and
// the runner's report, when it does come back, still moves it on.
func TestMergingTaskOfARunnerThatStaysAwayFails(t *testing.T) {
	fx := newFixture(t)
	fx.fleet.set(runnerOf("laptop", fx.orgA, false))
	p := fx.project(t, fx.orgA, "laptop")
	td := merging(t, fx, p)

	fx.d.Start() // the hub just started; laptop isn't connected
	got := waitStatus(t, fx, td.ID, model.StatusMerging)
	if got.Status != model.StatusFailed || !strings.HasPrefix(got.Error, InterruptedPrefix) ||
		!strings.Contains(got.Error, "merging") || got.Resumable || got.Runner != "laptop" {
		t.Fatalf("merging task of a runner that stayed away: %+v", got)
	}

	// The runner was merging all along, and finishes.
	fx.fleet.set(runnerOf("laptop", fx.orgA, true))
	report(t, fx.d, "laptop", TaskState{HubTodoID: td.ID, ID: "r1", Status: model.StatusMerging})
	if got := fx.get(t, td.ID); got.Status != model.StatusMerging || got.Error != "" {
		t.Fatalf("after the runner reported it merging: %s %q", got.Status, got.Error)
	}
	report(t, fx.d, "laptop", TaskState{HubTodoID: td.ID, ID: "r1", Status: model.StatusMerged, MergedCommit: "abc123"})
	if got := fx.get(t, td.ID); got.Status != model.StatusMerged || got.MergedCommit != "abc123" {
		t.Fatalf("after the runner reported it merged: %s %q", got.Status, got.MergedCommit)
	}
}

// A runner that goes offline mid-merge, while the hub runs, fails it the
// same way after the grace; a blip doesn't.
func TestRunnerGoingOfflineMidMerge(t *testing.T) {
	fx := newFixture(t)
	fx.fleet.set(runnerOf("laptop", fx.orgA, true))
	p := fx.project(t, fx.orgA, "laptop")
	td := merging(t, fx, p)

	fx.d.State("laptop", false)
	fx.d.State("laptop", true)
	time.Sleep(80 * time.Millisecond)
	if got := fx.get(t, td.ID); got.Status != model.StatusMerging {
		t.Fatalf("after a blip: %s %q", got.Status, got.Error)
	}
	fx.fleet.set(runnerOf("laptop", fx.orgA, false))
	fx.d.State("laptop", false)
	if got := waitStatus(t, fx, td.ID, model.StatusMerging); got.Status != model.StatusFailed {
		t.Fatalf("after going away mid-merge: %+v", got)
	}
}

// Revoking a runner fails its merging tasks too, to start over elsewhere.
func TestRevokedRunnersMergingTaskFails(t *testing.T) {
	fx := newFixture(t)
	fx.fleet.set(runnerOf("laptop", fx.orgA, true))
	p := fx.project(t, fx.orgA, "laptop")
	td := merging(t, fx, p)
	fx.fleet.remove("laptop")
	fx.d.Revoked("laptop")
	if got := fx.get(t, td.ID); got.Status != model.StatusFailed || got.Runner != "" || got.Resumable {
		t.Fatalf("merging task of a revoked runner: %+v", got)
	}
}

// The hub stopped after a runner took a task but before it recorded that:
// still queued here, the task is the runner's once it reports it running,
// rather than being sent again (to another runner, or from scratch).
func TestTaskTakenJustBeforeTheHubStoppedIsAdopted(t *testing.T) {
	fx := newFixture(t)
	fx.fleet.set(runnerOf("laptop", fx.orgA, true))
	fx.fleet.set(runnerOf("desk", fx.orgA, true))
	fx.fleet.set(runnerOf("other", fx.orgA, true)) // same org, not linked
	p := fx.project(t, fx.orgA, "laptop", "desk")
	td := fx.todo(t, p, nil)

	// Not by a runner the project isn't linked to, nor with old news.
	report(t, fx.d, "other", TaskState{HubTodoID: td.ID, ID: "o1", Status: model.StatusInProgress})
	report(t, fx.d, "laptop", TaskState{HubTodoID: td.ID, ID: "r1", Status: model.StatusReview})
	if got := fx.get(t, td.ID); got.Status != model.StatusQueued || got.Runner != "" {
		t.Fatalf("adopted by the wrong report: %+v", got)
	}

	report(t, fx.d, "laptop", TaskState{HubTodoID: td.ID, ID: "r1", Status: model.StatusInProgress})
	got := fx.get(t, td.ID)
	if got.Status != model.StatusInProgress || got.Runner != "laptop" || got.RemoteID != "r1" || got.StartedAt == nil {
		t.Fatalf("after the runner reported it: %+v", got)
	}
	if fx.d.Busy("laptop") != 1 {
		t.Errorf("laptop's load %d, want 1", fx.d.Busy("laptop"))
	}
	// The other runner can't take it over now.
	report(t, fx.d, "desk", TaskState{HubTodoID: td.ID, ID: "d1", Status: model.StatusInProgress})
	if got := fx.get(t, td.ID); got.Runner != "laptop" {
		t.Errorf("taken over by desk: %+v", got)
	}
	report(t, fx.d, "laptop", TaskState{HubTodoID: td.ID, ID: "r1", Status: model.StatusReview, Summary: "done"})
	if got := fx.get(t, td.ID); got.Status != model.StatusReview {
		t.Errorf("after it finished: %s", got.Status)
	}
	// Dispatch afterwards goes on as usual.
	if _, _, err := fx.d.Dispatch(context.Background(), p, fx.todo(t, p, nil)); err != nil {
		t.Fatal(err)
	}
}
