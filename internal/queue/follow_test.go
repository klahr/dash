package queue

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

// followHarness is a runner's clone of a hub's project with an origin, and
// a second clone standing in for another runner. The runner adopted the
// clone (its owner may work in it too): it only ever fast-forwards.
func followHarness(t *testing.T) (h *harness, r *RemoteSync, origin, other string) {
	t.Helper()
	return followHarnessAs(t, model.HubAdopted)
}

// followHarnessAs is followHarness with the clone come by as how
// (model.Project.HubCheckout).
func followHarnessAs(t *testing.T, how string) (h *harness, r *RemoteSync, origin, other string) {
	t.Helper()
	h = newHarness(t, &fakeExec{fn: writeFile("", "x.txt")}, model.Project{Name: "p"})
	origin, other, _ = h.runnerProject()
	p, err := h.st.UpdateProject(h.proj.ID, func(p *model.Project) error { p.HubCheckout = how; return nil })
	if err != nil {
		t.Fatal(err)
	}
	h.proj = p
	// A tracked file to have uncommitted changes in.
	write(t, h.proj.Path, "README", "hello\n")
	gitRun(t, h.proj.Path, "add", "README")
	gitRun(t, h.proj.Path, "commit", "-q", "-m", "readme")
	gitRun(t, h.proj.Path, "push", "-q", "origin", "main")
	r, _ = h.remote()
	return h, r, origin, other
}

func head(t *testing.T, dir, ref string) string {
	t.Helper()
	return gitRun(t, dir, "rev-parse", ref)
}

func TestOriginMovedFastForwardsCleanCheckout(t *testing.T) {
	parallel(t)

	h, r, _, other := followHarness(t)
	pushFrom(t, other, "theirs.txt", "theirs\n")
	sha := head(t, other, "main")

	if err := r.OriginMoved(context.Background(), h.proj, "main", sha); err != nil {
		t.Fatal(err)
	}
	if got := head(t, h.proj.Path, "main"); got != sha {
		t.Fatalf("main = %s; want it fast-forwarded to %s", got, sha)
	}
	if _, err := os.Stat(h.proj.Path + "/theirs.txt"); err != nil {
		t.Fatal("the checkout's files didn't follow:", err)
	}
	if st := r.Status(h.proj); st.Behind != 0 || st.Stuck != "" {
		t.Fatalf("status = %+v; want caught up", st)
	}
	if len(r.Behind()) != 0 {
		t.Fatalf("Behind() = %+v; want none", r.Behind())
	}
}

func TestOriginMovedLeavesDirtyCheckoutBehind(t *testing.T) {
	parallel(t)

	h, r, _, other := followHarness(t)
	write(t, h.proj.Path, "README", "my edit\n")
	before := head(t, h.proj.Path, "main")
	pushFrom(t, other, "theirs.txt", "theirs\n")

	if err := r.OriginMoved(context.Background(), h.proj, "main", head(t, other, "main")); err != nil {
		t.Fatal(err)
	}
	if got := head(t, h.proj.Path, "main"); got != before {
		t.Fatal("a checkout with uncommitted changes was moved")
	}
	if b, _ := os.ReadFile(h.proj.Path + "/README"); string(b) != "my edit\n" {
		t.Fatalf("the uncommitted change was lost: %q", b)
	}
	st := r.Status(h.proj)
	if st.Behind != 1 || !strings.Contains(st.Stuck, "uncommitted changes") {
		t.Fatalf("status = %+v; want 1 behind, stuck on uncommitted changes", st)
	}
	if b := r.Behind(); len(b) != 1 || b[0].ProjectID != h.proj.ID {
		t.Fatalf("Behind() = %+v; want the project", b)
	}

	// Committed or stashed: the next fetch brings it up to date.
	gitRun(t, h.proj.Path, "checkout", "--", "README")
	r.fetch(context.Background(), h.proj)
	if st := r.Status(h.proj); st.Behind != 0 || st.Stuck != "" {
		t.Fatalf("after cleaning up: %+v; want caught up", st)
	}
}

func TestOriginMovedLeavesDivergedCheckoutBehind(t *testing.T) {
	parallel(t)

	h, r, _, other := followHarness(t)
	gitRun(t, h.proj.Path, "commit", "--allow-empty", "-q", "-m", "mine")
	mine := head(t, h.proj.Path, "main")
	pushFrom(t, other, "theirs.txt", "theirs\n")

	if err := r.OriginMoved(context.Background(), h.proj, "main", head(t, other, "main")); err != nil {
		t.Fatal(err)
	}
	if head(t, h.proj.Path, "main") != mine {
		t.Fatal("a diverged branch was moved")
	}
	st := r.Status(h.proj)
	if st.Behind != 1 || st.Ahead != 1 || !strings.Contains(st.Stuck, "diverged") {
		t.Fatalf("status = %+v; want 1 behind, 1 ahead, stuck as diverged", st)
	}
}

