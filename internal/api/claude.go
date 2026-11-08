package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/tunnel"
)

// Where Claude runs for the general chat and the short calls (titles,
// summaries, cleaning up instructions).
//
// A hub with LocalClaude off never runs Claude itself: each of those runs
// on a runner of the user who asked, with that runner's `claude` login, so
// it is billed to them. The hub picks only runners the caller registered,
// in the org they work in; the runner checks the user it is told (see
// tunnel.UserHeader) against the one it was registered by. With none of
// theirs online, a title falls back to the start of the instructions, a
// summary to the first sentence of Claude's report, and the chat and the
// clean-up say so.

// SmallClaude is Claude's short calls on this computer (an agent.Titler).
type SmallClaude interface {
	Title(ctx context.Context, prompt string) (string, error)
	Summary(ctx context.Context, report string) (string, error)
	Refine(ctx context.Context, prompt string) (string, error)
}

// errNoClaudeRunner is why the hub can't run something on the caller's runner.
var errNoClaudeRunner = errors.New("no runner of yours is online: this hub runs Claude only on its users' own runners. " +
	"Start `dash runner` on your computer (Settings → Runners → Add a runner)")

// localClaude reports whether this Dash runs Claude itself.
func (s *Server) localClaude() bool {
	return (s.LocalClaude == nil || s.LocalClaude()) && agent.LocalAllowed()
}

// claudeRunners are c's own runners in c's org, by name: those the hub may
// run c's chat and short calls on.
func (s *Server) claudeRunners(c Caller) []tunnel.Runner {
	fl := s.fleet()
	if fl == nil || c.User == 0 {
		return nil
	}
	var out []tunnel.Runner
	for _, r := range fl.List() {
		if r.Owner != nil && r.Owner.UserID == c.User && r.OrgID() == c.Org {
			out = append(out, r)
		}
	}
	return out
}

// claudeRunner picks c's runner to run Claude on: want if it is one of
// c's (and online), else the first of c's that is online.
func (s *Server) claudeRunner(c Caller, want string) (tunnel.Runner, error) {
	rs := s.claudeRunners(c)
	if want != "" {
		i := slices.IndexFunc(rs, func(r tunnel.Runner) bool { return r.ID == want })
		if i < 0 {
			return tunnel.Runner{}, fmt.Errorf("%w: that runner isn't yours", errBadRequest)
		}
		if !rs[i].Online {
			return tunnel.Runner{}, fmt.Errorf("your runner %s is offline: start it, or pick another", rs[i].Name)
		}
		return rs[i], nil
	}
	for _, r := range rs {
		if r.Online {
			return r, nil
		}
	}
	return tunnel.Runner{}, errNoClaudeRunner
}

// smallCall runs one of Claude's short calls (kind: title, summary or
// refine) for sv's caller on their runner.
func (sv *service) smallCall(ctx context.Context, kind, text string) (string, error) {
	r, err := sv.s.claudeRunner(sv.c, "")
	if err != nil {
		return "", err
	}
	var out struct {
		Text string `json:"text"`
	}
	ctx = tunnel.WithUser(ctx, sv.c.User)
	if err := sv.s.fleet().Call(ctx, r.ID, http.MethodPost, "/api/hub/claude/"+kind, map[string]string{"text": text}, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Text) == "" {
		return "", fmt.Errorf("%s answered nothing", r.Name)
	}
	return out.Text, nil
}

// title names a task: here, on the caller's runner, or from the
// instructions' first line when neither can.
func (sv *service) title(ctx context.Context, prompt string) string {
	var title string
	var err error
	if sv.s.localClaude() {
		title, err = sv.s.Titler.Title(ctx, prompt)
	} else {
		title, err = sv.smallCall(ctx, "title", prompt)
	}
	if err != nil {
		slog.Debug("auto title", "err", err)
		return plainTitle(prompt)
	}
	return title
}

// maxPlainTitle is how long a title made from the instructions may be, in runes.
const maxPlainTitle = 60

// plainTitle is a title without Claude: the instructions' first non-empty
// line, without Markdown heading or list marks, cut at a word.
func plainTitle(prompt string) string {
	var line string
	for l := range strings.SplitSeq(prompt, "\n") {
		if l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#>*-+ ")); l != "" {
			line = l
			break
		}
	}
	line = strings.Join(strings.Fields(line), " ")
	if r := []rune(line); len(r) > maxPlainTitle {
		cut := string(r[:maxPlainTitle])
		if i := strings.LastIndexByte(cut, ' '); i > maxPlainTitle/2 {
			cut = cut[:i]
		}
		line = strings.TrimRight(cut, " ,.;:") + "…"
	}
	return line
}

