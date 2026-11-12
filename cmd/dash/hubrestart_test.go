//go:build !windows

package main

import (
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fleet is a hub and one runner, dash's real binaries, sharing a project
// whose origin is served over HTTPS. The runner's claude is
// testdata/fakeclaude, which holds each task while the file hold exists.
type fleet struct {
	t                  *testing.T
	dash, dir, gitBin  string
	repo               string // origin's bare repository
	hubArgs, hubEnv    []string
	runnerArgs, runEnv []string
	hub, runner        *proc
	api                *client
	addr               string
	runnerID, project  string
	hold               string
}

func newFleet(t *testing.T) *fleet {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("needs git")
	}
	backend := filepath.Join(strings.TrimSpace(run(t, "", nil, gitBin, "--exec-path")), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("needs git-http-backend")
	}
	f := &fleet{t: t, dir: t.TempDir(), gitBin: gitBin}
	dash, fake := buildDash(t)
	f.dash = dash
	f.hold = filepath.Join(f.dir, "hold")

	repos := filepath.Join(f.dir, "repos")
	gitEnv := []string{"GIT_SSL_NO_VERIFY=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=" + filepath.Join(f.dir, "gitconfig")}
	seed := filepath.Join(f.dir, "seed")
	run(t, "", gitEnv, gitBin, "init", "-q", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, gitEnv, gitBin, "add", ".")
	run(t, seed, gitEnv, gitBin, "commit", "-q", "-m", "first")
	f.repo = filepath.Join(repos, "app.git")
	run(t, "", gitEnv, gitBin, "clone", "-q", "--bare", seed, f.repo)
	run(t, f.repo, gitEnv, gitBin, "config", "http.receivepack", "true")
	origin := httptest.NewTLSServer(&cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + repos, "GIT_HTTP_EXPORT_ALL=1"}})
	t.Cleanup(origin.Close)

	hubPath := pathWithout(t, "claude")
	f.hubEnv = append(gitEnv, "PATH="+hubPath, "DASH_USERNAME=admin", "DASH_PASSWORD=a-long-enough-password")
	f.hubArgs = []string{"-data", filepath.Join(f.dir, "hub"), "-projects", filepath.Join(f.dir, "hub-projects"),
		"-env", filepath.Join(f.dir, "none.env")}
	f.startHub()

	var code struct{ Code string }
	f.api.must("POST", "/api/runners/codes", map[string]any{}, http.StatusOK, &code)
	f.runEnv = append(gitEnv, "FAKECLAUDE_HOLD="+f.hold, "PATH="+filepath.Dir(fake)+string(os.PathListSeparator)+hubPath)
	f.runnerArgs = []string{"runner", "-data", filepath.Join(f.dir, "runner"), "-projects", filepath.Join(f.dir, "runner-projects"),
		"-name", "laptop", "-env", filepath.Join(f.dir, "none.env")}
	f.runner = startDash(t, f.dash, f.runEnv, append(f.runnerArgs, "--server", "http://"+f.addr, "--code", code.Code)...)
	f.waitOnline(true)

	var p struct{ ID string }
	f.api.must("POST", "/api/projects", map[string]any{"name": "app", "remote_url": origin.URL + "/app.git", "runners": []string{f.runnerID}},
		http.StatusCreated, &p)
	f.project = p.ID
	return f
}

// startHub starts the hub (again) on the same data and signs in: first on
// a free address, then again on that one.
func (f *fleet) startHub(extra ...string) {
	f.t.Helper()
	args := append(slicesClone(f.hubArgs), extra...)
	if f.addr == "" {
		f.hub, f.addr, f.api = startHub(f.t, f.dash, f.hubEnv, args...)
	} else {
		f.hub = startDash(f.t, f.dash, f.hubEnv, append([]string{"-addr", f.addr}, args...)...)
		f.api = newClient(f.t, "http://"+f.addr)
		waitFor(f.t, f.hub, "the hub to start", func() bool { code, _ := f.api.try("GET", "/api/version", nil); return code != 0 })
	}
	f.api.must("POST", "/api/login", map[string]string{"username": "admin", "password": "a-long-enough-password"}, http.StatusOK, nil)
}

