package mcptools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

func TestInitializeNegotiatesVersion(t *testing.T) {
	s := &Server{Name: "t"}
	for asked, want := range map[string]string{
		"2025-06-18": "2025-06-18",
		"2024-11-05": "2024-11-05",
		"2099-01-01": ProtocolVersions[0], // unknown: answer ours, don't echo
		"":           ProtocolVersions[0],
	} {
		res, rerr := s.Handle(Request{Method: "initialize", Params: []byte(`{"protocolVersion":"` + asked + `"}`)})
		if rerr != nil {
			t.Fatal(rerr)
		}
		if got := res.(map[string]any)["protocolVersion"]; got != want {
			t.Errorf("asked %q: got %v, want %s", asked, got, want)
		}
	}
}

func TestHTTPRejectsUnsupportedVersionHeader(t *testing.T) {
	s := &Server{Name: "t"}
	for v, want := range map[string]int{"": http.StatusOK, "2025-06-18": http.StatusOK, "1999-01-01": http.StatusBadRequest} {
		r := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		if v != "" {
			r.Header.Set("MCP-Protocol-Version", v)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("version %q: %d", v, w.Code)
		}
	}
}

// restartBoard is a Board and Restarter with one task, recording what the
// tools ask of it.
type restartBoard struct {
	Board
	t     model.Todo
	calls []string
}

func (b *restartBoard) Todo(id string) (model.Todo, error) {
	if id != b.t.ID {
		return model.Todo{}, fmt.Errorf("no task %q", id)
	}
	return b.t, nil
}

func (b *restartBoard) QueueTodo(id string) (model.Todo, error) {
	b.calls = append(b.calls, "queue")
	b.t.Status = model.StatusQueued
	return b.t, nil
}

func (b *restartBoard) UpdateTodo(id string, u TodoUpdate) (model.Todo, error) {
	b.calls = append(b.calls, "update")
	if u.Prompt != nil {
		b.t.Prompt = *u.Prompt
	}
	return b.t, nil
}

func (b *restartBoard) StopTodo(id string) (model.Todo, error) {
	b.calls = append(b.calls, "stop")
	b.t.Status, b.t.Error = model.StatusFailed, "canceled"
	return b.t, nil
}

func (b *restartBoard) RequeueTodo(id string, fresh bool, note string) (model.Todo, error) {
	b.calls = append(b.calls, fmt.Sprintf("requeue fresh=%v note=%q", fresh, note))
	b.t.Status = model.StatusQueued
	return b.t, nil
}

func runTool(t *testing.T, tools []Tool, name, args string) (any, error) {
	t.Helper()
	for _, tl := range tools {
		if tl.Name == name {
			var a Args
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				t.Fatal(err)
			}
			return tl.Run(a)
		}
	}
	t.Fatalf("no tool %s", name)
	return nil, nil
}

func TestRestartTools(t *testing.T) {
	running := model.Todo{ID: "t1", Status: model.StatusInProgress, SessionID: "s"}
	b := &restartBoard{t: running}
	tools := Tools(b, Options{})

	// Running, without fresh: stopped, new prompt saved and sent as a note.
	if _, err := runTool(t, tools, "restart_todo", `{"todo":"t1","prompt":"v2"}`); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(b.calls, "; "); !strings.HasPrefix(got, "stop; update; requeue fresh=false note=") || !strings.Contains(got, "v2") {
		t.Errorf("restart: %s", got)
	}
	// fresh: no note, the prompt is the task's.
	b.t, b.calls = running, nil
	if _, err := runTool(t, tools, "restart_todo", `{"todo":"t1","prompt":"v3","fresh":true}`); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(b.calls, "; "); got != `stop; update; requeue fresh=true note=""` {
		t.Errorf("restart fresh: %s", got)
	}
	// Failed: no stop; queue_todo retries it, fresh or not.
	b.t, b.calls = model.Todo{ID: "t1", Status: model.StatusFailed}, nil
	if _, err := runTool(t, tools, "restart_todo", `{"todo":"t1"}`); err != nil {
		t.Fatal(err)
	}
	b.t.Status = model.StatusFailed
	if _, err := runTool(t, tools, "queue_todo", `{"todo":"t1"}`); err != nil {
		t.Fatal(err)
	}
	b.t.Status = model.StatusFailed
	if _, err := runTool(t, tools, "queue_todo", `{"todo":"t1","fresh":true}`); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(b.calls, "; "); got != `requeue fresh=false note=""; queue; requeue fresh=true note=""` {
		t.Errorf("failed task: %s", got)
	}

	// Refused: nothing asked of the board.
	for _, c := range []struct {
		tool string
		st   model.TodoStatus
	}{
		{"stop_todo", model.StatusMerged}, {"stop_todo", model.StatusFailed}, {"restart_todo", model.StatusReview},
		{"restart_todo", model.StatusDraft}, {"queue_todo", model.StatusReview}, {"queue_todo", model.StatusInProgress},
	} {
		b.t, b.calls = model.Todo{ID: "t1", Status: c.st}, nil
		if _, err := runTool(t, tools, c.tool, `{"todo":"t1"}`); err == nil || !strings.Contains(err.Error(), string(c.st)) || len(b.calls) != 0 {
			t.Errorf("%s of a %s task: %v %v", c.tool, c.st, err, b.calls)
		}
	}

	// A board that can't stop tasks gets neither tool, nor fresh.
	for _, tl := range Tools(struct{ Board }{b}, Options{}) {
		if tl.Name == "stop_todo" || tl.Name == "restart_todo" {
			t.Errorf("%s without a Restarter", tl.Name)
		}
		if _, ok := tl.schema()["properties"].(map[string]any)["fresh"]; ok && tl.Name == "queue_todo" {
			t.Error("queue_todo has fresh without a Restarter")
		}
	}
}
