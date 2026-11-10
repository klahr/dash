package main

import (
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/localrun"
)

// origin is a project's git remote served over HTTPS (runners clone only
// network remotes), and the environment git needs to use it.
type origin struct {
	git    string   // the git binary
	remote string   // its URL
	bare   string   // the bare repository behind it
	env    []string // for git and dash: no certificate check, an identity
}

func newOrigin(t *testing.T, dir string) origin {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("needs git")
	}
	backend := filepath.Join(strings.TrimSpace(run(t, "", nil, gitBin, "--exec-path")), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("needs git-http-backend")
	}
	repos := filepath.Join(dir, "repos")
	env := []string{"GIT_SSL_NO_VERIFY=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=" + filepath.Join(dir, "gitconfig")}
	seed := filepath.Join(dir, "seed")
	run(t, "", env, gitBin, "init", "-q", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, env, gitBin, "add", ".")
	run(t, seed, env, gitBin, "commit", "-q", "-m", "first")
	bare := filepath.Join(repos, "app.git")
	run(t, "", env, gitBin, "clone", "-q", "--bare", seed, bare)
	run(t, bare, env, gitBin, "config", "http.receivepack", "true")
	srv := httptest.NewTLSServer(&cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + repos, "GIT_HTTP_EXPORT_ALL=1"}})
	t.Cleanup(srv.Close)
	return origin{git: gitBin, remote: srv.URL + "/app.git", bare: bare, env: env}
}

// buildDash builds dash and the fake claude into dir.
func buildDash(t *testing.T, dir string) (dash, fakeClaude string) {
	t.Helper()
	dash = filepath.Join(dir, "dash")
	run(t, "", nil, "go", "build", "-o", dash, ".")
	fakeClaude = filepath.Join(dir, "fakebin", "claude")
	run(t, "", nil, "go", "build", "-o", fakeClaude, "./testdata/fakeclaude")
	return dash, fakeClaude
}

// registerRunner starts a runner named name with the fake claude,
// registered with a code from the hub, and returns it with its id.
func registerRunner(t *testing.T, api *client, dash, fake, dir, name string, env []string, extra ...string) (*proc, string, []string) {
	t.Helper()
	var code struct{ Code string }
	api.must("POST", "/api/runners/codes", map[string]any{}, http.StatusOK, &code)
	args := append([]string{"runner", "-data", filepath.Join(dir, name), "-projects", filepath.Join(dir, name+"-projects"),
		"-name", name, "-env", filepath.Join(dir, "none.env")}, extra...)
	runner := startDash(t, dash, env, append(args, "--server", api.base, "--code", code.Code)...)
	var id string
	waitFor(t, runner, "the runner to come online", func() bool {
		var rs struct {
			Runners []struct {
				ID, Name string
				Online   bool
			}
		}
		api.must("GET", "/api/runners", nil, http.StatusOK, &rs)
		for _, r := range rs.Runners {
			if r.Name == name && r.Online {
				id = r.ID
			}
		}
		return id != ""
	})
	return runner, id, args
}

