package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/store"
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
// The global chat also saves the question as soon as it arrives, so a turn cut
// short by a restart can be sent again; GET /api/chat sets X-Chat-Busy while
// one of its turns is running.
// A new "send" cancels any response still streaming. Without "project" or
// "todo" it is the global chat, run in the chatter's default directory; on
// a hub that doesn't run Claude itself, on one of the caller's runners
// ("runner": its id, else the first online), announced with
// {"kind":"runner","text":"<name>"}. A runner runs the global chat only for
// its owner, asked through the hub, and doesn't save it (the hub does).
// With "project", Claude runs in that project's main checkout and the turn is
// saved to GET /api/projects/{id}/chat. On a hub that doesn't run Claude, or
// for a project whose tasks run on runners, it runs instead on the caller's
// runner linked to the project (announced with {"kind":"runner"}), in that
// runner's clone and with the hub project's permission mode; the hub saves it.
// With "todo", the conversation is about that todo: Claude sees its title,
// status, instructions and changes, runs in its worktree while it has one,
// and the turn is saved to GET /api/todos/{id}/chat. With "review": true as
// well, the todo (which must be in review) is marked reviewing until the turn
// ends, and its status can't change meanwhile.
func (s *Server) chatWS(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
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
	relay := &chatRelay{} // the turn running on the caller's runner, if any

	for {
		var msg struct {
			Type     string              `json:"type"`
			Project  string              `json:"project"`
			Todo     string              `json:"todo"`
			Messages []agent.ChatMessage `json:"messages"`
			ID       string              `json:"id"`
			Decision string              `json:"decision"`
			Review   bool                `json:"review"` // with "todo": the turn reviews it, locking its status
			Runner   string              `json:"runner"` // chat on a hub that doesn't run Claude: which of the caller's runners
			Mode     string              `json:"mode"`   // a hub's project chat on its runner: the hub project's permission mode
		}
		if err := conn.ReadJSON(&msg); err != nil {
			cancelTurn() // denies whatever is waiting for approval
			return
		}
		if msg.Type == "permission" {
			if !approvals.answer(msg.ID, msg.Decision == "allow") {
				// Asked by the turn on the caller's runner.
				relay.send(map[string]string{"type": "permission", "id": msg.ID, "decision": msg.Decision})
			}
			continue
		}
		cancelTurn()
		if msg.Type != "send" {
			continue
		}
		if err := sv.writable(); err != nil {
			// The chat runs Claude, which can change the projects.
			send(agent.ChatDelta{Kind: "error", Text: err.Error()})
			continue
		}
		var dir, mode, pid string
		endReview := func() {}
		general := msg.Project == "" && msg.Todo == ""
		// Each conversation is saved with who wrote its new questions.
		user := sv.c.User
		save := func(msgs []agent.ChatMessage) error {
			return sv.data.SetChat(user, authored(sv.data.Chat(user), msgs, user))
		}
		chatter := s.Chat
		// The general chat, or a hub project's chat, for this runner's owner.
		forHub := msg.Todo == "" && auth.FromHub(r.Context())
		switch {
		case forHub:
			// This runner's owner, through the hub: Claude runs here, in a
			// scratch directory or the runner's clone of the hub's project,
			// and the hub keeps the conversation.
			if err := s.forOwner(r); err != nil {
				send(agent.ChatDelta{Kind: "error", Text: err.Error()})
				continue
			}
			if msg.Project != "" {
				p, ok := sv.hubProject(msg.Project)
				if !ok {
					send(agent.ChatDelta{Kind: "error", Text: "this runner has no clone of that project: link the project to it in Project settings → Runners"})
					continue
				}
				dir, mode, pid = p.Path, p.ChatPermissionMode, p.ID
				if validChatMode(msg.Mode) {
					mode = msg.Mode // the hub's project settings are the ones its users edit
				}
			} else {
				var err error
				if dir, err = s.chatScratch(); err != nil {
					send(agent.ChatDelta{Kind: "error", Text: err.Error()})
					continue
				}
			}
			save = func([]agent.ChatMessage) error { return nil }
		case general && sv.c.User == 0:
			send(agent.ChatDelta{Kind: "error", Text: "the general chat is a person's: sign in to use it"})
			continue
		case general && !s.localClaude():
			// This hub doesn't run Claude: the caller's own runner does.
			x, err := s.claudeRunner(sv.c, msg.Runner)
			if err == nil && len(msg.Messages) > 0 && len(msg.Messages[len(msg.Messages)-1].Attachments) > 0 {
				err = errors.New("files can't be sent to the chat on your runner; paste or describe them instead")
			}
			if err != nil {
				send(agent.ChatDelta{Kind: "error", Text: err.Error()})
				continue
			}
			chatter = runnerChat{fleet: s.fleet(), runner: x, user: sv.c.User, relay: relay}
		}
		if msg.Project != "" && msg.Todo == "" && !forHub {
			p, err := sv.data.GetProject(msg.Project)
			if err != nil {
				send(agent.ChatDelta{Kind: "error", Text: err.Error()})
				continue
			}
			dir, mode, pid = p.Path, p.ChatPermissionMode, p.ID
			save = func(msgs []agent.ChatMessage) error {
				prev, _ := sv.data.ProjectChat(p.ID)
				return sv.data.SetProjectChat(p.ID, authored(prev, msgs, user))
			}
			if s.projectChatOnRunner(p) {
				// Claude runs on the caller's runner, in its clone of the project.
				x, err := s.projectChatRunner(sv.c, p, msg.Runner)
				if err == nil && len(msg.Messages) > 0 && len(msg.Messages[len(msg.Messages)-1].Attachments) > 0 {
					err = errors.New("files can't be sent to the chat on your runner; paste or describe them instead")
				}
				switch {
				case err == nil:
					chatter = runnerChat{fleet: s.fleet(), runner: x, user: sv.c.User, relay: relay, project: p.ID, mode: p.ChatPermissionMode}
				case !s.localClaude():
					send(agent.ChatDelta{Kind: "error", Text: err.Error()})
					continue
				}
				// Else this Dash runs Claude itself and the chat stays here.
			}
		}
		if chatter == nil {
			send(agent.ChatDelta{Kind: "error", Text: "chat is not available on this Dash"})
			continue
		}
		msg.Messages = sv.chatAttachments(msg.Messages)
		prompt := withAttachments(msg.Messages)
		if msg.Todo != "" {
			t, err := sv.data.GetTodo(msg.Todo)
			if err != nil {
				send(agent.ChatDelta{Kind: "error", Text: err.Error()})
				continue
			}
			var diff string
			if p, err := sv.data.GetProject(t.ProjectID); err == nil {
				dir, mode = p.Path, p.ChatPermissionMode
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
			save = func(msgs []agent.ChatMessage) error {
				prev, _ := sv.data.TodoChat(t.ID)
				return sv.data.SetTodoChat(t.ID, authored(prev, msgs, user))
			}
			prompt = withTodoContext(t, diff, prompt)
			if msg.Review {
				if endReview, err = s.beginReview(sv, t.ID); err != nil {
					send(agent.ChatDelta{Kind: "error", Text: err.Error()})
					continue
				}
			}
		}
		var turnCtx context.Context
		turnCtx, cancelTurn = context.WithCancel(ctx)
		turnCtx = agent.WithApprover(turnCtx, approvals.forTurn(turnCtx))
		turnCtx = agent.WithPermissionMode(turnCtx, mode)
		turnCtx = agent.WithProject(turnCtx, pid)
		// The chat's tools act as its user in their org (for another Dash's
		// request, this Dash's env account in the default org).
		if owner := s.requestOwner(r); owner.Valid() {
			turnCtx = mcptools.WithCaller(turnCtx, owner)
		}
		if general {
			if err := save(msg.Messages); err != nil {
				slog.Error("save chat", "err", err)
			}
		}
		go func(ctx context.Context, history []agent.ChatMessage) {
			defer endReview()
			if general && !forHub {
				s.generalTurn(sv.c, 1)
				defer s.generalTurn(sv.c, -1)
			}
			var reply strings.Builder
			saved := false
			started := time.Now() // places the reply right after its question in a task's timeline
			err := chatter.Stream(ctx, dir, prompt, func(d agent.ChatDelta) {
				if d.Kind == "text" {
					reply.WriteString(d.Text)
				}
				if d.Kind == "done" && reply.Len() > 0 {
					// Save before telling the client, so a reload right after sees it.
					turn := append(history[:len(history):len(history)], agent.ChatMessage{Role: "assistant", Content: reply.String(), At: &started})
					if err := save(turn); err != nil {
						slog.Error("save chat", "err", err)
					}
					saved = true
				}
				send(d)
			})
			if err != nil && ctx.Err() == nil {
				slog.Warn("chat", "err", err)
				send(agent.ChatDelta{Kind: "error", Text: err.Error()})
			}
			// A turn that ended without an answer (rather than being cut short)
			// isn't resumed: take its question back out, as the client does.
			if general && !saved && ctx.Err() == nil && len(history) > 0 {
				if err := save(history[:len(history)-1]); err != nil {
					slog.Error("save chat", "err", err)
				}
			}
		}(turnCtx, msg.Messages)
	}
}

// beginReview marks a todo in review as being reviewed by a chat turn, which
// locks its status. The returned func clears the mark unless a later review
// turn has taken it over.
func (s *Server) beginReview(sv *service, id string) (func(), error) {
	s.reviewMu.Lock()
	defer s.reviewMu.Unlock()
	if _, err := sv.data.UpdateTodo(id, func(t *model.Todo) error {
		if t.Status != model.StatusReview {
			return fmt.Errorf("only tasks in review can be reviewed")
		}
		t.Reviewing = true
		return nil
	}); err != nil {
		return nil, err
	}
	s.reviewGen++
	gen := s.reviewGen
	if s.reviews == nil {
		s.reviews = map[string]uint64{}
	}
	s.reviews[id] = gen
	return func() {
		s.reviewMu.Lock()
		defer s.reviewMu.Unlock()
		if s.reviews[id] != gen {
			return
		}
		delete(s.reviews, id)
		if _, err := sv.data.UpdateTodo(id, func(t *model.Todo) error { t.Reviewing = false; return nil }); err != nil && !errors.Is(err, store.ErrNotFound) {
			slog.Warn("end review", "todo", id, "err", err)
		}
	}, nil
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

// chatAttachments replaces the attachments the client sent with this
// server's own records, so paths always come from the upload directory.
// Uploads that no longer exist are dropped.
func (sv *service) chatAttachments(history []agent.ChatMessage) []agent.ChatMessage {
	out := append([]agent.ChatMessage(nil), history...)
	for i, m := range out {
		if len(m.Attachments) == 0 {
			continue
		}
		var atts []model.Attachment
		for _, a := range m.Attachments {
			if found, err := sv.findUpload(a.ID); err == nil {
				atts = append(atts, found)
			}
		}
		out[i].Attachments = atts
	}
	return out
}

// authored sets who wrote each user message of msgs, a conversation the
// client sent in full: messages it already had (prev, as saved) keep their
// author, and new questions are user's. Claude's answers have none.
func authored(prev, msgs []agent.ChatMessage, user int64) []agent.ChatMessage {
	out := append([]agent.ChatMessage(nil), msgs...)
	for i := range out {
		switch {
		case out[i].Role != "user":
			out[i].CreatedBy = 0
		case i < len(prev) && prev[i].Role == out[i].Role && prev[i].Content == out[i].Content:
			out[i].CreatedBy = prev[i].CreatedBy
		default:
			out[i].CreatedBy = user
		}
	}
	return out
}

// withAttachments tells Claude where each message's attached files are.
func withAttachments(history []agent.ChatMessage) []agent.ChatMessage {
	out := append([]agent.ChatMessage(nil), history...)
	for i, m := range out {
		if len(m.Attachments) > 0 {
			out[i].Content = strings.TrimSpace(m.Content + "\n\n" + agent.AttachmentList(m.Attachments))
		}
	}
	return out
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
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if _, err := sv.process(r.PathValue("procid")); err != nil {
		writeErr(w, err)
		return
	}
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
