package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/store"
)

// Review comments: the reviewer's draft comments on lines of a task's diff
// (Changes), kept with the task until Rework sends them (rework with
// comments) or they are discarded. On a task with an open pull request
// followed here, each is also posted there as it is added, and edited and
// deleted there with it (queue.MergeQueue.PostComment); that never holds up
// the comment.

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
	c.ID, c.CreatedAt, c.Author, c.GitHub = store.NewID(), time.Now().UTC(), sv.authorName(), nil
	post := false
	t, err := sv.data.UpdateTodo(id, func(t *model.Todo) error {
		if !commentable(*t) {
			return fmt.Errorf("%w: comments go on a task in review", errBadRequest)
		}
		if len(t.Comments) >= maxComments {
			return fmt.Errorf("%w: a task takes at most %d comments; send these first", errBadRequest, maxComments)
		}
		c := c
		if post = sv.postsComments(*t); post {
			c.GitHub = &model.PostedComment{PR: t.PR.Number, Since: c.CreatedAt} // being posted
		}
		t.Comments = append(t.Comments, c)
		return nil
	})
	if err == nil && post {
		go func() { _ = sv.s.Merges.PostComment(context.Background(), id, c.ID) }()
	}
	return t, err
}

// postsComments reports whether comments on task t go to its pull request
// on GitHub too: it has an open one, which this Dash follows.
func (sv *service) postsComments(t model.Todo) bool {
	return sv.s.Merges != nil && !sv.s.hub() && queue.PostsComments(t)
}

// authorName is the caller's name, as a comment posted on GitHub names
// them; "" when this Dash doesn't know it (a request from the hub).
func (sv *service) authorName() string {
	if sv.c.User == 0 || sv.s.Store == nil {
		return ""
	}
	u, err := sv.s.Store.Accounts().User(sv.c.User)
	if err != nil {
		return ""
	}
	return u.Username
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
	posted := false
	t, err := sv.data.UpdateTodo(id, func(t *model.Todo) error {
		i := slices.IndexFunc(t.Comments, func(c model.ReviewComment) bool { return c.ID == cid })
		if i < 0 {
			return store.ErrNotFound
		}
		t.Comments[i].Text = text
		posted = t.Comments[i].GitHub.Posted()
		return nil
	})
	if err == nil && posted && sv.s.Merges != nil {
		go func() { _ = sv.s.Merges.UpdateComment(context.Background(), id, cid) }()
	}
	return t, err
}

// retryComment posts task id's comment cid on its pull request again, or
// its text, after that failed: it waits for GitHub's answer.
func (sv *service) retryComment(ctx context.Context, id, cid string) (model.Todo, error) {
	t, err := sv.data.GetTodo(id)
	if err != nil {
		return t, err
	}
	i := slices.IndexFunc(t.Comments, func(c model.ReviewComment) bool { return c.ID == cid })
	switch {
	case i < 0:
		return t, store.ErrNotFound
	case !sv.postsComments(t):
		return t, fmt.Errorf("%w: the task has no open pull request on GitHub to post it on", errBadRequest)
	case t.Comments[i].GitHub.Posted():
		err = sv.s.Merges.UpdateComment(ctx, id, cid)
	default:
		err = sv.s.Merges.PostComment(ctx, id, cid)
	}
	if err != nil {
		// The comment keeps why, shown with a retry (todo.updated).
		return t, fmt.Errorf("couldn't post it on GitHub: %w", err)
	}
	return sv.data.GetTodo(id)
}

// deleteComments deletes task id's draft comment cid, or all of them when
// cid is empty.
func (sv *service) deleteComments(id, cid string) (model.Todo, error) {
	var gone []model.ReviewComment
	t, err := sv.data.UpdateTodo(id, func(t *model.Todo) error {
		gone = nil
		if cid == "" {
			gone, t.Comments = t.Comments, nil
			return nil
		}
		for _, c := range t.Comments {
			if c.ID == cid {
				gone = append(gone, c)
			}
		}
		if len(gone) == 0 {
			return store.ErrNotFound
		}
		t.Comments = slices.DeleteFunc(t.Comments, func(c model.ReviewComment) bool { return c.ID == cid })
		return nil
	})
	if err == nil && sv.s.Merges != nil {
		// Deleted on GitHub too; one still being posted is once it is.
		for _, c := range gone {
			if c.GitHub != nil {
				go sv.s.Merges.DeleteComment(context.Background(), t, c)
			}
		}
	}
	return t, err
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

// POST /api/todos/{tid}/comments/{cid}/github posts a comment on the
// task's pull request again after that failed.
func (s *Server) retryComment(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	t, err := sv.retryComment(r.Context(), r.PathValue("tid"), r.PathValue("cid"))
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
