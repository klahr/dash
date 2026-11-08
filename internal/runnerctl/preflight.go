package runnerctl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/klahr/dash/internal/tunnel"
)

// Check is one preflight check's outcome.
type Check struct {
	Name   string
	OK     bool
	Warn   bool   // not OK, but registering can go ahead
	Detail string // what was found
	Fix    string // how to fix it, when not OK
}

// Preflight checks what a runner needs before it registers: git, a logged-in
// claude, and a hub reachable over HTTPS. Not gh: a runner without it merges
// and pushes tasks of projects that use pull requests only by default (see
// dispatch.Link.MergeModeSet). The funcs default to the real thing.
type Preflight struct {
	LookPath func(file string) (string, error)
	// Output runs a command and returns its stdout.
	Output func(ctx context.Context, name string, args ...string) ([]byte, error)
	Client *http.Client
}

func (p Preflight) lookPath(f string) (string, error) {
	if p.LookPath != nil {
		return p.LookPath(f)
	}
	return exec.LookPath(f)
}

func (p Preflight) output(ctx context.Context, name string, args ...string) ([]byte, error) {
	if p.Output != nil {
		return p.Output(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).Output()
}

// Run checks everything for a runner of server that runs claudeBin.
func (p Preflight) Run(ctx context.Context, server, claudeBin string) []Check {
	return []Check{p.git(), p.claude(ctx, claudeBin), p.hub(ctx, server)}
}

func (p Preflight) git() Check {
	c := Check{Name: "git"}
	path, err := p.lookPath("git")
	if err != nil {
		c.Detail = "not found on PATH"
		c.Fix = "install git (e.g. apt install git, or xcode-select --install on macOS)"
		return c
	}
	c.OK, c.Detail = true, path
	return c
}

func (p Preflight) claude(ctx context.Context, bin string) Check {
	c := Check{Name: "claude"}
	path, err := p.lookPath(bin)
	if err != nil {
		c.Detail = bin + " not found on PATH"
		c.Fix = "install Claude Code (https://docs.claude.com/en/docs/claude-code/setup), then run claude once to log in"
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := p.output(ctx, path, "auth", "status")
	var st struct {
		LoggedIn *bool  `json:"loggedIn"`
		Email    string `json:"email"`
		Method   string `json:"authMethod"`
	}
	if jerr := json.Unmarshal(out, &st); jerr != nil || st.LoggedIn == nil {
		if err == nil {
			err = jerr
		}
		// An older claude without `auth status`: can't tell.
		c.Warn, c.Detail = true, fmt.Sprintf("%s, but couldn't tell whether it's logged in (%v)", path, err)
		c.Fix = "make sure claude is logged in: run claude once"
		return c
	}
	if !*st.LoggedIn {
		c.Detail = path + ", not logged in"
		c.Fix = "run claude and log in (or set up a token with claude setup-token)"
		return c
	}
	c.OK, c.Detail = true, path+", logged in"
	if st.Email != "" {
		c.Detail += " as " + st.Email
	}
	return c
}

func (p Preflight) hub(ctx context.Context, server string) Check {
	c := Check{Name: "hub"}
	u, err := tunnel.CheckServer(server)
	if err != nil {
		c.Detail, c.Fix = err.Error(), "serve the hub over HTTPS (see docs/deploy.md) and use its https:// address"
		return c
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u.String()+"/runner/version", nil)
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	res, err := client.Do(req)
	if err != nil {
		c.Detail = "can't reach " + u.String() + ": " + err.Error()
		c.Fix = "check the address, that this computer is online and that the hub runs with a valid certificate"
		return c
	}
	defer res.Body.Close()
	var v struct {
		Version string `json:"version"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&v)
	c.OK, c.Detail = true, u.String()
	if v.Version != "" {
		c.Detail += " (Dash " + v.Version + ")"
	}
	return c
}

// Failed reports whether any check failed (warnings don't count).
func Failed(cs []Check) bool {
	for _, c := range cs {
		if !c.OK && !c.Warn {
			return true
		}
	}
	return false
}

// FormatChecks renders checks for the terminal.
func FormatChecks(cs []Check) string {
	var b strings.Builder
	for _, c := range cs {
		mark := "ok  "
		switch {
		case c.Warn:
			mark = "warn"
		case !c.OK:
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "  [%s] %-6s %s\n", mark, c.Name, c.Detail)
		if !c.OK && c.Fix != "" {
			fmt.Fprintf(&b, "         %6s → %s\n", "", c.Fix)
		}
	}
	return b.String()
}
