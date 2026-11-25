package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

// savedToolsChat says something, makes a tool call, writes a big file (an input
// over the cap) that fails with a long output, and says it's done.
type savedToolsChat struct{}

var bigFile = strings.Repeat("é", 3000) // 6000 bytes

func (savedToolsChat) Stream(_ context.Context, _ string, _ []agent.ChatMessage, emit func(agent.ChatDelta)) error {
	emit(agent.ChatDelta{Kind: "text", Text: "Looking… "})
	emit(agent.ChatDelta{Kind: "tool_use", ID: "t1", Tool: "mcp__dash_board__create_todo", Input: json.RawMessage(`{"title":"Fix login"}`)})
	emit(agent.ChatDelta{Kind: "tool_result", ID: "t1", Text: `{"id":"abc"}`})
	in, _ := json.Marshal(map[string]any{"file_path": "/tmp/x", "content": bigFile})
	emit(agent.ChatDelta{Kind: "tool_use", ID: "t2", Tool: "Write", Input: in})
	emit(agent.ChatDelta{Kind: "tool_result", ID: "t2", Text: strings.Repeat("x", 9000), IsError: true})
	emit(agent.ChatDelta{Kind: "text", Text: "Done."})
	emit(agent.ChatDelta{Kind: "done"})
	return nil
}

// okChat answers "ok".
type okChat struct{}

func (okChat) Stream(_ context.Context, _ string, _ []agent.ChatMessage, emit func(agent.ChatDelta)) error {
	emit(agent.ChatDelta{Kind: "text", Text: "ok"})
	emit(agent.ChatDelta{Kind: "done"})
	return nil
}

// checkTools checks the tool calls savedToolsChat's answer was saved with.
func checkTools(t *testing.T, m model.ChatMessage) {
	t.Helper()
	if m.Role != "assistant" || m.Content != "Looking… Done." || len(m.Tools) != 2 {
		t.Fatalf("saved answer: %+v", m)
	}
	a, b := m.Tools[0], m.Tools[1]
	pos := len("Looking… ")
	if a.ID != "t1" || a.Tool != "mcp__dash_board__create_todo" || string(a.Input) != `{"title":"Fix login"}` || a.Output != `{"id":"abc"}` || a.IsError || a.Cut || a.Pos != pos {
		t.Errorf("first call: %+v", a)
	}
	if b.Tool != "Write" || !b.IsError || b.Cut || b.Pos != pos || len(b.Output) > maxToolOutput+10 || !strings.HasPrefix(b.Output, "xxx") {
		t.Errorf("second call: %+v (output %d bytes)", b, len(b.Output))
	}
	var in map[string]string
	if err := json.Unmarshal(b.Input, &in); err != nil || in["file_path"] != "/tmp/x" || len(in["content"]) > maxToolString+10 || !strings.HasPrefix(in["content"], "éé") {
		t.Errorf("second call's input: %s (%v)", b.Input, err)
	}
}

// The tool calls of a project chat's answer are saved with it, where they
// were in its text, capped, and the chat's GET returns them.
func TestProjectChatSavesToolCalls(t *testing.T) {
	s, _, p := boardServer(t)
	s.Chat = savedToolsChat{}
	owner := testOwner(t, s)
	conn := chatSocket(t, s)
	if err := conn.WriteJSON(map[string]any{"type": "send", "project": p.ID, "base": 0, "messages": []agent.ChatMessage{cmdMsg("make a task")}}); err != nil {
		t.Fatal(err)
	}
	readTurn(t, conn, 0)
	msgs := raw(s).ProjectChat(p.ID, owner.User)
	if len(msgs) != 2 {
		t.Fatalf("saved: %+v", msgs)
	}
	checkTools(t, msgs[1])

	w := send(asOwnerHandler(t, s), "GET", "/api/projects/"+p.ID+"/chat", "")
	var got []model.ChatMessage
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got) != 2 {
		t.Fatalf("GET: %d %s", w.Code, w.Body)
	}
	checkTools(t, got[1])
}

// The general chat's client sends its conversation back without the tool
// calls: the saved answers keep theirs.
func TestGeneralChatKeepsToolCalls(t *testing.T) {
	s, _, _ := boardServer(t)
	s.Chat = savedToolsChat{}
	c := ownerCaller(t, s)
	conn := chatSocket(t, s)
	if err := conn.WriteJSON(map[string]any{"type": "send", "base": 0, "messages": []agent.ChatMessage{cmdMsg("make a task")}}); err != nil {
		t.Fatal(err)
	}
	readTurn(t, conn, 0)
	saved := raw(s).Chat(c.Org, c.User)
	if len(saved) != 2 {
		t.Fatalf("saved: %+v", saved)
	}
	checkTools(t, saved[1])

	s.Chat = okChat{}
	next := []agent.ChatMessage{saved[0], {Role: "assistant", Content: saved[1].Content}, cmdMsg("thanks")}
	if err := conn.WriteJSON(map[string]any{"type": "send", "base": 2, "messages": next}); err != nil {
		t.Fatal(err)
	}
	readTurn(t, conn, 0)
	waitFor(t, "the turn to end", func() bool { return !s.generalBusy(c) })
	saved = raw(s).Chat(c.Org, c.User)
	if len(saved) != 4 {
		t.Fatalf("saved: %+v", saved)
	}
	checkTools(t, saved[1])
}

