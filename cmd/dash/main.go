// Command dash is the Dash orchestration server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/billing"
	"github.com/klahr/dash/internal/buildinfo"
	"github.com/klahr/dash/internal/mail"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/orgdomain"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/runnerctl"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

func main() {
	// `dash runner --server https://hub --code XXXX-XXXX`: this computer's
	// projects show in the hub's dashboard, over a connection it opens itself.
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "version" {
		fmt.Println("dash", buildinfo.Version)
		return
	}
	if len(args) > 0 && args[0] == "healthcheck" {
		os.Exit(healthcheck(args[1:]))
	}
	if len(args) > 0 && args[0] == "backup" {
		os.Exit(backupCmd(args[1:]))
	}
	if len(args) > 0 && args[0] == "reset-2fa" {
		os.Exit(resetTwoFactor(args[1:]))
	}
	runnerMode := len(args) > 0 && args[0] == "runner"
	if runnerMode {
		args = args[1:]
		// dash runner setup|install|uninstall|status
		if len(args) > 0 && slices.Contains(runnerctl.Commands, args[0]) {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			code := (&runnerctl.CLI{Version: buildinfo.Version}).Main(ctx, args[0], args[1:])
			stop()
			os.Exit(code)
		}
	}
	o := parseFlags(args, runnerMode)
	if o.showVersion {
		fmt.Println("dash", buildinfo.Version)
		return
	}

	// The env file first: it may set the log format too.
	envErr := auth.LoadDotEnv(o.envFile)
	logger, err := newLogger(os.Stderr, envOr(o.logFormat, "DASH_LOG_FORMAT"), envOr(o.logLevel, "DASH_LOG_LEVEL"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "dash:", err)
		os.Exit(2)
	}
	slog.SetDefault(logger)
	slog.Info("dash", "version", buildinfo.Version)
	for _, name := range o.removed {
		slog.Warn("-" + name + " is gone and ignored: the hub never runs Claude or tasks itself, runners (dash runner) do. Remove it from the start command")
	}
	if o.hubCommands {
		slog.Info("-hub-commands is no longer needed: a runner always takes commands from the hub. Remove it from the start command")
	}
	if o.pauseAt {
		slog.Info("-pause-at is gone and ignored: the queue pauses only when a usage window's limit is reached. Remove it from the start command")
	}
	for _, name := range o.ignored {
		slog.Warn("-" + name + " is a flag of dash runner, ignored by the hub, which runs no tasks, Claude or commands itself. Remove it from the hub's start command (pass it to dash runner)")
	}
	if envErr != nil {
		fatal("read env file", "path", o.envFile, "err", envErr)
	}
	o.resolveEnv()
	start(o)
}

// shutdownWait is how long Dash waits, on the way out, for its running tasks
// and merge to stop (Claude gets 10 seconds after SIGTERM before SIGKILL).
const shutdownWait = 20 * time.Second

// warnUnlinked logs the hub's projects no runner is linked to: made on the
// hub before it stopped running anything itself. They stay, with their
// tasks, and their files stay on disk; their work waits until a runner is
// linked (Project settings → Runners), and the dashboard says so.
func warnUnlinked(ps []model.Project) {
	for _, p := range ps {
		if !p.OnRunners() {
			slog.Warn("project needs a runner: the hub runs nothing itself, so its tasks, git and commands wait until one is linked "+
				"(Project settings → Runners), or remove it from the hub (its files stay) and add it on a runner with \"Add projects from this computer…\"",
				"project", p.Name, "path", p.Path)
		}
	}
}

// runnerFlags are about running tasks, Claude, scans and commands, which only
// runners (`dash runner`) do: the hub accepts them, with a warning, and
// ignores them (they are left out of its -h).
var runnerFlags = []string{"projects", "parallel", "claude", "claude-args", "sign-commits", "chat", "chat-tools", "chat-allow",
	"permission-prompts", "editor", "title-model", "cleanup-days"}

// printFlags is fs.PrintDefaults without the flags hide picks.
func printFlags(fs *flag.FlagSet, hide func(name string) bool) {
	shown := flag.NewFlagSet(fs.Name(), flag.ContinueOnError)
	shown.SetOutput(fs.Output())
	fs.VisitAll(func(f *flag.Flag) {
		if !hide(f.Name) {
			shown.Var(f.Value, f.Name, f.Usage)
		}
	})
	shown.PrintDefaults()
}

