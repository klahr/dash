package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// Draft comments on a task's diff are stored with the task (so they outlive
// a closed pane or a reload), can be edited and deleted, and Rework with
// comments sends them all in one note and clears them.
func TestReviewCommentsStoredAndSentAsRework(t *testing.T) {
	s, _, p := boardServer(t)
	h := loggedIn(t, s.Routes())
	td, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "t", Prompt: "p", Status: model.StatusReview, Worktree: &model.Worktree{Branch: "dash/t"}})
	if err != nil {
		t.Fatal(err)
	}
	setStatus := func(st ...model.TodoStatus) {
		t.Helper()
		for _, to := range st {
			if _, err := raw(s).UpdateTodo(td.ID, func(x *model.Todo) error { x.Status = to; return nil }); err != nil {
				t.Fatalf("to %s: %v", to, err)
			}
		}
	}
	if d, _ := raw(s).GetTodo(td.ID); d.Status != model.StatusReview {
		setStatus(model.StatusQueued, model.StatusInProgress, model.StatusReview)
	}
	base := "/api/todos/" + td.ID + "/comments"
	add := func(body string) model.Todo {
		t.Helper()
		w := do(t, h, "POST", base, body)
		if w.Code != http.StatusOK {
			t.Fatalf("add %s: %d %s", body, w.Code, w.Body)
		}
		var got model.Todo
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	add(`{"file":"main.go","side":"new","line":3,"end_line":5,"code":" a\n-b\n+c","text":"Why this?"}`)
	got := add(`{"file":"web/app.js","line":12,"end_line":12,"code":"+x = 1","text":"first draft"}`)
	gone := add(`{"file":"gone.go","line":1,"text":"never mind"}`)
	if len(gone.Comments) != 3 {
		t.Fatalf("comments: %+v", gone.Comments)
	}
	c := got.Comments[1]
	if c.ID == "" || c.Side != "new" || c.EndLine != 0 || c.CreatedAt.IsZero() {
		t.Errorf("a one-line comment: %+v", c)
	}

	for _, bad := range []string{`{"file":"a.go","line":1,"text":"  "}`, `{"line":1,"text":"x"}`, `{"file":"a.go","line":0,"text":"x"}`,
		`{"file":"a.go","line":5,"end_line":2,"text":"x"}`, `{"file":"a.go","side":"left","line":1,"text":"x"}`} {
		if w := do(t, h, "POST", base, bad); w.Code != http.StatusBadRequest {
			t.Errorf("add %s: want 400, got %d", bad, w.Code)
		}
	}

	if w := do(t, h, "PATCH", base+"/"+c.ID, `{"text":"Make this a constant."}`); w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "DELETE", base+"/"+gone.Comments[2].ID, ""); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "DELETE", base+"/nope", ""); w.Code != http.StatusNotFound {
		t.Errorf("delete a missing comment: %d", w.Code)
	}

	// Stored: the task as read again has them.
	stored, _ := raw(s).GetTodo(td.ID)
	if len(stored.Comments) != 2 || stored.Comments[1].Text != "Make this a constant." {
		t.Fatalf("stored: %+v", stored.Comments)
	}

	if w := do(t, h, "POST", "/api/todos/"+td.ID+"/rework", `{"feedback":"Run the tests too.","comments":true}`); w.Code != http.StatusOK {
		t.Fatalf("rework: %d %s", w.Code, w.Body)
	}
	sent, _ := raw(s).GetTodo(td.ID)
	if len(sent.Comments) != 0 || sent.Status != model.StatusQueued || len(sent.Feedback) != 1 {
		t.Fatalf("after rework: comments %v, status %s, feedback %q", sent.Comments, sent.Status, sent.Feedback)
	}
	note := sent.Feedback[0]
	for _, want := range []string{"### 1. main.go, lines 3–5", "```diff\n a\n-b\n+c\n```", "Why this?",
		"### 2. web/app.js, line 12", "Make this a constant.", "### Also\n\nRun the tests too."} {
		if !strings.Contains(note, want) {
			t.Errorf("the rework note lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "never mind") {
		t.Errorf("a deleted comment was sent:\n%s", note)
	}

	// Not in review: no new comments, and a second send has nothing to send.
	if w := do(t, h, "POST", base, `{"file":"a.go","line":1,"text":"late"}`); w.Code != http.StatusBadRequest {
		t.Errorf("comment on a queued task: %d", w.Code)
	}
	setStatus(model.StatusInProgress, model.StatusReview)
	if w := do(t, h, "POST", "/api/todos/"+td.ID+"/rework", `{"comments":true}`); w.Code != http.StatusBadRequest {
		t.Errorf("rework with no comments: %d %s", w.Code, w.Body)
	}
	// Discard all.
	add(`{"file":"a.go","line":1,"text":"one"}`)
	add(`{"file":"a.go","line":2,"text":"two"}`)
	if w := do(t, h, "DELETE", base, ""); w.Code != http.StatusOK {
		t.Fatalf("discard: %d", w.Code)
	}
	if d, _ := raw(s).GetTodo(td.ID); len(d.Comments) != 0 {
		t.Errorf("after discard: %+v", d.Comments)
	}
}
