package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A hub on a computer without Claude Code does all its Claude work on a
// runner: a task (whose board tools change the hub's board), its title, the
// general chat and a scan. With the runner gone, new work waits, saying no
// runner is online. Both are dash's real binaries; the runner's `claude` is
// testdata/fakeclaude, and the hub's PATH has no claude at all.
func TestHubWithoutClaudeRunsItsWorkOnARunner(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs dash")
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("needs git")
	}
	backend := filepath.Join(strings.TrimSpace(run(t, "", nil, gitBin, "--exec-path")), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("needs git-http-backend")
	}
	dir := t.TempDir()
	dash := filepath.Join(dir, "dash")
	run(t, "", nil, "go", "build", "-o", dash, ".")
	fake := filepath.Join(dir, "fakebin", "claude")
	run(t, "", nil, "go", "build", "-o", fake, "./testdata/fakeclaude")
	calls := filepath.Join(dir, "claude-calls")

	// The project's origin, served over HTTPS (runners clone only network
	// remotes); both computers skip the test certificate's check.
	repos := filepath.Join(dir, "repos")
	gitEnv := []string{"GIT_SSL_NO_VERIFY=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=" + filepath.Join(dir, "gitconfig")}
	seed := filepath.Join(dir, "seed")
	run(t, "", gitEnv, gitBin, "init", "-q", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, gitEnv, gitBin, "add", ".")
	run(t, seed, gitEnv, gitBin, "commit", "-q", "-m", "first")
	run(t, "", gitEnv, gitBin, "clone", "-q", "--bare", seed, filepath.Join(repos, "app.git"))
	run(t, filepath.Join(repos, "app.git"), gitEnv, gitBin, "config", "http.receivepack", "true")
	origin := httptest.NewTLSServer(&cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + repos, "GIT_HTTP_EXPORT_ALL=1"}})
	defer origin.Close()
	remote := origin.URL + "/app.git"
	hubClone := filepath.Join(dir, "hub-projects", "app")
	run(t, "", gitEnv, gitBin, "clone", "-q", remote, hubClone)

	// The hub: default flags, no claude on its PATH.
	hubPath := pathWithout(t, "claude")
	addr := freeAddr(t)
	hubLogs := startDash(t, dash, append(gitEnv, "PATH="+hubPath, "DASH_USERNAME=admin", "DASH_PASSWORD=a-long-enough-password"),
		"-addr", addr, "-data", filepath.Join(dir, "hub"), "-projects", filepath.Join(dir, "hub-projects"), "-env", filepath.Join(dir, "none.env"))
	base := "http://" + addr
	api := newClient(t, base)
	waitFor(t, hubLogs, "the hub to start", func() bool { code, _ := api.try("GET", "/api/version", nil); return code != 0 })
	api.must("POST", "/api/login", map[string]string{"username": "admin", "password": "a-long-enough-password"}, http.StatusOK, nil)
	var p struct{ ID, RemoteURL string }
	api.must("POST", "/api/projects", map[string]string{"name": "app", "path": hubClone}, http.StatusCreated, &p)

	// A runner, registered with a code, with the fake claude.
	var code struct{ Code string }
	api.must("POST", "/api/runners/codes", map[string]any{}, http.StatusOK, &code)
	runnerEnv := append(gitEnv, "FAKECLAUDE_LOG="+calls, "PATH="+filepath.Dir(fake)+string(os.PathListSeparator)+hubPath)
	runnerArgs := []string{"runner", "-data", filepath.Join(dir, "runner"), "-projects", filepath.Join(dir, "runner-projects"),
		"-name", "laptop", "-env", filepath.Join(dir, "none.env")}
	runner := startDash(t, dash, runnerEnv, append(runnerArgs, "--server", base, "--code", code.Code)...)
	var runnerID string
	waitFor(t, runner, "the runner to come online", func() bool {
		var rs struct {
			Runners []struct {
				ID, Name string
				Online   bool
			}
		}
		api.must("GET", "/api/runners", nil, http.StatusOK, &rs)
		for _, r := range rs.Runners {
			if r.Name == "laptop" && r.Online {
				runnerID = r.ID
			}
		}
		return runnerID != ""
	})
	api.must("PATCH", "/api/projects/"+p.ID, map[string]any{"runners": []string{runnerID}}, http.StatusOK, nil)

	// A task without a title: titled on the runner, run there, and its
	// report_issue lands on the hub's board.
	var td struct{ ID string }
	api.must("POST", "/api/projects/"+p.ID+"/todos", map[string]any{"prompt": "add a done file", "enqueue": true}, http.StatusCreated, &td)
	var task struct{ Title, Status, Runner, Error string }
	waitFor(t, runner, "the task to reach review", func() bool {
		api.must("GET", "/api/todos/"+td.ID, nil, http.StatusOK, &task)
		return task.Status == "review" || task.Status == "failed"
	})
	if task.Status != "review" || task.Runner != runnerID {
		_, log := api.try("GET", "/api/todos/"+td.ID+"/log", nil)
		t.Fatalf("task: %+v\n%s", task, log)
	}
	if task.Title != "Title from the runner" {
		t.Errorf("task's title %q wasn't made on the runner", task.Title)
	}
	if _, diff := api.try("GET", "/api/todos/"+td.ID+"/diff", nil); !strings.Contains(diff, "done-by-runner.txt") {
		t.Errorf("task's diff from the runner: %s", diff)
	}
	if !api.hasIssue(p.ID, "Issue reported by the task on the runner") {
		t.Error("the task's report_issue didn't reach the hub's board")
	}

	// The general chat answers from the runner.
	if got := api.chat(addr); !strings.Contains(got, "Hello from the runner") {
		t.Errorf("chat: %q", got)
	}

	// A scan runs on the runner and files its findings on the hub.
	name := api.scanName(p.ID)
	var sr struct{ ID string }
	api.must("POST", "/api/projects/"+p.ID+"/scans/"+name+"/run", nil, http.StatusAccepted, &sr)
	var scanRun struct{ Status, Error, Activity string }
	waitFor(t, runner, "the scan to finish", func() bool {
		api.must("GET", "/api/scan-runs/"+sr.ID, nil, http.StatusOK, &scanRun)
		return scanRun.Status != "running"
	})
	if scanRun.Status != "succeeded" {
		t.Fatalf("scan run: %+v", scanRun)
	}
	if !api.hasIssue(p.ID, "Scan finding from the runner") {
		t.Error("the scan's finding isn't on the hub's board")
	}
	if _, log := api.try("GET", "/api/scan-runs/"+sr.ID+"/log", nil); !strings.Contains(log, "on runner laptop") {
		t.Errorf("scan log: %s", log)
	}
	log, _ := os.ReadFile(calls)
	for _, kind := range []string{"small", "task", "chat", "scan"} {
		if !strings.Contains(string(log), kind+" ") {
			t.Errorf("the runner's claude never ran a %s: %s", kind, log)
		}
	}

	// The runner stops: new work waits, saying no runner is online.
	runner.stop()
	waitFor(t, hubLogs, "the runner to go offline", func() bool {
		var rs struct{ Runners []struct{ Online bool } }
		api.must("GET", "/api/runners", nil, http.StatusOK, &rs)
		return len(rs.Runners) == 1 && !rs.Runners[0].Online
	})
	var next struct{ ID string }
	api.must("POST", "/api/projects/"+p.ID+"/todos", map[string]any{"title": "next", "prompt": "next", "enqueue": true}, http.StatusCreated, &next)
	var waiting struct{ Status, Waiting string }
	waitFor(t, hubLogs, "the task to wait for a runner", func() bool {
		api.must("GET", "/api/todos/"+next.ID, nil, http.StatusOK, &waiting)
		return strings.Contains(waiting.Waiting, "no runner online")
	})
	if waiting.Status != "queued" {
		t.Errorf("task without a runner: %+v", waiting)
	}
	api.must("POST", "/api/projects/"+p.ID+"/scans/"+name+"/run", nil, http.StatusAccepted, &sr)
	waitFor(t, hubLogs, "the scan to wait for a runner", func() bool {
		api.must("GET", "/api/scan-runs/"+sr.ID, nil, http.StatusOK, &scanRun)
		return scanRun.Status == "running" && strings.Contains(scanRun.Activity, "no runner online")
	})

	// Back online, the waiting work goes to it.
	runner = startDash(t, dash, runnerEnv, runnerArgs...)
	waitFor(t, runner, "the waiting task and scan to run on the runner", func() bool {
		api.must("GET", "/api/todos/"+next.ID, nil, http.StatusOK, &waiting)
		api.must("GET", "/api/scan-runs/"+sr.ID, nil, http.StatusOK, &scanRun)
		return waiting.Status == "review" && scanRun.Status != "running"
	})
	if scanRun.Status != "succeeded" && scanRun.Status != "skipped" {
		t.Errorf("scan after the runner came back: %+v", scanRun)
	}
	if strings.Contains(hubLogs.String(), "start claude") {
		t.Errorf("the hub tried to run claude itself")
	}
}

