package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tunnel"
)

// "!" commands in the chat (see chatWS and agent.RunShell): a project's or
// a task's chat runs a command the user typed with a leading "!" where
// Claude would run, instead of asking Claude. On a hub that is always the
// runner the chat runs on (runnerChat.Shell), never the hub; a runner runs
// the hub's only with -hub-commands, for its owner (hubShell), and none when
// its chat is read-only.

var errReadOnlyShell = errors.New("this chat is read-only (-chat-tools readonly, or -chat api): it runs no commands, so ! commands are off too")

// errHubShell is why the hub may not run a "!" command on this runner.
const errHubShell = "this runner doesn't run ! commands for the hub; start it with -hub-commands to allow its owner to"

// hubShell reports whether the hub may run a "!" command on this runner for
// r: as for a terminal (hubTerminal), only with -hub-commands and only for
// the user who registered the runner, since the command is the hub's to
// choose.
func (s *Server) hubShell(r *http.Request) error {
	if !s.HubCommands {
		return errors.New(errHubShell)
	}
	if s.HubUser == 0 {
		return errors.New("this runner doesn't know who registered it, so it runs no ! commands for the hub; register it again with a new code")
	}
	return s.onlyForOwner(r, "runs ! commands")
}

// shellRequest is the command of a "shell" message's conversation: its last
// message, typed with a leading "!".
func shellRequest(general bool, msgs []agent.ChatMessage) (string, error) {
	if general {
		return "", errors.New("! commands run in a project's or a task's chat, in its checkout or worktree; the general chat has neither")
	}
	if len(msgs) == 0 || msgs[len(msgs)-1].Role != "user" {
		return "", fmt.Errorf("%w: no command to run", errBadRequest)
	}
	cmd, ok := agent.ShellCommand(msgs[len(msgs)-1].Content)
	if !ok {
		return "", fmt.Errorf("%w: a command to run starts with \"!\"", errBadRequest)
	}
	return cmd, nil
}

// shellAllowed reports why chatter's chat can't run a "!" command for r, if
// it can't. A chat on a runner is the runner's to allow (with -hub-commands,
// its owner only, and not a read-only chat), and goes only to one that said
// it runs them (an older runner wouldn't answer); the hub runs none itself.
func (s *Server) shellAllowed(r *http.Request, chatter agent.Chatter) error {
	if rc, ok := chatter.(runnerChat); ok {
		if rc.runner.Reported == nil || !rc.runner.Reported.ChatCommands {
			return fmt.Errorf("your runner %s doesn't run ! commands: it wasn't started with -hub-commands, its chat runs no commands (-chat api, or -chat-tools readonly), or it is older than this hub (update it)", rc.runner.Name)
		}
		return nil
	}
	if s.hub() || !localrun.Allowed() {
		return errors.New("this hub runs no commands itself: ! commands run on your runner linked to the project")
	}
	if agent.ReadOnlyChat(chatter) {
		return errReadOnlyShell
	}
	if auth.FromHub(r.Context()) {
		return s.hubShell(r)
	}
	return nil
}

// chatCommands reports whether this Dash's own chat runs "!" commands (for
// the UI: a hub's chats run on runners, which say for themselves).
func (s *Server) chatCommands() bool {
	return s.Chat != nil && !s.hub() && !agent.ReadOnlyChat(s.Chat)
}

// chatShell runs one "!" command for a chat connection.
type chatShell struct {
	chatter agent.Chatter // a runnerChat runs it on its runner; else here
	dir     string
	command string
	id      string // the client's, sent back on every delta
	timeout time.Duration
	send    func(agent.ChatDelta)
}

func (c chatShell) emit(d agent.ChatDelta) {
	d.ID = c.id
	c.send(d)
}

// run runs the command and returns history with its result on the last
// message (the command), or false when it couldn't (the client has been
// told why). Stopped (ctx ends), it still returns what it printed so far.
func (c chatShell) run(ctx context.Context, history []agent.ChatMessage) ([]agent.ChatMessage, bool) {
	started := time.Now()
	var res model.ChatShell
	if rc, ok := c.chatter.(runnerChat); ok {
		var err error
		if res, err = rc.Shell(ctx, history[len(history)-1], c.emit); err != nil {
			c.emit(agent.ChatDelta{Kind: "error", Text: err.Error()})
			return nil, false
		}
		res.Runner = rc.runner.Name
	} else {
		c.emit(agent.ChatDelta{Kind: "cwd", Text: c.dir})
		res = agent.RunShell(ctx, c.dir, c.command, c.timeout, c.emit)
	}
	m := history[len(history)-1]
	if m.At == nil {
		m.At = &started
	}
	m.Shell = &res
	return append(history[:len(history)-1:len(history)-1], m), true
}

// finish tells the client the command ended, and how.
func (c chatShell) finish(res model.ChatShell) {
	c.emit(agent.ChatDelta{Kind: "shell_done", Shell: &res})
}

// Shell runs msg's "!" command on c's runner, through its /ws/chat as a
// turn would run, streaming its output to emit. Ending ctx stops it there
// (a "cancel"), and what it printed until then still comes back.
func (c runnerChat) Shell(ctx context.Context, msg agent.ChatMessage, emit func(agent.ChatDelta)) (model.ChatShell, error) {
	emit(agent.ChatDelta{Kind: "runner", Text: c.runner.Name})
	// Not cancelled with ctx: the runner is asked to stop it instead, below.
	conn, err := c.fleet.DialWS(tunnel.WithUser(context.WithoutCancel(ctx), c.user), c.runner.ID, "/ws/chat")
	if err != nil {
		return model.ChatShell{}, err
	}
	defer conn.Close()
	var wmu sync.Mutex
	write := func(v any) error {
		wmu.Lock()
		defer wmu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteJSON(v)
	}
	defer context.AfterFunc(ctx, func() {
		_ = write(map[string]string{"type": "cancel"})
		// A runner that doesn't answer is given up on.
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	})()
	req := map[string]any{"type": "shell", "project": c.project, "mode": c.mode, "hub": true,
		"messages": []agent.ChatMessage{{Role: "user", Content: msg.Content}}}
	if c.todo != "" {
		req["todo"] = c.todo
	}
	if err := write(req); err != nil {
		return model.ChatShell{}, fmt.Errorf("%s: %w", c.runner.Name, err)
	}
	for {
		var d agent.ChatDelta
		if err := conn.ReadJSON(&d); err != nil {
			return model.ChatShell{}, fmt.Errorf("lost the connection to %s", c.runner.Name)
		}
		switch d.Kind {
		case "shell_done":
			if d.Shell == nil {
				return model.ChatShell{}, fmt.Errorf("%s didn't say how the command ended", c.runner.Name)
			}
			res := *d.Shell
			// The runner caps it already; don't take more than that from it.
			out, cut := agent.CapShellOutput(res.Output)
			res.Output, res.Truncated = out, res.Truncated || cut
			return res, nil
		case "error":
			return model.ChatShell{}, errors.New(d.Text)
		case "cwd":
			d.Text += " on " + c.runner.Name
		}
		emit(d)
	}
}
