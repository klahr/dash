package gitops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/testbin"
)

func fakeGh(t *testing.T, script string) *Git {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gh")
	testbin.Write(t, bin, "#!/bin/sh\n"+script)
	return &Git{GhBin: bin}
}

func TestCheckEvents(t *testing.T) {
	t.Parallel()

	g := fakeGh(t, `echo '[{"name":"test","event":"pull_request"},{"name":"CLAAssistant","event":"pull_request_target"},{"name":"lint","event":"pull_request_target"},{"name":"lint","event":"pull_request"}]'
`)
	ev := g.CheckEvents(context.Background(), "p", t.TempDir(), 4)
	if ev["test"] != "pull_request" || !RunsFromBase(ev["CLAAssistant"]) || ev["lint"] != "pull_request" {
		t.Fatalf("events = %v", ev)
	}
}

// Every page of a REST list is read: gh prints one JSON array per page.
func TestPRReviewsPages(t *testing.T) {
	t.Parallel()

	g := fakeGh(t, `echo '[{"id":1,"user":{"login":"a","type":"User"},"state":"APPROVED"}]'
echo '[{"id":2,"user":{"login":"ci[bot]","type":"Bot"},"state":"COMMENTED","body":"hi"}]'
`)
	rs, err := g.PRReviews(context.Background(), "p", t.TempDir(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].ID != 1 || rs[0].User.Bot() || rs[1].ID != 2 || !rs[1].User.Bot() || rs[1].Body != "hi" {
		t.Fatalf("reviews = %+v", rs)
	}
}

// A draft pull request is asked for; where GitHub has none, it is opened
// ready for review instead.
func TestOpenDraftPRFallsBack(t *testing.T) {
	t.Parallel()

	g := fakeGh(t, `case "$*" in
"pr list"*) echo '[]' ;;
*--draft*) echo "Draft pull requests are not supported in this repository." >&2; exit 1 ;;
"pr create"*) echo https://github.com/o/r/pull/12 ;;
esac
`)
	n, url, err := g.OpenDraftPR(context.Background(), "p", t.TempDir(), "main", "dash/x", "T", "B")
	if err != nil || n != 12 || url != "https://github.com/o/r/pull/12" {
		t.Fatalf("got %d %q %v", n, url, err)
	}
	// Any other failure isn't retried.
	g = fakeGh(t, `case "$*" in
"pr list"*) echo '[]' ;;
*) echo "boom" >&2; exit 1 ;;
esac
`)
	if _, _, err := g.OpenDraftPR(context.Background(), "p", t.TempDir(), "main", "dash/x", "T", "B"); err == nil {
		t.Fatal("want the error")
	}
}

// Review threads are read from every page of the GraphQL answer, by their
// first comment; owner and repo are left for gh to fill in.
func TestReviewThreads(t *testing.T) {
	t.Parallel()

	g := fakeGh(t, `case "$*" in
*"-F owner={owner} -F repo={repo} -F n=5"*) ;;
*) echo "bad args: $*" >&2; exit 1 ;;
esac
echo '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"T1","isResolved":false,"comments":{"nodes":[{"databaseId":10}]}}]}}}}}'
echo '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"T2","isResolved":true,"comments":{"nodes":[{"databaseId":20}]}},{"id":"T3","comments":{"nodes":[]}}]}}}}}'
`)
	ts, err := g.ReviewThreads(context.Background(), "p", t.TempDir(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 2 || ts[10] != (ReviewThread{ID: "T1"}) || ts[20] != (ReviewThread{ID: "T2", Resolved: true}) {
		t.Fatalf("threads = %+v", ts)
	}
}

// Replies, review requests and resolving threads are the calls GitHub
// expects.
func TestReviewReplyCalls(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	g := fakeGh(t, `echo "$*" >> `+log+"\n")
	ctx := context.Background()
	if err := g.ReplyToReviewComment(ctx, "p", dir, 5, 10, "ok"); err != nil {
		t.Fatal(err)
	}
	if err := g.RequestReview(ctx, "p", dir, 5, []string{"alice", "bob"}); err != nil {
		t.Fatal(err)
	}
	if err := g.RequestReview(ctx, "p", dir, 5, nil); err != nil {
		t.Fatal(err)
	}
	if err := g.ResolveReviewThread(ctx, "p", dir, "T1"); err != nil {
		t.Fatal(err)
	}
	if err := g.CommentPR(ctx, "p", dir, 5, "hi"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	want := "api -X POST repos/{owner}/{repo}/pulls/5/comments/10/replies -f body=ok\n" +
		"api -X POST repos/{owner}/{repo}/pulls/5/requested_reviewers -f reviewers[]=alice -f reviewers[]=bob\n" +
		"api graphql -f query=mutation($id:ID!){resolveReviewThread(input:{threadId:$id}){thread{isResolved}}} -f id=T1\n" +
		"pr comment 5 --body hi\n"
	if string(b) != want {
		t.Fatalf("calls:\n%s\nwant:\n%s", b, want)
	}
}

// Dash's line comments: posted on a line or a range of the head, in the
// conversation, edited and deleted with the calls GitHub expects; lines not
// in the diff are ErrNotInDiff, and deleting one already gone is no error.
func TestLineCommentCalls(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	g := fakeGh(t, `echo "$*" >> `+log+`
case "$*" in
*"line=99"*) echo 'gh: Validation Failed (HTTP 422)' >&2; exit 1 ;;
*"-X DELETE"*"/404"*) echo 'gh: Not Found (HTTP 404)' >&2; exit 1 ;;
*"-X DELETE"*"/500"*) echo 'gh: Server Error (HTTP 500)' >&2; exit 1 ;;
*pulls/5/comments*) echo '{"id":11,"pull_request_review_id":12,"html_url":"https://example.com/c11"}' ;;
*issues/5/comments*) echo '{"id":21,"html_url":"https://example.com/c21"}' ;;
esac
`)
	ctx := context.Background()
	c, err := g.PostLineComment(ctx, "p", dir, 5, LineComment{Commit: "abc", Path: "a.go", StartLine: 3, Line: 4, Body: "hi"})
	if err != nil || c.ID != 11 || c.ReviewID != 12 || c.HTMLURL != "https://example.com/c11" {
		t.Fatalf("line comment %+v, %v", c, err)
	}
	if _, err := g.PostLineComment(ctx, "p", dir, 5, LineComment{Commit: "abc", Path: "a.go", Old: true, Line: 2, Body: "old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.PostLineComment(ctx, "p", dir, 5, LineComment{Commit: "abc", Path: "a.go", Line: 99, Body: "x"}); !errors.Is(err, ErrNotInDiff) {
		t.Fatalf("want ErrNotInDiff, got %v", err)
	}
	ic, err := g.PostIssueComment(ctx, "p", dir, 5, "talk")
	if err != nil || ic.ID != 21 || ic.HTMLURL != "https://example.com/c21" {
		t.Fatalf("issue comment %+v, %v", ic, err)
	}
	if err := g.EditComment(ctx, "p", dir, 11, false, "new"); err != nil {
		t.Fatal(err)
	}
	if err := g.EditComment(ctx, "p", dir, 21, true, "new"); err != nil {
		t.Fatal(err)
	}
	if err := g.DeleteComment(ctx, "p", dir, 404, false); err != nil {
		t.Fatalf("a comment already gone: %v", err)
	}
	if err := g.DeleteComment(ctx, "p", dir, 500, true); err == nil {
		t.Fatal("want GitHub's error")
	}
	b, _ := os.ReadFile(log)
	want := "api -X POST repos/{owner}/{repo}/pulls/5/comments -f body=hi -f commit_id=abc -f path=a.go -F line=4 -f side=RIGHT -F start_line=3 -f start_side=RIGHT\n" +
		"api -X POST repos/{owner}/{repo}/pulls/5/comments -f body=old -f commit_id=abc -f path=a.go -F line=2 -f side=LEFT\n" +
		"api -X POST repos/{owner}/{repo}/pulls/5/comments -f body=x -f commit_id=abc -f path=a.go -F line=99 -f side=RIGHT\n" +
		"api -X POST repos/{owner}/{repo}/issues/5/comments -f body=talk\n" +
		"api -X PATCH repos/{owner}/{repo}/pulls/comments/11 -f body=new\n" +
		"api -X PATCH repos/{owner}/{repo}/issues/comments/21 -f body=new\n" +
		"api -X DELETE repos/{owner}/{repo}/pulls/comments/404\n" +
		"api -X DELETE repos/{owner}/{repo}/issues/comments/500\n"
	if string(b) != want {
		t.Fatalf("calls:\n%s\nwant:\n%s", b, want)
	}
}

// PRStatuses asks about every pull request in one GraphQL query and reads
// each one's checks; one GitHub didn't return, or with more checks than
// one page, is left for PRStatus.
func TestPRStatuses(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	g := fakeGh(t, `echo "$*" >> `+filepath.Join(dir, "calls")+`
cat <<'JSON'
{"data":{"repository":{
 "p7":{"state":"OPEN","mergeable":"MERGEABLE","headRefOid":"abc","isDraft":true,"reviewDecision":null,"updatedAt":"2026-11-17T10:00:00Z","mergeCommit":null,
  "commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"pageInfo":{"hasNextPage":false},"nodes":[
   {"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://example.com/1"},
   {"__typename":"StatusContext","context":"ci/legacy","state":"PENDING","targetUrl":"https://example.com/2"}]}}}}]}},
 "p8":{"state":"MERGED","mergeable":"UNKNOWN","headRefOid":"def","isDraft":false,"reviewDecision":"APPROVED","updatedAt":"2026-11-17T11:00:00Z","mergeCommit":{"oid":"m1"},
  "commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}},
 "p9":{"state":"OPEN","mergeable":"MERGEABLE","headRefOid":"ghi","updatedAt":"x",
  "commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"pageInfo":{"hasNextPage":true},"nodes":[]}}}}]}},
 "p10":null}},
 "errors":[{"message":"Could not resolve to a PullRequest with the number of 10."}]}
JSON
exit 1
`)
	sts := g.PRStatuses(context.Background(), "p", t.TempDir(), []int{7, 8, 9, 10})
	if len(sts) != 2 {
		t.Fatalf("want #7 and #8 only, got %+v", sts)
	}
	s7 := sts[7]
	if s7.State != "OPEN" || s7.HeadRefOid != "abc" || !s7.IsDraft || s7.UpdatedAt != "2026-11-17T10:00:00Z" || len(s7.Checks) != 2 ||
		!s7.Checks[0].Failed() || !s7.Checks[1].Pending() || s7.Checks[1].Label() != "ci/legacy" || s7.Checks[0].URL() != "https://example.com/1" {
		t.Fatalf("#7 = %+v", s7)
	}
	if s8 := sts[8]; s8.State != "MERGED" || s8.MergeCommit == nil || s8.MergeCommit.Oid != "m1" || s8.ReviewDecision != "APPROVED" || s8.Checks != nil {
		t.Fatalf("#8 = %+v", s8)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if calls := strings.Count(string(b), "api graphql"); calls != 1 {
		t.Fatalf("want one query, got %d:\n%s", calls, b)
	}
	if !strings.Contains(string(b), "p7:pullRequest(number:7){...f}") || !strings.Contains(string(b), "p10:pullRequest(number:10){...f}") {
		t.Fatalf("want every pull request asked for, got %s", b)
	}
}
