package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/klahr/dash/internal/clicheck"
	"github.com/klahr/dash/internal/localrun"
)

// acpChecker is the CLIChecker of an ACP agent: it starts the agent and
// reads its initialize answer (its version and abilities, ACPCaps), then
// opens a session in a scratch directory to see whether it is signed in
// (an auth_required error is not). A nil one checks nothing.
type acpChecker struct {
	Profile *ACPProfile
	Bin     string

	cli       clicheck.Base // its lock guards the fields below too
	checked   time.Time
	loggedOut bool
	found     ACPCaps
	// check replaces the real check (tests).
	check func(ctx context.Context) (acpInit, error, error)
}

var _ CLIChecker = (*acpChecker)(nil)

// acpCheckTimeout bounds one check.
var acpCheckTimeout = time.Minute

func (c *acpChecker) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return c.Profile.Bin
}

func (c *acpChecker) OnChange(f func()) {
	if c == nil {
		return
	}
	c.cli.OnChange(f)
}

func (c *acpChecker) CLIStatus() CLIStatus {
	if c == nil {
		return CLIStatus{}
	}
	var s CLIStatus
	c.cli.Read(func(version string, warnings []string) {
		s = CLIStatus{Version: version, Warnings: warnings, CheckedAt: c.checked, LoggedOut: c.loggedOut}
	})
	return s
}

// caps are the abilities the agent said it has when it last answered.
func (c *acpChecker) caps() ACPCaps {
	if c == nil {
		return ACPCaps{}
	}
	var out ACPCaps
	c.cli.Read(func(string, []string) { out = c.found })
	return out
}

// Run checks now and then every `every`, and the sign-in every five
// minutes between: both start the agent.
func (c *acpChecker) Run(ctx context.Context, every time.Duration) {
	if c == nil {
		return
	}
	clicheck.Loop(ctx, every, 5*time.Minute, func(ctx context.Context) { c.Check(ctx) }, func(ctx context.Context) { c.Check(ctx) })
}

// Check starts the agent now and records what it found.
func (c *acpChecker) Check(ctx context.Context) CLIStatus {
	if c == nil {
		return CLIStatus{}
	}
	ctx, cancel := context.WithTimeout(ctx, acpCheckTimeout)
	defer cancel()
	probe := c.check
	if probe == nil {
		probe = c.probe
	}
	init, err, sessErr := probe(ctx)
	var warns []string
	version, loggedOut := "", false
	var caps ACPCaps
	switch {
	case err != nil:
		warns = append(warns, fmt.Sprintf("Couldn't start `%s %s`: %v. %s to run this project's tasks here",
			c.bin(), strings.Join(c.Profile.Args, " "), err, upperFirst(c.Profile.Install)))
	default:
		caps = init.caps()
		version = caps.Version
		if version == "" {
			version = "unknown" // it answered: it is installed
			warns = append(warns, "the agent's initialize answer has no version")
		}
		switch {
		case errors.Is(sessErr, errACPLoggedOut):
			loggedOut = true
		case sessErr != nil:
			warns = append(warns, "Couldn't open a session: "+sessErr.Error())
		}
	}
	c.cli.Set(version, warns, func() bool {
		changed := loggedOut != c.loggedOut || caps != c.found
		c.checked, c.loggedOut = time.Now(), loggedOut
		if caps.Known || err != nil {
			c.found = caps
		}
		return changed
	})
	if caps.Known {
		lastCaps.Store(c.Profile.Name, caps)
	}
	return c.CLIStatus()
}

// probe starts the agent in a scratch directory, initializes it and opens
// a session without MCP servers. err is why it couldn't start or
// initialize; sessErr why the session didn't open (errACPLoggedOut: not
// signed in). An agent that wants signing in, but whose API key is set,
// counts as signed in: a run signs in with it.
func (c *acpChecker) probe(ctx context.Context) (init acpInit, err, sessErr error) {
	if err := checkLocal(); err != nil {
		return init, err, nil
	}
	dir, err := os.MkdirTemp("", "dash-acp-check-")
	if err != nil {
		return init, err, nil
	}
	defer os.RemoveAll(dir)
	proc, err := startACP(ctx, acpStart{Name: c.bin(), Args: c.Profile.Args, Dir: dir, Env: localrun.Environ()})
	if err != nil {
		return init, err, nil
	}
	defer proc.close()
	_, sessErr = proc.open(ctx, c.Profile, acpOpen{Cwd: dir, Servers: []acpMCPServer{}})
	if errors.Is(sessErr, errACPLoggedOut) && c.Profile.KeyEnv != "" && strings.TrimSpace(os.Getenv(c.Profile.KeyEnv)) != "" {
		sessErr = nil
	}
	return proc.init, nil, sessErr
}

// initialized records an initialize answer a run read.
func (c *acpChecker) initialized(i acpInit) {
	if c == nil {
		return
	}
	caps := i.caps()
	c.cli.Update(func() bool {
		changed := caps != c.found
		c.found = caps
		return changed
	})
	lastCaps.Store(c.Profile.Name, caps)
}

// signedOut records that a run found the agent not signed in.
func (c *acpChecker) signedOut() {
	if c == nil {
		return
	}
	c.cli.Update(func() bool {
		changed := !c.loggedOut
		c.loggedOut = true
		return changed
	})
}

// observed records warnings a run found.
func (c *acpChecker) observed(ws []string) {
	if c == nil {
		return
	}
	c.cli.Observed(ws)
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
