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
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/preview"
	"github.com/klahr/dash/internal/runner"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// chatWS streams Claude responses. Protocol (JSON text frames):
//
//	client → {"type":"send","project":"<id>","todo":"<id>","messages":[{"role":"user","content":"…"}, …]}
//	client → {"type":"cancel"} stops the reply streaming, keeping what it said so far
//	client → {"type":"permission","id":"<id>","decision":"allow"|"deny"}
//	client → {"type":"shell","id":"<id>","project":"<id>","todo":"<id>","messages":[…, {"role":"user","content":"!<command>"}]}
//	server → {"kind":"thinking"|"text"|"done"|"error","text":"…"}
//	server → {"kind":"cwd","text":"<dir>"} where the turn runs
//	server → {"kind":"tool_use","id":"…","tool":"Bash","input":{…}}
//	server → {"kind":"tool_result","id":"…","text":"<output>","is_error":bool}
//	server → {"kind":"permission","id":"<id>","tool":"Bash","input":{…}} asks to approve a tool call
//	server → {"kind":"permission_decided","id":"<id>","text":"allow"|"deny"|"timeout"}
//	server → {"kind":"shell_output","id":"<id>","text":"<output so far>"}
//	server → {"kind":"shell_done","id":"<id>","shell":{"command","output","exit_code",…}}
//	server → {"kind":"suggestion","text":"<the user's likely next message>"} after "done"
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
//
// A "shell" message runs a command the user typed with a leading "!" in a
// project's or a task's chat instead of asking Claude (see chatShell): in
// the directory Claude would run in, on the same runner, and its last
// message, with the command's result added, is saved like a turn. "cancel"
// stops it; its "id" comes back on its deltas.
//
// With "suggest": true on a "send", once Claude's reply is done and saved, a
// short call to the small model (see agent.Titler.Suggest) guesses what the
// user will say next, sent as a "suggestion" after "done" (nothing when it
// has no guess, fails or no runner is online). It runs where the turn's
// short calls run (this Dash, or the caller's runner on a hub), and is
// dropped when another message arrives first. Without it, no call is made.
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
	// The client sends the whole conversation each turn, which is kept in
	// memory and saved: refuse a longer one (the browser sees close code
	// 1009). A hub's request has the task's context and changes added.
	if auth.FromHub(r.Context()) {
		conn.SetReadLimit(maxChatFrame + maxChatDiff + 1<<20)
	} else {
		conn.SetReadLimit(maxChatFrame)
	}

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
	// stopTurn marks the running turn stopped by the user (a "cancel"), which
	// keeps what it said so far, before cancelTurn cuts it short.
	stopTurn := func() {}
	// turnDone closes when the last turn has finished saving.
	idle := make(chan struct{})
	close(idle)
	var turnDone <-chan struct{} = idle
	// turnSeq numbers the turns, so a stopped turn whose save comes late
	// (after the next send stopped waiting for it) doesn't write over the
	// newer conversation. Guarded by turnMu, held across that late save.
	var turnMu sync.Mutex
	var turnSeq uint64
	approvals := &chatApprovals{send: send, timeout: chatApprovalTimeout}
	relay := &chatRelay{} // the turn running on the caller's runner, if any

	for {
		var msg chatRequest
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
		if msg.Type == "cancel" {
			stopTurn()
		}
		cancelTurn()
		if msg.Type != "send" && msg.Type != "shell" {
			continue
		}
		shell := msg.Type == "shell"
		// Let a stopped turn save what it said before this one saves the
		// conversation (which already has it).
		select {
		case <-turnDone:
		case <-time.After(5 * time.Second):
		}
		if err := sv.writable(); err != nil {
			// The chat runs Claude, which can change the projects.
			send(agent.ChatDelta{Kind: "error", Text: err.Error()})
			continue
		}
		var command string
		if shell {
			var err error
			if command, err = shellRequest(msg.general(), msg.Messages); err != nil {
				send(agent.ChatDelta{Kind: "error", ID: msg.ID, Text: err.Error()})
				continue
			}
		}
		ct, err := s.resolveChatTurn(ctx, r, sv, msg, shell, relay)
		if err != nil {
			send(agent.ChatDelta{Kind: "error", Text: err.Error()})
			continue
		}
		if shell {
			if err := s.shellAllowed(r, ct.chatter); err != nil {
				send(agent.ChatDelta{Kind: "error", ID: msg.ID, Text: err.Error()})
				continue
			}
		}
		var turnCtx context.Context
		turnCtx, cancelTurn = context.WithCancel(ctx)
		turnCtx = agent.WithApprover(turnCtx, approvals.forTurn(turnCtx))
		turnCtx = agent.WithPermissionMode(turnCtx, ct.mode)
		turnCtx = agent.WithProject(turnCtx, ct.pid)
		// The chat's tools act as its user in their org (for another Dash's
		// request, this Dash's env account in the default org).
		if owner := s.requestOwner(r); owner.Valid() {
			turnCtx = mcptools.WithCaller(turnCtx, owner)
		}
		if ct.forHub {
			// The hub's chat: its tools act on the hub's board.
			turnCtx = mcptools.ForHub(turnCtx, msg.Project)
		}
		// From here this turn's conversation is the newest: a stopped turn
		// still saving finishes first, a later one skips its save.
		turnMu.Lock()
		turnSeq++
		seq := turnSeq
		turnMu.Unlock()
		// saveIfNewest saves a turn that ends late, unless a newer turn
		// already has the conversation.
		saveIfNewest := func(turn []agent.ChatMessage) {
			turnMu.Lock()
			defer turnMu.Unlock()
			if turnSeq == seq {
				if err := ct.save(turn); err != nil {
					slog.Error("save chat", "err", err)
				}
			}
		}
		if shell {
			stopTurn = func() {}
			sh := chatShell{chatter: ct.chatter, dir: ct.dir, command: command, id: msg.ID, timeout: s.ChatCommandTimeout, send: send}
			turnDone = runShellTurn(turnCtx, sh, ct.history, saveIfNewest)
			continue
		}
		var stopped atomic.Bool
		stopTurn = func() { stopped.Store(true) }
		// suggestNext guesses the user's next message once the reply is
		// saved. A runner answering its hub leaves that to the hub, which
		// asks it as a short call. The turn's context ends with the next
		// message: a late guess is dropped.
		var suggestNext func([]agent.ChatMessage)
		if msg.Suggest && !ct.forHub {
			suggestNext = func(answered []agent.ChatMessage) {
				go sendSuggestion(turnCtx, sv, answered, func(text string) {
					turnMu.Lock()
					defer turnMu.Unlock()
					if turnSeq == seq && turnCtx.Err() == nil {
						send(agent.ChatDelta{Kind: "suggestion", Text: text})
					}
				})
			}
		}
		turnDone = s.runClaudeTurn(turnCtx, sv.c, ct, &stopped, send, saveIfNewest, suggestNext)
	}
}

