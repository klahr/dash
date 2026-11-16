package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// A task's runner is set on create, changed and cleared with PATCH, kept by
// duplicates, and must be one of the project's runners.
func TestTodoRunOn(t *testing.T) {
	s, _, p := boardServer(t)
	if _, err := raw(s).UpdateProject(p.ID, func(x *model.Project) error {
		x.Runners = []string{"r1", "r2"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h := loggedIn(t, s.Routes())
	w := do(t, h, "POST", "/api/projects/"+p.ID+"/todos", `{"title":"x","run_on":" r1 "}`)
	var td model.Todo
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &td) != nil || td.RunOn != "r1" {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	stored := func(id string) string {
		t.Helper()
		got, err := raw(s).GetTodo(id)
		if err != nil {
			t.Fatal(err)
		}
		return got.RunOn
	}
	for _, c := range []struct{ body, want string }{
		{`{"run_on":"r2"}`, "r2"},
		{`{"title":"y"}`, "r2"},
		{`{"run_on":""}`, ""},
		{`{"run_on":"r1"}`, "r1"},
	} {
		if w := do(t, h, "PATCH", "/api/todos/"+td.ID, c.body); w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", c.body, w.Code, w.Body)
		}
		if got := stored(td.ID); got != c.want {
			t.Errorf("after %s: run_on %q, want %q", c.body, got, c.want)
		}
	}
	if w := do(t, h, "PATCH", "/api/todos/"+td.ID, `{"run_on":"elsewhere"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a runner not linked to the project: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "POST", "/api/projects/"+p.ID+"/todos", `{"title":"x","run_on":"elsewhere"}`); w.Code != http.StatusBadRequest {
		t.Errorf("create with a runner not linked to the project: %d %s", w.Code, w.Body)
	}
	w = do(t, h, "POST", "/api/todos/"+td.ID+"/duplicate", "")
	var dup model.Todo
	if json.Unmarshal(w.Body.Bytes(), &dup) != nil || dup.RunOn != "r1" {
		t.Errorf("duplicate: %d %s", w.Code, w.Body)
	}

	// Only while it is a draft, queued or failed.
	for _, st := range []model.TodoStatus{model.StatusQueued, model.StatusInProgress} {
		if _, err := raw(s).SetStatus(td.ID, st, ""); err != nil {
			t.Fatal(err)
		}
	}
	if w := do(t, h, "PATCH", "/api/todos/"+td.ID, `{"run_on":"r2"}`); w.Code != http.StatusBadRequest {
		t.Errorf("change while running: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "PATCH", "/api/todos/"+td.ID, `{"run_on":"r1"}`); w.Code != http.StatusOK {
		t.Errorf("unchanged while running: %d %s", w.Code, w.Body)
	}
}
