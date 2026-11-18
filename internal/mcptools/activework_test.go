package mcptools

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// workBoard is one project's tasks and the files each changes; the board
// methods active_work doesn't use panic (nil Board).
type workBoard struct {
	Board
	todos []model.Todo
	files map[string][]string
	fail  map[string]error

	mu    sync.Mutex
	asked []string // tasks whose files were asked for
}

func (b *workBoard) Project(ref string) (model.Project, error) {
	if ref != "web" {
		return model.Project{}, errors.New("no project " + ref)
	}
	return model.Project{ID: "web", Name: "web"}, nil
}

func (b *workBoard) Todos(string) ([]model.Todo, error) { return b.todos, nil }

func (b *workBoard) WorkFiles(t model.Todo) ([]string, error) {
	b.mu.Lock()
	b.asked = append(b.asked, t.ID)
	b.mu.Unlock()
	return b.files[t.ID], b.fail[t.ID]
}

func newWorkBoard() *workBoard {
	wt := &model.Worktree{Branch: "dash/x"}
	return &workBoard{
		todos: []model.Todo{
			{ID: "self", Status: model.StatusInProgress, Title: "This task", Worktree: wt},
			{ID: "q1", Status: model.StatusQueued, Title: "Add dark mode", Prompt: "# Add dark mode\n\nUse the theme tokens in web/static/style.css."},
			{ID: "r1", Status: model.StatusInProgress, Prompt: "Fix the login form:\nit loses the email on error.", Worktree: wt},
			{ID: "v1", Status: model.StatusReview, Title: "Rename store helpers", Prompt: strings.Repeat("long intent ", 40), Worktree: wt},
			{ID: "h1", Status: model.StatusReview, Title: "On a runner", Runner: "rn1"},
			{ID: "d1", Status: model.StatusDraft, Title: "Draft", Worktree: wt},
			{ID: "m1", Status: model.StatusMerged, Title: "Merged", Worktree: wt},
			{ID: "f1", Status: model.StatusFailed, Title: "Failed", Worktree: wt},
		},
		files: map[string][]string{
			"self": {"internal/api/server.go"},
			"r1":   {"web/static/app/login.js", "internal/api/auth.go"},
			"v1":   {"internal/store/sqlite.go", "internal/store/store.go", "docs/mcp.md"},
			"d1":   {"internal/api/auth.go"},
		},
		fail: map[string]error{"h1": errors.New("the runner rn1 is offline")},
	}
}

func runActiveWork(t *testing.T, b Board, a Args) string {
	t.Helper()
	tool := ActiveWork(b, Options{Project: "web", Todo: "self"})
	res, err := tool.Run(a)
	if err != nil {
		t.Fatal(err)
	}
	return res.(string)
}

func TestActiveWorkListsOtherActiveTasks(t *testing.T) {
	b := newWorkBoard()
	got := runActiveWork(t, b, Args{})
	want := `- r1 [running] Fix the login form:
  intent: it loses the email on error.
  files: web/static/app/login.js, internal/api/auth.go
- v1 [review] Rename store helpers
  intent: ` + strings.Repeat("long intent ", 13) + `lon…
  files: internal/store/sqlite.go, internal/store/store.go, docs/mcp.md
- h1 [review] On a runner
  files: unknown (the runner rn1 is offline)
- q1 [queued] Add dark mode
  intent: Use the theme tokens in web/static/style.css.
  files: none yet`
	if got != want {
		t.Fatalf("active_work:\n%s\nwant:\n%s", got, want)
	}
	// Not the calling task, not drafts or finished ones, and a queued
	// task that never started has no files to ask for.
	for _, id := range b.asked {
		if id == "self" || id == "d1" || id == "m1" || id == "f1" || id == "q1" {
			t.Errorf("asked for the files of %s", id)
		}
	}
}

func TestActiveWorkFiltersByPaths(t *testing.T) {
	b := newWorkBoard()
	got := runActiveWork(t, b, Args{"paths": []any{"./internal/store/", "docs/mcp.md"}})
	want := `- v1 [review] Rename store helpers
  intent: ` + strings.Repeat("long intent ", 13) + `lon…
  files (3 of 3 match): internal/store/sqlite.go, internal/store/store.go, docs/mcp.md`
	if got != want {
		t.Fatalf("active_work by path:\n%s\nwant:\n%s", got, want)
	}

	got = runActiveWork(t, b, Args{"paths": []any{"internal/api/auth.go"}})
	if !strings.HasPrefix(got, "- r1 [running]") || !strings.Contains(got, "files (1 of 2 match): internal/api/auth.go") || strings.Contains(got, "d1") {
		t.Fatalf("active_work by file:\n%s", got)
	}

	// A prefix of a name isn't a directory.
	if got := runActiveWork(t, b, Args{"paths": "internal/stor"}); got != "No other active task in this project touches internal/stor." {
		t.Fatalf("no match: %s", got)
	}
}