// chatRequest is a message from the chat client (see chatWS).
type chatRequest struct {
	Type     string              `json:"type"`
	Project  string              `json:"project"`
	Todo     string              `json:"todo"`
	Messages []agent.ChatMessage `json:"messages"`
	ID       string              `json:"id"`
	Decision string              `json:"decision"`
	Review   bool                `json:"review"`  // with "todo": the turn reviews it, locking its status
	Runner   string              `json:"runner"`  // chat on a hub that doesn't run Claude: which of the caller's runners
	Mode     string              `json:"mode"`    // a hub's project chat on its runner: the hub project's permission mode
	Suggest  bool                `json:"suggest"` // guess the user's next message once the reply is done
	// Hub marks a hub's own chat on its runner (runnerChat): Project
	// and Todo are the hub's ids, not the runner's.
	Hub bool `json:"hub"`
}

// general reports whether m is for the general chat, not a project's or a task's.
func (m chatRequest) general() bool { return m.Project == "" && m.Todo == "" }

// chatSetup is what one "send" or "shell" message runs: on which chatter,
// where and with which permission mode, and how its conversation is saved.
type chatSetup struct {
	chatter   agent.Chatter
	dir, mode string
	pid       string // the project the turn is in, if any
	// history is the conversation the client sent, with this server's
	// records of its attachments; prompt is what Claude is given for it.
	history, prompt []agent.ChatMessage
	save            func([]agent.ChatMessage) error
	endReview       func() // ends a task review the turn began
	general         bool   // the general chat
	forHub          bool   // this runner's owner's chat, asked through the hub
}

