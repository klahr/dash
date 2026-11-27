package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/klahr/dash/internal/model"
)

// sinceReview is a task's Changes back in Review after a rework: what
// changed since the user's last review (model.Todo.Reviews), or the full
// diff when that can't be shown on its own.
type sinceReview struct {
	// Round is the review this is: 2 for the first one after a rework.
	Round int `json:"round"`
	// Since is the reviewed commit Diff starts at; empty when Diff is the
	// full diff against the base branch, and Note then says why (empty
	// too: a first review).
	Since string `json:"since,omitempty"`
	Note  string `json:"note,omitempty"`
	Diff  string `json:"diff"`
}

// todoDiffSince handles GET /api/todos/{tid}/diff/since.
func (s *Server) todoDiffSince(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	res, err := sv.diffSince(r.Context(), r.PathValue("tid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// hubDiffSince handles GET /api/hub/tasks/{htid}/diff/since: a runner's
// diffSince of the hub's task, from the reviews the runner recorded.
func (s *Server) hubDiffSince(w http.ResponseWriter, r *http.Request) {
	sv, t, ok := s.hubTaskFor(w, r)
	if !ok {
		return
	}
	if t.Worktree == nil {
		writeJSON(w, http.StatusOK, sinceReview{Round: len(t.Reviews) + 1})
		return
	}
	res, err := sv.diffSince(r.Context(), t.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// diffSince is task id's patch since its last review (sinceReview). A task
// on a runner asks the runner, which has the branch and its own record of
// the reviewed commits; the round is the one counted here.
func (sv *service) diffSince(ctx context.Context, id string) (sinceReview, error) {
	t, err := sv.data.GetTodo(id)
	if err != nil {
		return sinceReview{}, err
	}
	res := sinceReview{Round: len(t.Reviews) + 1}
	full := func(note string) (sinceReview, error) {
		res.Since, res.Note = "", note
		res.Diff, err = sv.diff(ctx, id)
		return res, err
	}
	if t.Status != model.StatusReview || len(t.Reviews) == 0 {
		return full("")
	}
	if t.OnRunner() {
		b, err := sv.fetchFromRunner(ctx, t, "diff/since")
		var got sinceReview
		if err == nil {
			err = json.Unmarshal(b, &got)
		}
		if err != nil {
			slog.Info("diff since the last review from the runner", "todo", t.ID, "err", err)
			return full("The task's runner can't tell what changed since your last review (update Dash there).")
		}
		got.Round = res.Round
		return got, nil
	}
	if t.Worktree == nil {
		return sinceReview{}, fmt.Errorf("%w: task has no worktree yet", errBadRequest)
	}
	last := t.Reviews[len(t.Reviews)-1]
	if last.Commit == "" {
		return full("Dash has no record of the commit you last reviewed.")
	}
	p, err := sv.data.GetProject(t.ProjectID)
	if err != nil {
		return sinceReview{}, err
	}
	patch, moved, err := sv.s.Git.DiffSince(ctx, p.ID, p.Path, p.For(t).Base(), t.Worktree.Branch, last.Commit, last.Base)
	if err != nil {
		return sinceReview{}, err
	}
	if moved != "" {
		return full(moved)
	}
	res.Since, res.Diff = last.Commit, patch
	return res, nil
}
