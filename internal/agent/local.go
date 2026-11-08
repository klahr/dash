package agent

import (
	"errors"
	"sync/atomic"
)

// The hub never runs Claude itself: each user's chat, titles, clean-ups
// and tasks run on a runner (`dash runner`), with its own `claude` login. SetLocal installs the process-wide policy and every way
// this package reaches Claude (ClaudeCode, ClaudeCodeChat, Titler, Chat)
// checks it first, so a code path that forgets to route to a runner fails
// instead of billing the hub's account.

var localPolicy atomic.Pointer[func() bool]

// ErrNoLocalClaude is returned instead of running Claude when this Dash may
// not (the hub: only `dash runner` runs Claude).
var ErrNoLocalClaude = errors.New("this hub doesn't run Claude: Claude runs on each user's own runner (start `dash runner` on your computer)")

// SetLocal sets whether this process may run Claude itself (nil: always,
// the default, as on a runner). The hub sets it to never, as the safety
// net behind routing all its Claude work to runners. allowed is asked
// before each run.
func SetLocal(allowed func() bool) {
	if allowed == nil {
		localPolicy.Store(nil)
		return
	}
	localPolicy.Store(&allowed)
}

// LocalAllowed reports whether this process may run Claude itself.
func LocalAllowed() bool {
	f := localPolicy.Load()
	return f == nil || (*f)()
}

// checkLocal is what every Claude call in this package starts with.
func checkLocal() error {
	if !LocalAllowed() {
		return ErrNoLocalClaude
	}
	return nil
}