// resolveChatTurn works out where msg's turn runs and how it is saved: on
// this Dash or on one of the caller's runners, in a scratch directory, a
// project's checkout or a task's worktree. Its error is the client's to see.
func (s *Server) resolveChatTurn(ctx context.Context, r *http.Request, sv *service, msg chatRequest, shell bool, relay *chatRelay) (chatSetup, error) {
	ct := chatSetup{chatter: s.Chat, endReview: func() {}, general: msg.general()}
	// Each conversation is saved with who wrote its new questions.
	user := sv.c.User
	ct.save = func(msgs []agent.ChatMessage) error {
		return sv.data.SetChat(user, authored(sv.data.Chat(user), msgs, user))
	}
	// onRunner is the chat on x, the caller's runner, unless finding it
	// failed or the question has files, which a runner can't read.
	onRunner := func(x tunnel.Runner, err error, project, todo, mode string) (agent.Chatter, error) {
		if err == nil {
			err = noAttachments(msg.Messages)
		}
		if err != nil {
			return nil, err
		}
		return runnerChat{fleet: s.fleet(), runner: x, user: sv.c.User, relay: relay, project: project, todo: todo, mode: mode}, nil
	}
	// A chat for this runner's owner, asked through the hub.
	ct.forHub = auth.FromHub(r.Context())
	// ownViaHub: a chat in one of this runner's own projects (made or
	// added here, which the hub keeps no record of), opened through the
	// hub at /peer/{runner}/…. It runs and is saved here, as on this
	// runner, where the hub's UI reads it back from.
	ownViaHub := false
	if ct.forHub {
		if err := s.forOwner(r); err != nil {
			return ct, err
		}
		if msg.Project != "" {
			if _, linked := sv.hubProject(msg.Project); !linked {
				if msg.Hub {
					// The hub's project, which this runner has no checkout of.
					return ct, errors.New("this runner has no clone of that project: link the project to it in Project settings → Runners")
				}
				if _, err := sv.ownProject(msg.Project); err != nil {
					return ct, errors.New("this runner has no project with that id")
				}
				ct.forHub, ownViaHub = false, true
				user = hubUser(r) // the runner's owner, checked above
			}
		}
	}
	// ownMode is mode, or the one the hub sent for this runner's own project.
	ownMode := func(mode string) string {
		if ownViaHub && msg.Mode != "" && validChatMode(msg.Mode) {
			return msg.Mode
		}
		return mode
	}
	var runnerDiff, runnerBranch string // a hub task's changes, as this runner has them
	switch {
	case ct.forHub:
		// This runner's owner, through the hub: Claude runs here, in a
		// scratch directory, the runner's clone of the hub's project or
		// its worktree of the hub's task, and the hub keeps the
		// conversation (with the task's context already in it).
		if msg.Todo != "" && msg.Project == "" {
			return ct, errors.New("the hub didn't say which project the task is in")
		}
		if msg.Project != "" {
			p, _ := sv.hubProject(msg.Project) // found above
			ct.dir, ct.mode, ct.pid = p.Path, p.ChatPermissionMode, p.ID
			if validChatMode(msg.Mode) {
				ct.mode = msg.Mode // the hub's project settings are the ones its users edit
			}
			if t, ok := sv.hubTodo(msg.Todo); ok && t.ProjectID == p.ID {
				// Let Claude read the files as the task left them here.
				var dir string
				dir, runnerBranch, runnerDiff = s.taskWorktree(ctx, p, t, shell)
				if dir != "" {
					ct.dir = dir
				}
			}
		} else {
			var err error
			if ct.dir, err = s.chatScratch(); err != nil {
				return ct, err
			}
		}
		ct.save = func([]agent.ChatMessage) error { return nil }
	case ct.general && sv.c.User == 0:
		return ct, errors.New("the general chat is a person's: sign in to use it")
	case ct.general && s.hub():
		// This hub doesn't run Claude: the caller's own runner does.
		x, err := s.claudeRunner(sv.c, msg.Runner)
		if ct.chatter, err = onRunner(x, err, "", "", ""); err != nil {
			return ct, err
		}
	}
	if msg.Project != "" && msg.Todo == "" && !ct.forHub {
		p, err := sv.data.GetProject(msg.Project)
		if err != nil {
			return ct, err
		}
		ct.dir, ct.mode, ct.pid = p.Path, ownMode(p.ChatPermissionMode), p.ID
		ct.save = func(msgs []agent.ChatMessage) error {
			prev, _ := sv.data.ProjectChat(p.ID)
			return sv.data.SetProjectChat(p.ID, authored(prev, msgs, user))
		}
		if s.projectChatOnRunner(p) {
			// Claude runs on the caller's runner, in its clone of the project.
			x, err := s.projectChatRunner(sv.c, p, msg.Runner)
			switch c, err := onRunner(x, err, p.ID, "", p.ChatPermissionMode); {
			case err == nil:
				ct.chatter = c
			case s.hub():
				return ct, err
			}
			// Else this Dash runs Claude itself and the chat stays here.
		}
	}
	ct.history = sv.chatAttachments(msg.Messages)
	ct.prompt = withAttachments(agent.WithShellText(ct.history))
	if ct.forHub && msg.Todo != "" {
		ct.prompt = withRunnerChanges(runnerBranch, runnerDiff, ct.prompt)
	}
	if msg.Project != "" && msg.Todo == "" && !ct.forHub && !shell {
		// The user is trying the project in its Try terminal: the chat
		// reads it and files what they find (see TryInstructions).
		if p, err := sv.data.GetProject(msg.Project); err == nil {
			ct.prompt = withTryContext(s.tryContext(ctx, sv.c, p), ct.prompt)
		}
	}
	if msg.Todo != "" && !ct.forHub {
		t, err := sv.data.GetTodo(msg.Todo)
		if err == nil && ownViaHub && t.ProjectID != msg.Project {
			err = errors.New("this runner has no task with that id in that project")
		}
		if err != nil {
			return ct, err
		}
		var diff string
		if p, err := sv.data.GetProject(t.ProjectID); err == nil {
			ct.dir, ct.mode = p.Path, ownMode(p.ChatPermissionMode)
			runs := false
			if s.todoChatOnRunner(p, t) {
				// Claude runs on the caller's runner: the one that ran the
				// task, in its worktree, else one linked to the project, in
				// its clone. The runner adds the changes it has.
				x, err := s.todoChatRunner(sv.c, p, t, msg.Runner)
				switch c, err := onRunner(x, err, p.ID, t.ID, p.ChatPermissionMode); {
				case err == nil:
					ct.chatter, runs = c, true
				case s.hub():
					return ct, err
				}
				// Else this Dash runs Claude itself and the chat stays here.
			}
			if !runs {
				// Let Claude read the files as the todo left them.
				var dir string
				if dir, _, diff = s.taskWorktree(ctx, p, t, shell); dir != "" {
					ct.dir = dir
				}
			}
		}
		ct.save = func(msgs []agent.ChatMessage) error {
			prev, _ := sv.data.TodoChat(t.ID)
			return sv.data.SetTodoChat(t.ID, authored(prev, msgs, user))
		}
		ct.prompt = withTodoContext(t, diff, ct.prompt)
		if ct.chatter == nil {
			return ct, errChatUnavailable
		}
		if msg.Review && !shell {
			if ct.endReview, err = s.beginReview(sv, t.ID); err != nil {
				return ct, err
			}
		}
	}
	if ct.chatter == nil {
		return ct, errChatUnavailable
	}
	return ct, nil
}

