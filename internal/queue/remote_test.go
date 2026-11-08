package queue

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-c", "user.name=T", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// addOrigin gives the project a bare "GitHub" remote and returns a second
// clone of it, standing in for someone else pushing.
func (h *harness) addOrigin(autoPush bool, mode string) (origin, other string) {
	h.t.Helper()
	base := filepath.Dir(h.proj.Path)
	origin, other = filepath.Join(base, "origin.git"), filepath.Join(base, "other")
	gitRun(h.t, base, "init", "--bare", "-b", "main", origin)
	gitRun(h.t, h.proj.Path, "remote", "add", "origin", origin)
	gitRun(h.t, h.proj.Path, "push", "origin", "main")
	gitRun(h.t, base, "clone", origin, other)
	p, err := h.st.UpdateProject(h.proj.ID, func(p *model.Project) error {
		p.RemoteURL, p.AutoPush, p.MergeMode = origin, autoPush, mode
		return nil
	})
	if err != nil {
		h.t.Fatal(err)
	}
	h.proj = p
	return origin, other
}

func writeFile(dir, name string) func(int, agent.ExecRequest) error {
	return func(n int, req agent.ExecRequest) error {
		return os.WriteFile(filepath.Join(req.Dir, name), []byte(filepath.Base(req.Dir)+"\n"), 0o644)
	}
}

func TestMergeSyncsWithOriginAndPushes(t *testing.T) {
	h := newHarness(t, &fakeExec{fn: writeFile("", "mine.txt")}, model.Project{Name: "p", MaxAutoFixes: 1})
	origin, other := h.addOrigin(true, "")

	td := h.waitFor(h.add("mine").ID, model.StatusReview)
	// Meanwhile someone else pushes to GitHub.
	write(t, other, "theirs.txt", "theirs\n")
	gitRun(t, other, "add", "-A")
	gitRun(t, other, "commit", "-m", "theirs")
	gitRun(t, other, "push", "origin", "main")

	h.approve(td.ID)
	td = h.waitFor(td.ID, model.StatusMerged)
	if td.MergedCommit == "" {
		t.Fatal("want the merged commit recorded")
	}
	if got := gitRun(t, origin, "rev-parse", "main"); got != td.MergedCommit {
		t.Fatalf("origin/main = %s, want the todo's commit %s (pushed)", got, td.MergedCommit)
	}
	for _, f := range []string{"mine.txt", "theirs.txt"} {
		if _, err := os.Stat(filepath.Join(h.proj.Path, f)); err != nil {
			t.Fatalf("main checkout should have %s: %v", f, err)
		}
	}
	if p, _ := h.st.GetProject(h.proj.ID); p.SyncError != "" {
		t.Fatalf("unexpected sync error %q", p.SyncError)
	}
}

