package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/hub"
)

// chatWS streams Claude responses. Protocol (JSON text frames):
//
//	client → {"type":"send","messages":[{"role":"user","content":"…"}, …]}
//	client → {"type":"cancel"}
//	server → {"kind":"thinking"|"text"|"done"|"error","text":"…"}
//
// The client owns the conversation history and sends it in full each turn.
// Each completed turn is saved, and GET /api/chat returns it after a restart.
// A new "send" cancels any response still streaming.
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
		var turnCtx context.Context
		turnCtx, cancelTurn = context.WithCancel(ctx)
		go func(ctx context.Context, history []agent.ChatMessage) {
			var reply strings.Builder
			err := s.Chat.Stream(ctx, history, func(d agent.ChatDelta) {
				if d.Kind == "text" {
					reply.WriteString(d.Text)
				}
				if d.Kind == "done" && reply.Len() > 0 && s.Store != nil {
					// Save before telling the client, so a reload right after sees it.
					turn := append(history[:len(history):len(history)], agent.ChatMessage{Role: "assistant", Content: reply.String()})
					if err := s.Store.SetChat(turn); err != nil {
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