func TestActiveWorkNothingActive(t *testing.T) {
	b := &workBoard{todos: []model.Todo{{ID: "self", Status: model.StatusInProgress}, {ID: "d", Status: model.StatusDraft}}}
	if got := runActiveWork(t, b, Args{}); got != "No other task in this project is queued, running or in review." {
		t.Fatalf("nothing active: %s", got)
	}
}

func TestActiveWorkClipsLongFileLists(t *testing.T) {
	var fs []string
	for i := range 30 {
		fs = append(fs, "f"+string(rune('a'+i%26))+".go")
	}
	b := &workBoard{
		todos: []model.Todo{{ID: "big", Status: model.StatusReview, Title: "Big", Worktree: &model.Worktree{}}},
		files: map[string][]string{"big": fs},
	}
	got := runActiveWork(t, b, Args{})
	if !strings.HasSuffix(got, " +5 more") || strings.Count(got, ".go") != activeFilesShown {
		t.Fatalf("long list:\n%s", got)
	}
}

// A board that can't tell files still lists the tasks.
func TestActiveWorkWithoutFiles(t *testing.T) {
	b := todosBoard{todos: []model.Todo{{ID: "r", Status: model.StatusInProgress, Title: "R", Worktree: &model.Worktree{}}}}
	got, err := ActiveWork(b, Options{Project: "web"}).Run(Args{})
	if err != nil || got != "- r [running] R\n  files: none yet" {
		t.Fatalf("without WorkFiler: %v %q", err, got)
	}
}

// testsBoard is a board that runs a task's tests (TestRunner).
type testsBoard struct {
	*workBoard
	got   string
	tests model.Checks
}

func (b *testsBoard) RunTests(id string) (model.Checks, error) {
	b.got = id
	return b.tests, nil
}

func TestTaskTools(t *testing.T) {
	names := func(tools []Tool) string {
		var ns []string
		for _, tl := range tools {
			ns = append(ns, tl.Name)
		}
		return strings.Join(ns, ",")
	}
	if got, want := names(TaskTools(newWorkBoard(), Options{Project: "web", Todo: "self"})), strings.Join(TaskToolNames[:len(TaskToolNames)-1], ","); got != want {
		t.Fatalf("task tools %s, want %s", got, want)
	}
	tb := &testsBoard{workBoard: newWorkBoard()}
	tools := TaskTools(tb, Options{Project: "web", Todo: "self"})
	if got := names(tools); got != strings.Join(TaskToolNames, ",") {
		t.Fatalf("task tools %s, names %v", got, TaskToolNames)
	}
	if got := names(TaskTools(tb, Options{Project: "web"})); strings.Contains(got, RunTestsTool) {
		t.Fatalf("run_tests outside a task: %s", got)
	}

	run := tools[len(tools)-1]
	tb.tests = model.Checks{Command: "make test", Passed: true, Duration: 3, Output: "ok", Tree: "t1"}
	got, err := run.Run(Args{})
	if err != nil || tb.got != "self" || !strings.Contains(got.(string), "The tests passed (3s): `make test`.") ||
		!strings.Contains(got.(string), "doesn't run them again") || !strings.Contains(got.(string), "```\nok\n```") {
		t.Fatalf("run_tests passed: %v %q (task %q)", err, got, tb.got)
	}
	tb.tests = model.Checks{Command: "make test", ExitCode: 2, Output: "FAIL x"}
	if got, err = run.Run(Args{}); err != nil || !strings.Contains(got.(string), "The tests failed (exit 2") || strings.Contains(got.(string), "again") {
		t.Fatalf("run_tests failed: %v %q", err, got)
	}
	tb.tests = model.Checks{Command: "make test", Passed: true}
	if got, err = run.Run(Args{}); err != nil || !strings.Contains(got.(string), "Files changed while they ran") {
		t.Fatalf("run_tests on changing files: %v %q", err, got)
	}
}
