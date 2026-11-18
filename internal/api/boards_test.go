package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// A project's boards are added, renamed, moved and deleted over the API;
// tasks go on one by id or name, move with PATCH, and fall back to Main
// when their board is deleted.
func TestProjectBoards(t *testing.T) {
	parallel(t)
	s, _, p := boardServer(t)
	h := loggedIn(t, s.Routes())
	base := "/api/projects/" + p.ID + "/boards"

	var added struct {
		model.Project
		Board model.Board `json:"board"`
	}
	w := do(t, h, "POST", base, `{"name":"Web"}`)
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &added) != nil || added.Board.Name != "Web" || len(added.Boards) != 1 {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	web := added.Board.ID
	if w := do(t, h, "POST", base, `{"name":"main"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a board named main: %d %s", w.Code, w.Body)
	}
	_ = do(t, h, "POST", base, `{"name":"Ops"}`)
	var proj model.Project
	w = do(t, h, "PATCH", base+"/"+web, `{"name":"Website","position":1}`)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &proj) != nil || len(proj.Boards) != 2 || proj.Boards[1] != (model.Board{ID: web, Name: "Website"}) {
		t.Fatalf("rename and move: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "PATCH", base+"/nope", `{"name":"X"}`); w.Code != http.StatusNotFound {
		t.Errorf("rename a board that isn't there: %d", w.Code)
	}

	// By name, letter case aside; an unknown one is refused.
	var td model.Todo
	w = do(t, h, "POST", "/api/projects/"+p.ID+"/todos", `{"title":"x","board":"website"}`)
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &td) != nil || td.Board != web {
		t.Fatalf("create on a board: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "POST", "/api/projects/"+p.ID+"/todos", `{"title":"x","board":"nope"}`); w.Code != http.StatusBadRequest {
		t.Errorf("create on a board that isn't there: %d %s", w.Code, w.Body)
	}
	w = do(t, h, "POST", "/api/todos/"+td.ID+"/duplicate", ``)
	var dup model.Todo
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &dup) != nil || dup.Board != web {
		t.Errorf("a duplicate stays on its board: %d %s", w.Code, w.Body)
	}
	w = do(t, h, "PATCH", "/api/todos/"+dup.ID, `{"board":"Main"}`)
	var moved model.Todo
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &moved) != nil || moved.Board != "" {
		t.Errorf("move to Main: %d %s", w.Code, w.Body)
	}

	var gone struct {
		model.Project
		Moved int `json:"moved"`
	}
	w = do(t, h, "DELETE", base+"/"+web, ``)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &gone) != nil || gone.Moved != 1 || len(gone.Boards) != 1 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if got, _ := raw(s).GetTodo(td.ID); got.Board != "" {
		t.Errorf("the deleted board's task is on %q", got.Board)
	}
}
