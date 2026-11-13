package queue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

// claudeAnswer is Claude's final message on the review in reviewForClaude.
const claudeAnswer = "Renamed the file; kept the loop.\n\n```dash-review\n" +
	"[1] addressed: renamed it\n[2] addressed: review.md now\n[3] disagreed: the loop reads better than a map\n```"

// reviewForClaude puts on the fake GitHub in dir a review by alice asking
// for changes, with two line comments, each the start of a thread.
func reviewForClaude(t *testing.T, dir, head string) {
	write(t, dir, "pr.json", openPRView(head, `{"name":"build","status":"COMPLETED","conclusion":"SUCCESS"}`))
	write(t, dir, "reviews.json", `[{"id":11,"user":{"login":"alice","type":"User"},"author_association":"COLLABORATOR","state":"CHANGES_REQUESTED","body":"Please rename the file."}]`)
	write(t, dir, "comments.json", `[
		{"id":1,"pull_request_review_id":11,"user":{"login":"alice","type":"User"},"author_association":"COLLABORATOR","path":"review.txt","line":1,"body":"Not a txt file"},
		{"id":2,"pull_request_review_id":11,"user":{"login":"alice","type":"User"},"author_association":"COLLABORATOR","path":"review.txt","line":2,"body":"Use a map"}]`)
	write(t, dir, "threads.json", `{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[
		{"id":"T1","isResolved":false,"comments":{"nodes":[{"databaseId":1}]}},
		{"id":"T2","isResolved":false,"comments":{"nodes":[{"databaseId":2}]}}]}}}}}`)
}

