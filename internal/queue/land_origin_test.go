package queue

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/testbin"
)

// runnerProject makes h's project a runner's clone of a hub's project in
// the default ("local") merge mode, with an origin, and a gh that records
// every call (none is expected: no pull request is ever opened).
func (h *harness) runnerProject() (origin, other, ghCalls string) {
	h.t.Helper()
	origin, other = h.addOrigin(false, "")
	p, err := h.st.UpdateProject(h.proj.ID, func(p *model.Project) error { p.HubProjectID = "hub-p"; return nil })
	if err != nil {
		h.t.Fatal(err)
	}
	h.proj = p
	if !p.LandsOnOrigin() {
		h.t.Fatalf("a runner's clone in local mode should land on origin: %+v", p)
	}
	dir := h.t.TempDir()
	ghCalls = filepath.Join(dir, "calls")
	testbin.Write(h.t, filepath.Join(dir, "gh"), "#!/bin/sh\necho \"$*\" >> "+ghCalls+"\nexit 1\n")
	h.git.GhBin = filepath.Join(dir, "gh")
	return origin, other, ghCalls
}

// pushFrom commits name in the clone dir and pushes it to origin, as
// another runner (or a person) would.
func pushFrom(t *testing.T, dir, name, content string) {
	t.Helper()
	gitRun(t, dir, "pull", "-q", "--rebase", "origin", "main")
	write(t, dir, name, content)
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "elsewhere: "+name)
	gitRun(t, dir, "push", "-q", "origin", "main")
}

func noPR(t *testing.T, td model.Todo, ghCalls string) {
	t.Helper()
	if td.PR != nil {
		t.Errorf("a pull request was recorded: %+v", td.PR)
	}
	if b, err := os.ReadFile(ghCalls); err == nil {
		t.Errorf("gh was called: %s", b)
	}
}

// A hub's task on a runner lands on origin's default branch: squashed in
// the runner's clone, pushed, and merged with that commit. No pull request.
// Reverting it pushes the revert the same way.
func TestRunnerLandsHubTaskOnOriginWithoutPR(t *testing.T) {
	parallel(t)

	h := newHarness(t, &fakeExec{fn: writeFile("", "feature.txt")}, model.Project{Name: "p"})
	origin, _, ghCalls := h.runnerProject()

	td := h.waitFor(h.add("feature").ID, model.StatusReview)
	h.approve(td.ID)
	td = h.waitFor(td.ID, model.StatusMerged)
	if td.MergedCommit == "" || gitRun(t, origin, "rev-parse", "main") != td.MergedCommit {
		t.Fatalf("origin's main is %s, want the task's commit %q", gitRun(t, origin, "rev-parse", "main"), td.MergedCommit)
	}
	if msg := gitRun(t, origin, "log", "-1", "--format=%B", "main"); !strings.Contains(msg, "Dash-Todo: "+td.ID) {
		t.Errorf("origin's last commit isn't the task's squash: %s", msg)
	}
	if got := gitRun(t, h.proj.Path, "rev-parse", "main"); got != td.MergedCommit {
		t.Errorf("the clone's main is %s, want %s", got, td.MergedCommit)
	}
	noPR(t, td, ghCalls)

	if err := h.merges.Enqueue(model.MergeJob{TodoID: td.ID, ProjectID: h.proj.ID, Revert: true}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for x, _ := h.st.GetTodo(td.ID); x.Revert == nil; x, _ = h.st.GetTodo(td.ID) {
		if time.Now().After(deadline) {
			log, _ := h.engine.Log.Read(td.ID)
			t.Fatalf("never reverted (error %q)\n%s", x.Error, log)
		}
		time.Sleep(50 * time.Millisecond)
	}
	x, _ := h.st.GetTodo(td.ID)
	if got := gitRun(t, origin, "rev-parse", "main"); got != x.Revert.Commit || x.Revert.PRURL != "" {
		t.Fatalf("revert %+v not pushed: origin's main is %s", x.Revert, got)
	}
	noPR(t, x, ghCalls)
}

// Origin moves between the runner's sync and its push: the push is
// rejected, so it merges again on top of origin's new tip, runs the checks
// again and pushes.
func TestRunnerRetriesPushWhenOriginMoved(t *testing.T) {
	parallel(t)

	h := newHarness(t, &fakeExec{fn: writeFile("", "mine.txt")}, model.Project{Name: "p", TestCommand: "test -f mine.txt"})
	origin, other, ghCalls := h.runnerProject()

	td := h.waitFor(h.add("mine").ID, model.StatusReview)
	var once sync.Once
	h.merges.beforePush = func() { once.Do(func() { pushFrom(t, other, "theirs.txt", "theirs\n") }) }
	h.approve(td.ID)
	td = h.waitFor(td.ID, model.StatusMerged)

	if got := gitRun(t, origin, "rev-parse", "main"); got != td.MergedCommit {
		t.Fatalf("origin's main is %s, want the task's commit %s", got, td.MergedCommit)
	}
	for _, f := range []string{"mine.txt", "theirs.txt"} {
		if _, err := gitRunErr(origin, "cat-file", "-e", "main:"+f); err != nil {
			t.Errorf("origin's main lacks %s", f)
		}
	}
	if td.Checks == nil || td.Checks.Stage != "merge" || !td.Checks.Passed {
		t.Errorf("the checks weren't run again on top of origin's new tip: %+v", td.Checks)
	}
	log, _ := h.engine.Log.Read(td.ID)
	if !strings.Contains(log, "moved on before the push") {
		t.Errorf("the log doesn't tell of the retry:\n%s", log)
	}
	if td.AutoFixes != 0 {
		t.Errorf("a clean retry used %d fixes", td.AutoFixes)
	}
	noPR(t, td, ghCalls)
}

// Origin moves before the push with a change to the same lines: merging
// again conflicts, which goes back to Claude as a normal fix pass; once
// fixed it lands on origin.
func TestRunnerPushRaceConflictGoesToClaude(t *testing.T) {
	parallel(t)

	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		path := filepath.Join(req.Dir, "shared.txt")
		b, _ := os.ReadFile(path)
		if strings.Contains(string(b), "<<<<<<<") {
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
		}
		return os.WriteFile(path, []byte("mine\n"), 0o644)
	}}
	h := newHarness(t, exe, model.Project{Name: "p", MaxAutoFixes: 2})
	origin, other, ghCalls := h.runnerProject()

	td := h.waitFor(h.add("mine").ID, model.StatusReview)
	var once sync.Once
	h.merges.beforePush = func() { once.Do(func() { pushFrom(t, other, "shared.txt", "theirs\n") }) }
	h.approve(td.ID)
	td = h.waitFor(td.ID, model.StatusMerged)

	if td.AutoFixes != 1 {
		t.Errorf("want one fix pass for the conflict, got %d", td.AutoFixes)
	}
	if got := gitRun(t, origin, "rev-parse", "main"); got != td.MergedCommit {
		t.Fatalf("origin's main is %s, want the task's commit %s", got, td.MergedCommit)
	}
	shared := gitRun(t, origin, "show", "main:shared.txt")
	if !strings.Contains(shared, "mine") || !strings.Contains(shared, "theirs") || strings.Contains(shared, "<<<<<<<") {
		t.Errorf("origin's shared.txt should have both changes:\n%s", shared)
	}
	noPR(t, td, ghCalls)
}