// removedFlags are flags old start commands may still pass (-hub-claude
// off, -hub-tasks=off): each takes its value and is ignored, with a warning.
var removedFlags = []string{"hub-claude", "hub-tasks"}

// envBool reports whether the variable name is set to 1, true, yes or on.
func envBool(name string) bool {
	switch strings.ToLower(os.Getenv(name)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// lockoutConfig is the sign-in lockout from the flags (negative or zero:
// not set), else the environment, else the defaults.
func lockoutConfig(perIP, global int, window time.Duration) (auth.Lockout, error) {
	l := auth.DefaultLockout
	envInt := func(flagVal int, flagName, name string, min int, dst *int) error {
		if flagVal < 0 {
			v := os.Getenv(name)
			if v == "" {
				return nil
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			flagVal = n
		}
		if flagVal < min {
			return fmt.Errorf("-%s / %s must be at least %d, not %d", flagName, name, min, flagVal)
		}
		*dst = flagVal
		return nil
	}
	if err := envInt(perIP, "login-max-failures", "DASH_LOGIN_MAX_FAILURES", 1, &l.PerIP); err != nil {
		return l, err
	}
	if err := envInt(global, "login-global-failures", "DASH_LOGIN_GLOBAL_FAILURES", 0, &l.Global); err != nil {
		return l, err
	}
	if window <= 0 {
		if v := os.Getenv("DASH_LOGIN_WINDOW"); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return l, fmt.Errorf("DASH_LOGIN_WINDOW: want a duration such as 15m, not %q", v)
			}
			window = d
		}
	}
	if window > 0 {
		l.Window = window
	}
	if l.Global > 0 && l.Global < l.PerIP {
		slog.Warn("sign-in lockout: the global limit is below the per-address one, so one address can lock everyone out", "global", l.Global, "per_ip", l.PerIP)
	}
	slog.Info("sign-in lockout", "per_ip", l.PerIP, "global", l.Global, "window", l.Window)
	return l, nil
}

func envOr(v, name string) string {
	if v != "" {
		return v
	}
	return os.Getenv(name)
}

// configureBilling turns on per-seat billing when Stripe is configured, or
// says why it stays off when it's configured only in part. Without any
// Stripe settings it is off and says nothing: a self-hosted Dash has no
// limits and no billing pages.
func configureBilling(acc *store.Accounts, cfg billing.Config) *billing.Service {
	if !cfg.Configured() {
		return nil
	}
	if cfg.TrialDays < 0 {
		cfg.TrialDays = billing.DefaultTrialDays
		if v := os.Getenv("DASH_BILLING_TRIAL_DAYS"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				n = -1 // Check reports it
			}
			cfg.TrialDays = n
		}
	}
	if cfg.Grace == 0 {
		if v := os.Getenv("DASH_BILLING_GRACE"); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				slog.Error("billing stays off", "reason", "DASH_BILLING_GRACE: want a duration like 72h")
				return nil
			}
			cfg.Grace = d
		}
	}
	if err := cfg.Check(); err != nil {
		slog.Error("billing stays off", "reason", err)
		return nil
	}
	b := billing.New(acc, &billing.HTTPStripe{Key: cfg.SecretKey}, cfg)
	slog.Info("billing on: orgs with two or more members need a subscription", "trial_days", cfg.TrialDays,
		"grace", b.Grace(), "webhook", strings.TrimRight(cfg.PublicURL, "/")+"/api/billing/webhook")
	return b
}

