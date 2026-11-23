package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/klahr/dash/internal/store"
)

// The projects the caller starred in their active org: the sidebar lists
// them first and folds the rest away. Stars follow the person, not the
// browser.
//
//	GET    /api/me/starred-projects        {"projects": [key, …]}, in the order starred
//	PUT    /api/me/starred-projects/{key}  star a project
//	DELETE /api/me/starred-projects/{key}  unstar it
//
// A key is a project's id, or "<runner>/<id>" for a project only a runner
// has (escape the slash as %2F). Starring one at a time means two
// browsers starring at once don't undo each other.
//
// They need a person: a signed-in session or one of their API tokens.

func (s *Server) listStarredProjects(w http.ResponseWriter, r *http.Request) {
	sv := s.personCaller(w, r)
	if sv == nil {
		return
	}
	keys, err := sv.data.StarredProjects()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": keys})
}

func (s *Server) starProject(w http.ResponseWriter, r *http.Request) {
	s.setStar(w, r, true)
}

func (s *Server) unstarProject(w http.ResponseWriter, r *http.Request) {
	s.setStar(w, r, false)
}

func (s *Server) setStar(w http.ResponseWriter, r *http.Request, on bool) {
	sv := s.personCaller(w, r)
	if sv == nil {
		return
	}
	key := r.PathValue("key")
	var err error
	if on {
		err = sv.data.StarProject(key, time.Now())
	} else {
		err = sv.data.UnstarProject(key)
	}
	if errors.Is(err, store.ErrBadStarKey) || errors.Is(err, store.ErrTooManyStars) {
		err = fmt.Errorf("%w: %w", errBadRequest, err)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
