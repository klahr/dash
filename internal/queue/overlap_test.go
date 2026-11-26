package queue

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

func TestNamedFiles(t *testing.T) {
	for _, c := range []struct {
		text string
		want []string
	}{
		{"The code is in `web/static/index.html`, web/static/app/todos.js and ./internal/queue/todo.go.",
			[]string{"web/static/index.html", "web/static/app/todos.js", "internal/queue/todo.go"}},
		{"Update `README.md` and the CONTRIBUTING.md", []string{"README.md"}}, // no folder: backticks only
		{"See https://example.com/docs/a.html or /Users/me/x/y.go, e.g. `example.com`", nil},
		{"Fix internal/queue/todo.go:42 and internal/queue/todo.go again", []string{"internal/queue/todo.go"}},
		{"version 1.2.3 and docs/ folder and a/b.1", nil},
	} {
		if got := namedFiles(c.text); !slices.Equal(got, c.want) {
			t.Errorf("namedFiles(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

func TestSharedFiles(t *testing.T) {
	got := sharedFiles([]string{"web/static/app/todos.js", "README.md", "a/b.go"}, []string{"README.md", "todos.js", "c/b.go"})
	if want := []string{"web/static/app/todos.js", "README.md"}; !slices.Equal(got, want) {
		t.Errorf("sharedFiles = %q, want %q", got, want)
	}
}

func TestOverlapWait(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	task := func(id string, s model.TodoStatus, min int, prompt string) model.Todo {
		return model.Todo{ID: id, ProjectID: "p", Title: id, Prompt: prompt, Status: s, CreatedAt: at.Add(time.Duration(min) * time.Minute)}
	}
	q := task("q", model.StatusQueued, 5, "Change web/app.js")
	for _, c := range []struct {
		name  string
		other model.Todo
		q     func(*model.Todo)
		wait  bool
	}{
		{"queued ahead", task("x", model.StatusQueued, 1, "Fix web/app.js"), nil, true},
		{"queued behind", task("x", model.StatusQueued, 9, "Fix web/app.js"), nil, false},
		{"running", task("x", model.StatusInProgress, 9, "Fix web/app.js"), nil, true},
		{"in review", task("x", model.StatusReview, 9, "Fix web/app.js"), nil, true},
		{"in review, no changes", func() model.Todo { x := task("x", model.StatusReview, 9, "web/app.js"); x.NoChanges = true; return x }(), nil, false},
		{"merged", task("x", model.StatusMerged, 1, "Fix web/app.js"), nil, false},
		{"paused ahead", func() model.Todo { x := task("x", model.StatusQueued, 1, "web/app.js"); x.Paused = true; return x }(), nil, false},
		{"other files", task("x", model.StatusInProgress, 1, "Fix web/other.js"), nil, false},
		{"other project", func() model.Todo {
			x := task("x", model.StatusInProgress, 1, "web/app.js")
			x.ProjectID = "o"
			return x
		}(), nil, false},
		{"needs q", func() model.Todo {
			x := task("x", model.StatusQueued, 1, "web/app.js")
			x.DependsOn = []string{"q"}
			return x
		}(), nil, false},
		{"started now", task("x", model.StatusInProgress, 1, "web/app.js"), func(q *model.Todo) { q.StartNow = true }, false},
		{"sent back", task("x", model.StatusInProgress, 1, "web/app.js"), func(q *model.Todo) { q.Runs = 1 }, false},
		{"hub's task", task("x", model.StatusInProgress, 1, "web/app.js"), func(q *model.Todo) { q.HubTodoID = "h" }, false},
	} {
		qq := q
		if c.q != nil {
			c.q(&qq)
		}
		why := overlapWait(qq, []model.Todo{c.other, qq})
		if (why != "") != c.wait {
			t.Errorf("%s: overlapWait = %q, want wait %v", c.name, why, c.wait)
		}
		if why != "" && (!strings.Contains(why, GoFirstNote) || !strings.Contains(why, "web/app.js")) {
			t.Errorf("%s: reason %q doesn't say why", c.name, why)
		}
	}
}

// A task naming a file a task in review names too waits queued, while a
// task naming other files runs; it starts once the first is merged, on top
// of its code, and Start now runs a waiting task at once.
func TestTaskWaitsForTaskNamingSameFiles(t *testing.T) {
	parallel(t)

	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		switch {
		case strings.Contains(req.Prompt, "first"):
			write(t, req.Dir, "first.txt", "first\n")
		case strings.Contains(req.Prompt, "second"):
			write(t, req.Dir, "second.txt", "second\n")
		case strings.Contains(req.Prompt, "third"):
			write(t, req.Dir, "third.txt", "third\n")
		default:
			write(t, req.Dir, "other.txt", "other\n")
		}
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p"})
	queue := func(title, prompt string) model.Todo {
		td, err := h.st.CreateTodo(model.Todo{ProjectID: h.proj.ID, Title: title, Prompt: prompt})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.st.SetStatus(td.ID, model.StatusQueued, ""); err != nil {
			t.Fatal(err)
		}
		h.engine.Kick()
		return td
	}

	first := queue("first", "first: change web/app.js")
	h.waitFor(first.ID, model.StatusReview)
	second := queue("second", "second: change web/app.js too")
	other := queue("other", "other: change web/other.js")
	h.waitFor(other.ID, model.StatusReview)
	for range 3 {
		h.engine.Kick()
		time.Sleep(50 * time.Millisecond)
	}
	td, _ := h.st.GetTodo(second.ID)
	if td.Status != model.StatusQueued || !strings.Contains(td.Waiting, GoFirstNote) || !strings.Contains(td.Waiting, "first") {
		t.Fatalf("second: %s, waiting %q; want queued, waiting for first", td.Status, td.Waiting)
	}

	h.approve(first.ID)
	h.waitFor(first.ID, model.StatusMerged)
	h.waitFor(second.ID, model.StatusReview)
	if td, _ := h.st.GetTodo(second.ID); td.Waiting != "" {
		t.Errorf("second's waiting reason stayed: %q", td.Waiting)
	}

	third := queue("third", "third: change web/app.js again")
	for range 3 {
		h.engine.Kick()
		time.Sleep(50 * time.Millisecond)
	}
	if td, _ := h.st.GetTodo(third.ID); td.Status != model.StatusQueued {
		t.Fatalf("third started while second is in review: %s", td.Status)
	}
	if _, err := h.engine.StartNow(third.ID); err != nil {
		t.Fatal(err)
	}
	h.waitFor(third.ID, model.StatusReview)
}
