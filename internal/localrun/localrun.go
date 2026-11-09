// Package localrun guards everything Dash runs on its own computer: git,
// gh, the projects' shell commands (setup, test, run, deploy) and
// terminals.
//
// The hub runs nothing on its own computer: it signs people in, keeps the
// board and the database and forwards work to runners (`dash runner`).
// Set installs the process-wide policy, and every place in Dash that starts
// a process, runs Claude (package agent) or reads a project's working tree
// checks it first, so a code path that forgets to forward to a runner fails
// loudly instead of running on the hub. A few places that work without a
// checkout too (a ref name's format, a project's scan templates) ask
// Allowed to take the hub's way.
package localrun

import (
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
)

var policy atomic.Pointer[func() bool]

// ErrHub is returned instead of running something on the hub.
var ErrHub = errors.New("the hub runs nothing on its own computer: git, gh, project commands and terminals run on runners (start one with `dash runner`)")

// Refused is the log message of every refusal; tests look for it.
const Refused = "refused to run on the hub"

// Set sets whether this process may run commands, Claude and touch working
// trees itself (nil: always, the default, as on a runner). The hub sets it
// to never. allowed is asked before each command.
func Set(allowed func() bool) {
	if allowed == nil {
		policy.Store(nil)
		return
	}
	policy.Store(&allowed)
}

// Allowed reports whether this process may run commands itself.
func Allowed() bool {
	f := policy.Load()
	return f == nil || (*f)()
}

// Check is what every command Dash starts begins with: nil when it may run
// here, else ErrHub (wrapped with what), logged so a missed path shows.
func Check(what string) error {
	if Allowed() {
		return nil
	}
	slog.Error(Refused, "what", what)
	return fmt.Errorf("%s: %w", what, ErrHub)
}
