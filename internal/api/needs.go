package api

import (
	"net/http"
	"slices"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
)

// Needs you: one list of what waits on the user across every project they
// can see, so they can work through it in one pass instead of checking each
// project's board. The web client keeps it current from the event stream
// (todo.updated, todo.deleted, permission.updated) once it has loaded it.

// need is one item that waits on a person.
type need struct {
	// Kind: review (a task in Review), failed (a failed task), question
	// (Claude asks the user something) or permission (Claude asks to use a
	// tool).
	Kind      string    `json:"kind"`
	ProjectID string    `json:"project_id"`
	Since     time.Time `json:"since"` // when it started waiting
	// Todo is the task, for every kind but a request outside a task (a
	// project chat's).
	Todo *model.Todo `json:"todo,omitempty"`
	// Perm is the request of a question or permission.
	Perm *perm.Request `json:"perm,omitempty"`
}

// listNeeds is GET /api/needs: the caller's needs, oldest first.
func (s *Server) listNeeds(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.needs(sv))
}

// needs gathers what waits on sv's caller: tasks in Review as the Wall's
// Needs a human lists them (reviewWait: not a pull request that waits on
// GitHub, Claude or its checks), failed tasks, and pending questions and
// permission requests. Oldest first.
func (s *Server) needs(sv *service) []need {
	out := []need{}
	todos := sv.data.ListTodos("", model.StatusReview, model.StatusFailed)
	byID := make(map[string]model.Todo, len(todos))
	for _, t := range todos {
		byID[t.ID] = t
		n := need{ProjectID: t.ProjectID, Todo: &t}
		switch t.Status {
		case model.StatusReview:
			since, waits := reviewWait(t)
			if !waits {
				continue
			}
			n.Kind, n.Since = "review", since
		case model.StatusFailed:
			n.Kind, n.Since = "failed", failedSince(t)
		}
		out = append(out, n)
	}
	for _, req := range s.pendingPerms(sv, "") {
		n := need{Kind: "permission", ProjectID: req.ProjectID, Since: req.Created, Perm: &req}
		if req.Tool == perm.AskTool {
			n.Kind = "question"
		}
		if t, ok := byID[req.TodoID]; ok {
			n.Todo = &t
		} else if t, err := sv.data.GetTodo(req.TodoID); req.TodoID != "" && err == nil {
			n.Todo = &t
		}
		out = append(out, n)
	}
	slices.SortStableFunc(out, func(a, b need) int { return a.Since.Compare(b.Since) })
	return out
}
