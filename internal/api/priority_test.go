package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// A task's priority goes in as a number or as the level the board's forms
// show: "high" is 0, "urgent" -1, and "none" (like null) none at all.
func TestTaskPriorityLevels(t *testing.T) {
	parallel(t)
	s, _, p := boardServer(t)
	h := loggedIn(t, s.Routes())
	prio := func(body []byte) *int {
		t.Helper()
		var td model.Todo
		if err := json.Unmarshal(body, &td); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		return td.Priority
	}
	str := func(n *int) string {
		if n == nil {
			return "none"
		}
		b, _ := json.Marshal(*n)
		return string(b)
	}

	for body, want := range map[string]string{
		`{"title":"a","priority":"urgent"}`: "-1",
		`{"title":"a","priority":"High"}`:   "0",
		`{"title":"a","priority":"none"}`:   "none",
		`{"title":"a","priority":3}`:        "3",
		`{"title":"a","priority":null}`:     "none",
		`{"title":"a"}`:                     "none",
	} {
		w := do(t, h, "POST", "/api/projects/"+p.ID+"/todos", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
		if got := str(prio(w.Body.Bytes())); got != want {
			t.Errorf("%s: priority %s, want %s", body, got, want)
		}
	}
	for _, body := range []string{`{"title":"a","priority":"soon"}`, `{"title":"a","priority":1.5}`, `{"title":"a","priority":true}`} {
		if w := do(t, h, "POST", "/api/projects/"+p.ID+"/todos", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", body, w.Code, w.Body)
		}
	}

	w := do(t, h, "POST", "/api/projects/"+p.ID+"/todos", `{"title":"b"}`)
	var td model.Todo
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &td) != nil {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	for _, c := range []struct{ body, want string }{
		{`{"priority":"urgent"}`, "-1"},
		{`{"title":"b2"}`, "-1"}, // left out: kept
		{`{"priority":"none"}`, "none"},
		{`{"priority":"high"}`, "0"},
		{`{"priority":null}`, "none"},
		{`{"priority":5}`, "5"},
	} {
		w := do(t, h, "PATCH", "/api/todos/"+td.ID, c.body)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", c.body, w.Code, w.Body)
		}
		if got := str(prio(w.Body.Bytes())); got != c.want {
			t.Errorf("PATCH %s: priority %s, want %s", c.body, got, c.want)
		}
	}
	if w := do(t, h, "PATCH", "/api/todos/"+td.ID, `{"priority":"later"}`); w.Code != http.StatusBadRequest {
		t.Errorf("unknown level: %d %s, want 400", w.Code, w.Body)
	}
}
