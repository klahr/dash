package ci

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/testbin"
)

// clock is a fake time for the watcher and gh's rate limits.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func noRand(time.Duration) time.Duration { return 0 }

// limitedWatcher is newWatcher with a gh that logs its calls and, while
// the file "limited" is in the project's folder, fails like gh does when
// GitHub's hourly limit is used up (and can't say when it resets). The
// watcher and gh run on a fake clock.
func limitedWatcher(t *testing.T) (w *Watcher, p model.Project, c *clock, calls func() int, limit func(bool)) {
	w, p, setRuns := newWatcher(t, false)
	setRuns(runs(run(10, "CI", "aaa", "completed", "success")))
	dir := p.Path
	gh := filepath.Join(dir, "gh-limited")
	testbin.Write(t, gh, "#!/bin/sh\necho \"$*\" >> "+filepath.Join(dir, "calls")+"\n"+
		"if [ -f "+filepath.Join(dir, "limited")+" ]; then echo 'gh: API rate limit exceeded for user ID 1234567. (HTTP 403)' >&2; exit 1; fi\n"+
		"exec "+filepath.Join(dir, "gh")+" \"$@\"\n")
	g := w.Git.(*gitops.Git)
	g.GhBin = gh
	c = &clock{t: time.Date(2026, 11, 23, 13, 0, 0, 0, time.UTC)}
	w.Now, w.Rand, w.Every = c.now, noRand, time.Minute
	g.RateLimits().Now, g.RateLimits().Jitter = c.now, noRand
	calls = func() int {
		b, _ := os.ReadFile(filepath.Join(dir, "calls"))
		return strings.Count(string(b), "\n")
	}
	limit = func(on bool) {
		if on {
			testbin.Write(t, filepath.Join(dir, "limited"), "")
		} else {
			_ = os.Remove(filepath.Join(dir, "limited"))
		}
	}
	return w, p, c, calls, limit
}

// While GitHub rate limits gh, the build polls don't call it, and the
// status says so while keeping the builds last seen; once the limit ends
// they resume.
func TestBuildPollsPauseWhileRateLimited(t *testing.T) {
	w, p, c, calls, limit := limitedWatcher(t)
	ctx := context.Background()

	w.pollDue(ctx)
	if st := w.Status(p.ID); st.State != Passing || st.RateLimit != nil || calls() == 0 {
		t.Fatalf("want the first poll to read a passing build, got %+v after %d calls", st, calls())
	}

	limit(true)
	c.add(5 * time.Minute)
	w.pollDue(ctx)
	st := w.Status(p.ID)
	if st.RateLimit == nil || st.RateLimit.Kind != gitops.RateLimitPrimary || !st.RateLimit.Until.Equal(c.t.Add(time.Hour)) {
		t.Fatalf("want an hour's pause for the hourly limit, got %+v", st.RateLimit)
	}
	if st.State != Passing || len(st.Runs) != 1 || st.Error != "" {
		t.Fatalf("want the builds last seen kept while paused, got %+v", st)
	}

	// Polls (and a refresh) while paused don't call gh.
	n := calls()
	for range 5 {
		c.add(5 * time.Minute)
		w.pollDue(ctx)
	}
	if _, err := w.Refresh(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if calls() != n {
		t.Fatalf("gh was called %d times during the pause", calls()-n)
	}
	if st := w.Status(p.ID); st.RateLimit == nil {
		t.Fatal("want the pause still shown")
	}

	// The hour is over: the next poll reads the builds again.
	limit(false)
	c.add(35 * time.Minute)
	w.pollDue(ctx)
	if st := w.Status(p.ID); calls() == n || st.RateLimit != nil || st.State != Passing {
		t.Fatalf("want polls resumed after the pause, got %+v after %d calls", st, calls()-n)
	}
}

// An idle project is polled every IdleEvery; once a task runs, every
// Every again.
func TestIdleProjectsPolledLess(t *testing.T) {
	w, p, c, calls, _ := limitedWatcher(t)
	ctx := context.Background()

	w.pollDue(ctx)
	n := calls()
	c.add(2 * time.Minute)
	w.pollDue(ctx)
	if calls() != n {
		t.Fatal("an idle project was polled again after 2 minutes")
	}
	c.add(3 * time.Minute)
	w.pollDue(ctx)
	if calls() == n {
		t.Fatal("an idle project wasn't polled after 5 minutes")
	}

	n = calls()
	c.add(time.Minute)
	td, err := w.Store.CreateTodo(model.Todo{ProjectID: p.ID, Title: "work", Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Store.SetStatus(td.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Store.SetStatus(td.ID, model.StatusInProgress, ""); err != nil {
		t.Fatal(err)
	}
	w.pollDue(ctx)
	if calls() == n {
		t.Fatal("a project with a task running wasn't polled after a minute")
	}
}

// After a start, the projects' first polls are spread out, and each
// project's interval has its own jitter.
func TestPollsSpreadOut(t *testing.T) {
	w, p, c, _, _ := limitedWatcher(t)
	w.Rand = func(d time.Duration) time.Duration { return d / 2 }
	if w.due(p, c.t) {
		t.Fatal("the first poll should wait its share of the spread")
	}
	c.add(7500 * time.Millisecond) // half of min(Every/4, 15s)
	if !w.due(p, c.t) {
		t.Fatal("the first poll should be due once its share of the spread passed")
	}
	c.add(5*time.Minute + 2*time.Second)
	if w.due(p, c.t) {
		t.Fatal("an idle project's interval should have its jitter (3s) added")
	}
	c.add(time.Second)
	if !w.due(p, c.t) {
		t.Fatal("want the poll due after IdleEvery and its jitter")
	}
}

// hubGitHub is GitHub as a hub sees it: gh runs on a runner, whose rate
// limit comes back as the text of its error answer.
type hubGitHub struct {
	GitHub
	err error
	n   int
}

func (h *hubGitHub) Runs(ctx context.Context, projectID, dir, ref string, limit int) ([]gitops.Run, error) {
	h.n++
	if h.err != nil {
		return nil, h.err
	}
	return h.GitHub.Runs(ctx, projectID, dir, ref, limit)
}

// On a hub, a runner's rate limit pauses the project's build polls until it
// ends, and names the runner.
func TestHubPausesForRunnerRateLimit(t *testing.T) {
	w, p, c, _, _ := limitedWatcher(t)
	until := c.t.Add(10 * time.Minute)
	rl := &gitops.RateLimitError{Kind: gitops.RateLimitPrimary, Message: "API rate limit exceeded for 203.0.113.7.", Until: until, NoLogin: true}
	hub := &hubGitHub{GitHub: w.Git, err: errors.New("runner-1: " + rl.Error())}
	w.Git = hub
	ctx := context.Background()

	if _, err := w.Refresh(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	st := w.Status(p.ID)
	if st.RateLimit == nil || !st.RateLimit.NoLogin || !st.RateLimit.Until.Equal(until) {
		t.Fatalf("want the runner's limit shown, got %+v", st.RateLimit)
	}
	c.add(5 * time.Minute)
	_, _ = w.Refresh(ctx, p.ID)
	if hub.n != 1 {
		t.Fatalf("the runner was asked %d times during its pause", hub.n)
	}
	hub.err = nil
	c.add(5 * time.Minute)
	_, _ = w.Refresh(ctx, p.ID)
	if st := w.Status(p.ID); hub.n != 2 || st.RateLimit != nil || st.State != Passing {
		t.Fatalf("want the runner asked again after the pause, got %+v (%d calls)", st, hub.n)
	}
}
