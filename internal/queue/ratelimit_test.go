package queue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/testbin"
)

// ghSecondaryLimit is what gh prints when GitHub's secondary rate limit
// stops it.
const ghSecondaryLimit = "gh: You have exceeded a secondary rate limit. Please wait a few minutes before you try again. " +
	"If you reach out to GitHub Support for help, please include the request ID 0401:21A7:9C3E2:A1F55:6543A1C0 " +
	"and timestamp 2026-11-23 13:04:13 UTC. (HTTP 403)"

// limitGh puts a gh in front of the harness's fake one that, while the
// file "limited" is in dir, logs the call to "limited-calls" and fails
// like a rate limited gh. It runs gh's rate limits on a fake clock, and
// returns how to switch the limit on and off, and how many calls it
// refused.
func limitGh(t *testing.T, h *harness, dir string) (limit func(bool), refused func() int, advance func(time.Duration)) {
	bin := filepath.Join(dir, "gh-limited")
	testbin.Write(t, bin, "#!/bin/sh\nif [ -f "+filepath.Join(dir, "limited")+" ]; then echo \"$*\" >> "+filepath.Join(dir, "limited-calls")+
		"; echo '"+ghSecondaryLimit+"' >&2; exit 1; fi\nexec "+h.git.GhBin+" \"$@\"\n")
	h.git.GhBin = bin
	now := time.Now()
	h.git.RateLimits().Now = func() time.Time { return now }
	h.git.RateLimits().Jitter = func(time.Duration) time.Duration { return 0 }
	limit = func(on bool) {
		if on {
			write(t, dir, "limited", "")
		} else {
			_ = os.Remove(filepath.Join(dir, "limited"))
		}
	}
	refused = func() int {
		b, _ := os.ReadFile(filepath.Join(dir, "limited-calls"))
		return strings.Count(string(b), "\n")
	}
	return limit, refused, func(d time.Duration) { now = now.Add(d) }
}

// While GitHub rate limits gh, following pull requests skips its calls;
// once the pause is over it follows them again.
func TestPullRequestPollsPauseWhileRateLimited(t *testing.T) {
	parallel(t)

	h, dir := prHarness(t, "limit.txt", model.Project{})
	td := h.waitFor(h.add("limited").ID, model.StatusReview)
	limit, refused, advance := limitGh(t, h, dir)
	ctx := context.Background()

	limit(true)
	h.merges.pollPRs(ctx, "")
	if refused() != 1 {
		t.Fatalf("want one call refused before the pause, got %d", refused())
	}
	rl := h.git.RateLimit()
	if rl == nil || !strings.Contains(rl.Message, "secondary rate limit") {
		t.Fatalf("want a secondary limit in force, got %+v", rl)
	}
	for range 3 {
		h.merges.pollPRs(ctx, "")
		h.merges.schedulePRPolls()
	}
	if refused() != 1 {
		t.Fatalf("gh was called %d more times during the pause", refused()-1)
	}

	// The pause ends: the pull request is followed again (here: closed).
	limit(false)
	advance(time.Minute)
	before := ghCalls(t, dir)
	write(t, dir, "pr.json", `{"state":"CLOSED"}`)
	h.merges.pollPRs(ctx, "")
	if ghCalls(t, dir) == before {
		t.Fatal("no gh call after the pause")
	}
	if x, _ := h.st.GetTodo(td.ID); x.PR == nil || x.PR.State != "CLOSED" {
		t.Fatalf("want the closed pull request followed, got %+v", x.PR)
	}
}

// During a pause, merging a pull request from Dash still tries once; when
// GitHub limits it, the task stays in review and the refusal says until
// when, with GitHub's message.
func TestMergePRWhileRateLimited(t *testing.T) {
	parallel(t)

	h, dir := prHarness(t, "merge-limit.txt", model.Project{})
	td := h.waitFor(h.add("merge it").ID, model.StatusReview)
	limit, refused, _ := limitGh(t, h, dir)
	limit(true)
	h.merges.pollPRs(context.Background(), "") // starts the pause
	if h.git.RateLimit() == nil {
		t.Fatal("want a pause in force")
	}
	n := refused()

	_, err := h.merges.MergePR(context.Background(), td, "squash")
	var mr *MergeRefused
	if !errors.As(err, &mr) || !strings.Contains(mr.Reason, "GitHub rate limit (secondary), paused until") ||
		!strings.Contains(mr.Reason, "You have exceeded a secondary rate limit") {
		t.Fatalf("want the merge refused with the limit and GitHub's message, got %v", err)
	}
	if refused() != n+1 {
		t.Fatalf("want the merge tried once, got %d calls", refused()-n)
	}
	if x, _ := h.st.GetTodo(td.ID); x.Status != model.StatusReview || x.PR.State != "OPEN" {
		t.Fatalf("a rate limited merge changed the task: %s %+v", x.Status, *x.PR)
	}
}

// A task finishing during a pause still tries to open its pull request;
// GitHub limiting that leaves it in review saying so, and approving it
// once the pause is over opens it.
func TestOpenPRWhileRateLimited(t *testing.T) {
	parallel(t)

	h, dir := prHarness(t, "open-limit.txt", model.Project{})
	limit, refused, advance := limitGh(t, h, dir)
	limit(true)
	id := h.add("open it").ID
	var td model.Todo
	for deadline := time.Now().Add(waitTimeout); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if td, _ = h.st.GetTodo(id); td.Status == model.StatusReview && td.Error != "" {
			break
		}
	}
	if td.Status != model.StatusReview || td.PR != nil || !strings.Contains(td.Error, "GitHub rate limit (secondary), paused until") ||
		!strings.Contains(td.Error, "Approve to try again") {
		t.Fatalf("want the task in review saying GitHub limited it, got %s %q", td.Status, td.Error)
	}
	if refused() == 0 {
		t.Fatal("want opening the pull request tried")
	}

	limit(false)
	advance(time.Minute)
	h.approve(id)
	for deadline := time.Now().Add(waitTimeout); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if td, _ = h.st.GetTodo(id); td.PR != nil {
			break
		}
	}
	if td.Status != model.StatusReview || td.PR == nil || td.PR.Number != 7 || td.Error != "" {
		t.Fatalf("want the pull request opened once approved, got %s %+v %q", td.Status, td.PR, td.Error)
	}
}
