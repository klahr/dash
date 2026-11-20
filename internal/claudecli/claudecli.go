// Package claudecli notices when the installed Claude Code CLI changes in a
// way Dash depends on: its version, the flags Dash passes to every run
// (--output-format stream-json, --permission-prompt-tool, …) and the shape of
// the stream-json events Dash reads (system init, assistant and user
// messages, rate_limit_event, result). It doesn't stop anything: what it
// finds is shown as a warning on the Runners page and in the task log, so a
// changed CLI doesn't make Dash fail silently.
package claudecli

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/klahr/dash/internal/clicheck"
	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/textutil"
)

// Status is what Dash knows about the installed Claude Code CLI.
type Status struct {
	Version   string    `json:"version,omitempty"` // e.g. "2.1.319"; "" when unknown
	Warnings  []string  `json:"warnings,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
	// Account is the Claude account the CLI is logged in to (nil: logged
	// out, or a CLI or runner that doesn't say). Runners on one account
	// share its usage limits, so the hub paces them together.
	Account *Account `json:"account,omitempty"`
	// Auth is how the CLI signs in: an API key or a subscription, and any
	// login it could fall back to (nil: not checked yet, or a runner too
	// old to say; then it counts as not an API-key runner).
	Auth *Auth `json:"auth,omitempty"`
}

// Account is a Claude login, as `claude auth status` reports it.
type Account struct {
	Email string `json:"email"`
	// OrgID and OrgName are its Claude organization. Every seat of a Team
	// or Enterprise plan has its own limits, so they don't group runners.
	OrgID        string `json:"org_id,omitempty"`
	OrgName      string `json:"org_name,omitempty"`
	Subscription string `json:"subscription,omitempty"` // e.g. "max", "pro", "team"
}

// ParseAuthStatus reads `claude auth status` output: the account it is
// logged in to, or nil when it isn't (or the output isn't understood).
func ParseAuthStatus(out []byte) *Account {
	var st struct {
		LoggedIn     *bool  `json:"loggedIn"`
		Email        string `json:"email"`
		OrgID        string `json:"orgId"`
		OrgName      string `json:"orgName"`
		Subscription string `json:"subscriptionType"`
	}
	if json.Unmarshal(out, &st) != nil || st.LoggedIn == nil || !*st.LoggedIn {
		return nil
	}
	a := Account{Email: strings.TrimSpace(st.Email), OrgID: st.OrgID, OrgName: st.OrgName, Subscription: st.Subscription}
	if a.Email == "" {
		return nil // an API key or token login: no account to group by
	}
	return &a
}

// ProbeFlags are the flags Dash passes to `claude -p` that the probe checks
// Claude Code still accepts. The probe gives no prompt, so Claude Code stops
// before it calls the model.
var ProbeFlags = []string{
	"--output-format", "stream-json", "--verbose", "--include-partial-messages",
	"--permission-prompt-tool", "mcp__dash_perm__approve",
}

// Monitor keeps the latest Status. The zero value is ready to use; a nil
// Monitor checks nothing and reports nothing.
type Monitor struct {
	Bin string // default "claude"

	cli     clicheck.Base // the version and the warnings; its lock guards the fields below too
	checked time.Time
	account *Account // from the last Check or CheckAccount
	auth    *Auth    // likewise
	run     func(ctx context.Context, bin string, args ...string) (string, error)
	// runNoKey runs the CLI without KeyEnv (see Auth.Login).
	runNoKey func(ctx context.Context, bin string, args ...string) (string, error)
}

// OnChange sets f to be called after the Status changed (never, on a nil
// Monitor).
func (m *Monitor) OnChange(f func()) {
	if m == nil {
		return
	}
	m.cli.OnChange(f)
}

// Status is a copy of what the monitor knows.
func (m *Monitor) Status() Status {
	if m == nil {
		return Status{}
	}
	var s Status
	m.cli.Read(func(version string, warnings []string) {
		s = Status{Version: version, Warnings: warnings, CheckedAt: m.checked}
		if m.account != nil {
			a := *m.account
			s.Account = &a
		}
		if m.auth != nil {
			a := *m.auth
			s.Auth = &a
		}
	})
	return s
}

// Warnings is the warnings of the last Check and of the runs since: the ones
// a task log should start with.
func (m *Monitor) Warnings() []string { return m.Status().Warnings }

// AccountEvery is how often Run asks which account the CLI is logged in
// to, between full checks: so a log-out or a switch of accounts is noticed.
const AccountEvery = 5 * time.Minute

// Run checks now and then every `every` until ctx ends: Claude Code updates
// itself, so the version can change under a running Dash. In between it
// checks the account every AccountEvery.
func (m *Monitor) Run(ctx context.Context, every time.Duration) {
	if m == nil {
		return
	}
	clicheck.Loop(ctx, every, AccountEvery, func(ctx context.Context) { m.Check(ctx) }, func(ctx context.Context) { m.CheckAccount(ctx) })
}

// CheckAccount asks the CLI which account it is logged in to.
func (m *Monitor) CheckAccount(ctx context.Context) *Account {
	if m == nil {
		return nil
	}
	a, auth := m.account1(ctx, m.bin(), m.runner())
	m.cli.Update(func() bool {
		changed := !sameAccount(a, m.account) || !sameAuth(auth, m.auth)
		m.account, m.auth = a, auth
		return changed
	})
	return a
}

func (m *Monitor) account1(ctx context.Context, bin string, run func(context.Context, string, ...string) (string, error)) (*Account, *Auth) {
	out, _ := run(ctx, bin, "auth", "status")
	auth := ParseAuth([]byte(out))
	if auth != nil && auth.Method == AuthAPIKey {
		// Claude Code doesn't show a login next to a key: ask without it.
		noKey := m.runNoKey
		if noKey == nil {
			noKey = runCLINoKey
		}
		alt, _ := noKey(ctx, bin, "auth", "status")
		switch fallback := ParseAuth([]byte(alt)); {
		case fallback == nil:
			auth.Login = AuthSubscription // can't tell: count it as one
		case fallback.Method == AuthSubscription:
			auth.Login = cmp.Or(fallback.Subscription, AuthSubscription)
		case fallback.Method == AuthAPIKey && fallback.KeySource == KeyEnv:
			auth.Login = AuthSubscription // the key was still there: can't tell
		}
	}
	return ParseAuthStatus([]byte(out)), auth
}

func sameAuth(a, b *Auth) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameAccount(a, b *Account) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (m *Monitor) bin() string {
	if m.Bin == "" {
		return localrun.Claude
	}
	return m.Bin
}

func (m *Monitor) runner() func(context.Context, string, ...string) (string, error) {
	if m.run == nil {
		return runCLI
	}
	return m.run
}

// Check asks the CLI for its version and whether it still takes ProbeFlags.
func (m *Monitor) Check(ctx context.Context) Status {
	if m == nil {
		return Status{}
	}
	bin, run := m.bin(), m.runner()
	var warns []string
	version := ""
	out, err := run(ctx, bin, "--version")
	switch v := clicheck.VersionRe.FindStringSubmatch(out); {
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
	var acct *Account
	var auth *Auth
	if version != "" {
		acct, auth = m.account1(ctx, bin, run)
	}
	return m.set(version, warns, acct, auth)
}

func (m *Monitor) set(version string, probe []string, acct *Account, auth *Auth) Status {
	m.cli.Set(version, probe, func() bool {
		changed := !sameAccount(acct, m.account) || !sameAuth(auth, m.auth)
		m.checked, m.account, m.auth = time.Now(), acct, auth
		return changed
	})
	return m.Status()
}

// observed records warnings a run found, and reports the ones new to m.
func (m *Monitor) observed(ws []string) {
	if m == nil {
		return
	}
	m.cli.Observed(ws)
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
	return clicheck.Run(ctx, nil, bin, args...)
}

// runCLINoKey is runCLI without KeyEnv in the CLI's environment.
func runCLINoKey(ctx context.Context, bin string, args ...string) (string, error) {
	return clicheck.Run(ctx, withoutKey(localrun.Environ()), bin, args...)
}

func oneLine(s string) string { return textutil.Summary(s, 200) }
