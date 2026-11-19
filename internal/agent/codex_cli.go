package agent

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
	"github.com/klahr/dash/internal/textutil"
)

// codexChecker is the CLIChecker of the Codex CLI: its version
// (`codex --version`), whether `codex exec` still has the flags Dash
// passes (`codex exec --help`) and whether it is signed in (`codex login
// status`, which prints "Not logged in" when it isn't). The zero value
// checks "codex"; a nil one checks nothing.
type codexChecker struct {
	Bin string

	mu       sync.Mutex
	onChange func()
	st       CLIStatus
	probe    []string // warnings from the last Check
	seen     []string // warnings from runs with this version
	run      func(ctx context.Context, bin string, args ...string) (string, error)
}

var _ CLIChecker = (*codexChecker)(nil)

// codexProbe are what `codex exec --help` must mention: the flags and the
// subcommand Dash's runs use.
var codexProbe = []string{"--json", "--skip-git-repo-check", "--sandbox", "--ephemeral", "resume"}

func (c *codexChecker) bin() string {
	if c.Bin == "" {
		return codexBin
	}
	return c.Bin
}

func (c *codexChecker) runner() func(context.Context, string, ...string) (string, error) {
	if c.run == nil {
		return runCodexCLI
	}
	return c.run
}

// OnChange sets f to be called after what the checker found changed.
func (c *codexChecker) OnChange(f func()) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.onChange = f
	c.mu.Unlock()
}

// CLIStatus is what the checker found last, with the warnings of the runs
// since.
func (c *codexChecker) CLIStatus() CLIStatus {
	if c == nil {
		return CLIStatus{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.st
	s.Warnings = append(slices.Clone(c.probe), c.seen...)
	return s
}

// Run checks now and then every `every` until ctx ends (Codex updates
// itself too), and whether it is signed in every five minutes between.
func (c *codexChecker) Run(ctx context.Context, every time.Duration) {
	if c == nil {
		return
	}
	if every <= 0 {
		every = time.Hour
	}
	login := time.NewTicker(min(5*time.Minute, every))
	defer login.Stop()
	for {
		c.Check(ctx)
		full := time.After(every)
	wait:
		for {
			select {
			case <-ctx.Done():
				return
			case <-full:
				break wait
			case <-login.C:
				c.checkLogin(ctx)
			}
		}
	}
}

var codexVersion = regexp.MustCompile(`\b(\d+\.\d+\.\d+[0-9A-Za-z.+-]*)`)

// Check runs the CLI's version, help and login status now.
func (c *codexChecker) Check(ctx context.Context) CLIStatus {
	if c == nil {
		return CLIStatus{}
	}
	bin, run := c.bin(), c.runner()
	var warns []string
	version := ""
	out, err := run(ctx, bin, "--version")
	switch v := codexVersion.FindStringSubmatch(out); {
	case err != nil && (out == "" || v == nil):
		warns = append(warns, fmt.Sprintf("Couldn't run `%s --version`: %v. Install the Codex CLI (npm install -g @openai/codex) to run this project's tasks here", bin, err))
	case v == nil:
		warns = append(warns, fmt.Sprintf("`%s --version` printed %q, not a version like \"codex-cli 0.50.0\"", bin, textutil.Summary(out, 200)))
	default:
		version = v[1]
		if !strings.Contains(strings.ToLower(out), "codex") {
			warns = append(warns, fmt.Sprintf("`%s --version` printed %q: is it the Codex CLI?", bin, textutil.Summary(out, 200)))
		}
	}
	loggedOut := false
	if version != "" {
		help, _ := run(ctx, bin, "exec", "--help")
		for _, f := range codexProbe {
			if !strings.Contains(help, f) {
				warns = append(warns, fmt.Sprintf("`codex exec --help` doesn't mention %s, which Dash's runs use: Codex's command line may have changed", f))
			}
		}
		loggedOut = codexLoggedOut(run(ctx, bin, "login", "status"))
	}
	c.mu.Lock()
	changed := version != c.st.Version || loggedOut != c.st.LoggedOut || !slices.Equal(warns, c.probe)
	if version != c.st.Version {
		c.seen = nil // warnings from runs belong to the version that ran them
	}
	c.st = CLIStatus{Version: version, CheckedAt: time.Now(), LoggedOut: loggedOut}
	c.probe = warns
	notify := c.onChange
	c.mu.Unlock()
	if changed && notify != nil {
		notify()
	}
	return c.CLIStatus()
}

// checkLogin asks only whether the CLI is signed in.
func (c *codexChecker) checkLogin(ctx context.Context) {
	c.mu.Lock()
	installed := c.st.Version != ""
	c.mu.Unlock()
	if !installed {
		return
	}
	out := codexLoggedOut(c.runner()(ctx, c.bin(), "login", "status"))
	c.mu.Lock()
	changed := out != c.st.LoggedOut
	c.st.LoggedOut = out
	notify := c.onChange
	c.mu.Unlock()
	if changed && notify != nil {
		notify()
	}
}

// codexLoggedOut reads `codex login status`: "Logged in using ChatGPT" or
// "Logged in using an API key - sk-…" when signed in, "Not logged in" (and
// a failed exit) when not. Output it doesn't understand isn't logged out.
func codexLoggedOut(out string, err error) bool {
	l := strings.ToLower(out)
	switch {
	case strings.Contains(l, "not logged in"):
		return true
	case strings.Contains(l, "logged in"):
		return false
	}
	return err != nil && out == ""
}

// observed records warnings a run found (its output changed).
func (c *codexChecker) observed(ws []string) {
	if c == nil || len(ws) == 0 {
		return
	}
	c.mu.Lock()
	added := false
	for _, w := range ws {
		if len(c.seen) >= maxWarnings || slices.Contains(c.seen, w) || slices.Contains(c.probe, w) {
			continue
		}
		c.seen, added = append(c.seen, w), true
	}
	notify := c.onChange
	c.mu.Unlock()
	if added && notify != nil {
		notify()
	}
}

// maxWarnings caps the warnings kept from runs.
const maxWarnings = 8

// runCodexCLI runs the CLI briefly, with no input, in a temporary
// directory, and returns its output.
func runCodexCLI(ctx context.Context, bin string, args ...string) (string, error) {
	if err := checkLocal(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = os.TempDir()
	localrun.Group(cmd, syscall.SIGKILL, 5*time.Second)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}
