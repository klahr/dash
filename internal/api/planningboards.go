package api

import (
	"net/http"

	"github.com/klahr/dash/internal/model"
)

// Planning boards: an org's planning cards are on its boards (one per
// product or team, say), managed on the Planning page by anyone in the org,
// planners too (docs/planning.md). Every org has a default board, which
// can't be deleted; deleting another moves its cards to it.

// boardView is a board as the API answers it, with how many cards it has.
type boardView struct {
	model.CardBoard
	Cards int `json:"cards"`
}

// planningBoards handles GET /api/planning/boards: the org's boards, in
// their order.
func (s *Server) planningBoards(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	bs, err := sv.data.PlanningBoards()
	if err != nil {
		writeErr(w, err)
		return
	}
	counts, err := sv.data.PlanningBoardCounts()
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]boardView, len(bs))
	for i, b := range bs {
		out[i] = boardView{b, counts[b.ID]}
	}
	writeJSON(w, http.StatusOK, out)
}

// createPlanningBoard handles POST /api/planning/boards: {name}. The new
// board comes after the others.
func (s *Server) createPlanningBoard(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeStrict(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := sv.writable(); err != nil {
		writeErr(w, err)
		return
	}
	b, err := sv.data.CreatePlanningBoard(req.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, boardView{CardBoard: b})
}

// updatePlanningBoard handles PATCH /api/planning/boards/{bid}: name
// renames it, position puts it there among the boards (0 is the first).
func (s *Server) updatePlanningBoard(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req struct {
		Name     *string `json:"name"`
		Position *int    `json:"position"`
	}
	if err := decodeStrict(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := sv.writable(); err != nil {
		writeErr(w, err)
		return
	}
	b, err := sv.data.PlanningBoard(r.PathValue("bid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if req.Name != nil {
		if b, err = sv.data.RenamePlanningBoard(b.ID, *req.Name); err != nil {
			writeErr(w, err)
			return
		}
	}
	if req.Position != nil {
		if b, err = sv.data.MovePlanningBoard(b.ID, *req.Position); err != nil {
			writeErr(w, err)
			return
		}
	}
	counts, err := sv.data.PlanningBoardCounts()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, boardView{b, counts[b.ID]})
}

// deletePlanningBoard handles DELETE /api/planning/boards/{bid}: its cards
// move to the default board, which can't be deleted.
func (s *Server) deletePlanningBoard(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := sv.writable(); err != nil {
		writeErr(w, err)
		return
	}
	b, err := sv.data.PlanningBoard(r.PathValue("bid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := sv.data.DeletePlanningBoard(b.ID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
