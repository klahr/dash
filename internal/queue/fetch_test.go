package queue

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// remote returns the harness's RemoteSync on a fake clock.
func (h *harness) remote() (*RemoteSync, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	r := h.merges.Remote
	r.now = clk.now
	return r, clk
}

func TestRemoteLockIsSharedAndTryLockSkips(t *testing.T) {
	parallel(t)

	r := &RemoteSync{}
	unlock, ok := r.TryLock("p")
	if !ok {
		t.Fatal("TryLock on a free lock failed")
	}
	if _, ok := r.TryLock("p"); ok {
		t.Fatal("TryLock took a held lock")
	}
	if u, ok := r.TryLock("q"); !ok {
		t.Fatal("projects share a lock")
	} else {
		u()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := r.Lock(ctx, "p"); err == nil {
		t.Fatal("Lock didn't wait for the holder")
	}
	unlock()
	u, err := r.Lock(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	u()
}

func TestFetchSkipsWhileLockedAndReportsOriginAhead(t *testing.T) {
	parallel(t)

	h := newHarness(t, &fakeExec{fn: writeFile("", "x.txt")}, model.Project{Name: "p"})
	_, other := h.addOrigin(true, "")
	r, _ := h.remote()
	gitRun(t, other, "commit", "--allow-empty", "-q", "-m", "elsewhere")
	gitRun(t, other, "push", "-q", "origin", "main")

	// The merge worker holds the lock: the background fetch skips the tick.
	unlock, _ := r.TryLock(h.proj.ID)
	r.fetch(context.Background(), h.proj)
	if st := r.Status(h.proj); !st.Fetched.IsZero() || st.Behind != 0 {
		t.Fatalf("fetched while locked: %+v", st)
	}
	unlock()

	local := gitRun(t, h.proj.Path, "rev-parse", "main")
	r.fetch(context.Background(), h.proj)
	st := r.Status(h.proj)
	if st.Fetched.IsZero() || st.Behind != 1 || st.Ahead != 0 || st.Error != "" || !st.Enabled {
		t.Fatalf("status after fetch = %+v; want origin 1 ahead", st)
	}
	if gitRun(t, h.proj.Path, "rev-parse", "main") != local {
		t.Fatal("a background fetch moved the local branch")
	}
}

func TestFetchBacksOffAfterFailuresAndResetsOnSuccess(t *testing.T) {
	parallel(t)

	h := newHarness(t, &fakeExec{fn: writeFile("", "x.txt")}, model.Project{Name: "p"})
	origin, _ := h.addOrigin(true, "")
	r, clk := h.remote()
	r.Select(context.Background(), h.proj, false, false)
	if len(r.Due()) != 1 {
		t.Fatal("selected project isn't due")
	}

	// Origin goes away (offline).
	gone := origin + ".gone"
	if err := os.Rename(origin, gone); err != nil {
		t.Fatal(err)
	}
	for i, wait := range []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 5 * time.Minute} {
		r.Select(context.Background(), h.proj, false, false) // the open UI keeps it selected
		r.fetch(context.Background(), h.proj)
		st := r.Status(h.proj)
		if st.Error == "" || !st.RetryAt.Equal(clk.now().Add(wait)) {
			t.Fatalf("failure %d: error %q, retry at %v; want a retry after %v", i+1, st.Error, st.RetryAt, wait)
		}
		clk.add(wait - time.Second)
		if len(r.Due()) != 0 {
			t.Fatalf("failure %d: due again before %v", i+1, wait)
		}
		clk.add(time.Second)
		if len(r.Due()) != 1 {
			t.Fatalf("failure %d: not due after %v", i+1, wait)
		}
	}

	// Back online: one success clears the error and the backoff.
	if err := os.Rename(gone, origin); err != nil {
		t.Fatal(err)
	}
	r.fetch(context.Background(), h.proj)
	if st := r.Status(h.proj); st.Error != "" || !st.RetryAt.IsZero() {
		t.Fatalf("after success: %+v", st)
	}
	r.fetch(context.Background(), h.proj)
	gitRun(t, h.proj.Path, "remote", "set-url", "origin", gone)
	r.fetch(context.Background(), h.proj)
	if st := r.Status(h.proj); !st.RetryAt.Equal(clk.now().Add(time.Minute)) {
		t.Fatalf("backoff wasn't reset by the success: retry at %v", st.RetryAt)
	}
}

func TestDueOnlySelectedOrBusyProjectsThatSync(t *testing.T) {
	parallel(t)

	h := newHarness(t, &fakeExec{fn: writeFile("", "x.txt")}, model.Project{Name: "p"})
	r, clk := h.remote()
	r.Idle = -1 // no occasional fetch of idle projects (TestIdleProjectsArePolledSlowly)
	if len(r.Due()) != 0 {
		t.Fatal("a project without origin is due")
	}
	h.addOrigin(true, "")
	if len(r.Due()) != 0 {
		t.Fatal("an unselected idle project is due")
	}
	r.Select(context.Background(), h.proj, false, false)
	if len(r.Due()) != 1 {
		t.Fatal("selected project isn't due")
	}
	clk.add(11 * time.Minute)
	if len(r.Due()) != 0 {
		t.Fatal("selection never expires")
	}
	td, err := h.st.CreateTodo(model.Todo{ProjectID: h.proj.ID, Title: "busy", Paused: true}) // paused: stays queued
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SetStatus(td.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	if len(r.Due()) != 1 {
		t.Fatal("project with a queued todo isn't due")
	}
}

func TestNewTodoStartsFromOriginWhenLocalIsBehind(t *testing.T) {
	parallel(t)

	h := newHarness(t, &fakeExec{fn: writeFile("", "mine.txt")}, model.Project{Name: "p"})
	_, other := h.addOrigin(true, "")
	write(t, other, "theirs.txt", "theirs\n")
	gitRun(t, other, "add", "theirs.txt")
	gitRun(t, other, "commit", "-q", "-m", "theirs")
	gitRun(t, other, "push", "-q", "origin", "main")
	want := gitRun(t, other, "rev-parse", "HEAD")

	td := h.waitFor(h.add("a").ID, model.StatusReview)
	if td.Worktree.BaseCommit != want {
		t.Fatalf("worktree based on %s, want origin's %s", td.Worktree.BaseCommit, want)
	}
	if got := gitRun(t, h.proj.Path, "rev-parse", "main"); got != want {
		t.Fatalf("local main at %s, want fast-forwarded to %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(td.Worktree.Path, "theirs.txt")); err != nil {
		t.Fatal("worktree lacks origin's commit")
	}
	log, _ := h.engine.Log.Read(td.ID)
	if !strings.Contains(log, "fast-forwarded main to origin") {
		t.Fatalf("log doesn't mention the fast-forward:\n%s", log)
	}
}

func TestNewTodoKeepsDivergedLocalBranch(t *testing.T) {
	parallel(t)

	h := newHarness(t, &fakeExec{fn: writeFile("", "mine.txt")}, model.Project{Name: "p"})
	_, other := h.addOrigin(true, "")
	gitRun(t, other, "commit", "--allow-empty", "-q", "-m", "theirs")
	gitRun(t, other, "push", "-q", "origin", "main")
	gitRun(t, h.proj.Path, "commit", "--allow-empty", "-q", "-m", "unpushed")
	local := gitRun(t, h.proj.Path, "rev-parse", "main")

	td := h.waitFor(h.add("a").ID, model.StatusReview)
	if td.Worktree.BaseCommit != local {
		t.Fatalf("worktree based on %s, want local %s as is", td.Worktree.BaseCommit, local)
	}
	if gitRun(t, h.proj.Path, "rev-parse", "main") != local {
		t.Fatal("diverged local main was moved")
	}
}

func TestNewTodoStartsWhenOriginIsUnreachable(t *testing.T) {
	parallel(t)

	h := newHarness(t, &fakeExec{fn: writeFile("", "mine.txt")}, model.Project{Name: "p"})
	origin, _ := h.addOrigin(true, "")
	if err := os.RemoveAll(origin); err != nil {
		t.Fatal(err)
	}
	local := gitRun(t, h.proj.Path, "rev-parse", "main")
	td := h.waitFor(h.add("a").ID, model.StatusReview)
	if td.Worktree.BaseCommit != local {
		t.Fatalf("worktree based on %s, want local %s", td.Worktree.BaseCommit, local)
	}
	if st := h.merges.Remote.Status(h.proj); st.Error == "" {
		t.Fatal("fetch failure isn't shown on the project")
	}
}
