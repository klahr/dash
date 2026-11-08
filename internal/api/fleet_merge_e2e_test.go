package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/tunnel"
)

// writeExec stands in for Claude Code on the runner: it writes a file in
// the task's worktree.
type writeExec struct{ name string }

func (e writeExec) Run(ctx context.Context, req agent.ExecRequest, emit func(model.RunEvent)) (agent.ExecResult, error) {
	err := os.WriteFile(filepath.Join(req.Dir, e.name), []byte("added by "+filepath.Base(req.Dir)+"\n"), 0o644)
	return agent.ExecResult{SessionID: "s-" + filepath.Base(req.Dir), Turns: 1, Stop: "success"}, err
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-c", "user.name=T", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// A hub's task runs on a runner and, once approved on the hub, lands on
// origin's default branch: the runner squashes and pushes it, no pull
// request is opened, and the hub shows it merged with the pushed commit.
// Reverting it on the hub reverts it on origin the same way.
func TestHubTaskOnRunnerLandsOnOriginWithoutPR(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// The hub
	hub, _, _ := boardServer(t)
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	d := &dispatch.Dispatcher{Store: raw(hub), Fleet: rh, Grace: time.Minute}
	rh.OnState, rh.OnRevoke, rh.OnReport = d.State, d.Revoked, d.Report
	engine := queue.NewTodoEngine(&queue.TodoEngine{Store: raw(hub), Pub: hub.Hub, Log: &queue.TodoLog{Root: t.TempDir()},
		Remote: d, LocalTasks: func() bool { return false }})
	d.Kick = engine.Kick
	hub.Todos, hub.Dispatch = engine, d
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	wg.Add(1)
	go func() { defer wg.Done(); engine.Run(ctx) }()
	srv := httptest.NewServer(hub.Routes())
	defer srv.Close()

	// GitHub: a bare repository with a first commit.
	origin := filepath.Join(t.TempDir(), "origin.git")
	git(t, filepath.Dir(origin), "init", "-q", "--bare", "-b", "main", origin)
	seed := filepath.Join(t.TempDir(), "seed")
	git(t, filepath.Dir(seed), "clone", "-q", origin, seed)
	if err := os.WriteFile(filepath.Join(seed, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-q", "-m", "first")
	git(t, seed, "push", "-q", "origin", "HEAD:main")

	// The runner, with Claude, its engine and merge queue, and a gh that
	// fails the test if anything calls it.
	owner := testOwner(t, hub)
	code, _, err := rh.NewCode(owner)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := tunnel.Register(ctx, srv.URL, code, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	runner, runnerAPI, _ := runnerSide(t, creds.OrgID)
	gh := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\necho \"$*\" >> "+gh+".calls\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner.Git.GhBin = gh
	rlog := &queue.TodoLog{Pub: runner.Hub, Root: t.TempDir()}
	merges := queue.NewMergeQueue(raw(runner), runner.Git, runner.Hub, rlog, 16)
	merges.WorktreeRoot = filepath.Join(t.TempDir(), "worktrees")
	runner.Merges = merges
	runner.Todos = queue.NewTodoEngine(&queue.TodoEngine{Store: raw(runner), Git: runner.Git, Exec: writeExec{"about.txt"},
		Pub: runner.Hub, Log: rlog, Merges: merges, WorktreeRoot: merges.WorktreeRoot, MaxParallel: 1})
	merges.Kick = runner.Todos.Kick
	wg.Add(2)
	go func() { defer wg.Done(); runner.Todos.Run(ctx) }()
	go func() { defer wg.Done(); merges.Run(ctx) }()
	reporter := &dispatch.Reporter{Store: raw(runner), Parallel: 1,
		Send: func(ctx context.Context, r dispatch.Report) error { return tunnel.Report(ctx, creds, r) }}
	client := &tunnel.Client{Creds: creds, Handler: runnerAPI, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	wg.Add(1)
	go func() { defer wg.Done(); _ = client.Run(ctx) }()
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })

	// The hub's project, linked to the runner; nobody chose a merge mode.
	p, err := raw(hub).CreateProject(model.Project{Name: "shared", OrgID: owner.Org, RemoteURL: origin, DefaultBranch: "main",
		Runners: []string{creds.ID}})
	if err != nil {
		t.Fatal(err)
	}
	td, _ := raw(hub).CreateTodo(model.Todo{ProjectID: p.ID, Title: "add a page", Prompt: "add /about"})
	if _, err := raw(hub).SetStatus(td.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	engine.Kick()
	waitHub := func(what string, ok func(model.Todo) bool) model.Todo {
		t.Helper()
		var x model.Todo
		eventually(t, what, func() bool {
			_ = reporter.Send(ctx, reporter.Build(time.Now()))
			x, _ = raw(hub).GetTodo(td.ID)
			return ok(x)
		})
		return x
	}
	waitHub("the task to be ready for review on the hub", func(x model.Todo) bool { return x.Status == model.StatusReview })

	// Approved on the hub: the runner merges and pushes it.
	w := httptest.NewRecorder()
	hub.mux().ServeHTTP(w, asOwner(t, hub, httptest.NewRequest("POST", "/api/todos/"+td.ID+"/approve", nil)))
	if w.Code != http.StatusAccepted && w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	merged := waitHub("the hub to show the task merged", func(x model.Todo) bool { return x.Status == model.StatusMerged })
	tip := git(t, origin, "rev-parse", "main")
	if merged.MergedCommit != tip {
		t.Errorf("hub's merged commit %q, origin's main %s", merged.MergedCommit, tip)
	}
	if merged.PR != nil {
		t.Errorf("a pull request: %+v", merged.PR)
	}
	if out := git(t, origin, "show", "main:about.txt"); !strings.Contains(out, "added by") {
		t.Errorf("origin's main lacks the task's file: %q", out)
	}
	if msg := git(t, origin, "log", "-1", "--format=%B", "main"); !strings.Contains(msg, "add a page") {
		t.Errorf("origin's last commit isn't the task's squash: %s", msg)
	}
	if b, err := os.ReadFile(gh + ".calls"); err == nil {
		t.Errorf("gh was called on the runner: %s", b)
	}

	// Reverted from the hub: the runner pushes the revert.
	w = httptest.NewRecorder()
	hub.mux().ServeHTTP(w, asOwner(t, hub, httptest.NewRequest("POST", "/api/todos/"+td.ID+"/revert", nil)))
	if w.Code != http.StatusAccepted {
		t.Fatalf("revert: %d %s", w.Code, w.Body)
	}
	reverted := waitHub("the hub to show the revert", func(x model.Todo) bool { return x.Revert != nil })
	if tip := git(t, origin, "rev-parse", "main"); reverted.Revert.Commit != tip {
		t.Errorf("hub's revert commit %q, origin's main %s", reverted.Revert.Commit, tip)
	}
	if _, err := exec.Command("git", "-C", origin, "cat-file", "-e", "main:about.txt").CombinedOutput(); err == nil {
		t.Error("origin's main still has the reverted file")
	}
}
