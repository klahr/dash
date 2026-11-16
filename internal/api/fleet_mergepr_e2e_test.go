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

	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/testbin"
	"github.com/klahr/dash/internal/tunnel"
)

// Merge pull request on the hub goes to the runner that follows the task's
// pull request: the merge runs with the runner's gh, with the hub
// project's merge method, and the hub shows the task merged.
func TestHubMergesPullRequestOnRunner(t *testing.T) {
	parallel(t)

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	hub, _, _ := boardServer(t)
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	d := &dispatch.Dispatcher{Store: raw(hub), Fleet: rh, Grace: time.Minute}
	rh.OnState, rh.OnRevoke, rh.OnReport = d.State, d.Revoked, d.Report
	engine := queue.NewTodoEngine(&queue.TodoEngine{Store: raw(hub), Pub: hub.Hub, Log: &queue.TodoLog{Root: t.TempDir()}, Remote: d})
	d.Kick = engine.Kick
	hub.Todos, hub.Dispatch = engine, d
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	wg.Add(1)
	go func() { defer wg.Done(); engine.Run(ctx) }()
	srv := httptest.NewServer(hub.Routes())
	defer srv.Close()

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
	// The runner's gh: pull request #7 opens, pr view prints pr.json, and
	// pr merge makes it merged.json.
	ghDir := t.TempDir()
	file := func(name string) string { return filepath.Join(ghDir, name) }
	gh := file("gh")
	testbin.Write(t, gh, "#!/bin/sh\necho \"$*\" >> "+file("calls")+"\ncase \"$1 $2\" in\n"+
		"\"pr list\") echo '[]' ;;\n"+
		"\"pr create\") echo https://github.com/o/r/pull/7 ;;\n"+
		"\"pr view\") cat "+file("pr.json")+" ;;\n"+
		"\"pr ready\") ;;\n"+
		"\"pr merge\") cp "+file("merged.json")+" "+file("pr.json")+" ;;\n"+
		"\"api user\") echo octocat ;;\n"+
		"\"api \"*) echo '[]' ;;\n"+
		"*) exit 1 ;;\nesac\n")
	if err := os.WriteFile(file("pr.json"), []byte(`{"state":"OPEN","mergeable":"UNKNOWN","headRefOid":""}`), 0o644); err != nil {
		t.Fatal(err)
	}
	runner.Git.GhBin = gh
	rlog := &queue.TodoLog{Pub: runner.Hub, Root: t.TempDir()}
	merges := queue.NewMergeQueue(raw(runner), runner.Git, runner.Hub, rlog, 16)
	merges.WorktreeRoot = filepath.Join(t.TempDir(), "worktrees")
	merges.PRPoll = time.Hour // only the merge from Dash follows it
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

	p, err := raw(hub).CreateProject(model.Project{Name: "shared", OrgID: owner.Org, RemoteURL: origin, DefaultBranch: "main",
		Runners: []string{creds.ID}, MergeMode: model.MergePR, MergeModeSet: true, MergeMethod: "rebase"})
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
	x := waitHub("the task's pull request open on the hub", func(x model.Todo) bool { return x.Status == model.StatusReview && x.WaitsOnGitHub() })

	// GitHub merges the branch when asked (here: merged on origin first).
	branch := x.Worktree.Branch
	other := filepath.Join(t.TempDir(), "other")
	git(t, filepath.Dir(other), "clone", "-q", origin, other)
	git(t, other, "merge", "-q", "--squash", "origin/"+branch)
	git(t, other, "commit", "-q", "-m", "Merge PR 7")
	git(t, other, "push", "-q", "origin", "main")
	sha := git(t, other, "rev-parse", "HEAD")
	if err := os.WriteFile(file("merged.json"), []byte(`{"state":"MERGED","mergeCommit":{"oid":"`+sha+`"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	hub.mux().ServeHTTP(w, asOwner(t, hub, httptest.NewRequest("POST", "/api/todos/"+td.ID+"/merge-pr", nil)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"as":"octocat"`) {
		t.Fatalf("merge: %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(file("calls")); !strings.Contains(string(b), "pr merge 7 --rebase") {
		t.Fatalf("the runner's gh wasn't asked to merge with the hub project's method:\n%s", b)
	}
	merged := waitHub("the hub to show the task merged", func(x model.Todo) bool { return x.Status == model.StatusMerged })
	if merged.MergedCommit != sha {
		t.Errorf("hub's merged commit %q, want %s", merged.MergedCommit, sha)
	}
}
