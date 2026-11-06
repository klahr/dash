package queue

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

type nopPub struct{}

func (nopPub) Publish(model.Event) {}

// fakeExec stands in for Claude Code: each call runs fn in the worktree.
type fakeExec struct {
	mu    sync.Mutex
	calls []agent.ExecRequest
	fn    func(n int, req agent.ExecRequest) error
	block bool // run until canceled, like a Claude that never finishes
}

func (f *fakeExec) Run(ctx context.Context, req agent.ExecRequest, emit func(string)) (agent.ExecResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	n := len(f.calls)
	f.mu.Unlock()
	res := agent.ExecResult{SessionID: "sess-" + filepath.Base(req.Dir), CostUSD: 0.01, Turns: 1, Stop: "success"}
	if f.block {
		<-ctx.Done()
		return res, ctx.Err()
	}
	return res, f.fn(n, req)
}

func (f *fakeExec) Calls() []agent.ExecRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agent.ExecRequest(nil), f.calls...)
}

type harness struct {
	t      *testing.T
	st     *store.Store
	git    *gitops.Git
	engine *TodoEngine
	merges *MergeQueue
	proj   model.Project
}

func newHarness(t *testing.T, exe agent.Executor, p model.Project) *harness {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.json"), nopPub{})
	if err != nil {
		t.Fatal(err)
	}
	g := gitops.New(nopPub{})
	p.Path, p.DefaultBranch = filepath.Join(dir, "repo"), "main"
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := g.InitRepo(ctx, "p", p.Path, "main"); err != nil {
		t.Fatal(err)
	}
	p, err = st.CreateProject(p)
	if err != nil {
		t.Fatal(err)
	}
	log := &TodoLog{Pub: nopPub{}, Root: filepath.Join(dir, "logs")}
	mq := NewMergeQueue(st, g, nopPub{}, log, 16)
	e := NewTodoEngine(&TodoEngine{
		Store: st, Git: g, Exec: exe, Pub: nopPub{}, Log: log, Merges: mq,
		WorktreeRoot: filepath.Join(dir, "worktrees"), MaxParallel: 2,
	})
	mq.Kick = e.Kick
	go e.Run(ctx)
	go mq.Run(ctx)
	return &harness{t: t, st: st, git: g, engine: e, merges: mq, proj: p}
}

func (h *harness) add(title string) model.Todo {
	h.t.Helper()
	td, err := h.st.CreateTodo(model.Todo{ProjectID: h.proj.ID, Title: title, Prompt: "do " + title})
	if err != nil {
		h.t.Fatal(err)
	}
	if td, err = h.st.SetStatus(td.ID, model.StatusQueued, ""); err != nil {
		h.t.Fatal(err)
	}
	h.engine.Kick()
	return td
}

func (h *harness) waitFor(id string, want model.TodoStatus) model.Todo {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		td, err := h.st.GetTodo(id)
		if err == nil && td.Status == want {
			return td
		}
		time.Sleep(50 * time.Millisecond)
	}
	td, _ := h.st.GetTodo(id)
	log, _ := h.engine.Log.Read(id)
	h.t.Fatalf("todo %s: want %s, got %s (error %q)\nlog:\n%s", id, want, td.Status, td.Error, log)
	return td
}

