// Package claudecli notices when the installed Claude Code CLI changes in a
// way Dash depends on: its version, the flags Dash passes to every run
// (--output-format stream-json, --permission-prompt-tool, …) and the shape of
// the stream-json events Dash reads (system init, assistant and user
// messages, rate_limit_event, result). It doesn't stop anything: what it
// finds is shown as a warning on the Runners page and in the task log, so a
// changed CLI doesn't make Dash fail silently.
package claudecli

import (
	"bytes"
	"context"
	"fmt"
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

// Status is what Dash knows about the installed Claude Code CLI.
type Status struct {
	Version   string    `json:"version,omitempty"` // e.g. "2.1.319"; "" when unknown
	Warnings  []string  `json:"warnings,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
}

// ProbeFlags are the flags Dash passes to `claude -p` that the probe checks
// Claude Code still accepts. The probe gives no prompt, so Claude Code stops
// before it calls the model.
var ProbeFlags = []string{
	"--output-format", "stream-json", "--verbose", "--include-partial-messages",
	"--permission-prompt-tool", "mcp__dash_perm__approve",
}

// maxWarnings caps the warnings kept from runs, so a badly changed stream
// doesn't grow the report without bound.
const maxWarnings = 8

// Monitor keeps the latest Status. The zero value is ready to use; a nil
// Monitor checks nothing and reports nothing.
type Monitor struct {
	Bin string // default "claude"

	mu       sync.Mutex
	onChange func()
	version  string
	checked  time.Time
	probe    []string // from the last Check
	seen     []string // from runs with this version
	run      func(ctx context.Context, bin string, args ...string) (string, error)
}

// OnChange sets f to be called after the Status changed.
func (m *Monitor) OnChange(f func()) {
	m.mu.Lock()
	m.onChange = f
	m.mu.Unlock()
}

// Status is a copy of what the monitor knows.
func (m *Monitor) Status() Status {
	if m == nil {
		return Status{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := Status{Version: m.version, CheckedAt: m.checked}
	s.Warnings = append(append(s.Warnings, m.probe...), m.seen...)
	return s
}

// Warnings is the warnings of the last Check and of the runs since: the ones
// a task log should start with.
func (m *Monitor) Warnings() []string { return m.Status().Warnings }

// Run checks now and then every `every` until ctx ends: Claude Code updates
// itself, so the version can change under a running Dash.
func (m *Monitor) Run(ctx context.Context, every time.Duration) {
	if m == nil {
		return
	}
	if every <= 0 {
		every = time.Hour
	}
	for {
		m.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

var versionRe = regexp.MustCompile(`\b(\d+\.\d+\.\d+[0-9A-Za-z.+-]*)`)

// Check asks the CLI for its version and whether it still takes ProbeFlags.
func (m *Monitor) Check(ctx context.Context) Status {
	if m == nil {
		return Status{}
	}
	bin := m.Bin
	if bin == "" {
		bin = localrun.Claude
	}
	run := m.run
	if run == nil {
		run = runCLI
	}
	var warns []string
	version := ""
	out, err := run(ctx, bin, "--version")
	switch v := versionRe.FindStringSubmatch(out); {
	case err != nil && out == "":
		warns = append(warns, fmt.Sprintf("Couldn't run `%s --version`: %v", bin, err))
	case v == nil:
		warns = append(warns, fmt.Sprintf("`%s --version` printed %q, not a version like \"2.1.0 (Claude Code)\"", bin, oneLine(out)))
	default:
		version = v[1]
		if !strings.Contains(out, "Claude Code") {
			warns = append(warns, fmt.Sprintf("`%s --version` printed %q: is it Claude Code?", bin, oneLine(out)))
		}
	}
	if version != "" || err == nil {
		out, err = run(ctx, bin, append([]string{"-p"}, ProbeFlags...)...)
		warns = append(warns, probeWarnings(out, err)...)
	}
	return m.set(version, warns)
}

func (m *Monitor) set(version string, probe []string) Status {
	m.mu.Lock()
	changed := version != m.version || !slices.Equal(probe, m.probe)
	if version != m.version {
		m.seen = nil // warnings from runs belong to the version that ran them
	}
	m.version, m.probe, m.checked = version, probe, time.Now()
	notify := m.onChange
	m.mu.Unlock()
	if changed && notify != nil {
		notify()
	}
	return m.Status()
}

// observed records warnings a run found, and reports the ones new to m.
func (m *Monitor) observed(ws []string) {
	if m == nil || len(ws) == 0 {
		return
	}
	m.mu.Lock()
	added := false
	for _, w := range ws {
		if len(m.seen) >= maxWarnings || slices.Contains(m.seen, w) || slices.Contains(m.probe, w) {
			continue
		}
		m.seen, added = append(m.seen, w), true
	}
	notify := m.onChange
	m.mu.Unlock()
	if added && notify != nil {
		notify()
	}
}

var unknownOption = regexp.MustCompile(`unknown option '([^']+)'`)

// probeWarnings reads what `claude -p <ProbeFlags>` without a prompt said.
// Claude Code checks its flags first, then refuses to run without input.
func probeWarnings(out string, err error) []string {
	if m := unknownOption.FindStringSubmatch(out); m != nil {
		return []string{fmt.Sprintf("Claude Code no longer accepts %s, which Dash passes to its runs", m[1])}
	}
	if strings.Contains(out, "is invalid") || strings.Contains(out, "Allowed choices") {
		return []string{"Claude Code rejected a flag Dash passes to its runs: " + oneLine(out)}
	}
	if err == nil {
		return []string{"`claude -p` without a prompt succeeded; Dash expected it to ask for input. Its command line may have changed"}
	}
	if out == "" {
		return []string{fmt.Sprintf("Couldn't check Claude Code's flags: %v", err)}
	}
	return nil
}

// runCLI runs the CLI briefly, with no input, in a temporary directory, and
// returns its output.
func runCLI(ctx context.Context, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = os.TempDir()
	// Its own process group, so the timeout stops what claude started too,
	// and Run doesn't block on a grandchild holding the output pipe.
	localrun.Group(cmd, syscall.SIGKILL, 5*time.Second)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