// Without fixes left, the conflict after a push race fails the task with
// a clear error, and origin keeps only the other push.
func TestRunnerPushRaceConflictWithoutFixesFails(t *testing.T) {
	parallel(t)

	h := newHarness(t, &fakeExec{fn: writeFile("", "shared.txt")}, model.Project{Name: "p", MaxAutoFixes: 0})
	origin, other, ghCalls := h.runnerProject()

	td := h.waitFor(h.add("mine").ID, model.StatusReview)
	h.merges.beforePush = func() { pushFrom(t, other, "shared.txt", "theirs\n") }
	h.approve(td.ID)
	td = h.waitFor(td.ID, model.StatusFailed)
	if !strings.Contains(td.Error, "conflicts with main in shared.txt") {
		t.Errorf("error %q doesn't say what conflicted", td.Error)
	}
	if shared := gitRun(t, origin, "show", "main:shared.txt"); shared != "theirs" {
		t.Errorf("origin's shared.txt = %q, want only the other push", shared)
	}
	noPR(t, td, ghCalls)
}

// A task whose pull request was opened before the project switched to
// merging locally keeps its pull request: sent back to the merge queue it
// updates it rather than landing twice.
func TestOpenPullRequestIsFollowedAfterSwitchingToLocal(t *testing.T) {
	parallel(t)

	h := newHarness(t, &fakeExec{fn: writeFile("", "pr.txt")}, model.Project{Name: "p"})
	origin, _ := h.addOrigin(false, model.MergePR)
	if _, err := h.st.UpdateProject(h.proj.ID, func(p *model.Project) error { p.HubProjectID = "hub-p"; return nil }); err != nil {
		t.Fatal(err)
	}
	bin, _ := fakeGh(t, t.TempDir())
	h.git.GhBin = bin
	td := h.waitFor(h.add("pr").ID, model.StatusReview)
	h.approve(td.ID)
	deadline := time.Now().Add(10 * time.Second)
	for x, _ := h.st.GetTodo(td.ID); x.PR == nil; x, _ = h.st.GetTodo(td.ID) {
		if time.Now().After(deadline) {
			t.Fatal("no PR opened")
		}
		time.Sleep(50 * time.Millisecond)
	}
	before := gitRun(t, origin, "rev-parse", "main")

	// The hub switches the project to merging; the task is merged again
	// (e.g. after a fix pass).
	if _, err := h.st.UpdateProject(h.proj.ID, func(p *model.Project) error { p.MergeMode = model.MergeLocal; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := h.merges.Enqueue(model.MergeJob{TodoID: td.ID, ProjectID: h.proj.ID}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	x, _ := h.st.GetTodo(td.ID)
	if x.Status != model.StatusMerging || x.PR == nil || x.PR.State != "OPEN" {
		t.Errorf("want it still merging through its pull request, got %s %+v", x.Status, x.PR)
	}
	if got := gitRun(t, origin, "rev-parse", "main"); got != before {
		t.Errorf("origin's main moved to %s: landed besides its open pull request", got)
	}
}

func gitRunErr(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
