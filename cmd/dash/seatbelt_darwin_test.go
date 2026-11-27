package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A runner on macOS with -claude-sandbox deploy/sandbox/claude-seatbelt
// runs a task under sandbox-exec: the (fake) claude writes in the worktree
// (not outside it) and reaches the runner's board tools over the network,
// and Dash commits its change for review outside the sandbox.
func TestSeatbeltSandboxRunsATask(t *testing.T) {
	endToEnd(t)
	if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
		t.Skip("needs /usr/bin/sandbox-exec")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "deploy", "sandbox", "claude-seatbelt"))
	if err != nil {
		t.Fatal(err)
	}
	// A login of its own in the environment (the script then leaves the
	// keychain alone), and a Claude config folder in the test's.
	h := startRunnerHub(t, "-claude-sandbox", "env CLAUDE_CODE_OAUTH_TOKEN=test DASH_SANDBOX_CLAUDE="+t.TempDir()+" "+script)
	api, runner, p := h.api, h.runner, h.project

	var td struct{ ID string }
	api.must("POST", "/api/projects/"+p.ID+"/todos", map[string]any{"title": "done file", "prompt": "add a done file", "enqueue": true}, http.StatusCreated, &td)
	var task struct{ Status, Error string }
	waitFor(t, runner, "the task to reach review", func() bool {
		api.must("GET", "/api/todos/"+td.ID, nil, http.StatusOK, &task)
		return task.Status == "review" || task.Status == "failed"
	})
	if _, log := api.try("GET", "/api/todos/"+td.ID+"/log", nil); task.Status != "review" {
		t.Fatalf("task: %+v\n%s\nrunner: %s", task, log, runner)
	}
	if _, diff := api.try("GET", "/api/todos/"+td.ID+"/diff", nil); !strings.Contains(diff, "done-by-runner.txt") {
		t.Errorf("task's diff: %s", diff)
	}
	if !api.hasIssue(p.ID, "Issue reported by the task on the runner") {
		t.Error("the sandboxed task's report_issue didn't reach the board")
	}
	// The fake claude logs its calls to a file outside the worktree: the
	// sandbox kept it from writing there.
	if log, _ := os.ReadFile(h.calls); strings.Contains(string(log), "task ") {
		t.Errorf("the sandboxed claude wrote outside the worktree: %s", log)
	}
	if !strings.Contains(runner.String(), "Claude runs tasks and scans in a sandbox") {
		t.Errorf("the runner doesn't say it sandboxes Claude:\n%s", runner)
	}
}
