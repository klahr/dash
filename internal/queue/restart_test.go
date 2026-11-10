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
	"github.com/klahr/dash/internal/model"
)

// A shutdown in the middle of a merge (here, while the tests run again on
// top of a main that moved) leaves the todo merging, not failed or sent back
// to Claude, and nothing lands half-way. At the next start the merge runs
// again from the todo's own commits, also when a merge was left unfinished
// in its worktree, and lands once.
func TestMergeCutOffByShutdownResumesAtTheNextStart(t *testing.T) {
	parallel(t)

	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		write(t, req.Dir, "a.txt", "a\n")
		return nil
	}}
	dir := t.TempDir()
	runs, started := filepath.Join(dir, "runs"), filepath.Join(dir, "started")
	h := newHarness(t, exe, model.Project{Name: "p"})
	sh := func(script string) {
		t.Helper()
		cmd := exec.Command("sh", "-c", script)
		cmd.Dir = h.proj.Path
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", script, err, out)
		}
	}
	// Run 1 is the task's own; run 2, at the merge, hangs until the shutdown.
	h.proj, _ = h.st.UpdateProject(h.proj.ID, func(p *model.Project) error {
		p.TestCommand = "echo >> " + runs + "; if [ $(wc -l < " + runs + ") -eq 2 ]; then touch " + started + "; sleep 30; fi"
		return nil
	})
	td := h.waitFor(h.add("a").ID, model.StatusReview)
	before := headOf(t, h, "main")
	sh("echo b > b.txt && git add b.txt && git -c commit.gpgsign=false commit -qm b") // main moves: the merge tests again
	moved := headOf(t, h, "main")
	h.approve(td.ID)
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the merge's tests never started")
		}
	}

	h.stop() // SIGTERM
	got, _ := h.st.GetTodo(td.ID)
	if got.Status != model.StatusMerging || got.Error != "" {
		t.Fatalf("after a shutdown mid-merge: %s %q", got.Status, got.Error)
	}
	if head := headOf(t, h, "main"); head != moved || head == before {
		t.Fatalf("main moved during the cut-off merge: %s", head)
	}
	if log, _ := h.log.Read(td.ID); !strings.Contains(log, "merge interrupted by shutdown; will resume on restart") {
		t.Errorf("log:\n%s", log)
	}

	// A merge of another branch left unfinished in the worktree (a crash in
	// the middle of git merge) is undone, not committed with the todo.
	sh("git branch side && git checkout -q side && echo s > side.txt && git add side.txt && git -c commit.gpgsign=false commit -qm side && git checkout -q main")
	wt := got.Worktree.Path
	cmd := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@t", "merge", "--no-commit", "--no-ff", "side")
	cmd.Dir = wt
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start a merge in the worktree: %v\n%s", err, out)
	}

	// The next start: a new merge worker on the same database and checkouts.
	mq := NewMergeQueue(h.st, h.git, nopPub{}, h.log, 16)
	mq.WorktreeRoot = h.merges.WorktreeRoot
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); mq.Run(ctx) }()
	t.Cleanup(func() { cancel(); wg.Wait() })
	mq.Resume()
	deadline := time.Now().Add(20 * time.Second)
	for got, _ = h.st.GetTodo(td.ID); got.Status == model.StatusMerging && time.Now().Before(deadline); got, _ = h.st.GetTodo(td.ID) {
		time.Sleep(50 * time.Millisecond)
	}
	if got.Status != model.StatusMerged {
		log, _ := h.log.Read(td.ID)
		t.Fatalf("after the restart: %s %q\n%s", got.Status, got.Error, log)
	}
	if _, err := os.Stat(filepath.Join(h.proj.Path, "a.txt")); err != nil {
		t.Error("main hasn't the todo's change")
	}
	if _, err := os.Stat(filepath.Join(h.proj.Path, "side.txt")); err == nil {
		t.Error("the unfinished merge of side was landed with the todo")
	}
	out, _ := exec.Command("git", "-C", h.proj.Path, "log", "--format=%s", before+"..main").Output()
	if n := strings.Count(string(out), "\n"); n != 2 { // b, and the todo's squash
		t.Errorf("main gained %d commits, want 2:\n%s", n, out)
	}
}

func headOf(t *testing.T, h *harness, ref string) string {
	t.Helper()
	sha, err := h.git.Head(context.Background(), h.proj.ID, h.proj.Path, ref)
	if err != nil {
		t.Fatal(err)
	}
	return sha
}
