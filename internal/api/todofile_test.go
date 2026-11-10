package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

func TestEditTodoFileInReview(t *testing.T) {
	rr := newReleaseRepo(t, "")
	writeRepoFile(t, rr.repo, "a.txt", "one\n")
	rgit(t, rr.repo, "add", "-A")
	rgit(t, rr.repo, "commit", "-q", "-m", "a")
	wt := filepath.Join(t.TempDir(), "wt")
	rgit(t, rr.repo, "worktree", "add", "-q", "-b", "dash/todo-1", wt, "main")
	writeRepoFile(t, wt, "a.txt", "one\ntwo\n")
	rgit(t, wt, "commit", "-q", "-am", "Claude's work")
	td, err := raw(rr.s).CreateTodo(model.Todo{ProjectID: rr.p.ID, Title: "t", Prompt: "p",
		Worktree: &model.Worktree{Path: wt, Branch: "dash/todo-1", BaseBranch: "main"}})
	if err != nil {
		t.Fatal(err)
	}
	// A new task starts as a draft; put it in review, as a finished run does.
	for _, st := range []model.TodoStatus{model.StatusQueued, model.StatusInProgress, model.StatusReview} {
		if _, err := raw(rr.s).UpdateTodo(td.ID, func(x *model.Todo) error { x.Status = st; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	base := "/api/todos/" + td.ID + "/file"
	read := func(path string) (int, fileContent) {
		w := send(rr.owner, "GET", base+"?path="+url.QueryEscape(path), "")
		var f fileContent
		_ = json.Unmarshal(w.Body.Bytes(), &f)
		if w.Code != http.StatusOK {
			t.Logf("GET %s: %d %s", path, w.Code, w.Body)
		}
		return w.Code, f
	}
	save := func(blob, content string) (int, string) {
		b, _ := json.Marshal(todoFileEdit{Path: "a.txt", Blob: blob, Content: content})
		w := send(rr.owner, "PUT", base, string(b))
		return w.Code, w.Body.String()
	}

	code, f := read("a.txt")
	if code != http.StatusOK || f.Content == nil || *f.Content != "one\ntwo\n" || f.Branch != "dash/todo-1" {
		t.Fatalf("read: %d %+v", code, f)
	}
	if code, _ := read("missing.txt"); code != http.StatusNotFound {
		t.Errorf("a file the branch hasn't got: %d", code)
	}
	if code, _ := read("../x"); code != http.StatusBadRequest {
		t.Errorf("a path outside the repository: %d", code)
	}

	// Saving commits on the branch and updates the worktree and the task's size.
	if code, body := save(f.Blob, "one\n2\nthree\n"); code != http.StatusOK {
		t.Fatalf("save: %d %s", code, body)
	}
	if got := rgit(t, rr.repo, "show", "dash/todo-1:a.txt"); got != "one\n2\nthree" {
		t.Errorf("branch has %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "a.txt")); string(b) != "one\n2\nthree\n" {
		t.Errorf("worktree has %q", b)
	}
	if st := rgit(t, wt, "status", "--porcelain"); st != "" {
		t.Errorf("worktree not clean after the edit: %q", st)
	}
	if msg := rgit(t, wt, "log", "-1", "--format=%s"); msg != "Edit a.txt in review" {
		t.Errorf("commit message %q", msg)
	}
	if got, _ := raw(rr.s).GetTodo(td.ID); got.DiffFiles != 1 || got.DiffAdd != 2 {
		t.Errorf("task's size: %d files +%d", got.DiffFiles, got.DiffAdd)
	}

	// The old blob is stale now: refused, nothing changes.
	if code, body := save(f.Blob, "lost\n"); code != http.StatusConflict || !strings.Contains(body, "since it was opened") {
		t.Errorf("stale save: %d %s", code, body)
	}

	// Uncommitted changes to the file in the worktree are kept, not overwritten.
	_, f = read("a.txt")
	writeRepoFile(t, wt, "a.txt", "by hand\n")
	if code, body := save(f.Blob, "x\n"); code != http.StatusConflict || !strings.Contains(body, "uncommitted") {
		t.Errorf("save over a dirty worktree: %d %s", code, body)
	}
	rgit(t, wt, "checkout", "--", "a.txt")

	// Only work waiting for a human can be edited.
	if _, err := raw(rr.s).UpdateTodo(td.ID, func(x *model.Todo) error { x.Status = model.StatusMerging; return nil }); err != nil {
		t.Fatal(err)
	}
	if code, _ := save(f.Blob, "x\n"); code != http.StatusBadRequest {
		t.Errorf("save while merging: %d", code)
	}
	if code, _ := read("a.txt"); code != http.StatusBadRequest {
		t.Errorf("read while merging: %d", code)
	}
}