// configureMail sets up email (password reset) when SMTP is configured, and
// opens signup if asked to and email works. When it can't, it says why and
// Dash carries on invite-only.
func configureMail(authn *auth.Auth, signup bool, cfg mail.Config, publicURL string) {
	signup = signup || envBool("DASH_SIGNUP")
	if cfg.Port == 0 {
		cfg.Port = 587
		if v := os.Getenv("DASH_SMTP_PORT"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				cfg.Port = n
			} else {
				cfg.Port = -1 // Check reports it
			}
		}
	}
	if cfg.Host == "" && cfg.From == "" && !signup {
		return // no email at all, as before
	}
	sender, err := mail.NewSMTP(cfg)
	switch {
	case err != nil:
		slog.Error("email is off", "reason", err)
	case publicURL == "":
		err = errors.New("links in emails need the public URL: set -public-url or DASH_PUBLIC_URL")
		slog.Error("email is off", "reason", err)
	default:
		authn.SetMail(sender, publicURL)
		slog.Info("email on: password reset by email", "smtp", net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port)))
	}
	if !signup {
		return
	}
	if err == nil {
		err = authn.EnableSignup()
	}
	if err != nil {
		slog.Error("open signup stays off (invite-only)", "reason", err)
		return
	}
	slog.Info("open signup on: anyone can create an account by confirming their email address")
}

// runnerCredentials registers with the hub when given a code, and otherwise
// reads the credentials saved by an earlier registration.
func runnerCredentials(ctx context.Context, path, server, code, name string) (tunnel.Credentials, error) {
	if code != "" {
		if server == "" {
			return tunnel.Credentials{}, errors.New("--code needs --server, the hub's URL")
		}
		old, oldErr := tunnel.LoadCredentials(path)
		var prev string // registered with this hub before: replace that runner
		if u, err := tunnel.CheckServer(server); oldErr == nil && err == nil && old.Server == u.String() {
			prev = old.Token
		}
		c, err := tunnel.Register(ctx, server, code, name, prev)
		if err != nil {
			return c, err
		}
		if oldErr == nil {
			c.Projects = old.Projects
		}
		if err := tunnel.SaveCredentials(path, c); err != nil {
			return c, err
		}
		slog.Info("runner registered", "hub", c.Server, "name", c.Name, "saved", path)
		return c, nil
	}
	c, err := tunnel.LoadCredentials(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, errors.New("not registered yet: on the hub's Runners page, choose Add a runner and run the command it shows (or dash runner --server <hub URL> --code <code>)")
	}
	if err != nil {
		return c, err
	}
	if server != "" {
		u, err := tunnel.CheckServer(server)
		if err != nil {
			return c, err
		}
		if u.String() != c.Server {
			return c, fmt.Errorf("registered with %s, not %s: register with the new hub using --code", c.Server, u)
		}
	}
	return c, nil
}

// offlineWatch interrupts running tasks once the hub has been unreachable
// for grace: their permission prompts and reviews can't reach anyone.
func offlineWatch(grace time.Duration, todos *queue.TodoEngine) func(bool) {
	var mu sync.Mutex
	var timer *time.Timer
	return func(connected bool) {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			timer.Stop()
			timer = nil
		}
		if connected || grace <= 0 {
			return
		}
		timer = time.AfterFunc(grace, func() {
			if n := todos.Interrupt("this runner lost its connection to the hub for " + grace.String()); n > 0 {
				slog.Warn("runner: hub unreachable, interrupted running tasks", "tasks", n)
			}
		})
	}
}

