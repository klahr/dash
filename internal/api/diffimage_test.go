package api

import (
	"net/http"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/klahr/dash/internal/model"
)

func TestCommitImage(t *testing.T) {
	rr := newReleaseRepo(t, "")
	writeRepoFile(t, rr.repo, "pic.png", "\x89PNG\x00old")
	rgit(t, rr.repo, "add", "-A")
	rgit(t, rr.repo, "commit", "-q", "-m", "Add")
	writeRepoFile(t, rr.repo, "pic.png", "\x89PNG\x00new")
	writeRepoFile(t, rr.repo, "a.bin", "\x00")
	rgit(t, rr.repo, "add", "-A")
	rgit(t, rr.repo, "commit", "-q", "-m", "Change")
	sha := rgit(t, rr.repo, "rev-parse", "HEAD")

	get := func(q string) (int, string, http.Header) {
		w := rr.call(t, "GET", "/commits/"+sha+"/image?"+q, "")
		return w.Code, w.Body.String(), w.Header()
	}
	code, body, hdr := get("side=new&path=pic.png")
	if code != http.StatusOK || body != "\x89PNG\x00new" || hdr.Get("Content-Type") != "image/png" || hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("new: %d %q %v", code, body, hdr)
	}
	if code, body, _ := get("side=old&path=pic.png"); code != http.StatusOK || body != "\x89PNG\x00old" {
		t.Errorf("old: %d %q", code, body)
	}
	// Not there before: the file was added.
	if code, _, _ := get("side=old&path=a.png"); code != http.StatusNotFound {
		t.Errorf("missing: %d", code)
	}
	for _, bad := range []string{"side=new&path=a.bin", "side=new&path=../x.png", "side=new&path=-x.png", "side=mid&path=pic.png", "side=new&path="} {
		if code, body, _ := get(bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", bad, code, body)
		}
	}
}

func TestTodoImage(t *testing.T) {
	rr := newReleaseRepo(t, "")
	writeRepoFile(t, rr.repo, "pic.gif", "GIF\x00old")
	rgit(t, rr.repo, "add", "-A")
	rgit(t, rr.repo, "commit", "-q", "-m", "a")
	wt := filepath.Join(t.TempDir(), "wt")
	rgit(t, rr.repo, "worktree", "add", "-q", "-b", "dash/todo-1", wt, "main")
	writeRepoFile(t, wt, "pic.gif", "GIF\x00new")
	rgit(t, wt, "commit", "-q", "-am", "Claude's work")
	// main moves on meanwhile: the old side is still where the task started.
	writeRepoFile(t, rr.repo, "pic.gif", "GIF\x00main")
	rgit(t, rr.repo, "commit", "-q", "-am", "Elsewhere")
	td, err := raw(rr.s).CreateTodo(model.Todo{ProjectID: rr.p.ID, Title: "t", Prompt: "p",
		Worktree: &model.Worktree{Path: wt, Branch: "dash/todo-1", BaseBranch: "main"}})
	if err != nil {
		t.Fatal(err)
	}
	get := func(side string) (int, string) {
		w := send(rr.owner, "GET", "/api/todos/"+td.ID+"/image?side="+side+"&path="+url.QueryEscape("pic.gif"), "")
		return w.Code, w.Body.String()
	}
	if code, body := get("new"); code != http.StatusOK || body != "GIF\x00new" {
		t.Errorf("new: %d %q", code, body)
	}
	if code, body := get("old"); code != http.StatusOK || body != "GIF\x00old" {
		t.Errorf("old: %d %q", code, body)
	}
}
