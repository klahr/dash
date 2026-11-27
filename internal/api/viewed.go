package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/model"
)

// Viewed files: a reviewer marks each file of a task's diff (Changes) as
// viewed, and the pane and the task's card show how far their review has
// got. Each mark keeps the hash of the file's part of the diff it was made
// at, and counts only while the file still hashes so: a rework that changes
// the file clears it, one that doesn't keeps it (model.Todo.SetReviewFiles).

// patchFiles splits a patch (git diff output) into its files, as the web
// app's parseDiff does: each one's path (the "a/" side), lines added and
// removed, and a hash of its part of the patch.
func patchFiles(patch string) []model.DiffFile {
	var files []model.DiffFile
	var cur *model.DiffFile
	var part strings.Builder
	done := func() {
		if cur != nil {
			sum := sha256.Sum256([]byte(part.String()))
			cur.Hash = hex.EncodeToString(sum[:8])
			files = append(files, *cur)
		}
		part.Reset()
	}
	for _, line := range strings.Split(patch, "\n") {
		if rest, ok := strings.CutPrefix(line, "diff --git a/"); ok {
			done()
			path := rest
			if i := strings.LastIndex(rest, " b/"); i >= 0 {
				path = rest[:i]
			}
			cur = &model.DiffFile{Path: path}
		}
		if cur == nil {
			continue
		}
		part.WriteString(line)
		part.WriteByte('\n')
		if strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ") {
			continue
		}
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			cur.Lines++
		}
	}
	done()
	return files
}

// errSame tells UpdateTodo to keep a task that wouldn't change.
var errSame = errors.New("unchanged")

// reviewed reports whether task t takes viewed marks, and has its review
// files kept: it is in review, or failed with changes to rework.
func reviewed(t model.Todo) bool { return commentable(t) && t.Worktree != nil }

// setReviewFiles records patch as task id's diff (at its current version)
// and drops the viewed marks it outdates. A task that wouldn't change isn't
// written.
func (sv *service) setReviewFiles(id, patch string) (model.Todo, error) {
	files := patchFiles(patch)
	t, err := sv.data.UpdateTodo(id, func(t *model.Todo) error {
		if !t.SetReviewFiles(files, model.ReviewKeyOf(*t)) {
			return errSame
		}
		return nil
	})
	if errors.Is(err, errSame) {
		err = nil
	}
	return t, err
}

// setViewed marks file of task id viewed (or not) by the caller, at the
// file's part of the task's diff as it is now.
func (sv *service) setViewed(ctx context.Context, id, file string, viewed bool) (model.Todo, error) {
	t, err := sv.data.GetTodo(id)
	if err != nil {
		return t, err
	}
	if !reviewed(t) {
		return t, fmt.Errorf("%w: files are marked viewed on a task in review", errBadRequest)
	}
	patch, err := sv.diff(ctx, id)
	if err != nil {
		return t, err
	}
	if t, err = sv.setReviewFiles(id, patch); err != nil {
		return t, err
	}
	user := sv.c.author()
	return sv.data.UpdateTodo(id, func(t *model.Todo) error {
		hash := ""
		for _, f := range t.ReviewFiles {
			if f.Path == file {
				hash = f.Hash
			}
		}
		if hash == "" {
			return fmt.Errorf("%w: %s isn't in the task's changes", errBadRequest, file)
		}
		if viewed && len(t.Viewed) >= model.MaxViewed {
			return fmt.Errorf("%w: too many files marked viewed", errBadRequest)
		}
		t.SetViewed(user, file, hash, viewed)
		return nil
	})
}

// PUT /api/todos/{tid}/viewed, body {"file": …, "viewed": true|false}:
// mark a file of the task's diff viewed by the caller, or not.
func (s *Server) setViewed(w http.ResponseWriter, r *http.Request) {
	var req struct {
		File   string `json:"file"`
		Viewed bool   `json:"viewed"`
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.File == "" {
		writeErr(w, fmt.Errorf("%w: which file?", errBadRequest))
		return
	}
	t, err := sv.setViewed(r.Context(), r.PathValue("tid"), req.File, req.Viewed)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// reviewSync brings the review files of tasks in review up to date after
// their diff changed (a rework, an edit), so the marks a change outdates
// clear without anyone opening the task.
type reviewSync struct {
	once    sync.Once
	mu      sync.Mutex
	pending map[string]bool
	kick    chan struct{}
}

func (s *Server) viewedSync() *reviewSync {
	rs := &s.reviewSync
	rs.once.Do(func() {
		rs.pending = map[string]bool{}
		rs.kick = make(chan struct{}, 1)
	})
	return rs
}

// ObserveReviews is fed every store event; it never blocks. A task in
// review whose diff has moved on since its review files were read is
// queued for StartReviews to read again: its outdated marks clear, and
// progress counts its whole diff even where Changes shows only what
// changed since the last review.
func (s *Server) ObserveReviews(ev model.Event) {
	if ev.Type != model.EvTodoUpdated {
		return
	}
	t, ok := ev.Data.(model.Todo)
	if !ok || !reviewed(t) || t.ReviewKey == model.ReviewKeyOf(t) {
		return
	}
	rs := s.viewedSync()
	rs.mu.Lock()
	rs.pending[t.ID] = true
	rs.mu.Unlock()
	select {
	case rs.kick <- struct{}{}:
	default:
	}
}

// StartReviews reads the queued tasks' diffs again until ctx ends.
func (s *Server) StartReviews(ctx context.Context) {
	rs := s.viewedSync()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-rs.kick:
			}
			rs.mu.Lock()
			ids := rs.pending
			rs.pending = map[string]bool{}
			rs.mu.Unlock()
			for id := range ids {
				if err := s.syncReview(ctx, id); err != nil {
					slog.Debug("review files", "todo", id, "err", err)
				}
			}
		}
	}()
}

// syncReview reads task id's diff again and records it as its review files.
func (s *Server) syncReview(ctx context.Context, id string) error {
	org, ok := s.Store.TodoOrg(id)
	if !ok {
		return nil
	}
	sv := s.as(Caller{Org: org, Machine: true})
	t, err := sv.data.GetTodo(id)
	if err != nil || !reviewed(t) || t.ReviewKey == model.ReviewKeyOf(t) {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	patch, err := sv.diff(ctx, id)
	if err != nil {
		return err
	}
	_, err = sv.setReviewFiles(id, patch)
	return err
}