func (h *harness) approve(id string) {
	h.t.Helper()
	if _, err := h.st.SetStatus(id, model.StatusMerging, ""); err != nil {
		h.t.Fatal(err)
	}
	if err := h.merges.Enqueue(model.MergeJob{TodoID: id, ProjectID: h.proj.ID}); err != nil {
		h.t.Fatal(err)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFailingTestsGoBackToClaudeWithResume(t *testing.T) {
	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		if n == 1 {
			write(t, req.Dir, "a.txt", "broken\n")
		} else {
			write(t, req.Dir, "a.txt", "pass\n")
		}
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p", TestCommand: "grep -qx pass a.txt", MaxAutoFixes: 2})
	td := h.add("make a")
	td = h.waitFor(td.ID, model.StatusReview)

	if td.Checks == nil || !td.Checks.Passed {
		t.Fatalf("want passing checks, got %+v", td.Checks)
	}
	if td.AutoFixes != 1 || td.Runs != 2 {
		t.Fatalf("want 1 auto-fix over 2 runs, got %d / %d", td.AutoFixes, td.Runs)
	}
	if td.DiffFiles != 1 || td.DiffAdd != 1 || td.DiffDel != 0 {
		t.Fatalf("want diff stat 1 file +1 -0, got %d +%d -%d", td.DiffFiles, td.DiffAdd, td.DiffDel)
	}

	calls := exe.Calls()
	if calls[0].Resume != "" {
		t.Fatalf("first run should not resume")
	}
	if calls[1].Resume != td.SessionID || td.SessionID == "" {
		t.Fatalf("second run should resume %q, got %q", td.SessionID, calls[1].Resume)
	}
	if !strings.Contains(calls[1].Prompt, "grep -qx pass a.txt") || strings.Contains(calls[1].Prompt, "do make a") {
		t.Fatalf("resumed prompt should carry only the test failure, got:\n%s", calls[1].Prompt)
	}

	// Re-running the tests by hand records a "manual" result without Claude.
	write(t, td.Worktree.Path, "a.txt", "broken\n")
	if err := h.engine.RunTests(td.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if x, _ := h.st.GetTodo(td.ID); x.Checks != nil && x.Checks.Stage == "manual" {
			if x.Checks.Passed || x.Runs != 2 {
				t.Fatalf("manual run: want failing checks and no extra Claude run, got passed=%v runs=%d", x.Checks.Passed, x.Runs)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("manual test run never finished")
}

func TestTestsStillFailingAfterFixesReachesReview(t *testing.T) {
	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		write(t, req.Dir, "a.txt", "broken "+string(rune('0'+n))+"\n")
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p", TestCommand: "grep -qx pass a.txt", MaxAutoFixes: 1})
	td := h.waitFor(h.add("make a").ID, model.StatusReview)
	if td.Checks == nil || td.Checks.Passed || td.AutoFixes != 1 {
		t.Fatalf("want failing checks after 1 fix, got checks=%+v fixes=%d", td.Checks, td.AutoFixes)
	}
}

func TestSetupAndCopyFiles(t *testing.T) {
	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		for _, f := range []string{".env", "setup.txt"} {
			if _, err := os.Stat(filepath.Join(req.Dir, f)); err != nil {
				return err // fails the todo, which the test will report
			}
		}
		write(t, req.Dir, "done.txt", "x")
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p", SetupCommand: "echo hi > setup.txt", CopyFiles: []string{".env", "missing.txt"}})
	write(t, h.proj.Path, ".env", "SECRET=1\n")
	h.waitFor(h.add("needs env").ID, model.StatusReview)
}

func TestConflictIsResolvedByClaudeAndMerged(t *testing.T) {
	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		path := filepath.Join(req.Dir, "shared.txt")
		b, _ := os.ReadFile(path)
		switch {
		case strings.Contains(string(b), "<<<<<<<"):
			// Resolve: keep both sides.
			var keep []string
			for _, l := range strings.Split(string(b), "\n") {
				marker := false
				for _, m := range []string{"<<<<<<<", "|||||||", "=======", ">>>>>>>"} {
					marker = marker || strings.HasPrefix(l, m)
				}
				if !marker && l != "" {
					keep = append(keep, l)
				}
			}
			return os.WriteFile(path, []byte(strings.Join(keep, "\n")+"\n"), 0o644)
		default:
			return os.WriteFile(path, []byte("line from "+filepath.Base(req.Dir)+"\n"), 0o644)
		}
	}}
	h := newHarness(t, exe, model.Project{Name: "p", MaxAutoFixes: 2})
	a, b := h.add("first"), h.add("second")
	h.waitFor(a.ID, model.StatusReview)
	h.waitFor(b.ID, model.StatusReview)

	h.approve(a.ID)
	h.waitFor(a.ID, model.StatusMerged)
	h.approve(b.ID) // conflicts with a on shared.txt → Claude resolves → auto-merges
	tb := h.waitFor(b.ID, model.StatusMerged)
	if tb.AutoFixes != 1 {
		t.Fatalf("want 1 auto-fix for the conflict, got %d", tb.AutoFixes)
	}

	content, err := os.ReadFile(filepath.Join(h.proj.Path, "shared.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(content)
	if strings.Contains(got, "<<<<<<<") || !strings.Contains(got, "todo-"+a.ID) || !strings.Contains(got, "todo-"+b.ID) {
		t.Fatalf("main should contain both changes and no markers, got:\n%s", got)
	}
	// Squash: initial commit + one commit per todo.
	out, err := exec.Command("git", "-C", h.proj.Path, "log", "--oneline", "main").Output()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Split(strings.TrimSpace(string(out)), "\n")); n != 3 {
		t.Fatalf("want 3 commits on main (init + 2 squashes), got %d:\n%s", n, out)
	}
}

func TestConflictWithoutFixesLeftFails(t *testing.T) {
	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		return os.WriteFile(filepath.Join(req.Dir, "shared.txt"), []byte(filepath.Base(req.Dir)+"\n"), 0o644)
	}}
	h := newHarness(t, exe, model.Project{Name: "p", MaxAutoFixes: 0})
	a, b := h.add("first"), h.add("second")
	h.waitFor(a.ID, model.StatusReview)
	h.waitFor(b.ID, model.StatusReview)
	h.approve(a.ID)
	h.waitFor(a.ID, model.StatusMerged)
	h.approve(b.ID)
	tb := h.waitFor(b.ID, model.StatusFailed)
	if !tb.MergeMain || !strings.Contains(tb.Error, "conflicts") {
		t.Fatalf("want failed with MergeMain set for the rework, got merge_main=%v error=%q", tb.MergeMain, tb.Error)
	}
}

// Resolving a conflict by keeping exactly our side leaves nothing to stage;
// the merge must still be concluded.
func TestConflictResolvedAsOursStillMerges(t *testing.T) {
	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		return os.WriteFile(filepath.Join(req.Dir, "shared.txt"), []byte(filepath.Base(req.Dir)+"\n"), 0o644)
	}}
	h := newHarness(t, exe, model.Project{Name: "p", MaxAutoFixes: 1})
	a, b := h.add("first"), h.add("second")
	h.waitFor(a.ID, model.StatusReview)
	h.waitFor(b.ID, model.StatusReview)
	h.approve(a.ID)
	h.waitFor(a.ID, model.StatusMerged)
	h.approve(b.ID)
	h.waitFor(b.ID, model.StatusMerged)
}

