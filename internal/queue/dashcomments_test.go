package queue

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// addComments stores comments cs on task id, as the API does.
func addComments(t *testing.T, h *harness, id string, cs ...model.ReviewComment) {
	t.Helper()
	if _, err := h.st.UpdateTodo(id, func(x *model.Todo) error {
		x.Comments = append(x.Comments, cs...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// commentOf is task id's comment cid as stored.
func commentOf(t *testing.T, h *harness, id, cid string) model.ReviewComment {
	t.Helper()
	x, _ := h.st.GetTodo(id)
	for _, c := range x.Comments {
		if c.ID == cid {
			return c
		}
	}
	t.Fatalf("no comment %s on the task: %+v", cid, x.Comments)
	return model.ReviewComment{}
}

// A comment written in Dash goes to the task's pull request at once: as a
// line comment on its head, naming who wrote it, or in its conversation
// when its lines aren't in the diff. Editing and deleting it does the same
// there, a failure is kept on the comment and can be retried, and the
// watcher never sends what Dash posted back to Claude, though Dash's gh
// account reviewing for real still is.
func TestDashCommentsOnGitHub(t *testing.T) {
	parallel(t)

	h, dir := prHarness(t, "review.txt", model.Project{})
	td := h.waitFor(h.add("review").ID, model.StatusReview)
	head := gitRun(t, td.Worktree.Path, "rev-parse", "HEAD")
	write(t, dir, "pr.json", openPRView(head, `{"name":"build","status":"COMPLETED","conclusion":"SUCCESS"}`))
	ctx := context.Background()

	addComments(t, h, td.ID, model.ReviewComment{ID: "c1", File: "review.txt", Side: "new", Line: 1, EndLine: 2, Code: "+x", Text: "Why?", Author: "alice"})
	if err := h.merges.PostComment(ctx, td.ID, "c1"); err != nil {
		t.Fatal(err)
	}
	g := commentOf(t, h, td.ID, "c1").GitHub
	if !g.Posted() || g.ID != 501 || g.ReviewID != 601 || g.Issue || g.PR != 7 || g.URL != "https://github.com/o/r/pull/7#discussion_r501" || g.Error != "" {
		t.Fatalf("posted as %+v", g)
	}
	if x, _ := h.st.GetTodo(td.ID); !x.PR.DashPosted(501, false) || !x.PR.DashReview(601) {
		t.Fatalf("its ids aren't kept on the pull request: %+v", x.PR.Posted)
	}
	calls := ghCalls(t, dir)
	for _, want := range []string{
		"api -X POST repos/{owner}/{repo}/pulls/7/comments -f body=<sub>From **alice** in Dash</sub>\n\nWhy?\n\n" + dashMark,
		"-f commit_id=" + head + " -f path=review.txt -F line=2 -f side=RIGHT -F start_line=1 -f start_side=RIGHT",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("gh wasn't asked %q; calls:\n%s", want, calls)
		}
	}
	// Posted once only.
	if err := h.merges.PostComment(ctx, td.ID, "c1"); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(ghCalls(t, dir), "pulls/7/comments -f body"); n != 1 {
		t.Fatalf("posted %d times", n)
	}

	// Lines GitHub can't place in the diff: in the conversation, with the
	// file, the lines and the code.
	write(t, dir, "notdiff", "")
	addComments(t, h, td.ID, model.ReviewComment{ID: "c2", File: "review.txt", Side: "old", Line: 3, Code: "-old", Text: "Keep this?", Author: "alice"})
	if err := h.merges.PostComment(ctx, td.ID, "c2"); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, "notdiff"))
	if g := commentOf(t, h, td.ID, "c2").GitHub; !g.Posted() || !g.Issue || g.ID != 701 || g.URL != "https://github.com/o/r/pull/7#issuecomment-701" {
		t.Fatalf("posted as %+v", g)
	}
	calls = ghCalls(t, dir)
	for _, want := range []string{"-F line=3 -f side=LEFT",
		"api -X POST repos/{owner}/{repo}/issues/7/comments -f body=<sub>From **alice** in Dash</sub>\n\n" +
			"On `review.txt`, removed line 3 (as numbered before the change) (not in this pull request's diff):\n\n```diff\n-old\n```\n\nKeep this?"} {
		if !strings.Contains(calls, want) {
			t.Errorf("gh wasn't asked %q; calls:\n%s", want, calls)
		}
	}

	// Edited in Dash: edited on GitHub.
	if _, err := h.st.UpdateTodo(td.ID, func(x *model.Todo) error { x.Comments[0].Text = "Rename it."; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := h.merges.UpdateComment(ctx, td.ID, "c1"); err != nil {
		t.Fatal(err)
	}
	if calls := ghCalls(t, dir); !strings.Contains(calls, "api -X PATCH repos/{owner}/{repo}/pulls/comments/501 -f body=<sub>From **alice** in Dash</sub>\n\nRename it.") {
		t.Fatalf("not edited on GitHub; calls:\n%s", calls)
	}

	// GitHub down: the comment stays, saying why, and posts on a retry.
	write(t, dir, "down", "")
	addComments(t, h, td.ID, model.ReviewComment{ID: "c3", File: "review.txt", Side: "new", Line: 1, Text: "Later."})
	if err := h.merges.PostComment(ctx, td.ID, "c3"); err == nil {
		t.Fatal("want the error")
	}
	if g := commentOf(t, h, td.ID, "c3").GitHub; g == nil || g.Posted() || g.Error == "" {
		t.Fatalf("a failed post: %+v", g)
	}
	waitLog(t, h, td.ID, "⚠ couldn't post a comment on review.txt on pull request #7")
	os.Remove(filepath.Join(dir, "down"))
	if err := h.merges.PostComment(ctx, td.ID, "c3"); err != nil {
		t.Fatal(err)
	}
	if g := commentOf(t, h, td.ID, "c3").GitHub; !g.Posted() || g.Error != "" {
		t.Fatalf("after the retry: %+v", g)
	}
	if !strings.Contains(ghCalls(t, dir), "body=<sub>From someone in Dash</sub>") {
		t.Error("a comment with no author known doesn't say so")
	}

	// The watcher, on another MergeQueue (after a restart), knows what Dash
	// posted by its ids, though gh's account is a reviewer with no Dash mark.
	write(t, dir, "comments.json", `[{"id":501,"pull_request_review_id":601,"user":{"login":"octocat","type":"User"},"author_association":"OWNER","path":"review.txt","line":2,"body":"Rename it."}]`)
	dashReview := gitops.PRReview{ID: 601, User: gitops.GHUser{Login: "octocat", Type: "User"}, Association: "OWNER", State: "COMMENTED"}
	talk := []gitops.IssueComment{{ID: 701, User: gitops.GHUser{Login: "octocat", Type: "User"}, Association: "OWNER", Body: "/dash Keep this?"}}
	m2 := &MergeQueue{Store: h.st, Git: h.git, Log: h.log}
	x, _ := h.st.GetTodo(td.ID)
	if sent, ok := m2.reviewBack(ctx, x, h.proj, []gitops.PRReview{dashReview}, talk); sent || !ok {
		t.Fatalf("Dash's own comments were sent back to Claude (sent %v, ok %v)", sent, ok)
	}
	if x, _ = h.st.GetTodo(td.ID); x.Status != model.StatusReview || len(x.Feedback) != 0 {
		t.Fatalf("task %s, feedback %q", x.Status, x.Feedback)
	}

	// Deleted in Dash: deleted on GitHub.
	m2.DeleteComment(ctx, x, commentOf(t, h, td.ID, "c2"))
	if calls := ghCalls(t, dir); !strings.Contains(calls, "api -X DELETE repos/{owner}/{repo}/issues/comments/701") {
		t.Fatalf("not deleted on GitHub; calls:\n%s", calls)
	}

	// A real review by the same account goes to Claude, without Dash's comment.
	real := gitops.PRReview{ID: 602, User: dashReview.User, Association: "OWNER", State: "COMMENTED", Body: "Also add a test."}
	if sent, _ := m2.reviewBack(ctx, x, h.proj, []gitops.PRReview{dashReview, real}, nil); !sent {
		t.Fatal("a real review wasn't sent")
	}
	x, _ = h.st.GetTodo(td.ID)
	if fb := strings.Join(x.Feedback, "\n"); !strings.Contains(fb, "Also add a test.") || strings.Contains(fb, "Rename it.") {
		t.Fatalf("feedback:\n%s", fb)
	}
}

// A comment deleted in Dash while it was being posted (the API saw it
// without its GitHub id) is deleted on GitHub once the post is done.
func TestDashCommentDeletedWhilePosting(t *testing.T) {
	parallel(t)

	h, dir := prHarness(t, "review.txt", model.Project{})
	td := h.waitFor(h.add("review").ID, model.StatusReview)
	write(t, dir, "pr.json", openPRView(gitRun(t, td.Worktree.Path, "rev-parse", "HEAD"), ""))
	ctx := context.Background()
	being := model.ReviewComment{ID: "c1", File: "review.txt", Side: "new", Line: 1, Text: "one", GitHub: &model.PostedComment{PR: 7}}
	addComments(t, h, td.ID, being)

	// Not posted, and not being posted: nothing to delete.
	h.merges.DeleteComment(ctx, td, being)
	if strings.Contains(ghCalls(t, dir), "DELETE") {
		t.Fatalf("deleted what was never posted:\n%s", ghCalls(t, dir))
	}
	// The post finishes after the comment is gone from Dash: it leaves what
	// it posted for the delete waiting on it.
	if err := h.merges.PostComment(ctx, td.ID, "c1"); err != nil {
		t.Fatal(err)
	}
	posted := commentOf(t, h, td.ID, "c1").GitHub
	if _, err := h.st.UpdateTodo(td.ID, func(x *model.Todo) error { x.Comments = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	h.merges.postedNow.Store("c1", *posted)
	h.merges.DeleteComment(ctx, td, being)
	if !strings.Contains(ghCalls(t, dir), "-X DELETE repos/{owner}/{repo}/pulls/comments/501") {
		t.Fatalf("not deleted on GitHub:\n%s", ghCalls(t, dir))
	}
}

// When the comments go to Claude with a rework, those posted on the pull
// request are numbered for Claude to answer, and once its fix is pushed
// Dash answers each one's thread there and resolves those it addressed.
func TestDashCommentsAnsweredAfterRework(t *testing.T) {
	parallel(t)

	exe := &fakeExec{fn: writeFile("", "review.txt"), result: "Renamed it.\n\n```dash-review\n[1] addressed: renamed\n[2] disagreed: it is fine as is\n```"}
	h, dir := prHarnessWith(t, exe, model.Project{MaxAutoFixes: 1})
	td := h.waitFor(h.add("review").ID, model.StatusReview)
	write(t, dir, "pr.json", openPRView(gitRun(t, td.Worktree.Path, "rev-parse", "HEAD"), `{"name":"build","status":"COMPLETED","conclusion":"SUCCESS"}`))
	write(t, dir, "threads.json", `{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[
		{"id":"T501","isResolved":false,"comments":{"nodes":[{"databaseId":501}]}}]}}}}}`)
	ctx := context.Background()
	addComments(t, h, td.ID, model.ReviewComment{ID: "c1", File: "review.txt", Side: "new", Line: 1, Text: "Rename it.", Author: "alice"})
	if err := h.merges.PostComment(ctx, td.ID, "c1"); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "notdiff", "")
	addComments(t, h, td.ID, model.ReviewComment{ID: "c2", File: "review.txt", Side: "new", Line: 9, Text: "Use a map.", Author: "alice"})
	if err := h.merges.PostComment(ctx, td.ID, "c2"); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, "notdiff"))

	// Send rework, as the API does.
	if _, err := h.st.UpdateTodo(td.ID, func(x *model.Todo) error {
		note := agent.ReviewNote(x.Comments, "")
		if ask := OweDashReply(x, x.Comments); ask != "" {
			note += "\n\n" + ask
		}
		x.Feedback, x.Comments, x.Status = append(x.Feedback, note), nil, model.StatusQueued
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.engine.Kick()

	x := waitPR(t, h, td.ID, model.StatusReview, func(x model.Todo) bool { return x.Runs == 2 && x.PR.Reply == nil })
	waitLog(t, h, td.ID, "💬 replied to the review on pull request #7: answered 1 comment thread(s), resolved 1, left 0 open")
	if last := x.Feedback[len(x.Feedback)-1]; !strings.Contains(last, "### 1. review.txt, line 1") || !strings.Contains(last, "```dash-review") {
		t.Errorf("Claude isn't asked to answer each comment:\n%s", last)
	}
	calls := ghCalls(t, dir)
	for _, want := range []string{
		"pulls/7/comments/501/replies -f body=**Claude:** Addressed: renamed",
		"id=T501",
		"pr comment 7 --body **Claude's changes for the review by alice**",
		"`review.txt:9` (alice): it is fine as is",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("gh wasn't asked %q; calls:\n%s", want, calls)
		}
	}
	if n := strings.Count(calls, "-f body=<sub>From **alice** in Dash</sub>\n\nRename it."); n != 1 {
		t.Errorf("the comment was posted %d times, want once (not again on rework):\n%s", n, calls)
	}
}

// OweDashReply numbers the comments as the rework note does and owes an
// answer only for those on the task's open pull request.
func TestOweDashReply(t *testing.T) {
	x := model.Todo{PR: &model.PullRequest{Number: 7, State: "OPEN", Reply: &model.PRReply{ReviewID: 3, Reviewers: []string{"bob"}, Items: []model.PRReplyItem{{Ref: 1}}}}}
	cs := []model.ReviewComment{
		{File: "a", Line: 1, Text: "not posted"},
		{File: "b", Line: 2, Text: "line", Author: "alice", GitHub: &model.PostedComment{PR: 7, ID: 501}},
		{File: "c", Line: 3, Text: "issue", GitHub: &model.PostedComment{PR: 7, ID: 701, Issue: true}},
		{File: "d", Line: 4, Text: "other pull request", GitHub: &model.PostedComment{PR: 6, ID: 9}},
		{File: "e", Line: 5, Text: "failed", GitHub: &model.PostedComment{PR: 7, Error: "boom"}},
	}
	ask := OweDashReply(&x, cs)
	if !strings.Contains(ask, "pull request #7") || !strings.Contains(ask, "```dash-review") {
		t.Fatalf("ask: %s", ask)
	}
	want := []model.PRReplyItem{{Ref: 2, Author: "alice", Thread: 501, Where: "b:2"}, {Ref: 3, Where: "c:3"}}
	if r := x.PR.Reply; !r.Dash || !slices.Equal(r.Items, want) || r.ReviewID != 3 || len(r.Reviewers) != 1 {
		t.Fatalf("reply %+v", *r)
	}

	none := model.Todo{PR: &model.PullRequest{Number: 7, State: "OPEN"}}
	if ask := OweDashReply(&none, cs[:1]); ask != "" || none.PR.Reply != nil {
		t.Fatalf("nothing posted: %q %+v", ask, none.PR.Reply)
	}
	closed := model.Todo{PR: &model.PullRequest{Number: 7, State: "CLOSED"}}
	if ask := OweDashReply(&closed, cs); ask != "" {
		t.Fatalf("closed pull request: %q", ask)
	}
}
