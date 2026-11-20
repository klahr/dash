// Package clicheck holds what Dash's checkers of an agent CLI (Claude Code,
// Codex) share: the version and warnings they found, the warnings runs
// found since, the function to call when any of it changes, the loop that
// checks again now and then, and running the CLI briefly.
package clicheck

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/klahr/dash/internal/localrun"
)

// MaxWarnings caps the warnings kept from runs, so a badly changed CLI
// doesn't grow the report without bound.
const MaxWarnings = 8

// VersionRe finds a version like "2.1.0" in what `<cli> --version` printed.
var VersionRe = regexp.MustCompile(`\b(\d+\.\d+\.\d+[0-9A-Za-z.+-]*)`)

// Base is a checker's version and warnings, behind one lock that the
// checker's own fields share (see Set, Update and Read). The zero value is
// ready to use.
type Base struct {
	mu       sync.Mutex
	onChange func()
	version  string   // from the last full check
	probe    []string // warnings from the last full check
	seen     []string // warnings from runs with this version
}

// OnChange sets f to be called after what the checker found changed.
func (b *Base) OnChange(f func()) {
	b.mu.Lock()
	b.onChange = f
	b.mu.Unlock()
}

// Set records what a full check found: the version and its warnings. When
// the version changed, the warnings from runs are forgotten: they belong to
// the version that ran them. update (if not nil) runs under the lock to set
// the checker's own fields and says whether they changed. The OnChange
// function is called when anything did.
func (b *Base) Set(version string, probe []string, update func() bool) {
	b.mu.Lock()
	changed := version != b.version || !slices.Equal(probe, b.probe)
	if update != nil && update() {
		changed = true
	}
	if version != b.version {
		b.seen = nil
	}
	b.version, b.probe = version, probe
	notify := b.onChange
	b.mu.Unlock()
	if changed && notify != nil {
		notify()
	}
}

// Update runs f under the lock, and calls the OnChange function after it
// when f says something changed.
func (b *Base) Update(f func() bool) {
	b.mu.Lock()
	changed := f()
	notify := b.onChange
	b.mu.Unlock()
	if changed && notify != nil {
		notify()
	}
}

// Read runs f under the lock with the version and the warnings: the last
// full check's, then the runs' since (a new slice; nil when there are none).
func (b *Base) Read(f func(version string, warnings []string)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f(b.version, append(slices.Clone(b.probe), b.seen...))
}

// Observed records warnings a run found, and calls the OnChange function
// when one of them is new.
func (b *Base) Observed(ws []string) {
	if len(ws) == 0 {
		return
	}
	b.Update(func() bool {
		added := false
		for _, w := range ws {
			if len(b.seen) >= MaxWarnings || slices.Contains(b.seen, w) || slices.Contains(b.probe, w) {
				continue
			}
			b.seen, added = append(b.seen, w), true
		}
		return added
	})
}

// Loop calls check now and then every `every` (an hour when it isn't set)
// until ctx ends: the CLIs update themselves, so what they are can change
// under a running Dash. In between it calls side every sideEvery (at most
// every), for what changes more often, such as the sign-in.
func Loop(ctx context.Context, every, sideEvery time.Duration, check, side func(context.Context)) {
	if every <= 0 {
		every = time.Hour
	}
	tick := time.NewTicker(min(sideEvery, every))
	defer tick.Stop()
	for {
		check(ctx)
		full := time.After(every)
	wait:
		for {
			select {
			case <-ctx.Done():
				return
			case <-full:
				break wait
			case <-tick.C:
				side(ctx)
			}
		}
	}
}

// Run runs the CLI briefly, with no input and env (nil: this process's), in
// a temporary directory, and returns its output, trimmed.
func Run(ctx context.Context, env []string, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = os.TempDir()
	cmd.Env = env
	// Its own process group, so the timeout stops what the CLI started too,
	// and Run doesn't block on a grandchild holding the output pipe.
	localrun.Group(cmd, syscall.SIGKILL, 5*time.Second)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}
