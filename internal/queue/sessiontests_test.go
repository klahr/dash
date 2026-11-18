package queue

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
)

// sessionTestsRun is a task whose Claude run does claude(dir, run) once,
// where run calls run_tests (SessionTests) for it, in a project whose tests
// are command. It returns the task in review and its log.
func sessionTestsRun(t *testing.T, command string, claude func(dir string, run func() model.Checks)) (model.Todo, string) {
	t.Helper()
	ids := make(chan string, 1)
	var h *harness
	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		id := <-ids
		ids <- id
		claude(req.Dir, func() model.Checks {
			c, err := h.engine.SessionTests(id)
			if err != nil {
				t.Errorf("run_tests: %v", err)
			}
			return c
		})
		return nil
	}}
	h = newHarness(t, exe, model.Project{Name: "p", TestCommand: command, MaxAutoFixes: 1})
	td := h.add("make a")
	ids <- td.ID
	td = h.waitFor(td.ID, model.StatusReview)
	log, err := h.engine.Log.Read(td.ID)
	if err != nil {
		t.Fatal(err)
	}
	return td, string(log)
}

// Tests Claude ran through Dash that passed on exactly the files Dash
// commits aren't run again before review.
func TestSessionTestsPassedSkipReviewRun(t *testing.T) {
	parallel(t)

	td, log := sessionTestsRun(t, "grep -qx pass a.txt", func(dir string, run func() model.Checks) {
		write(t, dir, "a.txt", "pass\n")
		if c := run(); !c.Passed || c.Tree == "" || c.Stage != "session" {
			t.Errorf("run_tests: %+v", c)
		}
	})
	if n := strings.Count(log, "▶ tests: "); n != 1 {
		t.Errorf("tests ran %d times, want once:\n%s", n, log)
	}
	if !strings.Contains(log, "✔ tests already passed on ") || !strings.Contains(log, "; not running them again") {
		t.Errorf("no skip line in the log:\n%s", log)
	}
	if td.Checks == nil || !td.Checks.Passed || td.Checks.Stage != "session" {
		t.Errorf("checks %+v", td.Checks)
	}
}

// A file changed after Claude's run_tests: Dash runs the tests again.
func TestSessionTestsThenChangeRunsReview(t *testing.T) {
	parallel(t)

	td, log := sessionTestsRun(t, "grep -qx pass a.txt", func(dir string, run func() model.Checks) {
		write(t, dir, "a.txt", "pass\n")
		run()
		write(t, dir, "b.txt", "more\n")
	})
	if n := strings.Count(log, "▶ tests: "); n != 2 {
		t.Errorf("tests ran %d times, want twice:\n%s", n, log)
	}
	if strings.Contains(log, "already passed") {
		t.Errorf("skipped after a change:\n%s", log)
	}
	if td.Checks == nil || !td.Checks.Passed || td.Checks.Stage != "review" {
		t.Errorf("checks %+v", td.Checks)
	}
}

// Claude's run_tests failed: Dash runs the tests again, and its result
// counts, even on the same files.
func TestSessionTestsFailedRunsReview(t *testing.T) {
	parallel(t)

	// Fails the first time, passes after, without changing the worktree.
	count := filepath.Join(t.TempDir(), "count")
	command := "n=$(cat " + sq(count) + " 2>/dev/null || echo 0); echo $((n+1)) > " + sq(count) + "; [ $n -ge 1 ]"
	td, log := sessionTestsRun(t, command, func(dir string, run func() model.Checks) {
		write(t, dir, "a.txt", "pass\n")
		if c := run(); c.Passed || c.Tree == "" {
			t.Errorf("run_tests should fail on recorded files: %+v", c)
		}
	})
	if n := strings.Count(log, "▶ tests: "); n != 2 {
		t.Errorf("tests ran %d times, want twice:\n%s", n, log)
	}
	if td.Checks == nil || !td.Checks.Passed || td.Checks.Stage != "review" {
		t.Errorf("checks %+v", td.Checks)
	}
}

// run_tests only runs for a task running here.
func TestSessionTestsNeedARunningTask(t *testing.T) {
	parallel(t)

	h := newHarness(t, writesA(t), model.Project{Name: "p", TestCommand: "true"})
	td := h.waitFor(h.add("make a").ID, model.StatusReview)
	if _, err := h.engine.SessionTests(td.ID); err == nil || !strings.Contains(err.Error(), "isn't running") {
		t.Fatalf("want an error for a task in review, got %v", err)
	}
}

// A project with a Test command tells Claude to run it with run_tests, and
// gives the tool time for the tests.
func TestRunGetsTestsHint(t *testing.T) {
	parallel(t)

	h := newHarness(t, writesA(t), model.Project{Name: "p", TestCommand: "true"})
	h.engine.Board, h.engine.BoardURL = &mcptools.Tokens{}, "http://127.0.0.1:1"
	h.engine.Owner = func(model.Todo) (model.Owner, error) { return model.Owner{User: 1, Org: 1}, nil }
	h.engine.TestTimeout = 2 * time.Hour
	h.waitFor(h.add("work").ID, model.StatusReview)
	req := h.engine.Exec.(*fakeExec).Calls()[0]
	i := slices.Index(req.ExtraArgs, "--append-system-prompt")
	if i < 0 || !strings.Contains(req.ExtraArgs[i+1], fmt.Sprintf(testsHint, "true")) {
		t.Fatalf("no run_tests hint in %q", req.ExtraArgs)
	}
	if !slices.Contains(req.Env, "MCP_TOOL_TIMEOUT=7500000") {
		t.Fatalf("env %q, want a tool timeout past the tests' 2h", req.Env)
	}
}