var errChatUnavailable = errors.New("chat is not available on this Dash")

// taskWorktree is where a chat about t reads its files: t's worktree while
// it has one that isn't merged ("" otherwise), with its branch; and, for a
// Claude turn rather than a shell command, the changes t has made.
func (s *Server) taskWorktree(ctx context.Context, p model.Project, t model.Todo, shell bool) (dir, branch, diff string) {
	if t.Worktree == nil {
		return "", "", ""
	}
	if _, err := os.Stat(t.Worktree.Path); err == nil && t.Status != model.StatusMerged {
		dir, branch = t.Worktree.Path, t.Worktree.Branch
	}
	if s.Git != nil && !shell {
		var err error
		if diff, err = s.todoChanges(ctx, p, t); err != nil {
			slog.Warn("task chat diff", "todo", t.ID, "err", err)
		}
	}
	return dir, branch, diff
}

// runShellTurn runs sh, a "!" command, and saves the conversation with its
// result (unless a newer turn already has it) before the client hears it
// ended, so a reload has it. The channel closes when it has.
func runShellTurn(ctx context.Context, sh chatShell, history []agent.ChatMessage, saveIfNewest func([]agent.ChatMessage)) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		turn, ok := sh.run(ctx, history)
		if !ok {
			return
		}
		saveIfNewest(turn)
		sh.finish(*turn[len(turn)-1].Shell)
	}()
	return done
}

