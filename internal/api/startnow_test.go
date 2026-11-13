package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
	"github.com/klahr/dash/internal/usage"
)

// fields is a tool's JSON answer.
func fields(t *testing.T, text string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return m
}

// On a Dash that runs its own tasks and waits for the weekly pace, the
// chat's queue_todo starts a task now, and get_todo says why the others
// wait.
func TestBoardMCPStartNow(t *testing.T) {
	parallel(t)

	s, _, p := boardServer(t)
	s.Usage = usage.New(nil)
	payload, _ := json.Marshal(map[string]any{"status": "allowed", "unifiedWindows": map[string]any{
		"seven_day": map[string]any{"utilization": 0.9, "resetsAt": time.Now().Add(usage.WeekSpan / 2).Unix(), "status": "allowed"},
	}})
	s.Usage.Observe(payload)
	if err := s.Usage.SetPace(usage.PaceSettings{Follow: true}); err != nil {
		t.Fatal(err)
	}
	h := s.Routes()
	token, release := s.Board.Register(chatCaller(t, s), p.ID)
	defer release()

	queuedTask := withStatus(t, s, p.ID, model.StatusQueued)
	text, isErr := call(t, h, token, "get_todo", `{"todo":"`+queuedTask.ID+`"}`)
	if m := fields(t, text); isErr || m["waiting"] != "pace" || !strings.Contains(m["pace_reason"].(string), "to stay on pace") {
		t.Errorf("get_todo: %s", text)
	}
	if text, isErr := call(t, h, token, "queue_todo", `{"todo":"`+queuedTask.ID+`"}`); !isErr || !strings.Contains(text, "only draft and failed") {
		t.Errorf("queued again without start_now: %s", text)
	}
	if text, isErr := call(t, h, token, "queue_todo", `{"todo":"`+queuedTask.ID+`","start_now":true}`); isErr || fields(t, text)["start_now"] != true {
		t.Fatalf("start_now on a queued task: %s", text)
	}
	got, _ := raw(s).GetTodo(queuedTask.ID)
	if !got.StartNow || got.Status != model.StatusQueued {
		t.Errorf("stored %+v", got)
	}
	text, _ = call(t, h, token, "get_todo", `{"todo":"`+queuedTask.ID+`"}`)
	if m := fields(t, text); m["start_now"] != true || m["waiting"] != nil {
		t.Errorf("get_todo after start_now: %s", text)
	}

	draft, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "Now"})
	if err != nil {
		t.Fatal(err)
	}
	if text, isErr := call(t, h, token, "queue_todo", `{"todo":"`+draft.ID+`","start_now":true}`); isErr {
		t.Fatalf("start_now on a draft: %s", text)
	}
	if got, _ := raw(s).GetTodo(draft.ID); got.Status != model.StatusQueued || !got.StartNow {
		t.Errorf("draft started now: %+v", got)
	}
}

// pacedRunner is a hub's fleet with one online runner whose queue waits for
// the weekly pace.
type pacedRunner struct{ r tunnel.Runner }

func (f pacedRunner) Get(id string) (tunnel.Runner, bool) { return f.r, id == f.r.ID }
func (f pacedRunner) Call(context.Context, string, string, string, any, any) error {
	return nil
}

// On the hub, a chat (here one the hub runs on a runner) starts a hub task
// now, and get_todo tells from the project's runners that it waits for the
// pace.
func TestHubBoardStartNow(t *testing.T) {
	parallel(t)

	hub, runner, creds, linked, _ := hubWithRunner(t)
	owner := testOwner(t, hub)
	hub.Dispatch = &dispatch.Dispatcher{Store: raw(hub), Fleet: pacedRunner{tunnel.Runner{ID: creds.ID, Name: "laptop", Online: true, Parallel: 2,
		Owner:    &store.RunnerOwner{UserID: owner.User, Org: store.Org{ID: owner.Org}},
		Reported: &tunnel.Reported{Usage: &usage.Snapshot{Paused: true, Pacing: true, Reason: "Waiting 2h to stay on pace"}}}}}
	h := runner.BoardMCP()
	token, release := runner.Board.RegisterHub(chatCaller(t, runner), linked.ID)
	defer release()

	td := withStatus(t, hub, linked.ID, model.StatusQueued)
	text, isErr := call(t, h, token, "get_todo", `{"todo":"`+td.ID+`"}`)
	if m := fields(t, text); isErr || m["waiting"] != "pace" || !strings.Contains(m["pace_reason"].(string), "laptop has paused its queue (Waiting 2h to stay on pace)") {
		t.Errorf("get_todo: %s", text)
	}
	text, isErr = call(t, h, token, "list_todos", `{}`)
	if isErr || !strings.Contains(text, `"waiting": "pace"`) {
		t.Errorf("list_todos: %s", text)
	}
	if text, isErr := call(t, h, token, "queue_todo", `{"todo":"`+td.ID+`","start_now":true}`); isErr {
		t.Fatalf("queue_todo start_now: %s", text)
	}
	if got, _ := raw(hub).GetTodo(td.ID); !got.StartNow || got.Status != model.StatusQueued {
		t.Errorf("hub task %+v", got)
	}
	if n := len(raw(runner).ListTodos("")); n != 0 {
		t.Errorf("the runner's own board got %d tasks", n)
	}
}
