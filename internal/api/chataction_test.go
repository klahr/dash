package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// draftChat makes the tasks in made (create_todo, as the chat's tool would)
// and keeps the conversation it was given.
type draftChat struct {
	made []model.ChatTask
	mu   sync.Mutex
	got  []agent.ChatMessage
}

func (c *draftChat) Stream(_ context.Context, _ string, h []agent.ChatMessage, emit func(agent.ChatDelta)) error {
	c.mu.Lock()
	c.got = h
	c.mu.Unlock()
	for i, t := range c.made {
		id := "u" + string(rune('1'+i))
		emit(agent.ChatDelta{Kind: "tool_use", ID: id, Tool: "mcp__dash_board__create_todo"})
		emit(agent.ChatDelta{Kind: "tool_result", ID: id, Text: `{"id":"` + t.ID + `"}`, Task: &t})
	}
	emit(agent.ChatDelta{Kind: "text", Text: "Made it."})
	emit(agent.ChatDelta{Kind: "done"})
	return nil
}

func (c *draftChat) prompt() []agent.ChatMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.got
}

// A draft a project chat made is saved with the answer; queuing or
// discarding it from its card adds a note once (the task must be queued or
// gone by then, and one the chat made), which the next turn gives Claude.
func TestProjectChatActionNotes(t *testing.T) {
	s, _, p := boardServer(t)
	st := raw(s)
	login, _ := st.CreateTodo(model.Todo{ProjectID: p.ID, Title: "Fix login", Prompt: "x"})
	old, _ := st.CreateTodo(model.Todo{ProjectID: p.ID, Title: "Old idea", Prompt: "y"})
	other, _ := st.CreateTodo(model.Todo{ProjectID: p.ID, Title: "Not the chat's", Prompt: "z"})
	c := &draftChat{made: []model.ChatTask{{ID: login.ID, Title: "Fix login"}, {ID: old.ID, Title: "Old idea"}}}
	s.Chat = c
	h := asOwnerHandler(t, s)
	path := "/api/projects/" + p.ID + "/chat"

	conn := chatSocket(t, s)
	if err := conn.WriteJSON(map[string]any{"type": "send", "project": p.ID, "base": 0, "messages": []agent.ChatMessage{cmdMsg("make two tasks")}}); err != nil {
		t.Fatal(err)
	}
	if k := kindsOf(readTurn(t, conn, 0)); k[len(k)-1] != "done" {
		t.Fatalf("turn: %v", k)
	}
	saved := func() []model.ChatMessage {
		t.Helper()
		var msgs []model.ChatMessage
		if w := send(h, "GET", path, ""); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &msgs) != nil {
			t.Fatalf("GET: %d %s", w.Code, w.Body)
		}
		return msgs
	}
	if msgs := saved(); len(msgs) != 2 || len(msgs[1].Tasks) != 2 || msgs[1].Tasks[0].ID != login.ID || msgs[1].Tasks[1].Title != "Old idea" {
		t.Fatalf("saved answer's tasks: %+v", msgs)
	}

	act := func(todo, kind string) (int, model.ChatMessage) {
		t.Helper()
		w := send(h, "POST", path+"/actions", `{"todo":"`+todo+`","kind":"`+kind+`"}`)
		var m model.ChatMessage
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return w.Code, m
	}
	if code, _ := act(login.ID, "queued"); code != http.StatusConflict {
		t.Errorf("noting a draft as queued: %d", code)
	}
	if code, _ := act(old.ID, "discarded"); code != http.StatusConflict {
		t.Errorf("noting a task that's still there as discarded: %d", code)
	}
	if code, _ := act(other.ID, "queued"); code != http.StatusNotFound {
		t.Errorf("a task the chat didn't make: %d", code)
	}
	if code, _ := act(login.ID, "started"); code != http.StatusBadRequest {
		t.Errorf("an unknown kind: %d", code)
	}

	// Queued and discarded with the tasks' own routes, then noted.
	if w := send(h, "POST", "/api/todos/"+login.ID+"/enqueue", ""); w.Code != http.StatusOK {
		t.Fatalf("enqueue: %d %s", w.Code, w.Body)
	}
	if w := send(h, "DELETE", "/api/todos/"+old.ID, ""); w.Code/100 != 2 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	code, m := act(login.ID, "queued")
	if code != http.StatusOK || m.Role != "user" || m.Content != "You queued *Fix login*." || m.Action == nil || m.Action.Todo != login.ID {
		t.Errorf("queued note: %d %+v", code, m)
	}
	if code, m := act(old.ID, "discarded"); code != http.StatusOK || m.Content != "You discarded *Old idea*." {
		t.Errorf("discarded note: %d %+v", code, m)
	}
	if code, _ := act(login.ID, "queued"); code != http.StatusOK {
		t.Errorf("noting it again: %d", code)
	}
	msgs := saved()
	if got := chatContents(msgs); got != "make two tasks | Made it. | You queued *Fix login*. | You discarded *Old idea*." {
		t.Errorf("chat: %q", got)
	}

	// The next turn tells Claude what the user did.
	c.made = nil
	next := append(msgs, cmdMsg("what's left?"))
	if err := conn.WriteJSON(map[string]any{"type": "send", "project": p.ID, "base": len(msgs), "messages": next}); err != nil {
		t.Fatal(err)
	}
	if k := kindsOf(readTurn(t, conn, 0)); k[len(k)-1] != "done" {
		t.Fatalf("next turn: %v", k)
	}
	var prompt []string
	for _, m := range c.prompt() {
		prompt = append(prompt, m.Content)
	}
	all := strings.Join(prompt, "\n")
	for _, want := range []string{"I queued the draft task \"Fix login\" (id " + login.ID + ")", "I discarded the draft task \"Old idea\""} {
		if !strings.Contains(all, want) {
			t.Errorf("Claude wasn't told %q:\n%s", want, all)
		}
	}
	if _, err := st.GetTodo(old.ID); err != store.ErrNotFound {
		t.Errorf("discarded task: %v", err)
	}
}