// webhookPrivateAllowed reads -webhook-private: whether webhooks may reach
// private addresses. Unset, a private Dash (invite-only, no billing) allows
// it and a public one (Stripe billing or open signup) doesn't.
func webhookPrivateAllowed(v string, public bool) (bool, error) {
	switch strings.ToLower(v) {
	case "":
		return !public, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("want on or off, not %q", v)
}

// linkOrigin is where notifications link back to a project's tasks: its
// org's subdomain with org subdomains, else -public-url, else the listen
// address (fine on this computer only).
func linkOrigin(st *store.Store, d *orgdomain.Domain, publicURL, addr string, tls bool) func(projectID string) string {
	return func(projectID string) string {
		if d != nil {
			if org, ok := st.ProjectOrg(projectID); ok {
				if o, err := st.Accounts().Org(org); err == nil {
					return d.Origin(o.Slug)
				}
			}
		}
		if publicURL != "" {
			return strings.TrimRight(publicURL, "/")
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return ""
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "localhost"
		}
		scheme := "http"
		if tls {
			scheme = "https"
		}
		return scheme + "://" + net.JoinHostPort(host, port)
	}
}

// teePublisher passes the store's events on to the dashboards, and to also.
type teePublisher struct {
	store.Publisher
	also func(model.Event)
}

func (t *teePublisher) Publish(ev model.Event) {
	t.Publisher.Publish(ev)
	if t.also != nil {
		t.also(ev)
	}
}

// chain adds f to what also passes the events on to.
func (t *teePublisher) chain(f func(model.Event)) {
	prev := t.also
	if prev == nil {
		t.also = f
		return
	}
	t.also = func(ev model.Event) {
		prev(ev)
		f(ev)
	}
}

// serveLoopback serves h on a random 127.0.0.1 port for child processes on
// this machine and returns its base URL.
func serveLoopback(ctx context.Context, h http.Handler) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	go func() { <-ctx.Done(); _ = srv.Close() }()
	return "http://" + ln.Addr().String(), nil
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// resetTwoFactor is `dash reset-2fa [-data dir] <username or email>`: it
// turns off two-factor sign-in for someone who lost their authenticator and
// their recovery codes. It needs access to the server's data directory, which
// is the proof of being its operator. It works while Dash runs.
func resetTwoFactor(args []string) int {
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("dash reset-2fa", flag.ExitOnError)
	dataDir := fs.String("data", runnerctl.HubData(home), "the server's -data directory")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: dash reset-2fa [-data dir] <username or email>")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	path := filepath.Join(*dataDir, "dash.db")
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintln(os.Stderr, "no Dash database at", path, "(use -data)")
		return 1
	}
	st, err := store.Open(path, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open store:", err)
		return 1
	}
	defer st.Close()
	u, err := st.Accounts().ResetTOTP(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "reset two-factor sign-in:", err)
		return 1
	}
	fmt.Printf("Two-factor sign-in is off for %s. If an organization of theirs requires it, they set it up again when they next sign in.\n", u.Username)
	return 0
}

// backupSchedule is how often to back up and how many backups to keep,
// from the flags (negative or zero: not set), else the environment, else
// daily and 7.
func backupSchedule(every time.Duration, keep int) (time.Duration, int, error) {
	if every <= 0 {
		every = 24 * time.Hour
		if v := os.Getenv("DASH_BACKUP_EVERY"); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return 0, 0, fmt.Errorf("DASH_BACKUP_EVERY: want a duration such as 24h, not %q", v)
			}
			every = d
		}
	}
	if every < time.Minute {
		return 0, 0, fmt.Errorf("-backup-every must be at least a minute, not %s", every)
	}
	if keep < 0 {
		keep = 7
		if v := os.Getenv("DASH_BACKUP_KEEP"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return 0, 0, fmt.Errorf("DASH_BACKUP_KEEP: want a number, 0 or more, not %q", v)
			}
			keep = n
		}
	}
	return every, keep, nil
}

// backupCmd is `dash backup [-data dir] [-keep n] <file or directory>`: a
// consistent copy of the database, taken while Dash runs. Into a directory
// it writes a timestamped dash-<time>.db and, with -keep, removes older
// ones, so a cron job or systemd timer can call it as it is.
func backupCmd(args []string) int {
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("dash backup", flag.ExitOnError)
	dataDir := fs.String("data", runnerctl.HubData(home), "the server's -data directory")
	keep := fs.Int("keep", 0, "when backing up into a directory, keep this many timestamped backups there and remove older ones (0 = keep all)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: dash backup [-data dir] [-keep n] <file or directory>\n\n"+
			"Copies the database consistently while Dash runs. Into a directory, the copy is named dash-<UTC time>.db.")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	src, dest := filepath.Join(*dataDir, "dash.db"), fs.Arg(0)
	if _, err := os.Stat(src); err != nil {
		fmt.Fprintln(os.Stderr, "no Dash database at", src, "(use -data)")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if info, err := os.Stat(dest); err == nil && info.IsDir() || strings.HasSuffix(dest, string(filepath.Separator)) {
		path, err := store.BackupTo(ctx, src, dest, *keep, time.Now())
		if path == "" {
			fmt.Fprintln(os.Stderr, "backup:", err)
			return 1
		}
		fmt.Println(path)
		if err != nil { // written, but older ones weren't all removed
			fmt.Fprintln(os.Stderr, "backup:", err)
			return 1
		}
		return 0
	}
	if err := store.Backup(ctx, src, dest); err != nil {
		fmt.Fprintln(os.Stderr, "backup:", err)
		return 1
	}
	fmt.Println(dest)
	return 0
}
