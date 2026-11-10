package tracker

import (
	"context"
	"errors"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// NativeBackend is Dash's own board as a Backend: a pass-through to the
// store, where a card is a todo, its column its status and its comments the
// todo's chat. Native projects run no sync worker (their board is the
// store); it exists so code written against Backend works for every
// project. The REST API and the board keep using the store as before.
type NativeBackend struct {
	Store     *store.Store
	ProjectID string
}

// NewNative is projectID's board on Dash itself.
func NewNative(st *store.Store, projectID string) *NativeBackend {
	return &NativeBackend{Store: st, ProjectID: projectID}
}

func (n *NativeBackend) Name() string { return Native }

func (n *NativeBackend) ref(id string) Ref { return Ref{Provider: Native, ID: id} }

func (n *NativeBackend) List(_ context.Context, since time.Time) ([]Item, error) {
	if _, err := n.Store.GetProject(n.ProjectID); err != nil {
		return nil, err
	}
	var out []Item
	for _, t := range n.Store.ListTodos(n.ProjectID) {
		if !since.IsZero() && !t.UpdatedAt.After(since) {
			continue
		}
		out = append(out, Item{
			Ref: n.ref(t.ID), Title: t.Title, Description: t.Prompt, Priority: t.Priority,
			DependsOn: append([]string(nil), t.DependsOn...), Column: string(t.Status),
			UpdatedAt: t.UpdatedAt,
		})
	}
	return out, nil
}

func (n *NativeBackend) Create(_ context.Context, it Item) (Ref, error) {
	t, err := n.Store.CreateTodo(model.Todo{ProjectID: n.ProjectID, Title: it.Title, Prompt: it.Description, Priority: it.Priority, DependsOn: it.DependsOn})
	if err != nil {
		return Ref{}, err
	}
	if model.TodoStatus(it.Column) == model.StatusQueued {
		if _, err := n.Store.SetStatus(t.ID, model.StatusQueued, ""); err != nil {
			return n.ref(t.ID), err
		}
	}
	return n.ref(t.ID), nil
}

func (n *NativeBackend) own(ref Ref) error {
	t, err := n.Store.GetTodo(ref.ID)
	if err != nil {
		return err
	}
	if t.ProjectID != n.ProjectID {
		return store.ErrNotFound
	}
	return nil
}

// Update follows the board's rules: the prompt only changes while the todo
// is a draft, queued or failed.
func (n *NativeBackend) Update(_ context.Context, ref Ref, it Item) error {
	if err := n.own(ref); err != nil {
		return err
	}
	_, err := n.Store.UpdateTodo(ref.ID, func(t *model.Todo) error {
		if t.Prompt != it.Description && !PromptEditable(t.Status) {
			return errors.New("instructions can only be edited while draft, queued or failed")
		}
		t.Title, t.Prompt, t.Priority, t.DependsOn = it.Title, it.Description, it.Priority, it.DependsOn
		return nil
	})
	return err
}

func (n *NativeBackend) SetStatus(_ context.Context, ref Ref, status model.TodoStatus) error {
	if err := n.own(ref); err != nil {
		return err
	}
	_, err := n.Store.SetStatus(ref.ID, status, "")
	return err
}

func (n *NativeBackend) Comment(_ context.Context, ref Ref, markdown string) error {
	if err := n.own(ref); err != nil {
		return err
	}
	now := time.Now()
	return n.Store.AppendTodoChat(ref.ID, model.ChatMessage{Role: "assistant", Content: markdown, At: &now})
}

func (n *NativeBackend) Delete(_ context.Context, ref Ref) error {
	if err := n.own(ref); err != nil {
		return err
	}
	_, err := n.Store.DeleteTodo(ref.ID)
	return err
}

// PromptEditable reports whether a todo's prompt may change in status s:
// while it is a draft, queued or failed (the board's rule).
func PromptEditable(s model.TodoStatus) bool {
	return s == model.StatusDraft || s == model.StatusQueued || s == model.StatusFailed
}
