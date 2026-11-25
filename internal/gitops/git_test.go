package gitops

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestMainBranch(t *testing.T) {
	t.Parallel()

	ctx, g := context.Background(), New(nil)
	dir := filepath.Join(t.TempDir(), "repo")
	if err := g.InitRepo(ctx, "p", dir, "master"); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "checkout", "-q", "-b", "feature")
	if g.HasBranch(ctx, "p", dir, "main") || !g.HasBranch(ctx, "p", dir, "master") {
		t.Fatal("HasBranch wrong")
	}
	if b := g.MainBranch(ctx, "p", dir); b != "master" {
		t.Fatalf("MainBranch = %q, want master", b)
	}

	// A remote's HEAD wins over the main/master names.
	gitIn(t, dir, "update-ref", "refs/remotes/github/trunk", "HEAD")
	gitIn(t, dir, "symbolic-ref", "refs/remotes/github/HEAD", "refs/remotes/github/trunk")
	gitIn(t, dir, "branch", "trunk")
	if b := g.MainBranch(ctx, "p", dir); b != "trunk" {
		t.Fatalf("MainBranch = %q, want trunk", b)
	}
}

// A new project's folder inside another repository (a home folder kept as a
// dotfiles repo) gets a repository of its own: InitRepo used to find the
// enclosing one and make that the project.
func TestInitRepoInsideAnotherRepo(t *testing.T) {
	t.Parallel()

	ctx, g := context.Background(), New(nil)
	home := t.TempDir()
	gitIn(t, home, "init", "-q", "-b", "dotfiles")
	gitIn(t, home, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "dotfiles")
	dir := filepath.Join(home, "dash-projects", "app")
	if err := g.InitRepo(ctx, "p", dir, "main"); err != nil {
		t.Fatal(err)
	}
	if top, err := g.git(ctx, "p", dir, "rev-parse", "--show-toplevel"); err != nil || !samePath(top, dir) {
		t.Fatalf("top level = %q, %v; want %s", top, err, dir)
	}
	if b, _ := g.git(ctx, "p", dir, "branch", "--show-current"); b != "main" {
		t.Fatalf("branch = %q, want main", b)
	}
	if n, _ := g.git(ctx, "p", home, "rev-list", "--count", "HEAD"); n != "1" {
		t.Fatalf("the enclosing repo has %s commits, want 1", n)
	}

	// A bare repository is one of its own, and is left as it is.
	bare := filepath.Join(t.TempDir(), "bare.git")
	gitIn(t, filepath.Dir(bare), "clone", "-q", "--bare", home, bare)
	if err := g.InitRepo(ctx, "p", bare, "main"); err != nil {
		t.Fatal(err)
	}
	if b, _ := g.git(ctx, "p", bare, "rev-parse", "--is-bare-repository"); b != "true" {
		t.Fatalf("bare repository: is-bare = %q", b)
	}
}