// ---- helpers ----

func run(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// pathWithout is $PATH without the directories that have bin in them.
func pathWithout(t *testing.T, bin string) string {
	var keep []string
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(d, bin)); err != nil {
			keep = append(keep, d)
		}
	}
	p := strings.Join(keep, string(os.PathListSeparator))
	for _, need := range []string{"git", "bash"} {
		if !inPath(p, need) {
			t.Skipf("%s shares a directory with %s", need, bin)
		}
	}
	return p
}

func inPath(path, bin string) bool {
	for _, d := range filepath.SplitList(path) {
		if fi, err := os.Stat(filepath.Join(d, bin)); err == nil && !fi.IsDir() {
			return true
		}
	}
	return false
}

// proc is a dash process and its log.
type proc struct {
	cmd  *exec.Cmd
	logs *syncBuffer
	name string
}

func (p *proc) String() string { return p.logs.String() }

func (p *proc) stop() {
	if p.cmd.ProcessState != nil {
		return
	}
	_ = p.cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _ = p.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = p.cmd.Process.Kill()
		<-done
	}
}

func startDash(t *testing.T, bin string, env []string, args ...string) *proc {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	p := &proc{cmd: cmd, logs: &syncBuffer{}, name: strings.Join(args[:min(2, len(args))], " ")}
	cmd.Stdout, cmd.Stderr = p.logs, p.logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		p.stop()
		if t.Failed() {
			t.Logf("log of dash %s:\n%s", p.name, p.logs.String())
		}
	})
	return p
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitFor(t *testing.T, _ *proc, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); !ok(); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

