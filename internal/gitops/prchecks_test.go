package gitops

import (
	"context"
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
