package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/mcptools"
)

// ACPChat answers chat through an ACP agent. Each turn is a fresh session
// with the conversation so far in the prompt, as ClaudeCodeChat's are.
//
// It only reads: Dash rejects every permission request the agent sends,
// but for the Dash task tools that need no approval. Reads the agent
// doesn't ask about still happen.
type ACPChat struct {
	Provider ACP
	Dir      string // working directory of the global chat; default os.TempDir()
	Board    *mcptools.Tokens
	BoardURL string
}

// ReadOnly: the chat changes nothing.
func (ACPChat) ReadOnly() bool { return true }

// Chat is the read-only chat of the agent.
func (a ACP) Chat(cfg ChatConfig) Chatter {
	return ACPChat{Provider: a, Dir: cfg.Dir, Board: cfg.Board, BoardURL: cfg.BoardURL}
}

func (c ACPChat) Stream(ctx context.Context, dir string, history []ChatMessage, emit func(ChatDelta)) error {
	if len(history) == 0 {
		return fmt.Errorf("empty conversation")
	}
	if err := checkLocal(); err != nil {
		return err
	}
	a, p := c.Provider, c.Provider.Profile
	if err := a.ready(); err != nil {
		return err
	}
	cwd := dir
	if cwd == "" {
		cwd = c.Dir
	}
	if cwd == "" {
		cwd = os.TempDir()
	} else if err := os.MkdirAll(cwd, 0o755); err != nil {
		return err
	}
	emit(ChatDelta{Kind: "cwd", Text: cwd})
	coordinator := coordinatorFrom(ctx)
	servers := map[string]any{}
	var allow []string
	if cfg, release := chatBoard(ctx, c.Board, c.BoardURL); cfg != nil {
		defer release()
		servers[mcptools.ServerName] = cfg
		// Nobody can approve a call here, as in Claude Code's read-only chat.
		allow = slices.DeleteFunc(slices.Clone(mcptools.BoardTools), func(t string) bool { return slices.Contains(mcptools.AskTools, t) })
		if coordinator {
			allow = append(allow, mcptools.GoalToolNames...)
		}
	}
	var mu sync.Mutex
	tr := acpTranslator{Profile: p, Stream: true}
	replaying := true
	name, args := a.command(nil, false)
	started := time.Now()
	proc, err := startACP(ctx, acpStart{
		Name: name, Args: args, Dir: cwd, Env: localrun.Environ(),
		OnUpdate: func(u acpUpdate) {
			mu.Lock()
			defer mu.Unlock()
			if replaying {
				return
			}
			for _, d := range tr.Translate(u) {
				if d.Kind != acpStatus { // the chat has no log
					emit(d)
				}
			}
		},
		OnPermission: func(r acpPermRequest) bool {
			tc := &acpToolCall{}
			tc.merge(r.ToolCall)
			s, t, ok := tc.mcpTool()
			return ok && s == mcptools.ServerName && slices.Contains(allow, t)
		},
	})
	if err != nil {
		return fmt.Errorf("%s: %w", p.Title, err)
	}
	defer proc.close()
	a.CLI.initialized(proc.init)
	mcp, _ := acpServers(servers, "", proc.init.caps().HTTP)
	key := ""
	if p.KeyEnv != "" {
		key = strings.TrimSpace(os.Getenv(p.KeyEnv))
	}
	info, err := proc.open(ctx, p, acpOpen{Cwd: cwd, Servers: mcp, Key: key})
	mu.Lock()
	replaying = false
	mu.Unlock()
	if errors.Is(err, errACPLoggedOut) {
		a.CLI.signedOut()
		return a.loggedOut()
	}
	if err != nil {
		return fmt.Errorf("%s: %w", p.Title, err)
	}
	proc.configure(ctx, info, p.ChatMode, "", "")
	system := CoordinatorSystem
	if !coordinator {
		system = chatSystem(dir, " and can read files. You cannot edit files or run commands: Dash can't ask the user before a change in this chat.",
			servers[mcptools.ServerName] != nil)
	}
	prompt := "<system-instructions>\n" + system + "\n</system-instructions>\n\n" + transcript(history)
	stop, usage, err := proc.prompt(ctx, info.SessionID, prompt)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("%s: %w", p.Title, err)
	}
	if stop == "refusal" {
		emit(ChatDelta{Kind: "error", Text: p.Title + " declined this request."})
	}
	u := usageOf(usage)
	u.DurationMS = time.Since(started).Milliseconds()
	emit(usageDelta(u))
	emit(ChatDelta{Kind: "done"})
	return nil
}
