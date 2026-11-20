package agent

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/mcptools"
)

// CodexChat answers chat through the Codex CLI, on the account `codex` is
// signed in to. Each turn is a fresh `codex exec --json --ephemeral` run
// with the conversation so far in the prompt, as ClaudeCodeChat's are.
//
// It only reads: Codex runs in its read-only sandbox, since Dash can't ask
// the user before a call that changes something (Codex has no permission
// hook). It has the Dash task tools (Board): those that need no approval,
// as in Claude Code's read-only chat.
type CodexChat struct {
	Bin      string // default "codex"
	Dir      string // working directory of the global chat; default os.TempDir()
	Board    *mcptools.Tokens
	BoardURL string
	CLI      *codexChecker // optional
}

// ReadOnly: the Codex chat runs nothing that changes files.
func (CodexChat) ReadOnly() bool { return true }

// system is the turn's system prompt (developer instructions).
func (c CodexChat) system(dir string, coordinator bool) string {
	if coordinator {
		return CoordinatorSystem
	}
	tools := " and can read files and run commands that only read, in a read-only sandbox. You cannot edit files: " +
		"Dash can't ask the user before a change in this chat."
	return chatSystem(dir, tools, c.Board != nil && c.BoardURL != "", false)
}

func (c CodexChat) Stream(ctx context.Context, dir string, history []ChatMessage, emit func(ChatDelta)) error {
	if len(history) == 0 {
		return fmt.Errorf("empty conversation")
	}
	if err := checkLocal(); err != nil {
		return err
	}
	prov := Codex{Bin: c.Bin, CLI: c.CLI}
	if err := prov.ready(); err != nil {
		return err
	}
	coordinator := coordinatorFrom(ctx)
	o := codexOpts{json: true, readOnly: true, ephemeral: true, system: c.system(dir, coordinator)}
	if cfg, release := chatBoard(ctx, c.Board, c.BoardURL); cfg != nil {
		defer release()
		o.servers = map[string]any{mcptools.ServerName: cfg}
		// Nobody can approve a call here, as in Claude Code's read-only chat.
		allow := slices.DeleteFunc(slices.Clone(mcptools.BoardTools), func(t string) bool { return slices.Contains(mcptools.AskTools, t) })
		if coordinator {
			allow = append(allow, mcptools.GoalToolNames...)
		}
		o.allow = map[string][]string{mcptools.ServerName: allow}
	}
	args, env := o.args()
	cmd := exec.CommandContext(ctx, prov.bin(), args...)
	cmd.Env = localrun.Environ(env...)
	stdout, stderr, err := startChat(cmd, dir, c.Dir, history, emit)
	if err != nil {
		return err
	}
	started := time.Now()
	var tr codexTranslator
	wroteText := false
	pumpLines(stdout, func(line string) {
		ev, ok := parseCodexLine(line)
		if !ok {
			return
		}
		for _, d := range tr.Translate(ev) {
			switch d.Kind {
			case codexStatus:
				if strings.HasPrefix(d.Text, codexWarnPrefix) {
					c.CLI.observed([]string{strings.TrimPrefix(d.Text, codexWarnPrefix)})
				}
				continue // the chat has no log
			case "text":
				// Each agent message is a whole one; keep them apart.
				if wroteText {
					d.Text = "\n\n" + d.Text
				}
				wroteText = true
			}
			emit(d)
		}
	})
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if tr.Failure != "" {
		return fmt.Errorf("codex: %s", tr.Failure)
	}
	if waitErr != nil {
		why := strings.Join(tr.Errors, "; ")
		if why == "" {
			why = strings.TrimSpace(stderr.String())
		}
		return fmt.Errorf("codex exited: %w: %s", waitErr, why)
	}
	usage := tr.Usage
	usage.DurationMS = time.Since(started).Milliseconds()
	emit(usageDelta(usage))
	emit(ChatDelta{Kind: "done"})
	return nil
}
