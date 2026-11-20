package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/queue"
)

// Close closes a task in review that changed no files, with no commit; one
// with changes is refused (it is approved). Approving a task without
// changes, as older clients do, closes it the same way.
func TestCloseTaskWithoutChanges(t *testing.T) {
	s, _, _ := boardServer(t)
	s.Git = gitops.New(s.Hub)
	s.Merges = queue.NewMergeQueue(raw(s), s.Git, s.Hub, &queue.TodoLog{Pub: s.Hub, Root: t.TempDir()}, 4)
	p, err := raw(s).CreateProject(model.Project{Name: "p", OrgID: testOwner(t, s).Org, Path: t.TempDir(), DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	inReview := func(noChanges bool) model.Todo {
		td := reviewTask(t, s, p, nil)
		td, err := raw(s).UpdateTodo(td.ID, func(x *model.Todo) error {
			x.Worktree, x.NoChanges = nil, noChanges // removed already: nothing to diff
			if !noChanges {
				x.DiffFiles = 2
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return td
	}

	changed := inReview(false)
	if w := ownerDo(t, s, "POST", "/api/todos/"+changed.ID+"/close"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "approve it") {
		t.Fatalf("close with changes: %d %s", w.Code, w.Body)
	}

	for _, verb := range []string{"close", "approve"} {
		td := inReview(true)
		w := ownerDo(t, s, "POST", "/api/todos/"+td.ID+"/"+verb)
		if w.Code >= 300 {
			t.Fatalf("%s: %d %s", verb, w.Code, w.Body)
		}
		x, _ := raw(s).GetTodo(td.ID)
		if x.Status != model.StatusMerged || !x.NoChanges || x.MergedCommit != "" {
			t.Fatalf("%s: want closed with no changes, got status=%s no_changes=%v commit=%q", verb, x.Status, x.NoChanges, x.MergedCommit)
		}
		if w := ownerDo(t, s, "POST", "/api/todos/"+td.ID+"/revert"); w.Code != http.StatusBadRequest {
			t.Fatalf("revert of a closed task: %d %s", w.Code, w.Body)
		}
	}
}

// The project's "Skip Review for tasks that change no files" is saved like its other settings.
func TestProjectCloseNoChangesSetting(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	s, _, _ := boardServer(t)
	s.Git = gitops.New(s.Hub)
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	p, err := raw(s).CreateProject(model.Project{Name: "p", OrgID: testOwner(t, s).Org, Path: repo, DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if p.CloseNoChanges {
		t.Fatal("want it off by default")
	}
	w := httptest.NewRecorder()
	s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest("PATCH", "/api/projects/"+p.ID, strings.NewReader(`{"close_no_changes":true}`))))
	if w.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", w.Code, w.Body)
	}
	var got model.Project
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || !got.CloseNoChanges {
		t.Fatalf("want close_no_changes on in the answer, got %s (%v)", w.Body, err)
	}
	if x, _ := raw(s).GetProject(p.ID); !x.CloseNoChanges {
		t.Fatal("want it saved")
	}
}
