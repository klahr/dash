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
	"github.com/klahr/dash/internal/testbin"
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
	parallel(t)

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

// A manual push when origin has moved on brings in origin's commits first
// instead of failing as a non-fast-forward.
func TestPushDefaultSyncsWhenOriginMoved(t *testing.T) {
	parallel(t)

	h := newHarness(t, &fakeExec{}, model.Project{Name: "p"})
	origin, other := h.addOrigin(false, "")

	write(t, h.proj.Path, "mine.txt", "mine\n")
	gitRun(t, h.proj.Path, "add", "-A")
	gitRun(t, h.proj.Path, "commit", "-m", "mine")
	write(t, other, "theirs.txt", "theirs\n")
	gitRun(t, other, "add", "-A")
	gitRun(t, other, "commit", "-m", "theirs")
	gitRun(t, other, "push", "origin", "main")

	if err := h.merges.PushDefault(context.Background(), h.proj); err != nil {
		t.Fatal(err)
	}
	if got, want := gitRun(t, origin, "rev-parse", "main"), gitRun(t, h.proj.Path, "rev-parse", "main"); got != want {
		t.Fatalf("origin/main = %s, want local main %s", got, want)
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
	parallel(t)

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
	parallel(t)

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

// A fake gh: `pr list` finds nothing, `pr create` prints a URL, `pr view`
// prints whatever the test put in pr.json, the REST calls for reviews and
// review comments print reviews.json and comments.json (none when missing),
// the GraphQL query for review threads prints threads.json, and `pr merge`
// fails: Dash never merges a pull request. Comments, replies, review
// requests and resolving threads succeed, unless the test made a file
// "down": then they fail like an unreachable GitHub. Every call is logged
// to calls.
func fakeGh(t *testing.T, dir string) (bin, stateFile string) {
	stateFile = filepath.Join(dir, "pr.json")
	bin = filepath.Join(dir, "gh")
	file := func(name string) string { return filepath.Join(dir, name) }
	script := "#!/bin/sh\necho \"$*\" >> " + file("calls") + "\ncase \"$1 $2\" in\n" +
		"\"pr list\") echo '[]' ;;\n" +
		"\"pr create\") echo https://github.com/o/r/pull/7 ;;\n" +
		"\"pr view\") cat " + stateFile + " ;;\n" +
		"\"pr ready\"|\"pr close\") ;;\n" +
		"\"pr comment\") [ ! -f " + file("down") + " ] ;;\n" +
		"\"api \"*) case \"$*\" in\n" +
		"  *resolveReviewThread*|*\"-X POST\"*) [ ! -f " + file("down") + " ] ;;\n" +
		"  *graphql*) cat " + file("threads.json") + " 2>/dev/null || echo '{}' ;;\n" +
		"  */reviews*) cat " + file("reviews.json") + " 2>/dev/null || echo '[]' ;;\n" +
		"  */comments*) cat " + file("comments.json") + " 2>/dev/null || echo '[]' ;;\n" +
		"  *) exit 1 ;;\n  esac ;;\n" +
		"*) echo \"unexpected gh $*\" >&2; exit 1 ;;\nesac\n"
	testbin.Write(t, bin, script)
	return bin, stateFile
}

// ghCalls is what the fake gh in dir was asked, one call a line.
func ghCalls(t *testing.T, dir string) string {
	b, _ := os.ReadFile(filepath.Join(dir, "calls"))
	return string(b)
}

// openPRView is pr.json for an open pull request at head with checks (JSON).
func openPRView(head, checks string) string {
	return `{"state":"OPEN","mergeable":"MERGEABLE","headRefOid":"` + head + `","statusCheckRollup":[` + checks + `]}`
}

// prHarness is a project in pull request mode with the fake gh, whose
// Claude writes file name in every run.
func prHarness(t *testing.T, name string, p model.Project) (h *harness, dir string) {
	return prHarnessWith(t, &fakeExec{fn: writeFile("", name)}, p)
}

// prHarnessWith is prHarness with Claude played by exe.
func prHarnessWith(t *testing.T, exe agent.Executor, p model.Project) (h *harness, dir string) {
	p.Name = "p"
	h = newHarness(t, exe, p)
	h.addOrigin(false, model.MergePR)
	dir = t.TempDir()
	bin, _ := fakeGh(t, dir)
	h.git.GhBin = bin
	write(t, dir, "pr.json", `{"state":"OPEN","mergeable":"UNKNOWN","headRefOid":"","statusCheckRollup":[]}`)
	return h, dir
}

// When Claude finishes a task its branch is pushed and a draft pull
// request opens at once: the task waits in Review, linked to it, with no
// approval in Dash.
func TestPullRequestOpensWhenTaskFinishes(t *testing.T) {
	parallel(t)

	h, dir := prHarness(t, "pr.txt", model.Project{})
	td := h.waitFor(h.add("via pr").ID, model.StatusReview)
	if td.PR == nil || td.PR.Number != 7 || td.PR.State != "OPEN" || td.PR.URL != "https://github.com/o/r/pull/7" || td.Error != "" {
		t.Fatalf("want PR #7 open in review, got %+v (error %q)", td.PR, td.Error)
	}
	if calls := ghCalls(t, dir); !strings.Contains(calls, "pr create --base main --head "+td.Worktree.Branch) || !strings.Contains(calls, "--draft") {
		t.Fatalf("want a draft pull request opened, gh calls:\n%s", calls)
	}
	origin := filepath.Join(filepath.Dir(h.proj.Path), "origin.git")
	if got := gitRun(t, origin, "rev-parse", td.Worktree.Branch); got != gitRun(t, td.Worktree.Path, "rev-parse", "HEAD") {
		t.Fatal("branch not pushed")
	}
	if gitRun(t, h.proj.Path, "log", "--oneline", "main") != gitRun(t, origin, "log", "--oneline", "main") {
		t.Fatal("PR mode must not commit to main locally")
	}

	// Approving it in Dash isn't the review: that happens on GitHub.
	if err := reviewedElsewhere(h, td.ID); err == nil {
		t.Fatal("a task in review with its pull request open shouldn't be approvable in Dash")
	}

	// Its checks pass: it is marked ready for review, and waits for one.
	head := gitRun(t, td.Worktree.Path, "rev-parse", "HEAD")
	write(t, dir, "pr.json", `{"state":"OPEN","mergeable":"MERGEABLE","isDraft":true,"headRefOid":"`+head+`","statusCheckRollup":[
		{"name":"build","status":"COMPLETED","conclusion":"SUCCESS"}]}`)
	h.merges.PollPRs(context.Background())
	if calls := ghCalls(t, dir); !strings.Contains(calls, "pr ready 7") || strings.Contains(calls, "pr merge") {
		t.Fatalf("want it marked ready and not merged, gh calls:\n%s", calls)
	}
	x, _ := h.st.GetTodo(td.ID)
	if x.Status != model.StatusReview || x.PR.Draft || x.PR.Review != model.PRReviewPending || x.PR.Status != "Waiting for a review on GitHub" {
		t.Fatalf("want it waiting for a review, got %s %+v", x.Status, *x.PR)
	}
	if x.PR.Waits != model.PRWaitReview || x.PR.WaitsSince == nil {
		t.Fatalf("want it noted as waiting for a person's review since now, got %+v", *x.PR)
	}
}

// reviewedElsewhere is the API's check before approving a task.
func reviewedElsewhere(h *harness, id string) error {
	t, _ := h.st.GetTodo(id)
	if t.Status == model.StatusReview && t.WaitsOnGitHub() && t.Error == "" {
		return errors.New("reviewed on GitHub")
	}
	return nil
}

// An approved, green pull request waits for someone to merge it on GitHub;
// once they do, the task is merged.
func TestPullRequestMode(t *testing.T) {
	parallel(t)

	h, dir := prHarness(t, "pr.txt", model.Project{})
	td := h.waitFor(h.add("via pr").ID, model.StatusReview)
	head := gitRun(t, td.Worktree.Path, "rev-parse", "HEAD")
	write(t, dir, "reviews.json", `[{"id":5,"user":{"login":"alice","type":"User"},"state":"APPROVED","body":""}]`)
	write(t, dir, "pr.json", openPRView(head, ""))
	h.merges.PollPRs(context.Background())
	if x, _ := h.st.GetTodo(td.ID); x.Status != model.StatusReview || x.PR.HandledSHA != "" || strings.Contains(ghCalls(t, dir), "pr merge") {
		t.Fatalf("Dash merged an approved green pull request: %s %+v", x.Status, x.PR)
	} else if x.PR.Status != "Approved; waiting for you to merge on GitHub" || x.PR.Waits != model.PRWaitMerge || x.PR.WaitsSince == nil {
		t.Fatalf("status line %q, waits %q", x.PR.Status, x.PR.Waits)
	}

	// GitHub merges it (here: someone merges the branch on origin).
	other := filepath.Join(filepath.Dir(h.proj.Path), "other")
	gitRun(t, other, "fetch", "origin")
	gitRun(t, other, "merge", "--squash", "origin/"+td.Worktree.Branch)
	gitRun(t, other, "commit", "-m", "Merge PR 7")
	gitRun(t, other, "push", "origin", "main")
	sha := gitRun(t, other, "rev-parse", "HEAD")
	write(t, dir, "pr.json", `{"state":"MERGED","mergeCommit":{"oid":"`+sha+`"}}`)
	h.merges.PollPRs(context.Background())

	x := h.waitFor(td.ID, model.StatusMerged)
	if x.MergedCommit != sha || x.PR.State != "MERGED" {
		t.Fatalf("want merged at %s, got %s (%+v)", sha, x.MergedCommit, x.PR)
	}
	if _, err := os.Stat(filepath.Join(h.proj.Path, "pr.txt")); err != nil {
		t.Fatal("local main should catch up with the merged PR")
	}
}

// A task's green pull request waits for a review on GitHub, and once
// approved there for a person to merge it: Dash never merges it, on a
// runner (a hub's task) and on a Dash without runners alike.
func TestApprovedPullRequestWaitsForAPerson(t *testing.T) {
	parallel(t)

	t.Run("runner", func(t *testing.T) { testApprovedPullRequestWaits(t, "hub-p") })
	t.Run("no runners", func(t *testing.T) { testApprovedPullRequestWaits(t, "") })
}

func testApprovedPullRequestWaits(t *testing.T, hubProject string) {
	h, dir := prHarness(t, "hub.txt", model.Project{HubProjectID: hubProject})
	td := h.waitFor(h.add("on a runner").ID, model.StatusReview)
	head := gitRun(t, td.Worktree.Path, "rev-parse", "HEAD")
	write(t, dir, "pr.json", openPRView(head, `{"name":"build","status":"IN_PROGRESS"}`))
	h.merges.PollPRs(context.Background())
	// The card shows what the merge waits for.
	x, _ := h.st.GetTodo(td.ID)
	if x.PR.Status != "Waiting for checks: build (0/1 passed)" || len(x.PR.Checks) != 1 || x.PR.Checks[0] != (model.PRCheck{Name: "build", State: "pending"}) {
		t.Fatalf("PR = %+v, want the pending check kept on the task", *x.PR)
	}
	if x.PR.Waits != model.PRWaitReview || x.PR.WaitsSince == nil {
		t.Fatalf("PR = %+v, want a ready one waiting for a review while its checks run", *x.PR)
	}
	updated := x.UpdatedAt
	h.merges.PollPRs(context.Background())
	if x, _ := h.st.GetTodo(td.ID); !x.UpdatedAt.Equal(updated) {
		t.Fatal("an unchanged pull request shouldn't update the task on every poll")
	}

	// Green, but nobody approved it on GitHub yet (a bot's approval doesn't
	// count): it waits.
	write(t, dir, "reviews.json", `[{"id":3,"user":{"login":"ci-bot[bot]","type":"Bot"},"state":"APPROVED"}]`)
	write(t, dir, "pr.json", openPRView(head, `{"name":"build","status":"COMPLETED","conclusion":"SUCCESS"}`))
	h.merges.PollPRs(context.Background())
	if x, _ := h.st.GetTodo(td.ID); x.Status != model.StatusReview || strings.Contains(ghCalls(t, dir), "pr merge") {
		t.Fatalf("merged without an approving review: %s", x.Status)
	} else if x.PR.Status != "Waiting for a review on GitHub" || x.PR.Review != model.PRReviewPending {
		t.Fatalf("PR = %+v, want it waiting for a review", *x.PR)
	}

	// Approved on GitHub: it waits for a person to merge it there.
	write(t, dir, "reviews.json", `[{"id":3,"user":{"login":"ci-bot[bot]","type":"Bot"},"state":"APPROVED"},
		{"id":4,"user":{"login":"alice","type":"User"},"state":"APPROVED","body":"LGTM"}]`)
	h.merges.PollPRs(context.Background())
	if x, _ := h.st.GetTodo(td.ID); x.Status != model.StatusReview || strings.Contains(ghCalls(t, dir), "pr merge") {
		t.Fatalf("Dash merged an approved pull request: %s", x.Status)
	} else if x.PR.Status != "Approved; waiting for you to merge on GitHub" || x.PR.Waits != model.PRWaitMerge {
		t.Fatalf("PR = %+v, want it waiting for a person to merge it", *x.PR)
	}
}

// With "Approve tasks automatically" a green pull request is marked ready
// for review and waits for a person to merge it on GitHub, without waiting
// for an approving review; Dash doesn't merge it.
func TestAutoApprovedPullRequestIsNotMerged(t *testing.T) {
	parallel(t)

	h, dir := prHarness(t, "auto.txt", model.Project{AutoApprove: true})
	td := h.waitFor(h.add("auto").ID, model.StatusReview)
	if td.PR == nil {
		t.Fatal("want a pull request opened, approved automatically or not")
	}
	write(t, dir, "pr.json", `{"state":"OPEN","mergeable":"MERGEABLE","isDraft":true,"headRefOid":"`+gitRun(t, td.Worktree.Path, "rev-parse", "HEAD")+
		`","statusCheckRollup":[{"name":"build","status":"COMPLETED","conclusion":"SUCCESS"}]}`)
	h.merges.PollPRs(context.Background())
	calls := ghCalls(t, dir)
	if !strings.Contains(calls, "pr ready 7") || strings.Contains(calls, "pr merge") {
		t.Fatalf("want it marked ready and not merged, gh calls:\n%s", calls)
	}
	if x, _ := h.st.GetTodo(td.ID); x.Status != model.StatusReview || x.PR.Status != "Waiting for you to merge on GitHub" || x.PR.Waits != model.PRWaitMerge {
		t.Fatalf("want it waiting for a person to merge it, got %s %+v", x.Status, *x.PR)
	}
}

// A task approved in Dash before reviews moved to GitHub, still merging
// with its pull request open, isn't merged by Dash either: it waits in
// review for a person to merge it on GitHub.
func TestMergingPullRequestWaitsForAPerson(t *testing.T) {
	parallel(t)

	h, dir := prHarness(t, "old.txt", model.Project{AutoApprove: true})
	td := h.waitFor(h.add("old").ID, model.StatusReview)
	if _, err := h.st.UpdateTodo(td.ID, func(x *model.Todo) error { x.Status = model.StatusMerging; return nil }); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "pr.json", openPRView(gitRun(t, td.Worktree.Path, "rev-parse", "HEAD"), `{"name":"build","status":"COMPLETED","conclusion":"SUCCESS"}`))
	h.merges.PollPRs(context.Background())
	if x, _ := h.st.GetTodo(td.ID); x.Status != model.StatusReview || strings.Contains(ghCalls(t, dir), "pr merge") {
		t.Fatalf("want it back in review and not merged, got %s", x.Status)
	} else if x.PR.Waits != model.PRWaitMerge {
		t.Fatalf("PR = %+v, want it waiting for a person to merge it", *x.PR)
	}
}

// A review asking for changes, with its comments on lines of the diff, goes
// back to Claude like a rework; the fix is pushed to the same pull request
// and the same review isn't sent twice. Bots' reviews aren't sent.
func TestPullRequestReviewGoesBackToClaude(t *testing.T) {
	parallel(t)

	h, dir := prHarness(t, "review.txt", model.Project{MaxAutoFixes: 1})
	td := h.waitFor(h.add("review").ID, model.StatusReview)
	head := gitRun(t, td.Worktree.Path, "rev-parse", "HEAD")
	write(t, dir, "pr.json", openPRView(head, `{"name":"build","status":"COMPLETED","conclusion":"SUCCESS"}`))
	write(t, dir, "reviews.json", `[
		{"id":10,"user":{"login":"linter[bot]","type":"Bot"},"state":"COMMENTED","body":"Coverage dropped"},
		{"id":11,"user":{"login":"alice","type":"User"},"state":"CHANGES_REQUESTED","body":"Please rename the file."}]`)
	write(t, dir, "comments.json", `[
		{"id":1,"pull_request_review_id":11,"user":{"login":"alice","type":"User"},"path":"review.txt","line":1,"body":"Not a txt file"},
		{"id":2,"pull_request_review_id":10,"user":{"login":"linter[bot]","type":"Bot"},"path":"review.txt","line":1,"body":"Uncovered"}]`)
	h.merges.PollPRs(context.Background())

	x := waitPR(t, h, td.ID, model.StatusReview, func(x model.Todo) bool { return x.Runs == 2 })
	last := x.Feedback[len(x.Feedback)-1]
	for _, want := range []string{"pull request #7", "alice", "requested changes", "Please rename the file.", "review.txt:1", "Not a txt file"} {
		if !strings.Contains(last, want) {
			t.Errorf("Claude's note lacks %q:\n%s", want, last)
		}
	}
	if strings.Contains(last, "Coverage") || strings.Contains(last, "Uncovered") {
		t.Errorf("a bot's review was sent to Claude:\n%s", last)
	}
	if x.PR.Number != 7 || x.PR.ReviewID != 11 || x.AutoFixes != 0 {
		t.Fatalf("want the same pull request with review 11 handled and no auto-fix used, got %+v fixes %d", *x.PR, x.AutoFixes)
	}
	if !strings.Contains(x.SentBack, "alice") {
		t.Errorf("sent back %q", x.SentBack)
	}

	// Still requesting changes until alice looks again: waits, not resent.
	write(t, dir, "pr.json", openPRView(gitRun(t, td.Worktree.Path, "rev-parse", "HEAD"), `{"name":"build","status":"COMPLETED","conclusion":"SUCCESS"}`))
	h.merges.PollPRs(context.Background())
	time.Sleep(200 * time.Millisecond)
	if x, _ := h.st.GetTodo(td.ID); x.Runs != 2 || x.Status != model.StatusReview || x.PR.Review != model.PRChangesRequested {
		t.Fatalf("want it waiting for alice, got %s runs %d review %q", x.Status, x.Runs, x.PR.Review)
	}

	// She approves: it waits for her to merge it on GitHub.
	write(t, dir, "reviews.json", `[
		{"id":11,"user":{"login":"alice","type":"User"},"state":"CHANGES_REQUESTED","body":"Please rename the file."},
		{"id":12,"user":{"login":"alice","type":"User"},"state":"APPROVED","body":""}]`)
	h.merges.PollPRs(context.Background())
	if x, _ := h.st.GetTodo(td.ID); x.Status != model.StatusReview || x.PR.Waits != model.PRWaitMerge || strings.Contains(ghCalls(t, dir), "pr merge") {
		t.Fatalf("want it approved and waiting to be merged, got %s %+v", x.Status, *x.PR)
	}
}

// Closing the pull request on GitHub rejects the task.
func TestPullRequestClosedOnGitHubRejectsTask(t *testing.T) {
	parallel(t)

	h, dir := prHarness(t, "closed.txt", model.Project{})
	td := h.waitFor(h.add("closed").ID, model.StatusReview)
	write(t, dir, "pr.json", `{"state":"CLOSED"}`)
	h.merges.PollPRs(context.Background())
	x := h.waitFor(td.ID, model.StatusFailed)
	if !strings.Contains(x.Error, "closed on GitHub") || x.PR == nil || x.PR.State != "CLOSED" {
		t.Fatalf("want it rejected, got error %q PR %+v", x.Error, x.PR)
	}
	// No longer followed.
	h.merges.PollPRs(context.Background())
	if y, _ := h.st.GetTodo(td.ID); y.Status != model.StatusFailed {
		t.Fatalf("status %s", y.Status)
	}
}

// Rejecting the task in Dash closes its pull request and deletes its branch
// on GitHub.
func TestRejectingTaskClosesPullRequest(t *testing.T) {
	parallel(t)

	h, dir := prHarness(t, "reject.txt", model.Project{})
	td := h.waitFor(h.add("reject").ID, model.StatusReview)
	origin := filepath.Join(filepath.Dir(h.proj.Path), "origin.git")
	if gitRun(t, origin, "branch", "--list", td.Worktree.Branch) == "" {
		t.Fatal("branch not pushed")
	}
	if err := h.merges.RejectPR(context.Background(), td); err != nil {
		t.Fatal(err)
	}
	if calls := ghCalls(t, dir); !strings.Contains(calls, "pr close 7") {
		t.Fatalf("want the pull request closed, gh calls:\n%s", calls)
	}
	if b := gitRun(t, origin, "branch", "--list", td.Worktree.Branch); b != "" {
		t.Fatalf("branch %s still on origin", b)
	}
	if x, _ := h.st.GetTodo(td.ID); x.PR.State != "CLOSED" {
		t.Fatalf("PR %+v", x.PR)
	}
	// A task without one: nothing to close.
	if err := h.merges.RejectPR(context.Background(), model.Todo{ID: "x", ProjectID: h.proj.ID}); err != nil {
		t.Fatal(err)
	}
}

// A task that went to review before pull requests opened on finishing (or
// in a project switched to pull requests since) has none: approving it
// opens one, which is then reviewed on GitHub like the others.
func TestApprovingTaskWithoutPullRequestOpensOne(t *testing.T) {
	parallel(t)

	h := newHarness(t, &fakeExec{fn: writeFile("", "old.txt")}, model.Project{Name: "p"})
	h.addOrigin(false, model.MergeLocal)
	td := h.waitFor(h.add("old").ID, model.StatusReview)
	if td.PR != nil {
		t.Fatal("a pull request in local mode")
	}
	dir := t.TempDir()
	bin, _ := fakeGh(t, dir)
	h.git.GhBin = bin
	if _, err := h.st.UpdateProject(h.proj.ID, func(p *model.Project) error { p.MergeMode = model.MergePR; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := reviewedElsewhere(h, td.ID); err != nil {
		t.Fatal("a task in review without a pull request must stay approvable")
	}
	h.approve(td.ID)
	x := waitPR(t, h, td.ID, model.StatusReview, nil)
	if x.PR.Number != 7 || x.PR.State != "OPEN" {
		t.Fatalf("PR %+v", x.PR)
	}
}

func TestPullRequestFailingChecksGoBackToClaude(t *testing.T) {
	parallel(t)

	h, dir := prHarness(t, "ci.txt", model.Project{MaxAutoFixes: 1})
	td := h.waitFor(h.add("ci").ID, model.StatusReview)
	write(t, dir, "pr.json", openPRView("abc", `{"name":"build","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://example.com/x"}`))
	h.merges.PollPRs(context.Background())
	x := waitPR(t, h, td.ID, model.StatusReview, func(x model.Todo) bool { return x.AutoFixes == 1 && x.Runs == 2 })
	if last := x.Feedback[len(x.Feedback)-1]; !strings.Contains(last, "build") {
		t.Fatalf("want the failing check in Claude's note, got %q", last)
	}
	// Same head again: not sent twice.
	h.merges.PollPRs(context.Background())
	time.Sleep(200 * time.Millisecond)
	if x, _ := h.st.GetTodo(td.ID); x.AutoFixes != 1 {
		t.Fatalf("the same failure was sent back twice")
	}
}

func TestCleanupRemovesOrphansAndStaleWorktrees(t *testing.T) {
	parallel(t)

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

// waitPR waits for todo id to have a pull request in status want.
func waitPR(t *testing.T, h *harness, id string, want model.TodoStatus, ok func(model.Todo) bool) model.Todo {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		x, _ := h.st.GetTodo(id)
		if x.PR != nil && x.Status == want && (ok == nil || ok(x)) {
			return x
		}
		if time.Now().After(deadline) {
			t.Fatalf("want status %s with a PR, got %s (error %q, fixes %d, runs %d)", want, x.Status, x.Error, x.AutoFixes, x.Runs)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A fix pass that makes no new commit pushes the same head again. Rather
// than wait in merging forever for a head that never changes, the task
// fails with the failing checks named, and a merge done by hand on GitHub
// is still noticed.
func TestPullRequestHandledHeadStillFailing(t *testing.T) {
	parallel(t)

	// Claude writes the same file every run: the fix pass changes nothing.
	h, dir := prHarness(t, "same.txt", model.Project{MaxAutoFixes: 2})
	state := filepath.Join(dir, "pr.json")
	x := waitPR(t, h, h.add("ci").ID, model.StatusReview, nil)
	td := x
	head := gitRun(t, x.Worktree.Path, "rev-parse", "HEAD")
	failing := `{"state":"OPEN","mergeable":"MERGEABLE","headRefOid":"` + head + `","statusCheckRollup":[
		{"name":"build","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://example.com/x"}]}`
	write(t, filepath.Dir(state), "pr.json", failing)

	h.merges.PollPRs(context.Background())
	x = waitPR(t, h, td.ID, model.StatusReview, func(x model.Todo) bool { return x.Runs == 2 })
	// While the merge job pushes the branch again, GitHub's view is stale:
	// polling then must not judge it.
	for deadline := time.Now().Add(10 * time.Second); h.merges.inQueue(td.ID); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the merge job never finished")
		}
	}
	if got := gitRun(t, x.Worktree.Path, "rev-parse", "HEAD"); got != head {
		t.Fatalf("the fix pass was meant to make no commit, head moved %s → %s", head, got)
	}

	h.merges.PollPRs(context.Background())
	x = waitPR(t, h, td.ID, model.StatusFailed, nil)
	if !strings.Contains(x.Error, "build") || !strings.Contains(x.Error, "#7") || !strings.Contains(x.Error, "no new commit") {
		t.Fatalf("want an error naming the PR and the failing check, got %q", x.Error)
	}
	if x.AutoFixes != 1 || x.Runs != 2 {
		t.Fatalf("want one fix pass, got fixes %d runs %d", x.AutoFixes, x.Runs)
	}

	// Polling again changes nothing…
	h.merges.PollPRs(context.Background())
	if y, _ := h.st.GetTodo(td.ID); y.Status != model.StatusFailed || y.Runs != 2 {
		t.Fatalf("want it to stay failed, got %s runs %d", y.Status, y.Runs)
	}
	// …until someone merges it on GitHub.
	write(t, filepath.Dir(state), "pr.json", `{"state":"MERGED","mergeCommit":{"oid":"`+head+`"}}`)
	h.merges.PollPRs(context.Background())
	if y := h.waitFor(td.ID, model.StatusMerged); y.Error != "" || y.PR.State != "MERGED" {
		t.Fatalf("want merged without an error, got %q %+v", y.Error, y.PR)
	}
}

// A failing CLA check isn't sent to Claude as "fix the code": the task goes
// to review saying what blocks the merge.
func TestPullRequestCLAFailureGoesToReview(t *testing.T) {
	parallel(t)

	h, dir := prHarness(t, "cla.txt", model.Project{MaxAutoFixes: 2, AutoApprove: true})
	state := filepath.Join(dir, "pr.json")
	x := waitPR(t, h, h.add("cla").ID, model.StatusReview, nil)
	td := x
	head := gitRun(t, x.Worktree.Path, "rev-parse", "HEAD")
	write(t, filepath.Dir(state), "pr.json", `{"state":"OPEN","mergeable":"MERGEABLE","headRefOid":"`+head+`","statusCheckRollup":[
		{"name":"build","status":"COMPLETED","conclusion":"SUCCESS"},
		{"context":"license/cla","state":"FAILURE","targetUrl":"https://cla.example.com"}]}`)

	h.merges.PollPRs(context.Background())
	x = waitPR(t, h, td.ID, model.StatusReview, nil)
	if x.Runs != 1 || x.AutoFixes != 0 || !strings.Contains(x.Error, "license/cla") {
		t.Fatalf("want review naming the CLA check with no fix pass, got runs %d fixes %d error %q", x.Runs, x.AutoFixes, x.Error)
	}
	// Polling again doesn't say it again…
	h.merges.PollPRs(context.Background())
	if y, _ := h.st.GetTodo(td.ID); !y.UpdatedAt.Equal(x.UpdatedAt) {
		t.Fatal("the same block was written again")
	}
	// …and once the CLA is signed it carries on (here: to be merged on
	// GitHub, as the project approves automatically).
	write(t, dir, "pr.json", openPRView(head, `{"name":"build","status":"COMPLETED","conclusion":"SUCCESS"},
		{"context":"license/cla","state":"SUCCESS"}`))
	h.merges.PollPRs(context.Background())
	if y, _ := h.st.GetTodo(td.ID); y.Error != "" || y.Status != model.StatusReview || y.PR.Waits != model.PRWaitMerge {
		t.Fatalf("want it waiting to be merged, got %s error %q PR %+v", y.Status, y.Error, *y.PR)
	}
}