func slicesClone(s []string) []string { return append([]string(nil), s...) }

func (f *fleet) startRunner() {
	f.t.Helper()
	f.runner = startDash(f.t, f.dash, f.runEnv, f.runnerArgs...)
}

func (f *fleet) waitOnline(want bool) {
	f.t.Helper()
	waitFor(f.t, f.hub, "the runner to be online="+strconv.FormatBool(want), func() bool {
		var rs struct {
			Runners []struct {
				ID, Name string
				Online   bool
			}
		}
		f.api.must("GET", "/api/runners", nil, http.StatusOK, &rs)
		for _, r := range rs.Runners {
			if r.Name == "laptop" {
				f.runnerID = r.ID
				return r.Online == want
			}
		}
		return false
	})
}

type hubTask struct {
	ID, Title, Status, Runner, Error string
	MergedCommit                     string `json:"merged_commit"`
}

func (f *fleet) task(id string) hubTask {
	f.t.Helper()
	var td hubTask
	f.api.must("GET", "/api/todos/"+id, nil, http.StatusOK, &td)
	return td
}

func (f *fleet) newTask(title string) string {
	f.t.Helper()
	var td struct{ ID string }
	f.api.must("POST", "/api/projects/"+f.project+"/todos", map[string]any{"title": title, "prompt": "add a done file", "enqueue": true},
		http.StatusCreated, &td)
	return td.ID
}

func (f *fleet) waitStatus(id string, want ...string) hubTask {
	f.t.Helper()
	var td hubTask
	waitFor(f.t, f.hub, "task "+id+" to be "+strings.Join(want, " or "), func() bool {
		td = f.task(id)
		for _, w := range want {
			if td.Status == w {
				return true
			}
		}
		return false
	})
	return td
}

// holding makes the runner's tasks wait in claude until release.
func (f *fleet) holding() {
	f.t.Helper()
	_ = os.Remove(f.hold + ".pids")
	if err := os.WriteFile(f.hold, nil, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fleet) release() { _ = os.Remove(f.hold) }

// heldClaude waits for a task's claude to be held and returns its pid.
func (f *fleet) heldClaude() int {
	f.t.Helper()
	var pid int
	waitFor(f.t, f.runner, "claude to start the task", func() bool {
		b, _ := os.ReadFile(f.hold + ".pids")
		lines := strings.Fields(string(b))
		if len(lines) == 0 {
			return false
		}
		pid, _ = strconv.Atoi(lines[len(lines)-1])
		return pid > 0
	})
	return pid
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// stopGracefully sends p SIGTERM and fails unless it exits cleanly in time.
func stopGracefully(t *testing.T, p *proc, within time.Duration) {
	t.Helper()
	start := time.Now()
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("dash %s exited with %v after SIGTERM:\n%s", p.name, err, p.logs)
		}
	case <-time.After(within):
		_ = p.cmd.Process.Kill()
		<-done
		t.Fatalf("dash %s took longer than %s to stop after SIGTERM:\n%s", p.name, within, p.logs)
	}
	t.Logf("dash %s stopped in %s", p.name, time.Since(start).Round(time.Millisecond))
}

// crash kills p at once, as a crash or power cut would.
func crash(p *proc) {
	_ = p.cmd.Process.Kill()
	_ = p.cmd.Wait()
}

