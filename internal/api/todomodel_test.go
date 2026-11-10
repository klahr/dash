package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// A task's model is set on create, changed and cleared with PATCH, kept by
// other edits and duplicates, and checked: it can't pass as a claude flag.
func TestTodoModel(t *testing.T) {
	s, _, p := boardServer(t)
	h := loggedIn(t, s.Routes())
	w := do(t, h, "POST", "/api/projects/"+p.ID+"/todos", `{"title":"x","model":" opus "}`)
	var td model.Todo
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &td) != nil || td.Model != "opus" {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	stored := func(id string) string {
		t.Helper()
		got, err := raw(s).GetTodo(id)
		if err != nil {
			t.Fatal(err)
		}
		return got.Model
	}
	for _, c := range []struct{ body, want string }{
		{`{"model":"sonnet"}`, "sonnet"},
		{`{"title":"y"}`, "sonnet"},
		{`{"model":"claude-haiku-5-5"}`, "claude-haiku-5-5"},
		{`{"model":""}`, ""},
	} {
		if w := do(t, h, "PATCH", "/api/todos/"+td.ID, c.body); w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", c.body, w.Code, w.Body)
		}
		if got := stored(td.ID); got != c.want {
			t.Errorf("after %s: model %q, want %q", c.body, got, c.want)
		}
	}
	if w := do(t, h, "PATCH", "/api/todos/"+td.ID, `{"model":"--dangerously-skip-permissions"}`); w.Code != http.StatusBadRequest {
		t.Errorf("flag as model: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "POST", "/api/projects/"+p.ID+"/todos", `{"title":"x","model":"a b"}`); w.Code != http.StatusBadRequest {
		t.Errorf("create with a bad model: %d %s", w.Code, w.Body)
	}
	do(t, h, "PATCH", "/api/todos/"+td.ID, `{"model":"haiku"}`)
	w = do(t, h, "POST", "/api/todos/"+td.ID+"/duplicate", "")
	var dup model.Todo
	if json.Unmarshal(w.Body.Bytes(), &dup) != nil || dup.Model != "haiku" {
		t.Errorf("duplicate: %d %s", w.Code, w.Body)
	}
}