func TestOriginMovedDuplicatesAndOldNewsAreNoOps(t *testing.T) {
	parallel(t)

	h, r, origin, other := followHarness(t)
	pushFrom(t, other, "a.txt", "a\n")
	a := head(t, other, "main")
	pushFrom(t, other, "b.txt", "b\n")
	b := head(t, other, "main")
	ctx := context.Background()
	if err := r.OriginMoved(ctx, h.proj, "main", b); err != nil {
		t.Fatal(err)
	}
	if head(t, h.proj.Path, "main") != b {
		t.Fatal("not fast-forwarded")
	}

	// Origin can't be reached now: any fetch would fail and say so.
	if err := os.Rename(origin, origin+".gone"); err != nil {
		t.Fatal(err)
	}
	for _, sha := range []string{b, a, b} { // the same news again, and older news late
		if err := r.OriginMoved(ctx, h.proj, "main", sha); err != nil {
			t.Fatalf("news of %s: %v", sha, err)
		}
	}
	if st := r.Status(h.proj); st.Error != "" || st.Behind != 0 {
		t.Fatalf("status = %+v; want no fetch tried", st)
	}
	if head(t, h.proj.Path, "main") != b {
		t.Fatal("old news moved the branch back")
	}
	// Other branches are none of its business.
	if err := r.OriginMoved(ctx, h.proj, "feature", strings.Repeat("ab", 20)); err != nil {
		t.Fatal(err)
	}
	if st := r.Status(h.proj); st.Error != "" {
		t.Fatalf("news of another branch fetched: %+v", st)
	}
}

func TestOriginMovedNeverTouchesTaskWorktrees(t *testing.T) {
	parallel(t)

	h, r, _, other := followHarness(t)
	wt := h.t.TempDir() + "/wt"
	gitRun(t, h.proj.Path, "worktree", "add", "-q", "-b", "dash/task", wt, "main")
	before := head(t, wt, "HEAD")
	pushFrom(t, other, "theirs.txt", "theirs\n")
	if err := r.OriginMoved(context.Background(), h.proj, "main", head(t, other, "main")); err != nil {
		t.Fatal(err)
	}
	if head(t, wt, "HEAD") != before {
		t.Fatal("a task's worktree moved")
	}
	if head(t, h.proj.Path, "main") != head(t, other, "main") {
		t.Fatal("the main checkout didn't follow")
	}
}

func TestPullFastForwardsOnly(t *testing.T) {
	parallel(t)

	h, r, _, other := followHarness(t)
	ctx := context.Background()
	gitRun(t, h.proj.Path, "commit", "--allow-empty", "-q", "-m", "mine")
	pushFrom(t, other, "theirs.txt", "theirs\n")

	var refused *PullRefused
	if _, err := r.Pull(ctx, h.proj); !errors.As(err, &refused) || !strings.Contains(refused.Reason, "diverged") {
		t.Fatalf("pull of a diverged branch: %v; want it refused", err)
	}
	gitRun(t, h.proj.Path, "reset", "-q", "--hard", "HEAD~1")

	write(t, h.proj.Path, "README", "my edit\n")
	if _, err := r.Pull(ctx, h.proj); !errors.As(err, &refused) || !strings.Contains(refused.Reason, "uncommitted") {
		t.Fatalf("pull with uncommitted changes: %v; want it refused", err)
	}
	gitRun(t, h.proj.Path, "checkout", "--", "README")

	moved, err := r.Pull(ctx, h.proj)
	if err != nil || moved != 1 {
		t.Fatalf("pull = %d, %v; want 1 commit", moved, err)
	}
	if head(t, h.proj.Path, "main") != head(t, other, "main") {
		t.Fatal("not at origin's main")
	}
	if moved, err := r.Pull(ctx, h.proj); err != nil || moved != 0 {
		t.Fatalf("pull when up to date = %d, %v", moved, err)
	}
}

func TestIdleProjectsArePolledSlowly(t *testing.T) {
	parallel(t)

	h, r, _, _ := followHarness(t)
	_, clk := h.remote()
	r.Idle = 5 * time.Minute
	if len(r.Due()) != 1 {
		t.Fatal("a project never fetched isn't due")
	}
	r.fetch(context.Background(), h.proj)
	if len(r.Due()) != 0 {
		t.Fatal("an idle project is due right after a fetch")
	}
	clk.add(5 * time.Minute)
	if len(r.Due()) != 1 {
		t.Fatal("an idle project isn't due after Idle")
	}
	r.Idle = -1
	if len(r.Due()) != 0 {
		t.Fatal("Idle < 0 still polls")
	}
}

func TestFetchAllCatchesUpOnReconnect(t *testing.T) {
	parallel(t)

	h, r, _, other := followHarness(t)
	pushFrom(t, other, "theirs.txt", "theirs\n")
	want := head(t, other, "main")
	r.FetchAll(context.Background())
	if head(t, h.proj.Path, "main") != want {
		t.Fatal("FetchAll didn't bring the checkout up to origin")
	}
}

func TestLandingOnOriginReportsThePush(t *testing.T) {
	parallel(t)

	h := newHarness(t, &fakeExec{fn: writeFile("", "mine.txt")}, model.Project{Name: "p"})
	_, other, _ := h.runnerProject()
	var mu sync.Mutex
	var got []string
	h.merges.OnPushed = func(p model.Project, branch, sha string) {
		mu.Lock()
		got = append(got, p.ID+" "+branch+" "+sha)
		mu.Unlock()
	}
	td := h.waitFor(h.add("mine").ID, model.StatusReview)
	h.approve(td.ID)
	td = h.waitFor(td.ID, model.StatusMerged)
	gitRun(t, other, "pull", "-q", "origin", "main")
	want := h.proj.ID + " main " + head(t, other, "main")
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("pushes reported = %q; want [%q]", got, want)
	}
}