type client struct {
	t    *testing.T
	base string
	c    *http.Client
	jar  *cookiejar.Jar
}

func newClient(t *testing.T, base string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: base, jar: jar, c: &http.Client{Jar: jar, Timeout: 60 * time.Second}}
}

func (c *client) try(method, path string, body any) (int, string) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.c.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(out)
}

func (c *client) must(method, path string, body any, want int, out any) {
	c.t.Helper()
	code, b := c.try(method, path, body)
	if code != want {
		c.t.Fatalf("%s %s: %d %s", method, path, code, b)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(b), out); err != nil {
			c.t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
}

func (c *client) hasIssue(pid, title string) bool {
	c.t.Helper()
	_, b := c.try("GET", "/api/projects/"+pid+"/issues", nil)
	return strings.Contains(b, title)
}

func (c *client) scanName(pid string) string {
	c.t.Helper()
	var res struct{ Scans []struct{ Name string } }
	c.must("GET", "/api/projects/"+pid+"/scans", nil, http.StatusOK, &res)
	if len(res.Scans) == 0 {
		c.t.Fatal("no scans")
	}
	return res.Scans[0].Name
}

// chat sends one message to the general chat and returns its answer.
func (c *client) chat(addr string) string {
	c.t.Helper()
	u, _ := url.Parse(c.base)
	ws, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws/chat", http.Header{"Cookie": {cookieHeader(c.jar.Cookies(u))}})
	if err != nil {
		c.t.Fatal(err)
	}
	defer ws.Close()
	_ = ws.WriteJSON(map[string]any{"type": "send", "messages": []map[string]string{{"role": "user", "content": "hi"}}})
	_ = ws.SetReadDeadline(time.Now().Add(30 * time.Second))
	var text strings.Builder
	for {
		var d struct{ Kind, Text string }
		if err := ws.ReadJSON(&d); err != nil {
			c.t.Fatalf("chat: %v (so far %q)", err, text.String())
		}
		switch d.Kind {
		case "text":
			text.WriteString(d.Text)
		case "error":
			c.t.Fatalf("chat: %s", d.Text)
		case "done":
			return text.String()
		}
	}
}