// A NUL byte in a commit message (pasted into a task's prompt) used to make
// the commit fail with "fork/exec git: invalid argument".
func TestCommitMessageWithNUL(t *testing.T) {
	t.Parallel()

	ctx, g := context.Background(), New(nil)
	dir := filepath.Join(t.TempDir(), "repo")
	if err := g.InitRepo(ctx, "p", dir, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.git(ctx, "p", dir, g.committing(ctx, dir, "commit", "--allow-empty", "-m", "show $ sed but \x00instead")...); err != nil {
		t.Fatal(err)
	}
	out, err := g.git(ctx, "p", dir, "log", "-n1", "--format=%s")
	if err != nil || out != "show $ sed but instead" {
		t.Fatalf("log = %q, %v", out, err)
	}
}

// Graph returns commits with their parents in topological order, so a merge
// lists both parents and every commit comes before its parents.
func TestGraph(t *testing.T) {
	t.Parallel()

	ctx, g := context.Background(), New(nil)
	dir := filepath.Join(t.TempDir(), "repo")
	if err := g.InitRepo(ctx, "p", dir, "main"); err != nil {
		t.Fatal(err)
	}
	commit := func(msg string) {
		gitIn(t, dir, "-c", "user.name=T", "-c", "user.email=t@x", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "commit", "-q", "--allow-empty", "-m", msg)
	}
	commit("root")
	gitIn(t, dir, "checkout", "-q", "-b", "side")
	commit("side work")
	gitIn(t, dir, "checkout", "-q", "main")
	commit("main work, with | odd: chars")
	gitIn(t, dir, "-c", "user.name=T", "-c", "user.email=t@x", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "merge", "-q", "--no-ff", "-m", "merge side", "side")
	gitIn(t, dir, "-c", "tag.gpgsign=false", "tag", "v1")

	commits, err := g.Graph(ctx, "p", dir, "main", 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 5 { // InitRepo makes the first commit
		t.Fatalf("got %d commits: %+v", len(commits), commits)
	}
	at := map[string]int{}
	for i, c := range commits {
		at[c.Hash] = i
		if len(c.Hash) != 40 || !strings.HasPrefix(c.Hash, c.Short) || c.Author == "" || c.When == "" {
			t.Errorf("commit %d: %+v", i, c)
		}
	}
	for i, c := range commits {
		for _, p := range c.Parents {
			if j, ok := at[p]; !ok || j <= i {
				t.Errorf("parent %s of %q isn't listed after it", p, c.Subject)
			}
		}
	}
	m := commits[0]
	if m.Subject != "merge side" || len(m.Parents) != 2 || !slices.Contains(m.Refs, "tag: v1") || !slices.Contains(m.Refs, "HEAD -> main") {
		t.Errorf("merge: %+v", m)
	}
	if r := commits[4]; len(r.Parents) != 0 || r.Parents == nil || len(r.Refs) != 0 || r.Refs == nil {
		t.Errorf("root: %+v", r)
	}
	if commits[at[m.Parents[0]]].Subject != "main work, with | odd: chars" {
		t.Errorf("first parent: %+v", commits[at[m.Parents[0]]])
	}
}

// Graph also lists origin's commits the local branch doesn't have yet (pushed
// from elsewhere and fetched), besides the local ones origin doesn't have.
func TestGraphOriginAhead(t *testing.T) {
	t.Parallel()

	ctx, g := context.Background(), New(nil)
	dir := filepath.Join(t.TempDir(), "repo")
	if err := g.InitRepo(ctx, "p", dir, "main"); err != nil {
		t.Fatal(err)
	}
	commit := func(msg string) {
		gitIn(t, dir, "-c", "user.name=T", "-c", "user.email=t@x", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", msg)
	}
	commit("shared")
	commit("from elsewhere")
	gitIn(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitIn(t, dir, "reset", "-q", "--hard", "HEAD~1")

	// origin strictly ahead: its commit is listed, above the local tip.
	commits, err := g.Graph(ctx, "p", dir, "main", 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 3 || commits[0].Subject != "from elsewhere" || !slices.Contains(commits[0].Refs, "origin/main") ||
		commits[1].Subject != "shared" || !slices.Contains(commits[1].Refs, "HEAD -> main") {
		t.Fatalf("origin ahead: %+v", commits)
	}

	// Diverged: both tips are listed, each before its parents.
	commit("local only")
	commits, err = g.Graph(ctx, "p", dir, "main", 500)
	if err != nil {
		t.Fatal(err)
	}
	subjects := map[string]int{}
	at := map[string]int{}
	for i, c := range commits {
		subjects[c.Subject] = i
		at[c.Hash] = i
	}
	for _, s := range []string{"from elsewhere", "local only", "shared"} {
		if _, ok := subjects[s]; !ok {
			t.Fatalf("diverged: no %q in %+v", s, commits)
		}
	}
	for i, c := range commits {
		for _, p := range c.Parents {
			if j, ok := at[p]; ok && j <= i {
				t.Errorf("parent %s of %q isn't listed after it", p, c.Subject)
			}
		}
	}

	// No origin/main: only the local branch, as before.
	gitIn(t, dir, "update-ref", "-d", "refs/remotes/origin/main")
	commits, err = g.Graph(ctx, "p", dir, "main", 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 3 || commits[0].Subject != "local only" {
		t.Fatalf("no origin: %+v", commits)
	}
}
