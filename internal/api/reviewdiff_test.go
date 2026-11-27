package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// Sending a task back from Review records the commit the user reviewed and
// the comments sent with it; back in Review, diff/since is what changed
// since that commit, and the full diff, with a note, once main was merged
// into the branch.
func TestDiffSinceLastReview(t *testing.T) {
	s, _, p := boardServer(t)
	s.Git = gitops.New(nil)
	ctx := context.Background()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := s.Git.InitRepo(ctx, p.ID, repo, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw(s).UpdateProject(p.ID, func(x *model.Project) error { x.Path, x.DefaultBranch = repo, "main"; return nil }); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, "wt")
	git(t, repo, "worktree", "add", "-q", "-b", "dash/t", wt)
	commit := func(dir, file string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, file), []byte(file+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, dir, "add", file)
		git(t, dir, "commit", "-q", "-m", file)
	}
	td := withStatus(t, s, p.ID, model.StatusInProgress)
	// A run ends: its commit, and the review point the queue records.
	ran := func(file string) {
		t.Helper()
		commit(wt, file)
		head, base, err := s.Git.ReviewPoint(ctx, p.ID, wt, "main")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw(s).UpdateTodo(td.ID, func(x *model.Todo) error {
			x.Worktree = &model.Worktree{Path: wt, Branch: "dash/t", BaseBranch: "main"}
			x.ReviewHead, x.ReviewBase, x.Status = head, base, model.StatusReview
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	h := loggedIn(t, s.Routes())
	since := func() sinceReview {
		t.Helper()
		w := do(t, h, "GET", "/api/todos/"+td.ID+"/diff/since", "")
		if w.Code != http.StatusOK {
			t.Fatalf("diff/since: %d %s", w.Code, w.Body)
		}
		var got sinceReview
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	// The first review: the full diff, as today.
	ran("first.txt")
	reviewed := git(t, wt, "rev-parse", "HEAD")
	if got := since(); got.Round != 1 || got.Since != "" || got.Note != "" || !strings.Contains(got.Diff, "first.txt") {
		t.Fatalf("first review: %+v", got)
	}

	// A comment, sent with the rework: the round keeps the commit and it.
	if w := do(t, h, "POST", "/api/todos/"+td.ID+"/comments", `{"file":"first.txt","line":1,"code":"+first.txt","text":"Shout it"}`); w.Code != http.StatusOK {
		t.Fatalf("comment: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "POST", "/api/todos/"+td.ID+"/rework", `{"comments":true}`); w.Code != http.StatusOK {
		t.Fatalf("rework: %d %s", w.Code, w.Body)
	}
	got, _ := raw(s).GetTodo(td.ID)
	if len(got.Reviews) != 1 {
		t.Fatalf("reviews after the rework: %+v", got.Reviews)
	}
	r := got.Reviews[0]
	if r.Commit != reviewed || r.Base != git(t, repo, "rev-parse", "main") || r.At.IsZero() ||
		len(r.Comments) != 1 || r.Comments[0].Text != "Shout it" {
		t.Fatalf("the round recorded: %+v", r)
	}

	// Back in review: only what the rework changed.
	if _, err := raw(s).SetStatus(td.ID, model.StatusInProgress, ""); err != nil {
		t.Fatal(err)
	}
	ran("rework.txt")
	got2 := since()
	if got2.Round != 2 || got2.Since != reviewed || got2.Note != "" ||
		!strings.Contains(got2.Diff, "rework.txt") || strings.Contains(got2.Diff, "first.txt") {
		t.Fatalf("second review: %+v", got2)
	}
	// The full diff is still there.
	if w := do(t, h, "GET", "/api/todos/"+td.ID+"/diff", ""); !strings.Contains(w.Body.String(), "first.txt") || !strings.Contains(w.Body.String(), "rework.txt") {
		t.Fatalf("full diff: %s", w.Body)
	}

	// Main merged into the branch since: the full diff, saying why.
	commit(repo, "main.txt")
	git(t, wt, "merge", "-q", "--no-edit", "main")
	got3 := since()
	if got3.Round != 2 || got3.Since != "" || !strings.Contains(got3.Note, "main was merged") || !strings.Contains(got3.Diff, "first.txt") {
		t.Fatalf("after merging main: %+v", got3)
	}

	// Leaving Review another way than back to Claude (approved) records no round.
	if _, err := raw(s).SetStatus(td.ID, model.StatusMerging, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := raw(s).GetTodo(td.ID); len(got.Reviews) != 1 {
		t.Fatalf("reviews after approving: %+v", got.Reviews)
	}
}

// EndReview is a round only when the task leaves Review back to Claude, and
// its comments are those the change took away.
func TestEndReview(t *testing.T) {
	a := model.ReviewComment{ID: "a", Text: "sent"}
	b := model.ReviewComment{ID: "b", Text: "kept"}
	before := model.Todo{Status: model.StatusReview, ReviewHead: "h", ReviewBase: "b", Comments: []model.ReviewComment{a, b}}
	now := time.Now()
	r, ok := model.EndReview(before, model.Todo{Status: model.StatusQueued, Comments: []model.ReviewComment{b}}, now)
	if !ok || r.Commit != "h" || r.Base != "b" || !r.At.Equal(now) || len(r.Comments) != 1 || r.Comments[0].ID != "a" {
		t.Fatalf("EndReview back to the queue = %+v, %v", r, ok)
	}
	for _, to := range []model.TodoStatus{model.StatusMerging, model.StatusMerged, model.StatusReview} {
		if _, ok := model.EndReview(before, model.Todo{Status: to}, now); ok {
			t.Errorf("EndReview to %s is a round", to)
		}
	}
	if _, ok := model.EndReview(model.Todo{Status: model.StatusInProgress}, model.Todo{Status: model.StatusReview}, now); ok {
		t.Error("EndReview into Review is a round")
	}
}