// refine cleans up instructions here or on the caller's runner.
func (sv *service) refine(ctx context.Context, prompt string) (string, error) {
	if sv.s.localClaude() {
		if sv.s.Refiner == nil {
			return "", errNoRefiner
		}
		return sv.s.Refiner.Refine(ctx, prompt)
	}
	return sv.smallCall(ctx, "refine", prompt)
}

var errNoRefiner = errors.New("cleaning up instructions is not available")

// claudeInfo handles GET /api/claude: where the caller's chat runs, for the
// chat's header. local: this Dash runs it; else on one of runners.
func (s *Server) claudeInfo(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	type runner struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Online bool   `json:"online"`
	}
	out := struct {
		Local   bool     `json:"local"`
		Runners []runner `json:"runners"`
	}{Local: s.localClaude(), Runners: []runner{}}
	for _, x := range s.claudeRunners(sv.c) {
		out.Runners = append(out.Runners, runner{ID: x.ID, Name: x.Name, Online: x.Online})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- a runner's side: Claude for its owner, asked by the hub ----

// forOwner reports whether r, from the hub, is made for the user who
// registered this runner. A runner registered with an older hub doesn't
// know its owner and takes the hub's word (the hub sends only the
// owner's).
func (s *Server) forOwner(r *http.Request) error { return s.onlyForOwner(r, "runs Claude") }

// onlyForOwner is forOwner for something else this runner does only for
// its owner (does: "runs Claude", "lists its repositories", …).
func (s *Server) onlyForOwner(r *http.Request, does string) error {
	if !auth.FromHub(r.Context()) {
		return errors.New("only this runner's hub asks it")
	}
	uid, err := strconv.ParseInt(r.Header.Get(tunnel.UserHeader), 10, 64)
	if err != nil || uid == 0 {
		return fmt.Errorf("this runner %s only for its owner, and the hub didn't say whom this is for", does)
	}
	if s.HubUser != 0 && uid != s.HubUser {
		slog.Warn("runner: refused a request for another user", "does", does, "user", uid, "owner", s.HubUser)
		return fmt.Errorf("this runner %s only for the user who registered it", does)
	}
	return nil
}

// hubClaude handles POST /api/hub/claude/{kind}: one of Claude's short
// calls for this runner's owner, {"text"} in and out.
func (s *Server) hubClaude(w http.ResponseWriter, r *http.Request) {
	if err := s.forOwner(r); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if s.Small == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "this runner doesn't run Claude's short calls"})
		return
	}
	var call func(context.Context, string) (string, error)
	switch r.PathValue("kind") {
	case "title":
		call = s.Small.Title
	case "summary":
		call = s.Small.Summary
	case "refine":
		call = s.Small.Refine
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	out, err := call(r.Context(), req.Text)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": out})
}

// ---- the general chat on the caller's runner ----

// chatRelay is the runner's chat socket of the turn a hub's chat connection
// is running, for the browser's answers to its permission requests.
type chatRelay struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (c *chatRelay) set(conn *websocket.Conn) {
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
}

// send passes v on to the runner, if a turn is running there.
func (c *chatRelay) send(v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_ = c.conn.WriteJSON(v)
	}
}

// runnerChat runs general chat turns on runner, a runner of user's, through
// its /ws/chat: the hub keeps the conversation, the runner runs `claude`.
type runnerChat struct {
	fleet  *tunnel.Hub
	runner tunnel.Runner
	user   int64
	relay  *chatRelay
}

func (c runnerChat) Stream(ctx context.Context, _ string, history []agent.ChatMessage, emit func(agent.ChatDelta)) error {
	emit(agent.ChatDelta{Kind: "runner", Text: c.runner.Name})
	conn, err := c.fleet.DialWS(tunnel.WithUser(ctx, c.user), c.runner.ID, "/ws/chat")
	if err != nil {
		return err
	}
	defer conn.Close()
	c.relay.set(conn)
	defer c.relay.set(nil)
	// Ending the turn here (a new message, the browser leaving) closes the
	// runner's socket, which ends its turn and denies what waits for approval.
	defer context.AfterFunc(ctx, func() { conn.Close() })()
	_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if err := conn.WriteJSON(map[string]any{"type": "send", "messages": history}); err != nil {
		return fmt.Errorf("%s: %w", c.runner.Name, err)
	}
	for {
		var d agent.ChatDelta
		if err := conn.ReadJSON(&d); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("lost the connection to %s", c.runner.Name)
		}
		if d.Kind == "cwd" {
			d.Text += " on " + c.runner.Name
		}
		emit(d)
		if d.Kind == "done" || d.Kind == "error" {
			return nil
		}
	}
}

// chatScratch is the directory the general chat runs in on a runner, for
// its owner asking through the hub: none of the runner's projects.
func (s *Server) chatScratch() (string, error) {
	dir := s.ChatDir
	if dir == "" {
		dir = os.TempDir()
	}
	return dir, os.MkdirAll(dir, 0o700)
}