// A commit on main while the merge re-tests is merged in and tested before landing.
func TestMainMovingDuringMergeIsMergedAgain(t *testing.T) {
	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		write(t, req.Dir, "a.txt", "a\n")
		return nil
	}}
	runs := filepath.Join(t.TempDir(), "runs")
	h := newHarness(t, exe, model.Project{Name: "p", MaxAutoFixes: 1})
	commit := func(name string) string {
		return "echo x > " + name + " && git add " + name + " && git -c user.name=t -c user.email=t@t commit -qm " + name
	}
	// Run 1 is the review, run 2 the merge: it commits on main, so run 3 must follow.
	h.proj, _ = h.st.UpdateProject(h.proj.ID, func(p *model.Project) error {
		p.TestCommand = "echo >> " + runs + "; if [ $(wc -l < " + runs + ") -eq 2 ]; then (cd " + p.Path + " && " + commit("c.txt") + "); fi"
		return nil
	})
	td := h.waitFor(h.add("a").ID, model.StatusReview)
	if out, err := exec.Command("sh", "-c", "cd "+h.proj.Path+" && "+commit("b.txt")).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	h.approve(td.ID)
	h.waitFor(td.ID, model.StatusMerged)

	for _, f := range []string{"a.txt", "b.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(h.proj.Path, f)); err != nil {
			t.Fatalf("main should have %s: %v", f, err)
		}
	}
	if b, _ := os.ReadFile(runs); strings.Count(string(b), "\n") != 3 {
		t.Fatalf("want 3 test runs (review, merge, re-merge), got %d", strings.Count(string(b), "\n"))
	}
	if log, _ := h.engine.Log.Read(td.ID); !strings.Contains(log, "moved during the merge") {
		t.Fatalf("log should mention the re-merge:\n%s", log)
	}
}
