package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
)

// chatWS streams Claude responses. Protocol (JSON text frames):
//
//	client → {"type":"send","project":"<id>","todo":"<id>","messages":[{"role":"user","content":"…"}, …]}
//	client → {"type":"cancel"}
//	client → {"type":"permission","id":"<id>","decision":"allow"|"deny"}
//	server → {"kind":"thinking"|"text"|"done"|"error","text":"…"}
//	server → {"kind":"cwd","text":"<dir>"} where the turn runs
//	server → {"kind":"tool_use","id":"…","tool":"Bash","input":{…}}
//	server → {"kind":"tool_result","id":"…","text":"<output>","is_error":bool}
//	server → {"kind":"permission","id":"<id>","tool":"Bash","input":{…}} asks to approve a tool call
//	server → {"kind":"permission_decided","id":"<id>","text":"allow"|"deny"|"timeout"}
//
// The client owns the conversation history and sends it in full each turn.
// Each completed turn is saved, and GET /api/chat returns it after a restart.
// A new "send" cancels any response still streaming. Without "project" or
// "todo" it is the global chat, run in the chatter's default directory.
// With "project", Claude runs in that project's main checkout and the turn is
// saved to GET /api/projects/{id}/chat.
// With "todo", the conversation is about that todo: Claude sees its title,
// status, instructions and changes, runs in its worktree while it has one,
// and the turn is saved to GET /api/todos/{id}/chat.
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
	approvals := &chatApprovals{send: send, timeout: chatApprovalTimeout}

	for {
		var msg struct {
			Type     string              `json:"type"`
			Project  string              `json:"project"`
			Todo     string              `json:"todo"`
			Messages []agent.ChatMessage `json:"messages"`
			ID       string              `json:"id"`
			Decision string              `json:"decision"`
		}
		if err := conn.ReadJSON(&msg); err != nil {
			cancelTurn() // denies whatever is waiting for approval
			return
		}
		if msg.Type == "permission" {
			approvals.answer(msg.ID, msg.Decision == "allow")
			continue
		}
		cancelTurn()
		if msg.Type != "send" {
			continue
		}
		var dir string
		save := s.Store.SetChat
		prompt := msg.Messages
		if msg.Project != "" && msg.Todo == "" && s.Store != nil {
			p, err := s.Store.GetProject(msg.Project)
			if err != nil {
				send(agent.ChatDelta{Kind: "error", Text: err.Error()})
				continue
			}
			dir = p.Path
			save = func(msgs []agent.ChatMessage) error { return s.Store.SetProjectChat(p.ID, msgs) }
		}
		if msg.Todo != "" && s.Store != nil {
			t, err := s.Store.GetTodo(msg.Todo)
			if err != nil {
				send(agent.ChatDelta{Kind: "error", Text: err.Error()})
				continue
			}
			var diff string
			if p, err := s.Store.GetProject(t.ProjectID); err == nil {
				dir = p.Path
				if t.Worktree != nil {
					// Let Claude read the files as the todo left them.
					if _, err := os.Stat(t.Worktree.Path); err == nil && t.Status != model.StatusMerged {
						dir = t.Worktree.Path
					}
					if s.Git != nil {
						if diff, err = s.todoChanges(ctx, p, t); err != nil {
							slog.Warn("todo chat diff", "todo", t.ID, "err", err)
						}
					}
				}
			}
			save = func(msgs []agent.ChatMessage) error { return s.Store.SetTodoChat(t.ID, msgs) }
			prompt = withTodoContext(t, diff, msg.Messages)
		}
		var turnCtx context.Context
		turnCtx, cancelTurn = context.WithCancel(ctx)
		turnCtx = agent.WithApprover(turnCtx, approvals.forTurn(turnCtx))
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

// chatApprovalTimeout denies a chat tool call nobody answered.
const chatApprovalTimeout = 5 * time.Minute

// chatApprovals asks the chat client to approve tool calls and waits for
// the answer, one connection's worth.
type chatApprovals struct {
	send    func(agent.ChatDelta)
	timeout time.Duration

	mu      sync.Mutex
	pending map[string]chan bool
}

// forTurn returns the approver for one turn: requests are denied when the
// turn ends (a new message, or the client disconnects) as well as when
// Claude's own request is cancelled.
func (a *chatApprovals) forTurn(turn context.Context) perm.AskFunc {
	return func(ctx context.Context, tool string, input json.RawMessage) (bool, string) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		defer context.AfterFunc(turn, cancel)()
		return a.ask(ctx, tool, input)
	}
}

func (a *chatApprovals) ask(ctx context.Context, tool string, input json.RawMessage) (bool, string) {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	id := hex.EncodeToString(buf)
	ch := make(chan bool, 1)
	a.mu.Lock()
	if a.pending == nil {
		a.pending = map[string]chan bool{}
	}
	a.pending[id] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
	}()

	a.send(agent.ChatDelta{Kind: "permission", ID: id, Tool: tool, Input: input})
	timer := time.NewTimer(a.timeout)
	defer timer.Stop()
	decided := func(d string) { a.send(agent.ChatDelta{Kind: "permission_decided", ID: id, Text: d}) }
	select {
	case ok := <-ch:
		if ok {
			decided("allow")
			return true, ""
		}
		decided("deny")
		return false, "The user denied this action."
	case <-timer.C:
		decided("timeout")
		return false, "Nobody answered the permission request in time."
	case <-ctx.Done():
		decided("deny")
		return false, "The request was cancelled."
	}
}

// answer reports whether id was waiting.
func (a *chatApprovals) answer(id string, allow bool) bool {
	a.mu.Lock()
	ch, ok := a.pending[id]
	delete(a.pending, id)
	a.mu.Unlock()
	if ok {
		ch <- allow
	}
	return ok
}

// maxChatDiff caps the patch given to the todo chat each turn.
const maxChatDiff = 200_000

// withTodoContext tells Claude which todo the conversation is about by
// prefixing the first message. The todo is read fresh every turn, so edits
// to its instructions are seen; the saved history stays as the user typed it.
// diff is the patch the todo has made so far, if any.
func withTodoContext(t model.Todo, diff string, history []agent.ChatMessage) []agent.ChatMessage {
	if len(history) == 0 {
		return history
	}
	var b strings.Builder
	b.WriteString("This conversation is about a task that Claude Code carries out on its own in a git worktree of the project. ")
	if t.Status == model.StatusDraft {
		b.WriteString("It is still a draft, so help me refine its instructions into a clear, complete prompt for Claude Code. ")
	}
	fmt.Fprintf(&b, "\n\n<task status=%q>\n<title>%s</title>\n<instructions>\n%s\n</instructions>\n", t.Status, t.Title, t.Prompt)
	for _, f := range t.Feedback {
		fmt.Fprintf(&b, "<review_note>\n%s\n</review_note>\n", f)
	}
	if t.Error != "" {
		fmt.Fprintf(&b, "<error>%s</error>\n", t.Error)
	}
	if t.Summary != "" {
		fmt.Fprintf(&b, "<summary>%s</summary>\n", t.Summary)
	}
	if diff != "" {
		if len(diff) > maxChatDiff {
			diff = diff[:maxChatDiff] + "\n[diff truncated]"
		}
		if t.Worktree != nil && t.Status != model.StatusMerged {
			fmt.Fprintf(&b, "<worktree branch=%q>The files you can read are this worktree's, with the changes below applied.</worktree>\n", t.Worktree.Branch)
		}
		fmt.Fprintf(&b, "<changes>\n%s\n</changes>\n", diff)
	}
	b.WriteString("</task>\n\n")
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
