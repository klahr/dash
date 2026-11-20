package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/klahr/dash/internal/clicheck"
	"github.com/klahr/dash/internal/textutil"
)

// codexChecker is the CLIChecker of the Codex CLI: its version
// (`codex --version`), whether `codex exec` still has the flags Dash
// passes (`codex exec --help`) and whether it is signed in (`codex login
// status`, which prints "Not logged in" when it isn't). The zero value
// checks "codex"; a nil one checks nothing.
type codexChecker struct {
	Bin string

	cli       clicheck.Base // the version and the warnings; its lock guards the fields below too
	checked   time.Time
	loggedOut bool
	run       func(ctx context.Context, bin string, args ...string) (string, error)
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
	c.cli.OnChange(f)
}

// CLIStatus is what the checker found last, with the warnings of the runs
// since.
func (c *codexChecker) CLIStatus() CLIStatus {
	if c == nil {
		return CLIStatus{}
	}
	var s CLIStatus
	c.cli.Read(func(version string, warnings []string) {
		s = CLIStatus{Version: version, Warnings: warnings, CheckedAt: c.checked, LoggedOut: c.loggedOut}
	})
	return s
}

// Run checks now and then every `every` until ctx ends (Codex updates
// itself too), and whether it is signed in every five minutes between.
func (c *codexChecker) Run(ctx context.Context, every time.Duration) {
	if c == nil {
		return
	}
	clicheck.Loop(ctx, every, 5*time.Minute, func(ctx context.Context) { c.Check(ctx) }, c.checkLogin)
}

// Check runs the CLI's version, help and login status now.
func (c *codexChecker) Check(ctx context.Context) CLIStatus {
	if c == nil {
		return CLIStatus{}
	}
	bin, run := c.bin(), c.runner()
	var warns []string
	version := ""
	out, err := run(ctx, bin, "--version")
	switch v := clicheck.VersionRe.FindStringSubmatch(out); {
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
	c.cli.Set(version, warns, func() bool {
		changed := loggedOut != c.loggedOut
		c.checked, c.loggedOut = time.Now(), loggedOut
		return changed
	})
	return c.CLIStatus()
}

// checkLogin asks only whether the CLI is signed in.
func (c *codexChecker) checkLogin(ctx context.Context) {
	if c.CLIStatus().Version == "" {
		return // not installed
	}
	out := codexLoggedOut(c.runner()(ctx, c.bin(), "login", "status"))
	c.cli.Update(func() bool {
		changed := out != c.loggedOut
		c.loggedOut = out
		return changed
	})
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
	if c == nil {
		return
	}
	c.cli.Observed(ws)
}

// runCodexCLI runs the CLI briefly, with no input, in a temporary
// directory, and returns its output.
func runCodexCLI(ctx context.Context, bin string, args ...string) (string, error) {
	if err := checkLocal(); err != nil {
		return "", err
	}
	return clicheck.Run(ctx, nil, bin, args...)
}
