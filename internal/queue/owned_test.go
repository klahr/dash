package queue

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

// ownedHarness is followHarness for a clone the runner made itself: Dash
// keeps it in sync whatever is in it.
func ownedHarness(t *testing.T) (h *harness, r *RemoteSync, other string) {
	t.Helper()
	h, r, _, other = followHarnessAs(t, model.HubCloned)
	if !h.proj.DashOwnsCheckout() {
		t.Fatalf("a clone the runner made isn't Dash's: %+v", h.proj)
	}
	return h, r, other
}

// backups lists the clone's dash-backup/ branches.
func backups(t *testing.T, dir string) []string {
	t.Helper()
	return strings.Fields(gitRun(t, dir, "for-each-ref", "--format=%(refname:short)", "refs/heads/dash-backup/"))
}

// caughtUp fails unless dir's main is other's (origin's), checked out with
// origin's files, and the status says so.
func caughtUp(t *testing.T, h *harness, r *RemoteSync, other string) {
	t.Helper()
	if got, want := head(t, h.proj.Path, "main"), head(t, other, "main"); got != want {
		t.Fatalf("main = %s; want origin's %s", got, want)
	}
	if out := gitRun(t, h.proj.Path, "status", "--porcelain"); out != "" {
		t.Fatalf("the checkout isn't origin's: %q", out)
	}
	if st := r.Status(h.proj); st.Behind != 0 || st.Ahead != 0 || st.Stuck != "" {
		t.Fatalf("status = %+v; want caught up", st)
	}
}

// A clone whose config says core.bare = true (a stray `git init` with
// GIT_DIR naming one of its worktrees) fails every command in its folder.
// The next sync puts it right and catches up, with nothing to show for it.
func TestOwnedCloneRepairsCoreBare(t *testing.T) {
	parallel(t)

	h, r, other := ownedHarness(t)
	gitRun(t, h.proj.Path, "config", "core.bare", "true")
	if _, _, err := h.git.CheckoutState(context.Background(), h.proj.ID, h.proj.Path); err == nil {
		t.Fatal("core.bare = true didn't break the checkout; the test shows nothing")
	}
	pushFrom(t, other, "theirs.txt", "theirs\n")

	if err := r.OriginMoved(context.Background(), h.proj, "main", head(t, other, "main")); err != nil {
		t.Fatal(err)
	}
	if got := gitRun(t, h.proj.Path, "config", "core.bare"); got != "false" {
		t.Fatalf("core.bare = %q; want false", got)
	}
	caughtUp(t, h, r, other)
	if b := backups(t, h.proj.Path); len(b) != 0 {
		t.Fatalf("backups %v; nothing was in the way", b)
	}
	if len(r.Behind()) != 0 {
		t.Fatalf("Behind() = %+v; want nothing for the hub to show", r.Behind())
	}
}

// Uncommitted changes (and untracked files) in a clone Dash owns are saved
// on a backup branch, and the clone catches up with origin.
func TestOwnedCloneBacksUpUncommittedChanges(t *testing.T) {
	parallel(t)

	h, r, other := ownedHarness(t)
	before := head(t, h.proj.Path, "main")
	write(t, h.proj.Path, "README", "my edit\n")
	write(t, h.proj.Path, "notes.txt", "untracked\n")
	pushFrom(t, other, "theirs.txt", "theirs\n")

	if err := r.OriginMoved(context.Background(), h.proj, "main", head(t, other, "main")); err != nil {
		t.Fatal(err)
	}
	caughtUp(t, h, r, other)
	if _, err := os.Stat(filepath.Join(h.proj.Path, "notes.txt")); !os.IsNotExist(err) {
		t.Error("the untracked file is still in the checkout")
	}
	b := backups(t, h.proj.Path)
	if len(b) != 1 {
		t.Fatalf("backups %v; want one", b)
	}
	if got := gitRun(t, h.proj.Path, "show", b[0]+":README"); got != "my edit" {
		t.Errorf("backup's README = %q", got)
	}
	if got := gitRun(t, h.proj.Path, "show", b[0]+":notes.txt"); got != "untracked" {
		t.Errorf("backup's notes.txt = %q", got)
	}
	if got := head(t, h.proj.Path, b[0]+"~1"); got != before {
		t.Errorf("the backup's parent is %s; want the branch as it was, %s", got, before)
	}
}

// Local commits origin doesn't have (a diverged branch) are saved on a
// backup branch, and the clone catches up with origin.
func TestOwnedCloneBacksUpLocalCommits(t *testing.T) {
	parallel(t)

	h, r, other := ownedHarness(t)
	write(t, h.proj.Path, "mine.txt", "mine\n")
	gitRun(t, h.proj.Path, "add", "mine.txt")
	gitRun(t, h.proj.Path, "commit", "-q", "-m", "mine")
	mine := head(t, h.proj.Path, "main")
	pushFrom(t, other, "theirs.txt", "theirs\n")

	if err := r.OriginMoved(context.Background(), h.proj, "main", head(t, other, "main")); err != nil {
		t.Fatal(err)
	}
	caughtUp(t, h, r, other)
	if b := backups(t, h.proj.Path); len(b) != 1 || head(t, h.proj.Path, b[0]) != mine {
		t.Fatalf("backups %v; want one at the local commit %s", b, mine)
	}
}

