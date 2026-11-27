package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/klahr/dash/internal/model"
)

func TestPatchFiles(t *testing.T) {
	patch := "diff --git a/a.go b/a.go\nindex 1..2 100644\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n a\n-b\n+c\n+d\n" +
		"diff --git a/old name.txt b/new name.txt\nsimilarity index 90%\nrename from old name.txt\nrename to new name.txt\n" +
		"diff --git a/pic.png b/pic.png\nBinary files a/pic.png and b/pic.png differ\n"
	files := patchFiles(patch)
	if len(files) != 3 {
		t.Fatalf("files: %+v", files)
	}
	for i, want := range []struct {
		path  string
		lines int
	}{{"a.go", 3}, {"old name.txt", 0}, {"pic.png", 0}} {
		if f := files[i]; f.Path != want.path || f.Lines != want.lines || len(f.Hash) != 16 {
			t.Errorf("file %d: %+v, want %+v", i, f, want)
		}
	}
	// The same part hashes the same; a changed one doesn't.
	again := patchFiles("diff --git a/a.go b/a.go\nindex 1..2 100644\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n a\n-b\n+c\n+e\n" +
		"diff --git a/pic.png b/pic.png\nBinary files a/pic.png and b/pic.png differ\n")
	if again[0].Hash == files[0].Hash || again[1].Hash != files[2].Hash {
		t.Errorf("hashes: %+v then %+v", files, again)
	}
}

// Marks are per user; recording a new diff keeps the marks on the files
// it didn't change and drops the others.
func TestSetReviewFilesPrunesViewed(t *testing.T) {
	var td model.Todo
	files := []model.DiffFile{{Path: "a", Lines: 3, Hash: "h1"}, {Path: "b", Lines: 1, Hash: "h2"}}
	if !td.SetReviewFiles(files, "k1") {
		t.Error("first files: not changed")
	}
	td.SetViewed(1, "a", "h1", true)
	td.SetViewed(1, "b", "h2", true)
	td.SetViewed(2, "a", "h1", true)
	td.SetViewed(2, "a", "h1", true) // twice: one mark
	if len(td.Viewed) != 3 {
		t.Fatalf("viewed: %+v", td.Viewed)
	}
	td.SetViewed(1, "b", "", false)
	if len(td.Viewed) != 2 {
		t.Fatalf("unmarked: %+v", td.Viewed)
	}
	if td.SetReviewFiles(files, "k1") {
		t.Error("the same files and key: changed")
	}
	if !td.SetReviewFiles([]model.DiffFile{{Path: "a", Lines: 4, Hash: "h3"}, {Path: "b", Lines: 1, Hash: "h2"}}, "k2") || td.Viewed != nil {
		t.Errorf("a changed: %+v", td.Viewed)
	}
}

// A file marked viewed stays so across reads of the task; a rework that
// changes the file clears its mark by itself, and leaves the marks on the
// files it didn't touch.
func TestViewedFilesClearWhenChanged(t *testing.T) {
	rr := newReleaseRepo(t, "")
	writeRepoFile(t, rr.repo, "a.go", "package a\n")
	writeRepoFile(t, rr.repo, "b.go", "package b\n")
	rgit(t, rr.repo, "add", "-A")
	rgit(t, rr.repo, "commit", "-q", "-m", "base")
	wt := filepath.Join(t.TempDir(), "wt")
	rgit(t, rr.repo, "worktree", "add", "-q", "-b", "dash/todo-1", wt, "main")
	writeRepoFile(t, wt, "a.go", "package a\n\nvar A = 1\n")
	writeRepoFile(t, wt, "b.go", "package b\n\nvar B = 1\nvar C = 2\n")
	rgit(t, wt, "commit", "-q", "-am", "Claude's work")
	td, err := raw(rr.s).CreateTodo(model.Todo{ProjectID: rr.p.ID, Title: "t", Prompt: "p",
		Worktree: &model.Worktree{Path: wt, Branch: "dash/todo-1", BaseBranch: "main"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []model.TodoStatus{model.StatusQueued, model.StatusInProgress, model.StatusReview} {
		if _, err := raw(rr.s).UpdateTodo(td.ID, func(x *model.Todo) error { x.Status = to; x.Runs = 1; return nil }); err != nil {
			t.Fatalf("to %s: %v", to, err)
		}
	}
	mark := func(file string, viewed bool) (int, model.Todo) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"file": file, "viewed": viewed})
		w := send(rr.owner, "PUT", "/api/todos/"+td.ID+"/viewed", string(body))
		var got model.Todo
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		return w.Code, got
	}
	viewed := func(x model.Todo) map[string]bool {
		m := map[string]bool{}
		for _, v := range x.Viewed {
			m[v.File] = true
		}
		return m
	}
	if code, _ := mark("a.go", true); code != http.StatusOK {
		t.Fatalf("mark a.go: %d", code)
	}
	code, got := mark("b.go", true)
	if code != http.StatusOK || !viewed(got)["a.go"] || !viewed(got)["b.go"] || len(got.ReviewFiles) != 2 {
		t.Fatalf("marked both: %d %+v", code, got)
	}
	if got.ReviewFiles[1].Lines != 3 {
		t.Errorf("b.go lines: %+v", got.ReviewFiles)
	}
	if got.Viewed[0].User == 0 {
		t.Errorf("a signed-in user's mark: %+v", got.Viewed)
	}
	if code, _ := mark("nope.go", true); code != http.StatusBadRequest {
		t.Errorf("a file not in the diff: %d", code)
	}
	if code, got := mark("b.go", false); code != http.StatusOK || viewed(got)["b.go"] || !viewed(got)["a.go"] {
		t.Errorf("unmark b.go: %d %+v", code, got.Viewed)
	}
	mark("b.go", true)

	// A rework changes b.go only. The run's end changes the task's diff
	// version, which reads the diff again (ObserveReviews; here by hand).
	writeRepoFile(t, wt, "b.go", "package b\n\nvar B = 2\n")
	rgit(t, wt, "commit", "-q", "-am", "Rework")
	if _, err := raw(rr.s).UpdateTodo(td.ID, func(x *model.Todo) error { x.Runs = 2; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := rr.s.syncReview(context.Background(), td.ID); err != nil {
		t.Fatal(err)
	}
	stored, _ := raw(rr.s).GetTodo(td.ID)
	if v := viewed(stored); !v["a.go"] || v["b.go"] || stored.ReviewKey != model.ReviewKeyOf(stored) {
		t.Errorf("after the rework: %+v", stored.Viewed)
	}

	// Opening Changes clears outdated marks too, with no run.
	writeRepoFile(t, wt, "a.go", "package a\n\nvar A = 3\n")
	rgit(t, wt, "commit", "-q", "-am", "By hand")
	if w := send(rr.owner, "GET", "/api/todos/"+td.ID+"/diff", ""); w.Code != http.StatusOK {
		t.Fatalf("diff: %d", w.Code)
	}
	if stored, _ := raw(rr.s).GetTodo(td.ID); len(stored.Viewed) != 0 {
		t.Errorf("after reading a changed diff: %+v", stored.Viewed)
	}

	// Only a task in review takes marks.
	if _, err := raw(rr.s).UpdateTodo(td.ID, func(x *model.Todo) error { x.Status = model.StatusQueued; return nil }); err != nil {
		t.Fatal(err)
	}
	if code, _ := mark("a.go", true); code != http.StatusBadRequest {
		t.Errorf("mark a queued task's file: %d", code)
	}
}
