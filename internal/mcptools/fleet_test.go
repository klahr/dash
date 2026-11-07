package mcptools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// fakeAPI is one computer ("" prefix) holding one task, recording calls.
type fakeAPI struct{ calls []string }

func (f *fakeAPI) Call(method, path string, body, out any) error {
	b, _ := json.Marshal(body)
	f.calls = append(f.calls, method+" "+path+" "+string(b))
	switch {
	case path == "/api/machines":
		*out.(*[]machine) = []machine{{Name: "here", OK: true}}
	case strings.HasPrefix(path, "/api/todos/t1") && out != nil:
		*out.(*model.Todo) = model.Todo{ID: "t1", Status: model.StatusDraft}
	}
	return nil
}

func TestFleetUpdateTodoSendsOnlyGivenFields(t *testing.T) {
	api := &fakeAPI{}
	f := Fleet{API: api}
	title := "New"
	if _, err := f.UpdateTodo("t1", TodoUpdate{Title: &title, DependsOn: &[]string{}}); err != nil {
		t.Fatal(err)
	}
	last := api.calls[len(api.calls)-1]
	if last != `PATCH /api/todos/t1 {"depends_on":[],"title":"New"}` {
		t.Errorf("sent %s", last)
	}
	if err := f.DeleteTodo("t1"); err != nil {
		t.Fatal(err)
	}
	if last := api.calls[len(api.calls)-1]; !strings.HasPrefix(last, "DELETE /api/todos/t1") {
		t.Errorf("sent %s", last)
	}
}

func TestFleetToolsNamesUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, tl := range FleetTools(Fleet{}) {
		if seen[tl.Name] {
			t.Errorf("duplicate tool %s", tl.Name)
		}
		seen[tl.Name] = true
	}
	if !seen["update_todo"] || !seen["delete_todo"] {
		t.Errorf("tools %v", seen)
	}
}

func TestArgsOptList(t *testing.T) {
	var a Args
	_ = json.Unmarshal([]byte(`{"x":[],"y":["a"],"n":null}`), &a)
	if l := a.OptList("x"); l == nil || len(*l) != 0 || *l == nil {
		t.Errorf("empty: %v", l)
	}
	if l := a.OptList("y"); l == nil || len(*l) != 1 {
		t.Errorf("one: %v", l)
	}
	if a.OptList("missing") != nil || a.OptList("n") != nil {
		t.Error("missing/null should be nil")
	}
}
