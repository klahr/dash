package mcptools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/usage"
)

// startBoard is a restartBoard that can also start a task now; while pace
// is set its queue waits for the weekly pace.
type startBoard struct {
	restartBoard
	pace string
}

func (b *startBoard) StartNow(id string) (model.Todo, error) {
	b.calls = append(b.calls, "start_now")
	b.t.StartNow = true
	return b.t, nil
}

func (b *startBoard) PaceWait(t model.Todo) string {
	if t.Status != model.StatusQueued || t.StartNow {
		return ""
	}
	return b.pace
}

// asJSON is a tool's answer as JSON fields.
func asJSON(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return m
}

func TestQueueTodoStartNow(t *testing.T) {
	b := &startBoard{restartBoard: restartBoard{t: model.Todo{ID: "t1", Status: model.StatusDraft}}, pace: "Waiting 1h 20m to stay on pace"}
	tools := Tools(b, Options{})

	// A draft: queued, then started now.
	out, err := runTool(t, tools, "queue_todo", `{"todo":"t1","start_now":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(b.calls, "; "); got != "queue; start_now" {
		t.Errorf("draft: %s", got)
	}
	if m := asJSON(t, out); m["status"] != "queued" || m["start_now"] != true {
		t.Errorf("draft: answered %v", m)
	}

	// Already queued: without start_now it is refused, as before.
	b.t, b.calls = model.Todo{ID: "t1", Status: model.StatusQueued}, nil
	if _, err := runTool(t, tools, "queue_todo", `{"todo":"t1"}`); err == nil || !strings.Contains(err.Error(), "only draft and failed") {
		t.Errorf("queued without start_now: %v", err)
	}
	// get_todo and list_todos say it waits for the pace.
	out, err = runTool(t, tools, "get_todo", `{"todo":"t1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if m := asJSON(t, out); m["waiting"] != "pace" || m["pace_reason"] != b.pace || m["id"] != "t1" {
		t.Errorf("get_todo while pacing: %v", m)
	}
	// With start_now, only the flag is set.
	if _, err := runTool(t, tools, "queue_todo", `{"todo":"t1","start_now":true}`); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(b.calls, "; "); got != "start_now" {
		t.Errorf("queued: %s", got)
	}
	out, _ = runTool(t, tools, "get_todo", `{"todo":"t1"}`)
	if m := asJSON(t, out); m["start_now"] != true || m["waiting"] != nil {
		t.Errorf("get_todo after start_now: %v", m)
	}

	// Running: refused.
	b.t, b.calls = model.Todo{ID: "t1", Status: model.StatusInProgress}, nil
	if _, err := runTool(t, tools, "queue_todo", `{"todo":"t1","start_now":true}`); err == nil || len(b.calls) != 0 {
		t.Errorf("running: %v, calls %v", err, b.calls)
	}
}

func TestListTodosShowsThePaceWait(t *testing.T) {
	b := &startBoard{restartBoard: restartBoard{t: model.Todo{ID: "t1", Status: model.StatusQueued}}, pace: "Waiting 5m to stay on pace"}
	b.Board = todosBoard{todos: []model.Todo{
		{ID: "t1", Status: model.StatusQueued},
		{ID: "t2", Status: model.StatusQueued, StartNow: true},
		{ID: "t3", Status: model.StatusDraft},
	}}
	out, err := runTool(t, Tools(b, Options{Project: "p"}), "list_todos", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0]["waiting"] != "pace" || rows[1]["waiting"] != nil || rows[1]["start_now"] != true || rows[2]["waiting"] != nil {
		t.Errorf("rows: %s", raw)
	}
}

// todosBoard is a Board with one project's tasks.
type todosBoard struct {
	Board
	todos []model.Todo
}

func (b todosBoard) Project(ref string) (model.Project, error) { return model.Project{ID: ref}, nil }
func (b todosBoard) Todos(string) ([]model.Todo, error)        { return b.todos, nil }

// paceAPI is a hub with one computer, whose queue waits for the weekly
// pace, and one task, t1.
type paceAPI struct {
	calls []string
	t     model.Todo
}

func (f *paceAPI) Call(method, path string, body, out any) error {
	f.calls = append(f.calls, method+" "+path)
	switch {
	case path == "/api/machines":
		*out.(*[]machine) = []machine{{Name: "hub", OK: true}}
	case path == "/api/usage":
		*out.(*usage.Snapshot) = usage.Snapshot{Paused: true, Pacing: true, Reason: "Waiting 2h to stay on pace"}
	case path == "/api/todos/t1/enqueue":
		f.t.Status = model.StatusQueued
	case path == "/api/todos/t1/start-now":
		f.t.StartNow = true
	case path == "/api/todos/t1":
		*out.(*model.Todo) = f.t
	}
	return nil
}

// The fleet tools (the hub's /mcp and dash-mcp) start a task now through
// the hub's API, and tell from its usage that a queued task waits for the
// pace.
func TestFleetStartNow(t *testing.T) {
	api := &paceAPI{t: model.Todo{ID: "t1", Status: model.StatusDraft}}
	tools := FleetTools(Fleet{API: api})

	if _, err := runTool(t, tools, "queue_todo", `{"todo":"t1"}`); err != nil {
		t.Fatal(err)
	}
	out, err := runTool(t, tools, "get_todo", `{"todo":"t1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if m := asJSON(t, out); m["waiting"] != "pace" || m["pace_reason"] != "Waiting 2h to stay on pace" {
		t.Errorf("get_todo: %v", m)
	}
	api.calls = nil
	out, err = runTool(t, tools, "queue_todo", `{"todo":"t1","start_now":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if m := asJSON(t, out); m["start_now"] != true {
		t.Errorf("queue_todo: %v", m)
	}
	if !strings.Contains(strings.Join(api.calls, "; "), "POST /api/todos/t1/start-now") {
		t.Errorf("calls: %v", api.calls)
	}
	if strings.Contains(strings.Join(api.calls, "; "), "enqueue") {
		t.Errorf("an already queued task was queued again: %v", api.calls)
	}
}
