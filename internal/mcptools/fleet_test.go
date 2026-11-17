package mcptools

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

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

// countingBoard is one computer's board, counting the calls it gets.
type countingBoard struct {
	Computer
	mu       sync.Mutex
	projects []model.Project
	todos    map[string]model.Todo
	calls    map[string]int
}

func (b *countingBoard) count(what string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.calls == nil {
		b.calls = map[string]int{}
	}
	b.calls[what]++
}

func (b *countingBoard) n(what string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[what]
}

func (b *countingBoard) Projects() ([]model.Project, error) {
	b.count("projects")
	return b.projects, nil
}

func (b *countingBoard) Todo(id string) (model.Todo, error) {
	b.count("todo")
	if t, ok := b.todos[id]; ok {
		return t, nil
	}
	return model.Todo{}, errors.New("not found")
}

func (b *countingBoard) Todos(string) ([]model.Todo, error) {
	b.count("todos")
	var ts []model.Todo
	for _, t := range b.todos {
		ts = append(ts, t)
	}
	return ts, nil
}

func (b *countingBoard) PaceWait(model.Todo) string { b.count("pace"); return "" }

func (b *countingBoard) Text(string, string) (string, error) { b.count("text"); return "diff", nil }

func TestFleetRemembersWhereThingsLive(t *testing.T) {
	a := &countingBoard{projects: []model.Project{{ID: "pa", Name: "A"}}, todos: map[string]model.Todo{"ta": {ID: "ta"}}}
	b := &countingBoard{projects: []model.Project{{ID: "pb", Name: "B"}}, todos: map[string]model.Todo{"tb": {ID: "tb", Status: model.StatusQueued}}}
	listed := 0
	now := time.Unix(0, 0)
	f := Fleet{Computers: func() ([]Machine, error) {
		listed++
		return []Machine{{Name: "a", OK: true, Board: a}, {Name: "b", OK: true, Board: b}}, nil
	}, cache: &fleetCache{now: func() time.Time { return now }}}

	// get_todo: the task, then its pace, asks only its computer the second time.
	tt, err := f.Todo("tb")
	if err != nil || tt.ID != "tb" {
		t.Fatalf("Todo = %v, %v", tt, err)
	}
	// The search returns as soon as b has the task; a's lookup may still be on
	// its way. Wait for it, so the counts below don't depend on scheduling.
	for deadline := time.Now().Add(5 * time.Second); a.n("todo") == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("a was never asked for the task")
		}
	}
	f.PaceWait(tt)
	if _, err := f.Text("tb", "diff", 0, false); err != nil {
		t.Fatal(err)
	}
	if got := b.n("todo"); got != 1 {
		t.Errorf("b asked for the task %d times, want 1", got)
	}
	if b.n("pace") != 1 || b.n("text") != 1 || a.n("pace") != 0 || a.n("text") != 0 {
		t.Errorf("calls a %v, b %v", a.calls, b.calls)
	}

	// A project found once is found again without asking every computer.
	for range 3 {
		if _, err := f.Todos("B"); err != nil {
			t.Fatal(err)
		}
	}
	if a.n("projects") != 1 || b.n("projects") != 1 || b.n("todos") != 3 {
		t.Errorf("calls a %v, b %v", a.calls, b.calls)
	}
	// list_todos' tasks are remembered too: no search for ta's computer.
	if _, err := f.Todos("A"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Text("ta", "log", 0, true); err != nil {
		t.Fatal(err)
	}
	if a.n("todo") != 1 || b.n("todo") != 1 { // a: once, in the first search for tb
		t.Errorf("calls a %v, b %v", a.calls, b.calls)
	}
	if listed != 1 {
		t.Errorf("computers listed %d times, want 1", listed)
	}

	// list_projects always asks; the list expires after fleetTTL.
	if _, _, err := f.Projects(); err != nil {
		t.Fatal(err)
	}
	if a.n("projects") != 2 {
		t.Errorf("list_projects used the cache: %v", a.calls)
	}
	now = now.Add(fleetTTL)
	if _, err := f.Todos("A"); err != nil {
		t.Fatal(err)
	}
	if listed != 2 || a.n("projects") != 3 {
		t.Errorf("after the TTL: listed %d, a %v", listed, a.calls)
	}

	// An unknown project is looked for afresh before failing.
	if _, err := f.Todos("C"); err == nil {
		t.Error("found project C")
	}
	if a.n("projects") != 4 {
		t.Errorf("unknown project not asked afresh: %v", a.calls)
	}
}

func TestFleetWithoutCacheAsksEveryTime(t *testing.T) {
	a := &countingBoard{todos: map[string]model.Todo{"ta": {ID: "ta"}}}
	f := Fleet{Computers: func() ([]Machine, error) {
		return []Machine{{Name: "a", OK: true, Board: a}}, nil
	}}
	f.Todo("ta")
	f.Todo("ta")
	if a.n("todo") != 2 {
		t.Errorf("asked %d times", a.n("todo"))
	}
	if _, err := f.Todo("nope"); err == nil || !strings.Contains(err.Error(), "no task") {
		t.Errorf("err = %v", err)
	}
}