// Resetting the clone leaves its task worktrees, their branches and their
// uncommitted work as they are.
func TestOwnedCloneResetKeepsTaskWorktrees(t *testing.T) {
	parallel(t)

	h, r, other := ownedHarness(t)
	wt := filepath.Join(h.t.TempDir(), "wt")
	gitRun(t, h.proj.Path, "worktree", "add", "-q", "-b", "dash/task", wt, "main")
	write(t, wt, "task.txt", "task\n")
	gitRun(t, wt, "add", "task.txt")
	gitRun(t, wt, "commit", "-q", "-m", "task")
	task := head(t, wt, "HEAD")
	write(t, wt, "README", "task's uncommitted edit\n")
	write(t, h.proj.Path, "README", "my edit\n") // the main checkout is dirty: it gets reset
	pushFrom(t, other, "theirs.txt", "theirs\n")

	if err := r.OriginMoved(context.Background(), h.proj, "main", head(t, other, "main")); err != nil {
		t.Fatal(err)
	}
	caughtUp(t, h, r, other)
	if head(t, wt, "HEAD") != task || head(t, h.proj.Path, "dash/task") != task {
		t.Fatal("the task's branch moved")
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "README")); string(b) != "task's uncommitted edit\n" {
		t.Fatalf("the task's uncommitted edit is gone: %q", b)
	}
}

// A new todo in a clone Dash owns starts from origin's branch even when
// something was in the way, and its log says where that went.
func TestOwnedCloneFastForwardBeforeATodo(t *testing.T) {
	parallel(t)

	h, r, other := ownedHarness(t)
	write(t, h.proj.Path, "README", "my edit\n")
	pushFrom(t, other, "theirs.txt", "theirs\n")

	note := r.FastForward(context.Background(), h.proj)
	caughtUp(t, h, r, other)
	b := backups(t, h.proj.Path)
	if len(b) != 1 || !strings.Contains(note, b[0]) {
		t.Fatalf("note %q, backups %v; want the note to name the backup", note, b)
	}
}

// Someone's own project (not a runner's clone) never moves on its own: a
// fetch leaves uncommitted changes and local commits where they are.
func TestOwnProjectIsNeverReset(t *testing.T) {
	parallel(t)

	h := newHarness(t, &fakeExec{fn: writeFile("", "x.txt")}, model.Project{Name: "p"})
	_, other := h.addOrigin(true, "")
	r, _ := h.remote()
	gitRun(t, h.proj.Path, "commit", "--allow-empty", "-q", "-m", "mine")
	mine := head(t, h.proj.Path, "main")
	write(t, h.proj.Path, "notes.txt", "mine\n")
	gitRun(t, h.proj.Path, "config", "core.bare", "true") // not Dash's to repair either
	pushFrom(t, other, "theirs.txt", "theirs\n")

	ctx := context.Background()
	r.fetch(ctx, h.proj)
	if err := r.OriginMoved(ctx, h.proj, "main", head(t, other, "main")); err != nil {
		t.Fatal(err)
	}
	gitRun(t, h.proj.Path, "config", "--file", filepath.Join(h.proj.Path, ".git", "config"), "core.bare", "false")
	if head(t, h.proj.Path, "main") != mine {
		t.Fatal("someone's own branch moved")
	}
	if b, _ := os.ReadFile(filepath.Join(h.proj.Path, "notes.txt")); string(b) != "mine\n" {
		t.Fatal("someone's own file changed")
	}
	if b := backups(t, h.proj.Path); len(b) != 0 {
		t.Fatalf("backups %v in someone's own project", b)
	}
}

// A task's setup and test commands don't inherit git variables that point
// at another repository: with GIT_DIR naming the task's worktree (set by a
// hook, say, in the process that started Dash), a test's `git init` of a
// temporary repository would rewrite the clone to core.bare = true.
func TestTaskCommandsIgnoreInheritedGitDir(t *testing.T) {
	root := t.TempDir() // not a harness: those run in parallel, and t.Setenv can't
	clone, wt := filepath.Join(root, "clone"), filepath.Join(root, "wt")
	gitRun(t, root, "init", "-q", "-b", "main", clone)
	gitRun(t, clone, "commit", "-q", "--allow-empty", "-m", "first")
	gitRun(t, clone, "worktree", "add", "-q", "-b", "dash/task", wt, "main")
	t.Setenv("GIT_DIR", filepath.Join(clone, ".git", "worktrees", "wt"))

	tmp := filepath.Join(t.TempDir(), "scratch")
	exit, tail, err := runShell(context.Background(), wt, "git init -q "+tmp, time.Minute, nil, func(string) {})
	if err != nil || exit != 0 {
		t.Fatalf("git init: %d %v %s", exit, err, tail)
	}
	cfg := filepath.Join(clone, ".git", "config")
	if got := gitRun(t, root, "config", "--file", cfg, "core.bare"); got != "false" {
		t.Fatalf("the clone's core.bare = %q after a task command's git init", got)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".git")); err != nil {
		t.Fatalf("the command's git init didn't make its own repository: %v", err)
	}
}
