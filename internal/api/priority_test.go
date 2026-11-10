package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// A task's priority is optional: created without one it is null, PATCH
// leaves it alone when absent, sets it (0 included) and clears it with null.
func TestTodoPriorityIsOptional(t *testing.T) {
	s, _, p := boardServer(t)
	h := loggedIn(t, s.Routes())
	decodeTodo := func(body []byte) model.Todo {
		t.Helper()
		var td model.Todo
		if err := json.Unmarshal(body, &td); err != nil {
			t.Fatal(err, string(body))
		}
		return td
	}
	w := do(t, h, "POST", "/api/projects/"+p.ID+"/todos", `{"title":"x"}`)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	td := decodeTodo(w.Body.Bytes())
	if td.Priority != nil {
		t.Fatalf("created with priority %d, want none", *td.Priority)
	}
	stored := func() *int {
		got, err := raw(s).GetTodo(td.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.Priority
	}
	for _, c := range []struct {
		body string
		want *int
	}{
		{`{"priority":0}`, new(0)},
		{`{"title":"y"}`, new(0)}, // absent: kept
		{`{"priority":4}`, new(4)},
		{`{"priority":null}`, nil},
		{`{"title":"z"}`, nil},
	} {
		if w := do(t, h, "PATCH", "/api/todos/"+td.ID, c.body); w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", c.body, w.Code, w.Body)
		}
		got := stored()
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("after %s: priority %v, want %v", c.body, got, c.want)
		}
	}
	if w := do(t, h, "PATCH", "/api/todos/"+td.ID, `{"priority":"high"}`); w.Code != http.StatusBadRequest {
		t.Errorf("non-number priority: %d %s", w.Code, w.Body)
	}
	w = do(t, h, "POST", "/api/projects/"+p.ID+"/todos", `{"title":"first","priority":0}`)
	if td := decodeTodo(w.Body.Bytes()); td.Priority == nil || *td.Priority != 0 {
		t.Errorf("created with priority 0: %v", td.Priority)
	}
}