// runClaudeTurn streams ct's reply to the client and saves the
// conversation: when the reply is done, or with what it said so far when
// the user stopped it. The channel closes when it has finished saving.
// suggestNext, if set, is given the saved conversation of a reply that is
// done (see sendSuggestion).
func (s *Server) runClaudeTurn(ctx context.Context, c Caller, ct chatSetup, stopped *atomic.Bool, send func(agent.ChatDelta), saveIfNewest func([]agent.ChatMessage), suggestNext func([]agent.ChatMessage)) <-chan struct{} {
	history := ct.history
	if ct.general {
		if err := ct.save(history); err != nil {
			slog.Error("save chat", "err", err)
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer ct.endReview()
		if ct.general && !ct.forHub {
			s.generalTurn(c, 1)
			defer s.generalTurn(c, -1)
		}
		var reply strings.Builder
		var usage *model.ChatUsage
		saved := false
		var turn []agent.ChatMessage // the conversation with its new reply, once saved
		started := time.Now()        // places the reply right after its question in a task's timeline
		answered := func() []agent.ChatMessage {
			return append(history[:len(history):len(history)], agent.ChatMessage{Role: "assistant", Content: reply.String(), At: &started, Usage: usage})
		}
		err := ct.chatter.Stream(ctx, ct.dir, ct.prompt, func(d agent.ChatDelta) {
			if ctx.Err() != nil {
				return // cut short: the client has moved on
			}
			if d.Kind == "text" {
				reply.WriteString(d.Text)
			}
			if d.Kind == "usage" {
				usage = d.Usage
			}
			if d.Kind == "done" && reply.Len() > 0 {
				// Save before telling the client, so a reload right after sees it.
				turn = answered()
				if err := ct.save(turn); err != nil {
					slog.Error("save chat", "err", err)
				}
				saved = true
			}
			send(d)
		})
		if stopped.Load() && !saved {
			// Stopped by the user: keep what it said so far, as the client
			// does, or (with nothing said) take the question back out.
			switch {
			case reply.Len() > 0:
				saveIfNewest(answered())
			case ct.general && len(history) > 0:
				saveIfNewest(history[:len(history)-1])
			}
			return
		}
		if err != nil && ctx.Err() == nil {
			slog.Warn("chat", "err", err)
			send(agent.ChatDelta{Kind: "error", Text: err.Error()})
		}
		if suggestNext != nil && saved && err == nil && ctx.Err() == nil {
			suggestNext(turn)
		}
		// A turn that ended without an answer (rather than being cut short)
		// isn't resumed: take its question back out, as the client does.
		if ct.general && !saved && ctx.Err() == nil && len(history) > 0 {
			if err := ct.save(history[:len(history)-1]); err != nil {
				slog.Error("save chat", "err", err)
			}
		}
	}()
	return done
}

// suggestionTimeout gives up on a chat suggestion: by then the user is typing.
const suggestionTimeout = 45 * time.Second

// sendSuggestion guesses the user's next message after turn and passes it
// to deliver, unless there is none.
func sendSuggestion(ctx context.Context, sv *service, turn []agent.ChatMessage, deliver func(string)) {
	ctx, cancel := context.WithTimeout(ctx, suggestionTimeout)
	defer cancel()
	if text := sv.suggest(ctx, turn); text != "" {
		deliver(text)
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

// noAttachments refuses a question with files for a chat that runs on a
// runner, which can't read the hub's uploads.
func noAttachments(history []agent.ChatMessage) error {
	if len(history) > 0 && len(history[len(history)-1].Attachments) > 0 {
		return errors.New("files can't be sent to the chat on your runner; paste or describe them instead")
	}
	return nil
}

// withRunnerChanges adds, on a runner, the changes its copy of a hub's task
// has made (diff, on branch when Claude runs in its worktree) to the
// conversation the hub sent, which already says what the task is.
func withRunnerChanges(branch, diff string, history []agent.ChatMessage) []agent.ChatMessage {
	if len(history) == 0 || diff == "" {
		return history
	}
	if len(diff) > maxChatDiff {
		diff = diff[:maxChatDiff] + "\n[diff truncated]"
	}
	var b strings.Builder
	if branch != "" {
		fmt.Fprintf(&b, "<worktree branch=%q>The files you can read are this worktree's, with the changes below applied.</worktree>\n", branch)
	}
	fmt.Fprintf(&b, "<changes>\n%s\n</changes>\n\n", diff)
	out := append([]agent.ChatMessage(nil), history...)
	out[0].Content = b.String() + out[0].Content
	return out
}

// maxChatFrame caps a chat client's message, the whole conversation with
// the new question, which caps how long a conversation can grow.
const maxChatFrame = 8 << 20

// maxChatDiff caps the patch given to the todo chat each turn.
const maxChatDiff = 200_000

// withTryContext puts note (TryInstructions, or "") before the newest
// question, so it describes the session as it is now.
func withTryContext(note string, history []agent.ChatMessage) []agent.ChatMessage {
	if note == "" || len(history) == 0 {
		return history
	}
	out := append([]agent.ChatMessage(nil), history...)
	out[len(out)-1].Content = note + out[len(out)-1].Content
	return out
}

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
	s.terminalWS(w, r, sess, nil)
}

// previewTerminalWS handles GET /ws/todos/{tid}/preview: it attaches to the
// task's terminal preview, with procWS's protocol. Typing keeps the preview
// from being stopped as idle. A runner's task is reached the same way under
// /peer/{runner}/.
func (s *Server) previewTerminalWS(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if s.Previews == nil {
		writeErr(w, preview.ErrNotFound)
		return
	}
	tid := r.PathValue("tid")
	if _, err := sv.data.GetTodo(tid); err != nil {
		writeErr(w, preview.ErrNotFound)
		return
	}
	sess, touch, err := s.Previews.Terminal(tid)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.terminalWS(w, r, sess, touch)
}

// terminalWS streams sess to xterm.js over a WebSocket (see procWS);
// onInput, if set, is called on every keystroke.
func (s *Server) terminalWS(w http.ResponseWriter, r *http.Request, sess *runner.Session, onInput func()) {
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
				if onInput != nil {
					onInput()
				}
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
	if p, err := s.Procs.Get(sess.Info.ID); err == nil && !p.Running && p.ExitCode != nil {
		b, _ := json.Marshal(map[string]any{"type": "exit", "code": *p.ExitCode})
		_ = write(websocket.TextMessage, b)
	}
}
