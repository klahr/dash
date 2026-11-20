package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// projectChatAction handles POST /api/projects/{pid}/chat/actions
// {"todo":"<id>","kind":"queued"|"discarded"}: the user queued or discarded
// a draft from its card in their project chat (with the task's own routes,
// POST /api/todos/{id}/enqueue and DELETE /api/todos/{id}), and the chat
// notes it ("You queued *Fix login*."), so Claude sees it on the next turn.
// The task must be one an answer in the chat made, and must be queued (no
// longer a draft) or deleted by now. A note the chat already has isn't
// added again. It answers with the note.
func (s *Server) projectChatAction(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req struct {
		Todo string `json:"todo"`
		Kind string `json:"kind"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := sv.writable(); err != nil {
		writeErr(w, err)
		return
	}
	pid, user := r.PathValue("pid"), chatUser(r, sv.c)
	stored, err := sv.data.ProjectChat(pid, user)
	if err != nil {
		writeErr(w, err)
		return
	}
	note, done, err := chatActionNote(stored, req.Todo, req.Kind, func(id string) (model.Todo, error) { return sv.data.GetTodo(id) })
	if err != nil {
		writeErr(w, err)
		return
	}
	if !done {
		note.CreatedBy = user
		if err := sv.data.AppendProjectChat(pid, user, note); err != nil {
			writeErr(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, note)
}

// chatActionNote is the note for the user's action kind on task id, in a
// chat that has stored; done when the chat has it already. get reads a task.
func chatActionNote(stored []agent.ChatMessage, id, kind string, get func(string) (model.Todo, error)) (note agent.ChatMessage, done bool, err error) {
	if kind != model.ChatQueued && kind != model.ChatDiscarded {
		return note, false, fmt.Errorf("%w: kind is %q or %q", errBadRequest, model.ChatQueued, model.ChatDiscarded)
	}
	var made *model.ChatTask
	for _, m := range stored {
		if a := m.Action; a != nil && a.Kind == kind && a.Todo == id {
			return m, true, nil
		}
		for i := range m.Tasks {
			if m.Tasks[i].ID == id {
				made = &m.Tasks[i]
			}
		}
	}
	if id == "" || made == nil {
		return note, false, fmt.Errorf("%w: no task this chat made", store.ErrNotFound)
	}
	a := model.ChatAction{Kind: kind, Todo: id, Title: made.Title}
	t, err := get(id)
	switch {
	case kind == model.ChatQueued && err != nil:
		return note, false, err
	case kind == model.ChatQueued && t.Status == model.StatusDraft:
		return note, false, fmt.Errorf("%w: the task is still a draft", errConflict)
	case kind == model.ChatDiscarded && err == nil:
		return note, false, fmt.Errorf("%w: the task isn't deleted", errConflict)
	case kind == model.ChatDiscarded && !errors.Is(err, store.ErrNotFound):
		return note, false, err
	}
	if kind == model.ChatQueued && t.Title != "" {
		a.Title = t.Title
	}
	at := now()
	return agent.ChatMessage{Role: "user", Content: agent.ChatActionNote(a), At: &at, Action: &a}, false, nil
}
