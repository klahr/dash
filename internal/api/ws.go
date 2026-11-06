package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/model"
)

// chatWS streams Claude responses. Protocol (JSON text frames):
//
//	client → {"type":"send","project":"<id>","todo":"<id>","messages":[{"role":"user","content":"…"}, …]}
//	client → {"type":"cancel"}
//	server → {"kind":"thinking"|"text"|"done"|"error","text":"…"}
//
// The client owns the conversation history and sends it in full each turn.
// Each completed turn is saved, and GET /api/chat returns it after a restart.
// A new "send" cancels any response still streaming. Claude runs in the
// project's main checkout when "project" names a project on this machine.
// With "todo", the conversation is about that todo: Claude sees its title,
// status and instructions, and the turn is saved to GET /api/todos/{id}/chat.
func (s *Server) chatWS(w http.ResponseWriter, r *http.Request) {
	conn, err := hub.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	var wmu sync.Mutex
	send := func(d agent.ChatDelta) {
		wmu.Lock()
		defer wmu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_ = conn.WriteJSON(d)
	}

	ctx, cancelAll := context.WithCancel(r.Context())
	defer cancelAll()
	var cancelTurn context.CancelFunc = func() {}

	for {
		var msg struct {
			Type     string              `json:"type"`
			Project  string              `json:"project"`
			Todo     string              `json:"todo"`
			Messages []agent.ChatMessage `json:"messages"`
		}
		if err := conn.ReadJSON(&msg); err != nil {
			cancelTurn()
			return
		}
		cancelTurn()
		if msg.Type != "send" {
			continue
		}
		var dir string
		if msg.Project != "" && s.Store != nil {
			if p, err := s.Store.GetProject(msg.Project); err == nil {
				dir = p.Path
			}
		}
		save := s.Store.SetChat
		prompt := msg.Messages
		if msg.Todo != "" && s.Store != nil {
			t, err := s.Store.GetTodo(msg.Todo)
			if err != nil {
				send(agent.ChatDelta{Kind: "error", Text: err.Error()})
				continue
			}
			if p, err := s.Store.GetProject(t.ProjectID); err == nil {
				dir = p.Path
			}
			save = func(msgs []agent.ChatMessage) error { return s.Store.SetTodoChat(t.ID, msgs) }
			prompt = withTodoContext(t, msg.Messages)
		}
		var turnCtx context.Context
		turnCtx, cancelTurn = context.WithCancel(ctx)
		go func(ctx context.Context, history []agent.ChatMessage) {
			var reply strings.Builder
			err := s.Chat.Stream(ctx, dir, prompt, func(d agent.ChatDelta) {
				if d.Kind == "text" {
					reply.WriteString(d.Text)
				}
				if d.Kind == "done" && reply.Len() > 0 && s.Store != nil {
					// Save before telling the client, so a reload right after sees it.
					turn := append(history[:len(history):len(history)], agent.ChatMessage{Role: "assistant", Content: reply.String()})
					if err := save(turn); err != nil {
						slog.Error("save chat", "err", err)
					}
				}
				send(d)
			})
			if err != nil && ctx.Err() == nil {
				slog.Warn("chat", "err", err)
				send(agent.ChatDelta{Kind: "error", Text: err.Error()})
			}
		}(turnCtx, msg.Messages)
	}
}

// withTodoContext tells Claude which todo the conversation is about by
// prefixing the first message. The todo is read fresh every turn, so edits
// to its instructions are seen; the saved history stays as the user typed it.
func withTodoContext(t model.Todo, history []agent.ChatMessage) []agent.ChatMessage {
	if len(history) == 0 {
		return history
	}
	var b strings.Builder
	b.WriteString("This conversation is about a todo: a task that Claude Code carries out on its own in a git worktree of the project. ")
	if t.Status == model.StatusDraft {
		b.WriteString("It is still a draft, so help me refine its instructions into a clear, complete prompt for Claude Code. ")
	}
	fmt.Fprintf(&b, "\n\n<todo status=%q>\n<title>%s</title>\n<instructions>\n%s\n</instructions>\n", t.Status, t.Title, t.Prompt)
	for _, f := range t.Feedback {
		fmt.Fprintf(&b, "<review_note>\n%s\n</review_note>\n", f)
	}
	if t.Error != "" {
		fmt.Fprintf(&b, "<error>%s</error>\n", t.Error)
	}
	b.WriteString("</todo>\n\n")
	out := append([]agent.ChatMessage(nil), history...)
	out[0].Content = b.String() + out[0].Content
	return out
}

// procWS attaches xterm.js to a process.
//
//	server → binary frames: raw terminal output (scrollback first)
//	server → text frame {"type":"exit","code":N} when the process ends
//	client → {"type":"input","data":"…"} | {"type":"resize","cols":N,"rows":N}
func (s *Server) procWS(w http.ResponseWriter, r *http.Request) {
	sess, err := s.Procs.Attach(r.PathValue("procid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	conn, err := hub.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		sess.Detach()
		return
	}
	defer conn.Close()
	defer sess.Detach()

	go func() {
		for {
			var msg struct {
				Type string `json:"type"`
				Data string `json:"data"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if err := conn.ReadJSON(&msg); err != nil {
				sess.Detach() // unblocks the writer below
				return
			}
			switch msg.Type {
			case "input":
				_ = sess.Write([]byte(msg.Data))
			case "resize":
				_ = sess.Resize(msg.Cols, msg.Rows)
			}
		}
	}()

	write := func(typ int, b []byte) error {
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteMessage(typ, b)
	}
	if len(sess.Scrollback) > 0 {
		if err := write(websocket.BinaryMessage, sess.Scrollback); err != nil {
			return
		}
	}
	for chunk := range sess.Output {
		if err := write(websocket.BinaryMessage, chunk); err != nil {
			return
		}
	}
	// Output closed: either the process exited or the client went away.
	if info := s.Procs.List(sess.Info.ProjectID); len(info) > 0 {
		for _, p := range info {
			if p.ID == sess.Info.ID && !p.Running && p.ExitCode != nil {
				b, _ := json.Marshal(map[string]any{"type": "exit", "code": *p.ExitCode})
				_ = write(websocket.TextMessage, b)
			}
		}
	}
}
