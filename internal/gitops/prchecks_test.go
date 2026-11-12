package gitops

import (
	"context"
	"os"
	"path/filepath"
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
