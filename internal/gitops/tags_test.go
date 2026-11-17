package gitops

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// Tags pushed from another computer come in with the default branch's fetch
// and with FetchTags; a local tag that differs from origin's stays and
// doesn't make the fetch fail.
func TestFetchBringsTags(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	g, repo, other := remoteRepos(t)
	sha := commitIn(t, other, "a.txt")
	gitOut(t, other, "-c", "tag.gpgsign=false", "tag", "v1.0.0")
	gitOut(t, other, "-c", "tag.gpgsign=false", "tag", "clash")
	gitOut(t, other, "push", "-q", "origin", "main", "--tags")
	gitOut(t, repo, "-c", "tag.gpgsign=false", "tag", "clash") // same name, another commit

	if err := g.FetchBranch(ctx, "p", repo, "main", false); err != nil {
		t.Fatalf("fetch with a clashing tag: %v", err)
	}
	if got, _ := g.Commit(ctx, "p", repo, "refs/tags/v1.0.0"); got != sha {
		t.Errorf("v1.0.0 is %q, want %s", got, sha)
	}
	if got, _ := g.Commit(ctx, "p", repo, "refs/tags/clash"); got == sha {
		t.Error("the local tag was overwritten")
	}

	gitOut(t, other, "-c", "tag.gpgsign=false", "tag", "v1.1.0")
	gitOut(t, other, "push", "-q", "origin", "v1.1.0")
	changed, err := g.FetchTags(ctx, "p", repo)
	if err != nil || !changed {
		t.Fatalf("FetchTags: %v %v", changed, err)
	}
	if changed, err := g.FetchTags(ctx, "p", repo); err != nil || changed {
		t.Errorf("nothing new: %v %v", changed, err)
	}
	tags, _ := g.Tags(ctx, "p", repo)
	if !slices.Contains(tags, "v1.1.0") || !g.HasTag(ctx, "p", repo, "v1.1.0") {
		t.Errorf("tags %v", tags)
	}
}

func TestCreateAndPushTag(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	g, repo, _ := remoteRepos(t)
	sha := commitIn(t, repo, "a.txt")
	if g.OnRemote(ctx, "p", repo, sha) {
		t.Fatal("unpushed commit counted as on origin")
	}
	gitOut(t, repo, "push", "-q", "origin", "main")
	if !g.OnRemote(ctx, "p", repo, sha) {
		t.Fatal("pushed commit not on origin")
	}
	if err := g.CreateTag(ctx, "p", repo, "v0.1.0", sha, "Release v0.1.0\n\n- notes"); err != nil {
		t.Fatal(err)
	}
	if err := g.PushTag(ctx, "p", repo, "v0.1.0"); err != nil {
		t.Fatal(err)
	}
	if ok, err := g.RemoteHasTag(ctx, "p", repo, "v0.1.0"); err != nil || !ok {
		t.Fatalf("origin has it: %v %v", ok, err)
	}
	if ok, _ := g.RemoteHasTag(ctx, "p", repo, "v9.9.9"); ok {
		t.Error("origin has a tag that was never pushed")
	}
	if typ := gitOut(t, repo, "cat-file", "-t", "v0.1.0"); typ != "tag" {
		t.Errorf("not annotated: %s", typ)
	}
	cs, err := g.CommitsBetween(ctx, "p", repo, "", "v0.1.0", time.Time{})
	if err != nil || len(cs) != 2 || cs[0].SHA != sha {
		t.Errorf("commits %+v %v", cs, err)
	}
}

func TestOnlyClobbered(t *testing.T) {
	t.Parallel()

	out := " ! [rejected]        clash      -> clash  (would clobber existing tag)\n"
	if !onlyClobbered(out) {
		t.Error("clobbered tag counted as a failure")
	}
	if onlyClobbered(out + "fatal: unable to access 'x': Could not resolve host\n") {
		t.Error("a real failure counted as clobbered")
	}
	if onlyClobbered("fatal: couldn't find remote ref main") || onlyClobbered(strings.Repeat("\n", 2)) {
		t.Error("no clobber, yet ok")
	}
}

// A branch origin doesn't have counts only the commits past what the remote
// branches have, not its whole history; a since bound leaves older commits
// out of CommitsBetween.
func TestUnpushedNeverPushedBranch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	g, repo, _ := remoteRepos(t)
	gitOut(t, repo, "checkout", "-q", "-b", "dev")
	commitIn(t, repo, "a.txt")
	gitOut(t, repo, "commit", "-q", "--allow-empty", "-m", "Task\n\nDash-Todo: t1")

	if n, err := g.Unpushed(ctx, "p", repo, "dev"); err != nil || n != 2 {
		t.Errorf("unpushed %d %v, want 2", n, err)
	}
	if ids, err := g.UnpushedTodos(ctx, "p", repo, "dev"); err != nil || !slices.Equal(ids, []string{"t1"}) {
		t.Errorf("unpushed todos %v %v", ids, err)
	}
	if cs, err := g.CommitsBetween(ctx, "p", repo, "", "dev", time.Now().Add(time.Hour)); err != nil || len(cs) != 0 {
		t.Errorf("commits since the future: %+v %v", cs, err)
	}
	if cs, err := g.CommitsBetween(ctx, "p", repo, "", "dev", time.Now().Add(-time.Hour)); err != nil || len(cs) < 2 || !slices.Equal(cs[0].Todos, []string{"t1"}) {
		t.Errorf("commits since an hour ago: %+v %v", cs, err)
	}
}
