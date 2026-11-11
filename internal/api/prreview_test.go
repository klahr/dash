package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/testbin"
)

// prReviewServer is a server with a project in pull request mode, a gh that
// logs its calls (returned) and answers nothing else, and a task in review
// whose pull request #7 is open on GitHub.
func prReviewServer(t *testing.T) (*Server, model.Todo, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	s, _, _ := boardServer(t)
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	testbin.Write(t, filepath.Join(dir, "gh"), "#!/bin/sh\necho \"$*\" >> "+calls+"\n")
	s.Git = gitops.New(s.Hub)
	s.Git.GhBin = filepath.Join(dir, "gh")
	s.Merges = queue.NewMergeQueue(raw(s), s.Git, s.Hub, &queue.TodoLog{Root: t.TempDir()}, 4)
	p, err := raw(s).CreateProject(model.Project{Name: "gh", OrgID: testOwner(t, s).Org, Path: repo, DefaultBranch: "main",
		MergeMode: model.MergePR, MergeModeSet: true})
	if err != nil {
		t.Fatal(err)
	}
	td := reviewTask(t, s, p, &model.PullRequest{Number: 7, URL: "https://github.com/o/r/pull/7", State: "OPEN"})
	return s, td, calls
}

// reviewTask is a task of p in review, with pull request pr (or none).
func reviewTask(t *testing.T, s *Server, p model.Project, pr *model.PullRequest) model.Todo {
	t.Helper()
	td, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "t", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []model.TodoStatus{model.StatusQueued, model.StatusInProgress, model.StatusReview} {
		if _, err := raw(s).SetStatus(td.ID, st, ""); err != nil {
			t.Fatal(err)
		}
	}
	td, err = raw(s).UpdateTodo(td.ID, func(x *model.Todo) error {
		name := "todo-" + x.ID
		x.Worktree = &model.Worktree{Name: name, Branch: "dash/" + name, Path: filepath.Join(t.TempDir(), name)}
		x.PR = pr
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return td
}

func ownerDo(t *testing.T, s *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest(method, path, nil)))
	return w
}

// waitCalls waits for gh to have been called with want.
func waitCalls(t *testing.T, calls, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, _ := os.ReadFile(calls)
		if strings.Contains(string(b), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("gh never called with %q; calls:\n%s", want, b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A task whose pull request is open on GitHub is reviewed there: approving
// it in Dash is refused. One without a pull request (in review from before
// pull requests opened on finishing) is still approved, which opens one.
func TestApproveRefusedWhileReviewedOnGitHub(t *testing.T) {
	s, td, _ := prReviewServer(t)
	w := ownerDo(t, s, "POST", "/api/todos/"+td.ID+"/approve")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "review it there") {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	if x, _ := raw(s).GetTodo(td.ID); x.Status != model.StatusReview {
		t.Fatalf("status %s", x.Status)
	}

	// Opening or pushing it failed (the task says why): approving retries.
	if _, err := raw(s).UpdateTodo(td.ID, func(x *model.Todo) error { x.Error = "Couldn't open the pull request: boom"; return nil }); err != nil {
		t.Fatal(err)
	}
	if w := ownerDo(t, s, "POST", "/api/todos/"+td.ID+"/approve"); w.Code != http.StatusAccepted {
		t.Fatalf("approve to retry: %d %s", w.Code, w.Body)
	}

	p, _ := raw(s).GetProject(td.ProjectID)
	old := reviewTask(t, s, p, nil)
	if w := ownerDo(t, s, "POST", "/api/todos/"+old.ID+"/approve"); w.Code != http.StatusAccepted {
		t.Fatalf("approve without a pull request: %d %s", w.Code, w.Body)
	}
	if x, _ := raw(s).GetTodo(old.ID); x.Status != model.StatusMerging {
		t.Fatalf("status %s, want merging (to open its pull request)", x.Status)
	}
}

// Rejecting the task in Dash, by deleting it or moving it back to drafts,
// closes its pull request on GitHub.
func TestRejectingTaskInDashClosesPullRequest(t *testing.T) {
	s, td, calls := prReviewServer(t)
	if w := ownerDo(t, s, "DELETE", "/api/todos/"+td.ID); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	waitCalls(t, calls, "pr close 7")

	p, _ := raw(s).GetProject(td.ProjectID)
	back := reviewTask(t, s, p, &model.PullRequest{Number: 8, URL: "https://github.com/o/r/pull/8", State: "OPEN"})
	if w := ownerDo(t, s, "POST", "/api/todos/"+back.ID+"/cancel"); w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	waitCalls(t, calls, "pr close 8")
	deadline := time.Now().Add(10 * time.Second)
	for x, _ := raw(s).GetTodo(back.ID); x.Status != model.StatusDraft || x.PR.State != "CLOSED"; x, _ = raw(s).GetTodo(back.ID) {
		if time.Now().After(deadline) {
			t.Fatalf("want a draft with its pull request closed, got %s %+v", x.Status, x.PR)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