func TestRevertMergedTodo(t *testing.T) {
	h := newHarness(t, &fakeExec{fn: writeFile("", "feature.txt")}, model.Project{Name: "p"})
	origin, _ := h.addOrigin(true, "")
	td := h.waitFor(h.add("feature").ID, model.StatusReview)
	h.approve(td.ID)
	h.waitFor(td.ID, model.StatusMerged)

	if err := h.merges.Enqueue(model.MergeJob{TodoID: td.ID, ProjectID: h.proj.ID, Revert: true}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		x, _ := h.st.GetTodo(td.ID)
		if x.Revert != nil {
			if _, err := os.Stat(filepath.Join(h.proj.Path, "feature.txt")); !os.IsNotExist(err) {
				t.Fatalf("feature.txt should be gone after the revert (err=%v)", err)
			}
			if got := gitRun(t, origin, "rev-parse", "main"); got != x.Revert.Commit {
				t.Fatalf("revert not pushed: origin at %s, revert %s", got, x.Revert.Commit)
			}
			return
		}
		if time.Now().After(deadline) {
			log, _ := h.engine.Log.Read(td.ID)
			t.Fatalf("never reverted (error %q)\n%s", x.Error, log)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRunTimeLimitStopsClaude(t *testing.T) {
	exe := &fakeExec{block: true}
	h := newHarness(t, exe, model.Project{Name: "p", MaxRunMinutes: 1, MaxTurns: 7})
	h.engine.minute = 100 * time.Millisecond
	td := h.waitFor(h.add("forever").ID, model.StatusFailed)
	if !strings.Contains(td.Error, "1-minute limit") {
		t.Fatalf("want a time-limit error, got %q", td.Error)
	}
	if args := exe.Calls()[0].ExtraArgs; !slices.Contains(args, "--max-turns") || !slices.Contains(args, "7") {
		t.Fatalf("want --max-turns 7 passed to Claude, got %v", args)
	}
}

// A fake gh: `pr list` finds nothing, `pr create` prints a URL, and
// `pr view` prints whatever the test put in pr.json.
func fakeGh(t *testing.T, dir string) (bin, stateFile string) {
	stateFile = filepath.Join(dir, "pr.json")
	bin = filepath.Join(dir, "gh")
	script := "#!/bin/sh\ncase \"$1 $2\" in\n" +
		"\"pr list\") echo '[]' ;;\n" +
		"\"pr create\") echo https://github.com/o/r/pull/7 ;;\n" +
		"\"pr view\") cat " + stateFile + " ;;\n" +
		"*) echo \"unexpected gh $*\" >&2; exit 1 ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, stateFile
}

func TestPullRequestMode(t *testing.T) {
	h := newHarness(t, &fakeExec{fn: writeFile("", "pr.txt")}, model.Project{Name: "p"})
	origin, _ := h.addOrigin(false, model.MergePR)
	bin, state := fakeGh(t, t.TempDir())
	h.git.GhBin = bin
	write(t, filepath.Dir(state), "pr.json", `{"state":"OPEN","mergeable":"MERGEABLE","headRefOid":"abc","statusCheckRollup":[]}`)

	td := h.waitFor(h.add("via pr").ID, model.StatusReview)
	h.approve(td.ID)
	deadline := time.Now().Add(10 * time.Second)
	for {
		x, _ := h.st.GetTodo(td.ID)
		if x.PR != nil {
			if x.PR.Number != 7 || x.Status != model.StatusMerging {
				t.Fatalf("want PR #7 while merging, got %+v status %s", x.PR, x.Status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no PR opened (status %s, error %q)", x.Status, x.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := gitRun(t, origin, "rev-parse", td.Worktree.Branch); got == "" {
		t.Fatal("branch not pushed")
	}
	if gitRun(t, h.proj.Path, "log", "--oneline", "main") != gitRun(t, origin, "log", "--oneline", "main") {
		t.Fatal("PR mode must not commit to main locally")
	}

	// GitHub merges it (here: someone merges the branch on origin).
	other := filepath.Join(filepath.Dir(h.proj.Path), "other")
	gitRun(t, other, "fetch", "origin")
	gitRun(t, other, "merge", "--squash", "origin/"+td.Worktree.Branch)
	gitRun(t, other, "commit", "-m", "Merge PR 7")
	gitRun(t, other, "push", "origin", "main")
	sha := gitRun(t, other, "rev-parse", "HEAD")
	write(t, filepath.Dir(state), "pr.json", `{"state":"MERGED","mergeCommit":{"oid":"`+sha+`"}}`)
	h.merges.PollPRs(context.Background())

	x := h.waitFor(td.ID, model.StatusMerged)
	if x.MergedCommit != sha || x.PR.State != "MERGED" {
		t.Fatalf("want merged at %s, got %s (%+v)", sha, x.MergedCommit, x.PR)
	}
	if _, err := os.Stat(filepath.Join(h.proj.Path, "pr.txt")); err != nil {
		t.Fatal("local main should catch up with the merged PR")
	}
}

func TestPullRequestFailingChecksGoBackToClaude(t *testing.T) {
	h := newHarness(t, &fakeExec{fn: writeFile("", "ci.txt")}, model.Project{Name: "p", MaxAutoFixes: 1})
	h.addOrigin(false, model.MergePR)
	bin, state := fakeGh(t, t.TempDir())
	h.git.GhBin = bin
	write(t, filepath.Dir(state), "pr.json", `{"state":"OPEN","mergeable":"MERGEABLE","headRefOid":"abc","statusCheckRollup":[
		{"name":"build","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://example.com/x"}]}`)

	td := h.waitFor(h.add("ci").ID, model.StatusReview)
	h.approve(td.ID)
	deadline := time.Now().Add(10 * time.Second)
	for x, _ := h.st.GetTodo(td.ID); x.PR == nil; x, _ = h.st.GetTodo(td.ID) {
		if time.Now().After(deadline) {
			t.Fatal("no PR opened")
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.merges.PollPRs(context.Background())
	deadline = time.Now().Add(10 * time.Second)
	for {
		x, _ := h.st.GetTodo(td.ID)
		if x.AutoFixes == 1 && x.Runs == 2 && x.Status == model.StatusMerging {
			if last := x.Feedback[len(x.Feedback)-1]; !strings.Contains(last, "build") {
				t.Fatalf("want the failing check in Claude's note, got %q", last)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("want a fix run and back to merging, got status %s fixes %d runs %d", x.Status, x.AutoFixes, x.Runs)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Same head again: not sent twice.
	h.merges.PollPRs(context.Background())
	time.Sleep(200 * time.Millisecond)
	if x, _ := h.st.GetTodo(td.ID); x.AutoFixes != 1 {
		t.Fatalf("the same failure was sent back twice")
	}
}

func TestCleanupRemovesOrphansAndStaleWorktrees(t *testing.T) {
	h := newHarness(t, &fakeExec{fn: func(n int, req agent.ExecRequest) error { return errors.New("boom") }}, model.Project{Name: "p"})
	h.merges.WorktreeRoot = h.engine.WorktreeRoot
	h.merges.CleanupAfter = time.Hour
	td := h.waitFor(h.add("no changes").ID, model.StatusFailed) // Claude errored
	if td.Worktree == nil {
		t.Fatal("want a worktree")
	}
	// An orphaned worktree dir and branch from a todo that no longer exists.
	orphan := filepath.Join(h.engine.WorktreeRoot, h.proj.ID, "todo-gone")
	gitRun(t, h.proj.Path, "worktree", "add", "-b", "dash/todo-gone", orphan)

	rep, err := h.merges.Cleanup(context.Background(), h.proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Worktrees != 1 || rep.Branches != 1 {
		t.Fatalf("want the orphan removed only (failed todo is fresh), got %+v", rep)
	}
	if _, err := os.Stat(td.Worktree.Path); err != nil {
		t.Fatal("a recently failed todo's worktree must be kept")
	}

	// Once it's been failed for longer than CleanupAfter, it goes too.
	h.merges.CleanupAfter = time.Nanosecond
	rep, _ = h.merges.Cleanup(context.Background(), h.proj.ID)
	if rep.Worktrees != 1 || rep.Branches != 0 {
		t.Fatalf("want the stale worktree (and its branch with it) removed, got %+v", rep)
	}
	if x, _ := h.st.GetTodo(td.ID); x.Worktree != nil {
		t.Fatal("todo should no longer point at a worktree")
	}
	if b := gitRun(t, h.proj.Path, "branch", "--list", "dash/*"); b != "" {
		t.Fatalf("dash branches left: %q", b)
	}
}