// New chat puts a conversation aside; it can be listed, opened again (the
// current one going aside in its place), deleted and restored.
func TestPastChatsAPI(t *testing.T) {
	s, _, p := boardServer(t)
	h := asOwnerHandler(t, s)
	owner := testOwner(t, s)
	c := ownerCaller(t, s)
	for _, tc := range []struct {
		name, base string
		set        func(...model.ChatMessage)
		get        func() []model.ChatMessage
	}{
		{"general", "/api/chat",
			func(m ...model.ChatMessage) { _ = raw(s).SetChat(c.Org, c.User, m) },
			func() []model.ChatMessage { return raw(s).Chat(c.Org, c.User) }},
		{"project", "/api/projects/" + p.ID + "/chat",
			func(m ...model.ChatMessage) { _ = raw(s).AppendProjectChat(p.ID, owner.User, m...) },
			func() []model.ChatMessage { return raw(s).ProjectChat(p.ID, owner.User) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.set(cmdMsg("first question\nmore"), model.ChatMessage{Role: "assistant", Content: "first answer"})
			if w := send(h, "DELETE", tc.base, ""); w.Code != http.StatusNoContent {
				t.Fatalf("New chat: %d %s", w.Code, w.Body)
			}
			if got := tc.get(); len(got) != 0 {
				t.Fatalf("after New chat: %+v", got)
			}
			w := send(h, "GET", tc.base, "")
			if w.Header().Get("X-Chat-Past") != "1" {
				t.Errorf("X-Chat-Past: %q", w.Header().Get("X-Chat-Past"))
			}
			list := func() []model.PastChat {
				t.Helper()
				w := send(h, "GET", tc.base+"/past", "")
				var out []model.PastChat
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatalf("GET past: %d %s", w.Code, w.Body)
				}
				return out
			}
			past := list()
			if len(past) != 1 || past[0].Title != "first question" || past[0].Count != 2 || past[0].Messages != nil {
				t.Fatalf("past: %+v", past)
			}
			tc.set(cmdMsg("second"))
			w = send(h, "POST", tc.base+"/past/"+past[0].ID+"/open", "")
			var opened []model.ChatMessage
			if err := json.Unmarshal(w.Body.Bytes(), &opened); err != nil || len(opened) != 2 || opened[1].Content != "first answer" {
				t.Fatalf("open: %d %s", w.Code, w.Body)
			}
			if got := tc.get(); len(got) != 2 || got[0].Content != "first question\nmore" {
				t.Fatalf("current after open: %+v", got)
			}
			past = list()
			if len(past) != 1 || past[0].Title != "second" {
				t.Fatalf("past after open: %+v", past)
			}
			if w := send(h, "DELETE", tc.base+"/past/"+past[0].ID, ""); w.Code != http.StatusNoContent {
				t.Fatalf("delete: %d %s", w.Code, w.Body)
			}
			if got := list(); len(got) != 0 {
				t.Fatalf("past after delete: %+v", got)
			}
			if w := send(h, "POST", tc.base+"/past/"+past[0].ID+"/open", ""); w.Code != http.StatusNotFound {
				t.Errorf("open a deleted one: %d", w.Code)
			}
			if w := send(h, "POST", tc.base+"/past/"+past[0].ID+"/restore", ""); w.Code != http.StatusNoContent {
				t.Fatalf("restore: %d %s", w.Code, w.Body)
			}
			if got := list(); len(got) != 1 || got[0].ID != past[0].ID {
				t.Fatalf("past after restore: %+v", got)
			}
		})
	}
	// A past chat of the general chat isn't the project chat's.
	general := send(h, "GET", "/api/chat/past", "")
	var gl []model.PastChat
	_ = json.Unmarshal(general.Body.Bytes(), &gl)
	if len(gl) != 1 {
		t.Fatalf("general past: %s", general.Body)
	}
	if w := send(h, "POST", "/api/projects/"+p.ID+"/chat/past/"+gl[0].ID+"/open", ""); w.Code != http.StatusNotFound {
		t.Errorf("opened the general chat's past chat as the project's: %d", w.Code)
	}
}
