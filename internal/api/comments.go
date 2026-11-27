package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// Review comments: the reviewer's draft comments on lines of a task's diff
// (Changes), kept with the task until Rework sends them (rework with
// comments) or they are discarded.

const (
	maxComments    = 200
	maxCommentText = 20000
	maxCommentCode = 20000
)

// commentable reports whether t takes new review comments: it is in review,
// or failed, with changes Claude can rework.
func commentable(t model.Todo) bool {
	return t.Status == model.StatusReview || t.Status == model.StatusFailed
}

// cleanComment checks a new comment c and tidies it.
func cleanComment(c model.ReviewComment) (model.ReviewComment, error) {
	c.File, c.Text = strings.TrimSpace(c.File), strings.TrimSpace(c.Text)
	switch {
	case c.File == "":
		return c, fmt.Errorf("%w: a comment needs the file it is on", errBadRequest)
	case c.Text == "":
		return c, fmt.Errorf("%w: the comment is empty", errBadRequest)
	case len(c.Text) > maxCommentText || len(c.Code) > maxCommentCode:
		return c, fmt.Errorf("%w: the comment is too long", errBadRequest)
	case c.Line < 1 || (c.EndLine != 0 && c.EndLine < c.Line):
		return c, fmt.Errorf("%w: bad line numbers", errBadRequest)
	}
	if c.Side == "" {
		c.Side = "new"
	}
	if c.Side != "new" && c.Side != "old" {
		return c, fmt.Errorf("%w: side is new or old", errBadRequest)
	}
	if c.EndLine == c.Line {
		c.EndLine = 0
	}
	return c, nil
}

// addComment adds a draft review comment to task id.
func (sv *service) addComment(id string, c model.ReviewComment) (model.Todo, error) {
	c, err := cleanComment(c)
	if err != nil {
		return model.Todo{}, err
	}
	c.ID, c.CreatedAt = store.NewID(), time.Now().UTC()
	return sv.data.UpdateTodo(id, func(t *model.Todo) error {
		if !commentable(*t) {
			return fmt.Errorf("%w: comments go on a task in review", errBadRequest)
		}
		if len(t.Comments) >= maxComments {
			return fmt.Errorf("%w: a task takes at most %d comments; send these first", errBadRequest, maxComments)
		}
		t.Comments = append(t.Comments, c)
		return nil
	})
}

// editComment changes the text of task id's draft comment cid.
func (sv *service) editComment(id, cid, text string) (model.Todo, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return model.Todo{}, fmt.Errorf("%w: the comment is empty", errBadRequest)
	}
	if len(text) > maxCommentText {
		return model.Todo{}, fmt.Errorf("%w: the comment is too long", errBadRequest)
	}
	return sv.data.UpdateTodo(id, func(t *model.Todo) error {
		i := slices.IndexFunc(t.Comments, func(c model.ReviewComment) bool { return c.ID == cid })
		if i < 0 {
			return store.ErrNotFound
		}
		t.Comments[i].Text = text
		return nil
	})
}

// deleteComments deletes task id's draft comment cid, or all of them when
// cid is empty.
func (sv *service) deleteComments(id, cid string) (model.Todo, error) {
	return sv.data.UpdateTodo(id, func(t *model.Todo) error {
		if cid == "" {
			t.Comments = nil
			return nil
		}
		n := len(t.Comments)
		t.Comments = slices.DeleteFunc(t.Comments, func(c model.ReviewComment) bool { return c.ID == cid })
		if len(t.Comments) == n {
			return store.ErrNotFound
		}
		return nil
	})
}

// POST /api/todos/{tid}/comments: add a comment on lines of the task's diff.
func (s *Server) addComment(w http.ResponseWriter, r *http.Request) {
	var req model.ReviewComment
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	t, err := sv.addComment(r.PathValue("tid"), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// PATCH /api/todos/{tid}/comments/{cid}, body {"text": …}: edit a comment.
func (s *Server) editComment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	t, err := sv.editComment(r.PathValue("tid"), r.PathValue("cid"), req.Text)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// DELETE /api/todos/{tid}/comments/{cid} deletes a comment; DELETE
// /api/todos/{tid}/comments discards them all.
func (s *Server) deleteComments(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	t, err := sv.deleteComments(r.PathValue("tid"), r.PathValue("cid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}