// The hub restarting (a deploy with SIGTERM, a crash) loses no running
// work: a task running on a runner keeps running there, the runner
// reconnects and reports it, and it finishes on the hub. An approval
// interrupted by a hub crash lands once. A runner that stops gracefully
// stops its claude (none is left behind to run beside the resumed task),
// keeps its task in progress and resumes it; one that crashes and stays away
// past the hub's grace has its task failed, and coming back continues it.
// Nothing stays in progress or merging.
func TestHubAndRunnerRestartsLoseNoWork(t *testing.T) {
	endToEnd(t)
	f := newFleet(t)

	// 1. A deploy: SIGTERM to the hub while a task runs on the runner.
	f.holding()
	first := f.newTask("survive restarts")
	f.heldClaude()
	if td := f.waitStatus(first, "in_progress"); td.Runner != f.runnerID {
		t.Fatalf("task: %+v", td)
	}
	stopGracefully(t, f.hub, 10*time.Second)
	if !strings.Contains(f.hub.String(), "shutting down") {
		t.Errorf("the hub didn't say it was shutting down:\n%s", f.hub)
	}
	f.startHub()
	f.waitOnline(true)
	if td := f.task(first); td.Status != "in_progress" || td.Runner != f.runnerID {
		t.Fatalf("after the hub restarted: %+v", td)
	}

	// 2. A crash: the hub is killed, mid-task again.
	crash(f.hub)
	f.startHub()
	f.waitOnline(true)
	if td := f.task(first); td.Status != "in_progress" {
		t.Fatalf("after the hub crashed: %+v", td)
	}
	f.release()
	f.waitStatus(first, "review")

	// 3. Approving needs the runner: with it gone, the task stays in review.
	stopGracefully(t, f.runner, 30*time.Second)
	f.waitOnline(false)
	if code, body := f.api.try("POST", "/api/todos/"+first+"/approve", map[string]any{}); code < 400 {
		t.Errorf("approved with the runner offline: %d %s", code, body)
	}
	if td := f.task(first); td.Status != "review" {
		t.Fatalf("after an approval that couldn't reach the runner: %+v", td)
	}
	f.startRunner()
	f.waitOnline(true)

	// …and an approval cut short by a hub crash lands, once.
	f.api.must("POST", "/api/todos/"+first+"/approve", map[string]any{}, http.StatusAccepted, nil)
	crash(f.hub)
	f.startHub()
	f.waitOnline(true)
	if td := f.waitStatus(first, "merged", "failed"); td.Status != "merged" {
		t.Fatalf("after approving and a hub crash: %+v", td)
	}
	log := run(t, "", nil, f.gitBin, "--git-dir", f.repo, "log", "--format=%s", "main")
	if n := strings.Count(log, "survive restarts"); n != 1 {
		t.Errorf("origin has the task's commit %d times:\n%s", n, log)
	}

	// 4. The runner stops gracefully mid-task (an upgrade of the runner):
	// its claude stops with it, and the task resumes when it is back.
	f.holding()
	second := f.newTask("survive a runner restart")
	pid := f.heldClaude()
	f.waitStatus(second, "in_progress")
	stopGracefully(t, f.runner, 30*time.Second)
	if alive(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("the runner left its task's claude (pid %d) running", pid)
	}
	if td := f.task(second); td.Status != "in_progress" {
		t.Fatalf("on the hub while the runner restarts: %+v", td)
	}
	f.startRunner()
	f.waitOnline(true)
	if _, log := f.api.try("GET", "/api/todos/"+second+"/log", nil); !strings.Contains(log, "interrupted by shutdown; will resume on restart") {
		t.Errorf("the task's log doesn't say it was kept to resume:\n%s", log)
	}
	f.heldClaude() // resumed
	f.release()
	if td := f.waitStatus(second, "review", "failed"); td.Status != "review" {
		t.Fatalf("after the runner came back: %+v", td)
	}

	// 5. The runner crashes mid-task (its computer too: claude dies with
	// it) and stays away past the hub's grace: the task fails, resumable,
	// and continues when the runner is back.
	stopGracefully(t, f.hub, 10*time.Second)
	f.startHub("-runner-grace", "2s")
	f.waitOnline(true)
	f.holding()
	third := f.newTask("survive a runner crash")
	pid = f.heldClaude()
	f.waitStatus(third, "in_progress")
	crash(f.runner)
	_ = syscall.Kill(pid, syscall.SIGKILL)
	td := f.waitStatus(third, "failed")
	if !strings.HasPrefix(td.Error, "interrupted: laptop went offline") {
		t.Errorf("after the runner stayed away: %+v", td)
	}
	f.release()
	f.startRunner()
	if td := f.waitStatus(third, "review"); td.Runner != f.runnerID {
		t.Fatalf("after the runner came back from a crash: %+v", td)
	}

	// Nothing is left running or merging.
	for _, id := range []string{first, second, third} {
		if td := f.task(id); td.Status == "in_progress" || td.Status == "merging" || td.Status == "queued" {
			t.Errorf("task %q stuck %s", td.Title, td.Status)
		}
	}
}
