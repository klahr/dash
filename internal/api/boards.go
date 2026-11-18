package api

import (
	"net/http"

	"github.com/klahr/dash/internal/model"
)

// A project's boards besides its main one (see docs/boards.md). Each call
// answers the project, with its boards.

// addBoard adds a board to a project: POST /api/projects/{pid}/boards {name}.
func (s *Server) addBoard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, b, err := sv.data.AddBoard(r.PathValue("pid"), req.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		model.Project
		Board model.Board `json:"board"`
	}{p, b})
}

// updateBoard renames a board or moves it among the project's boards:
// PATCH /api/projects/{pid}/boards/{bid} {name, position}.
func (s *Server) updateBoard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     *string `json:"name"`
		Position *int    `json:"position"` // 0: first after the main board
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	pid, bid := r.PathValue("pid"), r.PathValue("bid")
	p, err := sv.data.GetProject(pid)
	if err == nil && req.Name != nil {
		p, err = sv.data.RenameBoard(pid, bid, *req.Name)
	}
	if err == nil && req.Position != nil {
		p, err = sv.data.MoveBoard(pid, bid, *req.Position)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// deleteBoard removes a board; its tasks move to the main board:
// DELETE /api/projects/{pid}/boards/{bid}.
func (s *Server) deleteBoard(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	p, moved, err := sv.data.DeleteBoard(r.PathValue("pid"), r.PathValue("bid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		model.Project
		Moved int `json:"moved"` // tasks moved to the main board
	}{p, len(moved)})
}