// Once Claude's fix of a review is pushed, Dash posts its summary on the
// pull request, answers each thread (resolving the one it addressed,
// leaving open the one it pushed back on) and asks alice to review again;
// once only, also after a restart.
func TestReviewFixIsAnsweredOnGitHub(t *testing.T) {
	parallel(t)

	exe := &fakeExec{fn: writeFile("", "review.txt"), result: claudeAnswer}
	h, dir := prHarnessWith(t, exe, model.Project{MaxAutoFixes: 1})
	td := h.waitFor(h.add("review").ID, model.StatusReview)
	reviewForClaude(t, dir, gitRun(t, td.Worktree.Path, "rev-parse", "HEAD"))
	h.merges.PollPRs(context.Background())

	x := waitPR(t, h, td.ID, model.StatusReview, func(x model.Todo) bool { return x.Runs == 2 && x.PR.RepliedID == 11 })
	waitLog(t, h, td.ID, "💬 replied to the review on pull request #7: answered 2 comment thread(s), resolved 1, left 1 open; asked alice to review again")
	if last := x.Feedback[len(x.Feedback)-1]; !strings.Contains(last, "[3] On `review.txt:2`") || !strings.Contains(last, "```dash-review") {
		t.Errorf("Claude isn't asked to answer each comment:\n%s", last)
	}
	if x.PR.Reply != nil {
		t.Errorf("reply still owed after posting: %+v", *x.PR.Reply)
	}
	calls := ghCalls(t, dir)
	for _, want := range []string{
		"pr comment 7 --body **Claude's changes for the review by alice**", "Renamed the file; kept the loop.",
		"`review.txt:2` (alice): the loop reads better than a map",
		"pulls/7/comments/1/replies -f body=**Claude:** Addressed: review.md now",
		"pulls/7/comments/2/replies -f body=**Claude:** Not changed: the loop reads better than a map",
		"id=T1", "pulls/7/requested_reviewers -f reviewers[]=alice",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("gh wasn't asked %q; calls:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "id=T2") {
		t.Errorf("the thread Claude pushed back on was resolved:\n%s", calls)
	}
	if strings.Contains(calls, "dash-review") {
		t.Errorf("the answers block was posted as is:\n%s", calls)
	}

	// Polled again, and after a restart: nothing posted twice.
	h.merges.PollPRs(context.Background())
	m2 := &MergeQueue{Store: h.st, Git: h.git, Log: h.log}
	t2, _ := h.st.GetTodo(td.ID)
	m2.replyToReview(context.Background(), t2, h.proj)
	m2.reviewFailed(context.Background(), td.ID, "boom")
	time.Sleep(200 * time.Millisecond)
	if n := strings.Count(ghCalls(t, dir), "pr comment"); n != 1 {
		t.Fatalf("%d comments posted, want 1:\n%s", n, ghCalls(t, dir))
	}
	// A reply recorded but already posted (Dash stopped in between) isn't
	// posted again either.
	if _, err := h.st.UpdateTodo(td.ID, func(x *model.Todo) error {
		x.PR.Reply = &model.PRReply{ReviewID: 11, Report: "again"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t2, _ = h.st.GetTodo(td.ID)
	m2.replyToReview(context.Background(), t2, h.proj)
	if n := strings.Count(ghCalls(t, dir), "pr comment"); n != 1 {
		t.Fatalf("an answered review was answered again:\n%s", ghCalls(t, dir))
	}
}

// When GitHub fails the replies, the task doesn't: it waits in review with
// its fix pushed, and the failure is in its log.
func TestReviewReplyFailureDoesntFailTask(t *testing.T) {
	parallel(t)

	exe := &fakeExec{fn: writeFile("", "review.txt"), result: claudeAnswer}
	h, dir := prHarnessWith(t, exe, model.Project{MaxAutoFixes: 1})
	td := h.waitFor(h.add("review").ID, model.StatusReview)
	reviewForClaude(t, dir, gitRun(t, td.Worktree.Path, "rev-parse", "HEAD"))
	write(t, dir, "down", "")
	h.merges.PollPRs(context.Background())

	x := waitPR(t, h, td.ID, model.StatusReview, func(x model.Todo) bool { return x.Runs == 2 && x.PR.RepliedID == 11 })
	waitLog(t, h, td.ID, "💬 replied to the review on pull request #7")
	if x, _ = h.st.GetTodo(td.ID); x.Error != "" || x.Status != model.StatusReview {
		t.Fatalf("task %s, error %q", x.Status, x.Error)
	}
	log, _ := h.engine.Log.Read(td.ID)
	for _, want := range []string{"couldn't post Claude's summary on pull request #7", "couldn't reply to a comment", "couldn't ask alice to review again"} {
		if !strings.Contains(log, want) {
			t.Errorf("%q isn't in the task's log:\n%s", want, log)
		}
	}
}

// waitLog waits for task id's log to say want.
func waitLog(t *testing.T, h *harness, id, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		log, _ := h.engine.Log.Read(id)
		if strings.Contains(log, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the log never said %q:\n%s", want, log)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A review whose fix fails, with nothing new pushed, gets a comment saying
// why, so the reviewer isn't left waiting.
func TestFailedReviewFixIsSaidOnGitHub(t *testing.T) {
	parallel(t)

	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		if n > 1 {
			return errors.New("Claude fell over")
		}
		return os.WriteFile(filepath.Join(req.Dir, "review.txt"), []byte("x\n"), 0o644)
	}}
	h, dir := prHarnessWith(t, exe, model.Project{})
	td := h.waitFor(h.add("review").ID, model.StatusReview)
	head := gitRun(t, td.Worktree.Path, "rev-parse", "HEAD")
	reviewForClaude(t, dir, head)
	h.merges.PollPRs(context.Background())

	x := waitPR(t, h, td.ID, model.StatusFailed, func(x model.Todo) bool { return x.PR.RepliedID == 11 })
	calls := ghCalls(t, dir)
	if !strings.Contains(calls, "pr comment 7 --body Claude couldn't finish the changes asked for in the review by alice") ||
		!strings.Contains(calls, "Claude fell over") {
		t.Fatalf("want a comment saying the fix failed; calls:\n%s", calls)
	}
	if strings.Contains(calls, "replies") || strings.Contains(calls, "requested_reviewers") {
		t.Errorf("a failed fix answered the threads or asked for a review:\n%s", calls)
	}
	if x.PR.Reply != nil || gitRun(t, filepath.Join(filepath.Dir(h.proj.Path), "origin.git"), "rev-parse", td.Worktree.Branch) != head {
		t.Errorf("reply %+v; branch moved on origin", x.PR.Reply)
	}
}