// The hub runs nothing on its own computer: started with no git, gh,
// claude (nor even bash) on its PATH, it takes a shared project through a
// whole task — created, run, tested, tested again, landed and pushed —
// on a runner with the fake claude, and shows the project's git history,
// branches, files and commands from the runner. With the runner gone, those
// views say no runner is online; projects on the hub's own disk are refused.
func TestHubRunsNothingItself(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs dash")
	}
	dir := t.TempDir()
	o := newOrigin(t, dir)
	dash, fake := buildDash(t, dir)

	// The hub: an empty PATH, so any git, gh, claude or shell it tried
	// to run would fail (and log localrun.Refused before even trying).
	emptyBin := filepath.Join(dir, "empty-bin")
	if err := os.MkdirAll(emptyBin, 0o755); err != nil {
		t.Fatal(err)
	}
	addr := freeAddr(t)
	hubLogs := startDash(t, dash, append(o.env, "PATH="+emptyBin, "DASH_USERNAME=admin", "DASH_PASSWORD=a-long-enough-password"),
		"-addr", addr, "-data", filepath.Join(dir, "hub"), "-projects", filepath.Join(dir, "hub-projects"), "-env", filepath.Join(dir, "none.env"))
	api := newClient(t, "http://"+addr)
	waitFor(t, hubLogs, "the hub to start", func() bool { code, _ := api.try("GET", "/api/version", nil); return code != 0 })
	api.must("POST", "/api/login", map[string]string{"username": "admin", "password": "a-long-enough-password"}, http.StatusOK, nil)

	// No project on the hub's disk.
	hubClone := filepath.Join(dir, "hub-projects", "app")
	run(t, "", o.env, o.git, "clone", "-q", o.remote, hubClone)
	if code, body := api.try("POST", "/api/projects", map[string]any{"name": "app", "path": hubClone, "existing": true}); code != http.StatusBadRequest ||
		!strings.Contains(body, "keeps no projects on its own computer") {
		t.Errorf("adding a clone on the hub: %d %s", code, body)
	}
	if code, body := api.try("POST", "/api/projects", map[string]any{"name": "new"}); code != http.StatusBadRequest {
		t.Errorf("a new project on the hub: %d %s", code, body)
	}

	// A runner that takes the hub's commands, with git and the fake claude.
	calls := filepath.Join(dir, "claude-calls")
	runnerEnv := append(o.env, "FAKECLAUDE_LOG="+calls, "PATH="+filepath.Dir(fake)+string(os.PathListSeparator)+os.Getenv("PATH"))
	runner, runnerID, runnerArgs := registerRunner(t, api, dash, fake, dir, "laptop", runnerEnv, "-hub-commands")

	// A shared project: the remote and the runner it is linked to.
	var p struct {
		ID        string
		RemoteURL string `json:"remote_url"`
		Runners   []string
	}
	api.must("POST", "/api/projects", map[string]any{"name": "app", "remote_url": o.remote, "runners": []string{runnerID},
		"test_command": "test -f done-by-runner.txt && echo tests-ran-on-the-runner"}, http.StatusCreated, &p)
	if len(p.Runners) != 1 || p.RemoteURL != o.remote {
		t.Fatalf("shared project: %+v", p)
	}

	// Create and run a task: on the runner, with its tests there.
	var td struct{ ID string }
	api.must("POST", "/api/projects/"+p.ID+"/todos", map[string]any{"title": "done file", "prompt": "add a done file", "enqueue": true}, http.StatusCreated, &td)
	var task struct{ Status, Runner, Error string }
	waitFor(t, runner, "the task to reach review", func() bool {
		api.must("GET", "/api/todos/"+td.ID, nil, http.StatusOK, &task)
		return task.Status == "review" || task.Status == "failed"
	})
	taskLog := func() string { _, log := api.try("GET", "/api/todos/"+td.ID+"/log", nil); return log }
	if task.Status != "review" || task.Runner != runnerID {
		t.Fatalf("task: %+v\n%s", task, taskLog())
	}
	if n := strings.Count(taskLog(), "tests-ran-on-the-runner"); n < 1 {
		t.Errorf("the tests didn't run on the runner:\n%s", taskLog())
	}
	if _, diff := api.try("GET", "/api/todos/"+td.ID+"/diff", nil); !strings.Contains(diff, "done-by-runner.txt") {
		t.Errorf("task's diff from the runner: %s", diff)
	}

	// Test it again: on the runner, in the task's worktree there.
	before := strings.Count(taskLog(), "tests-ran-on-the-runner")
	api.must("POST", "/api/todos/"+td.ID+"/test", nil, http.StatusAccepted, nil)
	waitFor(t, runner, "the tests to run again", func() bool { return strings.Count(taskLog(), "tests-ran-on-the-runner") > before })

	// The project's history, branches, files and commands come from the runner.
	var graph struct{ Branch, Graph string }
	api.must("GET", "/api/projects/"+p.ID+"/graph", nil, http.StatusOK, &graph)
	if graph.Branch != "main" || !strings.Contains(graph.Graph, "first") {
		t.Errorf("graph from the runner: %+v", graph)
	}
	if code, body := api.try("GET", "/api/projects/"+p.ID+"/branches", nil); code != http.StatusOK || !strings.Contains(body, "main") {
		t.Errorf("branches from the runner: %d %s", code, body)
	}
	if code, body := api.try("GET", "/api/projects/"+p.ID+"/files", nil); code != http.StatusOK || !strings.Contains(body, "README.md") {
		t.Errorf("files from the runner: %d %s", code, body)
	}
	if code, body := api.try("GET", "/api/projects/"+p.ID+"/file?path=README.md", nil); code != http.StatusOK || !strings.Contains(body, "# app") {
		t.Errorf("a file from the runner: %d %s", code, body)
	}
	var remote struct {
		ProjectID string `json:"project_id"`
	}
	api.must("GET", "/api/projects/"+p.ID+"/remote", nil, http.StatusOK, &remote)
	if remote.ProjectID != p.ID {
		t.Errorf("the runner's project id leaked into the hub's answer: %+v", remote)
	}
	var proc struct{ ID, Runner string }
	api.must("POST", "/api/projects/"+p.ID+"/processes", map[string]any{"kind": "task", "preset": "test", "todo_id": td.ID}, http.StatusCreated, &proc)
	if !strings.HasPrefix(proc.ID, runnerID+"~") || proc.Runner != "laptop" {
		t.Errorf("process on the runner: %+v", proc)
	}
	waitFor(t, runner, "the test command to show in the project's processes", func() bool {
		_, body := api.try("GET", "/api/projects/"+p.ID+"/processes", nil)
		return strings.Contains(body, proc.ID) && strings.Contains(body, `"running":false`)
	})
	if code, body := api.try("DELETE", "/api/processes/"+proc.ID, nil); code != http.StatusAccepted && code != http.StatusOK {
		t.Errorf("stopping the runner's process: %d %s", code, body)
	}
	// A terminal: the runner takes the hub's commands (-hub-commands), and
	// the admin registered it, so it opens one in its checkout.
	var shell struct {
		ID, Dir, Kind string
		Main          bool
	}
	api.must("POST", "/api/projects/"+p.ID+"/processes", map[string]any{"kind": "pty", "command": "sleep 30"}, http.StatusCreated, &shell)
	if !strings.HasPrefix(shell.ID, runnerID+"~") || shell.Kind != "pty" || !shell.Main ||
		!strings.HasPrefix(shell.Dir, filepath.Join(dir, "laptop-projects")) {
		t.Errorf("a terminal from the hub: %+v", shell)
	}
	if code, body := api.try("DELETE", "/api/processes/"+shell.ID, nil); code != http.StatusAccepted && code != http.StatusOK {
		t.Errorf("closing the terminal: %d %s", code, body)
	}

	// Merge: the runner lands it on origin, and the hub hears it.
	api.must("POST", "/api/todos/"+td.ID+"/approve", nil, http.StatusAccepted, nil)
	waitFor(t, runner, "the task to be merged", func() bool {
		api.must("GET", "/api/todos/"+td.ID, nil, http.StatusOK, &task)
		return task.Status == "merged" || task.Status == "failed"
	})
	if task.Status != "merged" {
		t.Fatalf("merge: %+v\n%s", task, taskLog())
	}
	if files := run(t, o.bare, o.env, o.git, "ls-tree", "--name-only", "main"); !strings.Contains(files, "done-by-runner.txt") {
		t.Errorf("origin's main after the merge: %s", files)
	}
	// Push: the runner pushes its clone's main (already there).
	api.must("POST", "/api/projects/"+p.ID+"/push", nil, http.StatusOK, nil)

	// The runner stops: the views say so, and nothing runs on the hub.
	runner.stop()
	waitFor(t, hubLogs, "the runner to go offline", func() bool {
		var rs struct{ Runners []struct{ Online bool } }
		api.must("GET", "/api/runners", nil, http.StatusOK, &rs)
		return len(rs.Runners) == 1 && !rs.Runners[0].Online
	})
	for _, path := range []string{"/graph", "/files", "/branches"} {
		if code, body := api.try("GET", "/api/projects/"+p.ID+path, nil); code != http.StatusServiceUnavailable ||
			!strings.Contains(body, "No runner is online for this project. Start one with `dash runner`.") {
			t.Errorf("%s with the runner offline: %d %s", path, code, body)
		}
	}
	if code, body := api.try("POST", "/api/projects/"+p.ID+"/processes", map[string]any{"kind": "task", "preset": "test"}); code != http.StatusServiceUnavailable {
		t.Errorf("a command with the runner offline: %d %s", code, body)
	}

	// Back online, it serves the views again.
	runner = startDash(t, dash, runnerEnv, runnerArgs...)
	waitFor(t, runner, "the graph from the runner again", func() bool {
		code, _ := api.try("GET", "/api/projects/"+p.ID+"/graph", nil)
		return code == http.StatusOK
	})
	if log := hubLogs.String(); strings.Contains(log, localrun.Refused) || strings.Contains(log, "start claude") {
		t.Errorf("the hub tried to run something itself:\n%s", log)
	}
}
