package agent

import (
	"errors"
	"log/slog"

	"github.com/klahr/dash/internal/localrun"
)

// The hub never runs Claude itself: each user's chat, titles, clean-ups
// and tasks run on a runner (`dash runner`), with its own `claude` login.
// Every way this package reaches Claude (ClaudeCode, ClaudeCodeChat,
// Titler, Chat) checks the process-wide guard first (localrun, which the
// hub turns off for everything it would run itself), so a code path that
// forgets to route to a runner fails loudly instead of billing the hub's
// account.

// ErrNoLocalClaude is returned instead of running Claude on the hub (only
// `dash runner` runs Claude).
var ErrNoLocalClaude = errors.New("this hub doesn't run Claude: Claude runs on each user's own runner (start `dash runner` on your computer)")

// checkLocal is what every Claude call in this package starts with.
func checkLocal() error {
	if !localrun.Allowed() {
		slog.Error(localrun.Refused, "what", "claude")
		return ErrNoLocalClaude
	}
	return nil
}
