package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/api"
	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/billing"
	"github.com/klahr/dash/internal/branding"
	"github.com/klahr/dash/internal/buildinfo"
	"github.com/klahr/dash/internal/ci"
	"github.com/klahr/dash/internal/claudecli"
	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/mail"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/metrics"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/notify"
	"github.com/klahr/dash/internal/orgdomain"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/preview"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/runner"
	"github.com/klahr/dash/internal/runnerctl"
	"github.com/klahr/dash/internal/scan"
	"github.com/klahr/dash/internal/store"
	boardsync "github.com/klahr/dash/internal/tracker"
	"github.com/klahr/dash/internal/tracker/favro" // the Favro board backend
	"github.com/klahr/dash/internal/tunnel"
	"github.com/klahr/dash/internal/usage"
	"github.com/klahr/dash/web"
)

// fatal logs msg with its key-value pairs and exits: a setting Dash can't
// start with.
func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

// options are the command line of `dash` and `dash runner`.
type options struct {
	runnerMode bool
	home       string
	mode       string // runnerctl.ModeHub or ModeRunner

	showVersion                      bool
	addr, tlsCert, tlsKey            string
	addrSet                          bool // -addr given
	trustProxy                       bool
	loginPerIP, loginGlobal          int
	loginWindow                      time.Duration
	cleanupDays, logMaxMB            int
	backupDir                        string
	backupKeep                       int
	backupEvery                      time.Duration
	logFormat, logLevel, metricsAddr string
	dataDir, projectRoot             string
	parallel                         int
	claudeBin, claudeArgs            string
	signCommits                      bool
	chatBackend, chatTools           string
	chatAllow                        []string
	chatCommandTimeout               time.Duration
	pauseAt                          float64
	askPerms                         bool
	editor                           string
	autoTitles                       bool
	titleModel, machine              string
	publicURL, previewDomain         string
	orgDomain, orgReserved           string
	webhookPrivate, allowUnsafe      string
	previewIdle, scanTimeout         time.Duration
	scanSchedules                    bool
	envFile                          string
	signup                           bool
	smtpHost                         string
	smtpPort                         int
	smtpUser, smtpFrom               string
	stripeKey, stripeWebhook         string
	stripePrice                      string
	trialDays                        int
	billingGrace                     time.Duration
	brandName, sourceURL             string
	termsURL, privacyURL             string
	contactEmail, logo               string
	landing                          bool
	teamPrice, pricingURL            string
	runnerGrace                      time.Duration
	removed, ignored                 []string // removed flags, and a runner's flags given to the hub
	hubServer, regCode               string   // runner
	offlineGrace                     time.Duration
	hubCommands                      bool
	githubProtocol                   string
}

// parseFlags reads the command line (without `runner`) into options.
func parseFlags(args []string, runnerMode bool) *options {
	o := &options{runnerMode: runnerMode}
	o.home, _ = os.UserHomeDir()
	f := flag.CommandLine
	f.BoolVar(&o.showVersion, "version", false, "print the version and exit (also: dash version)")
	f.StringVar(&o.addr, "addr", "127.0.0.1:7788", "listen address; to reach Dash from other devices use HTTPS (-tls-cert, or a proxy with -trust-proxy)")
	f.StringVar(&o.tlsCert, "tls-cert", "", "serve HTTPS with this certificate (PEM), e.g. from certbot or `tailscale cert`")
	f.StringVar(&o.tlsKey, "tls-key", "", "private key for -tls-cert")
	f.BoolVar(&o.trustProxy, "trust-proxy", false, "trust X-Forwarded-Proto/-For/-Host from a reverse proxy that terminates TLS (Caddy, nginx, a Cloudflare Tunnel, Tailscale Serve)")
	f.IntVar(&o.loginPerIP, "login-max-failures", -1, fmt.Sprintf("failed sign-ins from one address within -login-window before it is locked out until the window passes (default $DASH_LOGIN_MAX_FAILURES, else %d)", auth.DefaultLockout.PerIP))
	f.IntVar(&o.loginGlobal, "login-global-failures", -1, fmt.Sprintf("failed sign-ins from all addresses together within -login-window before every new sign-in is refused until the window passes; signed-in sessions carry on; 0 = no global limit (default $DASH_LOGIN_GLOBAL_FAILURES, else %d)", auth.DefaultLockout.Global))
	f.DurationVar(&o.loginWindow, "login-window", 0, fmt.Sprintf("the window failed sign-ins are counted in (default $DASH_LOGIN_WINDOW, else %s)", auth.DefaultLockout.Window))
	f.IntVar(&o.cleanupDays, "cleanup-days", 14, "daily cleanup: remove worktrees of tasks that failed this many days ago, plus orphaned worktrees, branches, logs and uploads (0 = off)")
	f.IntVar(&o.logMaxMB, "log-max-mb", 32, "cap each task's log files at this many MB: a longer log keeps its newest half (0 = no cap)")
	f.StringVar(&o.backupDir, "backup-dir", "", "write a timestamped backup of the database (dash-<time>.db) to this directory every -backup-every; empty = off (or $DASH_BACKUP_DIR)")
	f.IntVar(&o.backupKeep, "backup-keep", -1, "keep this many backups in -backup-dir, removing older ones; 0 = keep all (default $DASH_BACKUP_KEEP, else 7)")
	f.DurationVar(&o.backupEvery, "backup-every", 0, "how often to back up to -backup-dir (default $DASH_BACKUP_EVERY, else 24h)")
	f.StringVar(&o.logFormat, "log-format", "", "log as text (key=value lines) or json (one object per line, for log shippers) (default $DASH_LOG_FORMAT, else text)")
	f.StringVar(&o.logLevel, "log-level", "", "log debug, info, warn or error and above (default $DASH_LOG_LEVEL, else info)")
	f.StringVar(&o.metricsAddr, "metrics-addr", "", "serve /metrics (Prometheus) and /healthz on this extra listener, e.g. 127.0.0.1:9090; only on loopback unless $DASH_METRICS_TOKEN is set (or $DASH_METRICS_ADDR; see docs/deploy.md#monitoring)")
	defaultData, mode := runnerctl.HubData(o.home), runnerctl.ModeHub
	if runnerMode {
		defaultData, mode = runnerctl.RunnerData(o.home), runnerctl.ModeRunner
	}
	o.mode = mode
	f.StringVar(&o.dataDir, "data", defaultData, "state, logs and worktrees (a hub and a runner on one computer need different ones)")
	f.StringVar(&o.projectRoot, "projects", "", "where new projects are created (default $DASH_PROJECTS, else ~/dash-projects)")
	f.IntVar(&o.parallel, "parallel", 3, "max tasks executed concurrently")
	f.StringVar(&o.claudeBin, "claude", "claude", "Claude Code binary used to execute tasks")
	f.StringVar(&o.claudeArgs, "claude-args", "--permission-mode acceptEdits --allowedTools Bash",
		"extra flags for `claude -p` (space separated), e.g. --allowedTools / --model")
	f.BoolVar(&o.signCommits, "sign-commits", false, "GPG/SSH-sign commits Dash makes (needs a non-interactive signing agent)")
	f.StringVar(&o.chatBackend, "chat", "claude-code",
		"chat backend: claude-code (uses the claude CLI login, e.g. a subscription) or api (Anthropic API key)")
	f.StringVar(&o.chatTools, "chat-tools", agent.ChatShell,
		"claude-code chat tools: shell (every tool, your Claude Code settings and MCP servers, as in a terminal; you approve calls they don't allow in the chat) or readonly (Read, Grep, Glob)")
	f.Func("chat-allow", "a tool the shell chat may use without asking, as for --allowedTools, e.g. \"Bash(ssh user@host *)\" (repeatable)", func(s string) error {
		o.chatAllow = append(o.chatAllow, s)
		return nil
	})
	f.DurationVar(&o.chatCommandTimeout, "chat-command-timeout", agent.DefaultShellTimeout, "how long a command run from a chat with a leading ! may run before it is stopped")
	f.Float64Var(&o.pauseAt, "pause-at", 0.9, "pause the task queue when a subscription usage window reaches this fraction (0 = only when limited)")
	f.BoolVar(&o.askPerms, "permission-prompts", true, "send Claude's permission requests to the UI instead of denying them")
	f.StringVar(&o.editor, "editor", os.Getenv("DASH_EDITOR"), "command that opens a worktree on this machine, e.g. \"code\" (or $DASH_EDITOR)")
	f.BoolVar(&o.autoTitles, "auto-titles", true, "name tasks created without a title and summarise finished ones (a short claude call with -title-model)")
	f.StringVar(&o.titleModel, "title-model", "haiku", "model for automatic titles and summaries")
	hostname, _ := os.Hostname()
	f.StringVar(&o.machine, "name", strings.ToLower(strings.Split(hostname, ".")[0]), "this computer's name in the dashboard")
	f.StringVar(&o.publicURL, "public-url", "", "public HTTPS origin (e.g. https://dash.example.com) that serves /mcp with OAuth for claude.ai custom connectors; empty = off (or $DASH_PUBLIC_URL)")
	f.StringVar(&o.previewDomain, "preview-domain", "", "serve each task's live preview at https://<task>.<this domain>, e.g. preview.dash.example.com; needs a wildcard DNS record and certificate (see docs/previews.md; or $DASH_PREVIEW_DOMAIN)")
	f.StringVar(&o.orgDomain, "org-domain", "", "serve each organization at https://<slug>.<this domain>, e.g. dash.example.com; needs a wildcard DNS record and certificate (see docs/deploy.md; or $DASH_ORG_DOMAIN). Empty = off")
	f.StringVar(&o.orgReserved, "org-reserved", "", "comma-separated subdomains no organization may take, replacing the defaults ("+strings.Join(store.DefaultReservedSlugs, ",")+"); the preview domain's label is always reserved (or $DASH_ORG_RESERVED)")
	f.StringVar(&o.webhookPrivate, "webhook-private", "", "let outgoing notification webhooks reach private, loopback and link-local addresses: on, off, or empty = on unless Stripe billing is configured, signup is open or Dash is internet-facing (-public-url or -trust-proxy), i.e. on an invite-only private Dash (or $DASH_WEBHOOK_PRIVATE)")
	f.StringVar(&o.allowUnsafe, "allow-unsafe", "", "start an internet-facing Dash (-public-url or -trust-proxy) despite these failed safety checks, with a warning: comma-separated "+strings.Join(exposureChecks, ",")+", or all (see docs/deploy.md; or $DASH_ALLOW_UNSAFE)")
	f.DurationVar(&o.previewIdle, "preview-idle", preview.DefaultIdle, "stop a live preview after this long without traffic")
	f.DurationVar(&o.scanTimeout, "scan-timeout", scan.DefaultRunTimeout, "stop a scan's Claude run after this long and mark it failed")
	f.BoolVar(&o.scanSchedules, "scan-schedules", true, "run scans (.dash/scans) on their schedules; off = only when started by hand")
	f.StringVar(&o.envFile, "env", ".env", "file with DASH_USERNAME / DASH_PASSWORD (real env vars take precedence)")
	f.BoolVar(&o.signup, "signup", false, "open signup: anyone may create an account with their email address (needs SMTP and -public-url; see docs/signup.md; or DASH_SIGNUP=1)")
	f.StringVar(&o.smtpHost, "smtp-host", "", "SMTP server for signup and password-reset emails (or $DASH_SMTP_HOST)")
	f.IntVar(&o.smtpPort, "smtp-port", 0, "SMTP port: 587 (STARTTLS) or 465 (TLS) (default $DASH_SMTP_PORT, else 587)")
	f.StringVar(&o.smtpUser, "smtp-user", "", "SMTP username, with $DASH_SMTP_PASSWORD (or $DASH_SMTP_USERNAME)")
	f.StringVar(&o.smtpFrom, "smtp-from", "", "From address of Dash's emails, e.g. \"Dash <dash@example.com>\" (or $DASH_SMTP_FROM)")
	f.StringVar(&o.stripeKey, "stripe-secret-key", "", "Stripe secret key: turns on per-seat billing of orgs with two or more members (see docs/billing.md; or $STRIPE_SECRET_KEY)")
	f.StringVar(&o.stripeWebhook, "stripe-webhook-secret", "", "signing secret of the Stripe webhook endpoint /api/billing/webhook (or $STRIPE_WEBHOOK_SECRET)")
	f.StringVar(&o.stripePrice, "stripe-price", "", "Stripe price id (price_…) of the recurring per-seat price (or $STRIPE_PRICE_ID)")
	f.IntVar(&o.trialDays, "billing-trial-days", -1, fmt.Sprintf("trial of an org's first subscription, in days; 0 = none (default $DASH_BILLING_TRIAL_DAYS, else %d)", billing.DefaultTrialDays))
	f.DurationVar(&o.billingGrace, "billing-grace", 0, fmt.Sprintf("how long an org may need a subscription it doesn't have before its board becomes read-only (default $DASH_BILLING_GRACE, else %s)", billing.DefaultGrace))
	f.StringVar(&o.brandName, "brand-name", "", "product name in page titles, the sign-in pages and emails (default \"Dash\"; or $DASH_BRAND_NAME; see docs/branding.md)")
	f.StringVar(&o.sourceURL, "source-url", "", "the \"Source code\" link in every page's footer, for the AGPL (default the upstream repository; or $DASH_SOURCE_URL)")
	f.StringVar(&o.termsURL, "terms-url", "", "terms of service, linked in the footer (or $DASH_TERMS_URL)")
	f.StringVar(&o.privacyURL, "privacy-url", "", "privacy policy, linked in the footer (or $DASH_PRIVACY_URL)")
	f.StringVar(&o.contactEmail, "contact-email", "", "address people can write to, in the footer and emails (or $DASH_CONTACT_EMAIL)")
	f.StringVar(&o.logo, "logo", "", "image file shown instead of Dash's mark on the pages (or $DASH_LOGO)")
	f.BoolVar(&o.landing, "landing", false, "show a landing page at / to visitors who aren't signed in, instead of the sign-in page (or DASH_LANDING=1)")
	f.StringVar(&o.teamPrice, "team-price", "", "the team plan's price on the landing page, as shown, e.g. \"€8 per seat / month\" (or $DASH_TEAM_PRICE)")
	f.StringVar(&o.pricingURL, "pricing-url", "", "where the landing page's \"see pricing\" links to when -team-price isn't set (or $DASH_PRICING_URL)")
	// Removed flags, still accepted so an old start command keeps working.
	for _, name := range removedFlags {
		f.Func(name, "removed and ignored: the hub never runs Claude or tasks itself", func(string) error {
			o.removed = append(o.removed, name)
			return nil
		})
	}
	f.DurationVar(&o.runnerGrace, "runner-grace", dispatch.DefaultGrace, "fail the tasks of a runner that has been offline this long, ready to be queued again (hub)")
	if runnerMode {
		f.Init("dash runner", flag.ExitOnError)
		f.Usage = func() {
			fmt.Fprintf(f.Output(), "usage: dash runner [flags]          run this computer as a runner of a hub\n"+
				"       dash runner %s [-h]\n\nflags:\n", strings.Join(runnerctl.Commands, "|"))
			f.PrintDefaults()
		}
		f.StringVar(&o.hubServer, "server", "", "the hub's URL, e.g. https://dash.example.com (saved after registering)")
		f.StringVar(&o.regCode, "code", "", "registration code from the hub's Runners page (only the first time, or to register again)")
		f.DurationVar(&o.offlineGrace, "offline-grace", 10*time.Minute, "when the hub has been unreachable this long, stop running tasks and mark them interrupted (0 = never)")
		f.StringVar(&o.githubProtocol, "github-protocol", "", "how this runner clones, fetches and pushes the hub's GitHub projects: ssh (git@github.com:…, with this computer's SSH keys) or https (needs a git credential helper, e.g. gh auth setup-git) (default $DASH_GITHUB_PROTOCOL, else ssh); a project's clone URL, when set, is used as it is")
		f.BoolVar(&o.hubCommands, "hub-commands", false, "let the hub set the projects' run/test/deploy/setup commands and run ad-hoc commands, and open terminals for the runner's owner from the hub; off = only commands set on this computer run")
	} else {
		f.Usage = func() {
			fmt.Fprintf(f.Output(), "usage: dash [flags]          run the hub, the dashboard (see dash runner -h for a runner's flags)\n"+
				"       dash version|healthcheck|backup|reset-2fa\n\nflags:\n")
			printFlags(f, func(name string) bool {
				return slices.Contains(runnerFlags, name) || slices.Contains(removedFlags, name)
			})
		}
	}
	_ = f.Parse(args)
	f.Visit(func(fl *flag.Flag) {
		o.addrSet = o.addrSet || fl.Name == "addr"
		// The hub runs no tasks, Claude or commands: the flags about those
		// are a runner's. Old start commands may still pass them.
		if !runnerMode && slices.Contains(runnerFlags, fl.Name) {
			o.ignored = append(o.ignored, fl.Name)
		}
	})
	return o
}

// resolveEnv fills in the settings left empty on the command line from the
// environment (after the env file is read).
func (o *options) resolveEnv() {
	o.publicURL = envOr(o.publicURL, "DASH_PUBLIC_URL")
	o.previewDomain = envOr(o.previewDomain, "DASH_PREVIEW_DOMAIN")
	o.orgDomain = envOr(o.orgDomain, "DASH_ORG_DOMAIN")
	o.metricsAddr = envOr(o.metricsAddr, "DASH_METRICS_ADDR")
	o.githubProtocol = envOr(o.githubProtocol, "DASH_GITHUB_PROTOCOL")
}

// app is a running hub or runner: what the startup phases make and wire up.
type app struct {
	o                                   *options
	user, pass                          string
	ctx                                 context.Context
	stop                                context.CancelFunc
	useTLS                              bool
	events                              *hub.Hub
	storeEvents, procEvents, permEvents *teePublisher
	st                                  *store.Store
	authn                               *auth.Auth
	oauth                               *auth.OAuth
	git                                 *gitops.Git
	procs                               *runner.Manager
	previews                            *preview.Manager
	tracker                             *usage.Tracker
	cli                                 *claudecli.Monitor
	perms                               *perm.Broker
	todoLog                             *queue.TodoLog
	merges                              *queue.MergeQueue
	todos                               *queue.TodoEngine
	small                               agent.Titler
	scans                               *scan.Runner
	scheduler                           *scan.Scheduler
	builds                              *ci.Watcher
	apiSrv                              *api.Server
	exposed                             exposure
	metricsToken                        string
	boards                              *boardsync.Manager // hub
	runnerCreds                         tunnel.Credentials // runner
	reporter                            *dispatch.Reporter // runner
	mcpURL, permURL                     string
}

// start starts Dash with the options and serves until it is stopped.
func start(o *options) {
	a := &app{o: o}
	a.openState()
	defer a.st.Close()
	a.configureOrgDomain()
	a.resolveProjectRoot()
	a.configureGoogle()
	a.configureOAuthTLS()

	a.ctx, a.stop = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer a.stop()
	if !o.runnerMode {
		// The hub runs nothing on its own computer: no Claude, git, gh,
		// project commands or terminals, and no working trees. Every way to
		// run one in this process refuses (localrun), behind the API
		// forwarding each project's work to the runners linked to it.
		localrun.Set(func() bool { return false })
	}

	a.buildServices()
	defer a.procs.StopAll()
	a.configureServer()
	if o.runnerMode {
		a.setupRunner()
	} else {
		a.setupHub()
		defer a.boards.StopAll() // before the store closes
	}
	a.wireOwners()
	a.startMCPListener()
	a.fixProjects()
	a.startWorkers()
	// On the way out (SIGTERM, Ctrl-C), before the store closes: running
	// tasks stop their Claude and test processes (each in a process group
	// of its own, which would outlive Dash and run beside the resumed task)
	// and stay in progress, and a merge in hand stays merging; both resume
	// at the next start.
	defer func() {
		a.stop()
		if !a.todos.Wait(shutdownWait) {
			slog.Warn("shutdown: tasks still stopping", "after", shutdownWait)
		}
		if !a.merges.Wait(shutdownWait) {
			slog.Warn("shutdown: the merge worker is still stopping", "after", shutdownWait)
		}
	}()
	if o.runnerMode {
		go a.merges.Remote.Run(a.ctx) // the hub fetches nothing: its projects' clones are on runners
	}
	a.configureChat()

	if o.runnerMode {
		statePath := runnerctl.StatePath(o.dataDir)
		defer os.Remove(statePath)
		if !a.connectHub(statePath) {
			return // the tunnel ended
		}
	}
	a.serve()
}

// openState claims the data directory and opens the store, the accounts and
// sign-in.
func (a *app) openState() {
	o := a.o
	a.user, a.pass = os.Getenv("DASH_USERNAME"), os.Getenv("DASH_PASSWORD")
	if o.runnerMode && !o.addrSet && a.user == "" && a.pass == "" {
		// Nobody signs in to a runner without a listener of its own; the hub
		// reaches it through the tunnel.
		a.user, a.pass = "runner", rand.Text()
	}
	// A hub and a runner on one computer each have their own data directory.
	if o.runnerMode {
		if err := runnerctl.MigrateRunnerData(o.home, o.dataDir); err != nil {
			fatal("move the runner's registration", "err", err)
		}
	}
	if err := runnerctl.ClaimData(o.dataDir, o.mode); err != nil {
		fatal("data directory", "err", err)
	}
	a.events = hub.New()
	a.storeEvents = &teePublisher{Publisher: a.events}
	st, err := store.Open(filepath.Join(o.dataDir, "dash.db"), a.storeEvents)
	if err != nil {
		fatal("open store", "err", err)
	}
	a.st = st
	a.events.ProjectOrg = st.ProjectOrg // dashboards get only their org's events
	// The first start makes DASH_USERNAME / DASH_PASSWORD the first user;
	// later starts keep that user's name and password in step with them.
	if err := st.Accounts().Bootstrap(a.user, a.pass); err != nil {
		fatal("accounts", "err", err) // refuse to run open: Dash executes commands
	}
	a.authn = auth.New(st.Accounts())
	a.authn.Legacy(a.user, a.pass)
	a.authn.TrustProxy, hub.TrustProxy = o.trustProxy, o.trustProxy
	if a.authn.Lockout, err = lockoutConfig(o.loginPerIP, o.loginGlobal, o.loginWindow); err != nil {
		fatal("sign-in lockout", "err", err)
	}
	if err := a.authn.ImportSessions(filepath.Join(o.dataDir, "sessions.json")); err != nil {
		slog.Warn("import sessions.json", "err", err)
	}
}

// configureOrgDomain serves each organization on a subdomain of
// -org-domain (hub).
func (a *app) configureOrgDomain() {
	o := a.o
	if o.orgDomain == "" || o.runnerMode {
		return
	}
	d, err := orgdomain.New(o.orgDomain, orgdomain.ParseList(envOr(o.orgReserved, "DASH_ORG_RESERVED")), o.previewDomain)
	if err != nil {
		fatal("org subdomains", "err", err)
	}
	d.UseDefaults(o.publicURL)
	if u, err := url.Parse(o.publicURL); o.publicURL != "" && (err != nil || !strings.EqualFold(u.Hostname(), d.Base)) {
		slog.Warn("-public-url isn't on the org domain: it should be https://" + d.Base)
	}
	a.authn.Domains = d
	a.st.SlugReserved = d.Reserved
	hub.ForwardedHostOK = d.Under
	fixed, err := a.st.Accounts().FixSlugs(time.Now())
	if err != nil {
		fatal("org subdomains", "err", err)
	}
	for _, org := range fixed {
		slog.Warn("organization's slug couldn't be a subdomain; renamed", "org", org.Name, "slug", org.Slug)
	}
	slog.Info("org subdomains", "hosts", "<org>."+d.Base, "reserved", strings.Join(d.ReservedList(), ","))
}

// resolveProjectRoot picks where new projects are created when -projects
// isn't set.
func (a *app) resolveProjectRoot() {
	o := a.o
	if o.projectRoot != "" {
		return
	}
	o.projectRoot = filepath.Join(o.home, "dash-projects")
	if v := os.Getenv("DASH_PROJECTS"); v != "" {
		o.projectRoot = store.CleanPath(v)
	} else if c, err := tunnel.LoadCredentials(filepath.Join(o.dataDir, "runner.json")); o.runnerMode && err == nil && c.Projects != "" {
		o.projectRoot = c.Projects // from `dash runner setup --projects`
	}
}

// configureGoogle sets up Sign in with Google (docs/users.md) and the hub's
// secret key: the instance's client from the environment; orgs' own clients
// and one set in settings are sealed in the database with the secret key.
func (a *app) configureGoogle() {
	o, authn := a.o, a.authn
	if o.runnerMode {
		return
	}
	authn.PublicURL = o.publicURL
	authn.Google = auth.GoogleConfig{ClientID: os.Getenv("DASH_GOOGLE_CLIENT_ID"), ClientSecret: os.Getenv("DASH_GOOGLE_CLIENT_SECRET")}
	if (authn.Google.ClientID == "") != (authn.Google.ClientSecret == "") {
		fatal("DASH_GOOGLE_CLIENT_ID and DASH_GOOGLE_CLIENT_SECRET go together")
	}
	key, err := auth.LoadSecretKey(os.Getenv("DASH_SECRET_KEY"), filepath.Join(o.dataDir, "secret.key"))
	if err == nil {
		err = authn.SetSecretKey(key)
	}
	if err != nil {
		fatal("secret key", "err", err)
	}
	if authn.Google.ClientID != "" && o.publicURL != "" {
		slog.Info("google sign-in", "redirect_uri", strings.TrimRight(o.publicURL, "/")+auth.GoogleCallbackPath)
	} else if authn.Google.ClientID != "" {
		slog.Warn("google sign-in without -public-url: the redirect URI follows the host the browser used; set DASH_PUBLIC_URL to the one registered with Google")
	}
}

// configureOAuthTLS turns on remote MCP with -public-url and checks the TLS
// flags.
func (a *app) configureOAuthTLS() {
	o := a.o
	if o.publicURL != "" {
		var err error
		if a.oauth, err = auth.NewOAuth(a.authn, o.publicURL); err != nil {
			fatal("oauth", "err", err)
		}
		if err := a.oauth.ImportLegacy(filepath.Join(o.dataDir, "oauth.json")); err != nil {
			slog.Warn("import oauth.json", "err", err)
		}
		slog.Info("remote MCP enabled", "url", a.oauth.Resource())
	}
	a.useTLS = o.tlsCert != "" || o.tlsKey != ""
	if a.useTLS && (o.tlsCert == "" || o.tlsKey == "") {
		fatal("-tls-cert and -tls-key go together")
	}
	if !a.useTLS && !o.trustProxy && !isLoopback(o.addr) {
		slog.Warn("Dash is reachable from the network over plain HTTP: passwords and session cookies travel unencrypted. " +
			"Use -tls-cert/-tls-key, or keep -addr on 127.0.0.1 behind an HTTPS proxy with -trust-proxy.")
	}
}

// buildServices makes the task queue, merges, previews, scans and the API
// server.
func (a *app) buildServices() {
	o, st, events, ctx := a.o, a.st, a.events, a.ctx
	a.git = gitops.New(events)
	a.git.SignCommits = o.signCommits
	a.procEvents = &teePublisher{Publisher: events}
	a.procs = runner.NewManager(a.procEvents)
	if err := a.procs.Keep(filepath.Join(o.dataDir, "processes.json")); err != nil {
		slog.Warn("read processes", "err", err)
	}
	a.previews = preview.New(a.procs, events, st, o.previewDomain)
	a.previews.TrustProxy, a.previews.Secure, a.previews.IdleAfter = o.trustProxy, a.authn.Secure, o.previewIdle
	a.storeEvents.chain(a.previews.Observe) // a task leaving review stops its preview
	if a.previews.Domain != "" {
		slog.Info("live previews", "hosts", "<task>."+a.previews.Domain)
	}

	a.tracker = usage.New(events, o.pauseAt)
	// What looks changed about the installed Claude Code CLI: checked on a
	// runner, shown in task logs and on the hub's Runners page.
	a.cli = &claudecli.Monitor{Bin: o.claudeBin}
	// A runner tells its hub of the requests of the hub's tasks (see reporter).
	a.permEvents = &teePublisher{Publisher: events}
	a.perms = perm.NewBroker(a.permEvents)
	a.perms.Saved = st
	a.todoLog = &queue.TodoLog{Pub: events, Root: filepath.Join(o.dataDir, "logs"), MaxBytes: int64(max(o.logMaxMB, 0)) << 20}
	a.merges = queue.NewMergeQueue(st, a.git, events, a.todoLog, 1024)
	uploads := filepath.Join(o.dataDir, "uploads")
	a.merges.WorktreeRoot, a.merges.UploadRoot = filepath.Join(o.dataDir, "worktrees"), uploads
	if o.cleanupDays > 0 {
		a.merges.CleanupAfter, a.merges.CleanupEvery = time.Duration(o.cleanupDays)*24*time.Hour, 24*time.Hour
	}
	a.todos = queue.NewTodoEngine(&queue.TodoEngine{
		Store:        st,
		Git:          a.git,
		Exec:         agent.ClaudeCode{Bin: o.claudeBin, Args: strings.Fields(o.claudeArgs), OnRateLimit: a.tracker.Observe, CLI: a.cli},
		Pub:          events,
		Log:          a.todoLog,
		Merges:       a.merges,
		Usage:        a.tracker,
		WorktreeRoot: a.merges.WorktreeRoot,
		MaxParallel:  o.parallel,
	})
	// Claude's short calls. Only a runner makes them: the hub has no tasks
	// of its own to summarise, and asks the caller's runner for titles and
	// clean-ups (Titler only says whether to name untitled tasks there).
	a.small = agent.Titler{Bin: o.claudeBin, Model: o.titleModel}
	if o.autoTitles && o.runnerMode {
		a.todos.Summarizer = a.small
	}
	var titler interface {
		Title(ctx context.Context, prompt string) (string, error)
	}
	if o.autoTitles {
		titler = a.small
	}
	// Scans read a throwaway checkout and file issues; they get their own
	// claude flags (read-only tools), not -claude-args.
	a.scans = &scan.Runner{
		Store: st, Git: a.git, Context: ctx,
		Exec:       agent.ClaudeCode{Bin: o.claudeBin, OnRateLimit: a.tracker.Observe, CLI: a.cli},
		Root:       filepath.Join(o.dataDir, "scans"),
		RunTimeout: o.scanTimeout,
		GitTools:   &scan.GitMCP{},
	}
	// How often a scan waiting for a runner asks again (default 5s): only
	// dash's own end-to-end tests set it, so they don't wait seconds for it.
	if v := os.Getenv("DASH_TEST_SCAN_WAIT_EVERY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			a.scans.WaitEvery = d
		}
	}
	a.scans.Cleanup(ctx)
	a.scans.PruneLogs()
	a.scheduler = &scan.Scheduler{Store: st, Runner: a.scans, Usage: a.tracker}
	a.builds = &ci.Watcher{Store: st, Git: a.git, Pub: events, Kick: a.todos.Kick, Every: time.Minute}
	// Try sessions' worktrees (a runner's): none outlives Dash, so those a
	// stopped Dash left are removed (git forgets them at the next prune).
	tryRoot := filepath.Join(o.dataDir, "try")
	_ = os.RemoveAll(tryRoot)
	a.apiSrv = &api.Server{
		Store: st, Git: a.git, Hub: events, Todos: a.todos, Merges: a.merges, Procs: a.procs, Previews: a.previews,
		Auth:        a.authn,
		Perms:       a.perms,
		Board:       &mcptools.Tokens{},
		OAuth:       a.oauth,
		Usage:       a.tracker,
		Scans:       a.scans,
		Scheduler:   a.scheduler,
		Editor:      o.editor,
		UploadRoot:  uploads,
		TryRoot:     tryRoot,
		CI:          a.builds,
		Peers:       &peer.Hub{Self: o.machine},
		Titler:      titler,
		ProjectRoot: o.projectRoot,
		Static:      web.Static,
		PublicURL:   strings.TrimRight(o.publicURL, "/"),
		Brand: branding.Config{
			Name: envOr(o.brandName, "DASH_BRAND_NAME"), SourceURL: envOr(o.sourceURL, "DASH_SOURCE_URL"),
			TermsURL: envOr(o.termsURL, "DASH_TERMS_URL"), PrivacyURL: envOr(o.privacyURL, "DASH_PRIVACY_URL"),
			Contact: envOr(o.contactEmail, "DASH_CONTACT_EMAIL"), Logo: envOr(o.logo, "DASH_LOGO"),
			Landing: o.landing || envBool("DASH_LANDING"), TeamPrice: envOr(o.teamPrice, "DASH_TEAM_PRICE"),
			PricingURL: envOr(o.pricingURL, "DASH_PRICING_URL"),
		}.WithDefaults(),
	}
}

// configureServer checks monitoring, branding, email and the safety of an
// internet-facing Dash.
func (a *app) configureServer() {
	o, apiSrv, authn := a.o, a.apiSrv, a.authn
	// Monitoring: /healthz always; /metrics with a token on this listener,
	// and on -metrics-addr.
	a.metricsToken = os.Getenv("DASH_METRICS_TOKEN")
	if err := checkMetrics(o.metricsAddr, a.metricsToken); err != nil {
		fatal("metrics", "err", err)
	}
	apiSrv.MetricsToken = a.metricsToken
	apiSrv.RegisterMetrics(metrics.Default)
	if err := apiSrv.Brand.Check(); err != nil {
		fatal("branding", "err", err) // a broken link in every page's footer
	}
	if apiSrv.Brand.Landing && !o.runnerMode {
		slog.Info("landing page on", "name", apiSrv.Brand.Name)
	}
	if !o.runnerMode {
		configureMail(authn, o.signup, mail.Config{
			Host: envOr(o.smtpHost, "DASH_SMTP_HOST"), Port: o.smtpPort, Username: envOr(o.smtpUser, "DASH_SMTP_USERNAME"),
			Password: os.Getenv("DASH_SMTP_PASSWORD"), From: envOr(o.smtpFrom, "DASH_SMTP_FROM"),
		}, o.publicURL)
	}
	// An internet-facing Dash refuses weak credentials and unsafe settings
	// (a runner without a listener of its own is reached only through its
	// hub's tunnel).
	a.exposed = exposure{
		PublicURL: o.publicURL, TrustProxy: o.trustProxy, Addr: o.addr,
		Username: a.user, Password: os.Getenv("DASH_PASSWORD"),
		SignupOpen: authn.SignupOpen(), MailOn: authn.MailOn(),
		AllowUnsafe: envOr(o.allowUnsafe, "DASH_ALLOW_UNSAFE"),
	}
	if o.runnerMode && !o.addrSet {
		a.exposed = exposure{}
	}
	if err := checkExposure(a.exposed); err != nil {
		fatal("unsafe configuration", "err", err)
	}
	// It answers with HSTS and Secure cookies whatever the proxy says.
	authn.Public = a.exposed.Public()
}

// setupHub wires the hub: billing, notifications, external boards, and the
// runners it dispatches tasks to.
func (a *app) setupHub() {
	o, st, events, ctx, apiSrv, authn, todos := a.o, a.st, a.events, a.ctx, a.apiSrv, a.authn, a.todos
	apiSrv.Billing = configureBilling(st.Accounts(), billing.Config{
		SecretKey:     envOr(o.stripeKey, "STRIPE_SECRET_KEY"),
		WebhookSecret: envOr(o.stripeWebhook, "STRIPE_WEBHOOK_SECRET"),
		PriceID:       envOr(o.stripePrice, "STRIPE_PRICE_ID"),
		TrialDays:     o.trialDays,
		Grace:         o.billingGrace,
		PublicURL:     o.publicURL,
	})
	if b := apiSrv.Billing; b != nil {
		authn.OnMembersChanged, authn.InviteNotice = b.MembersChanged, b.InviteNotice
		todos.Hold, a.scheduler.Hold = b.Hold, b.Hold
		b.OnChange = func(int64) { todos.Kick() }
	}
	// Outgoing notification webhooks; a runner's hub sends its own.
	// Strangers may create webhooks when billing is on or signup is
	// open, and anyone who takes over an account can on an
	// internet-facing hub: none of them may reach this hub's network by
	// default.
	public := apiSrv.Billing != nil || authn.SignupOpen() || a.exposed.Public()
	wp := envOr(o.webhookPrivate, "DASH_WEBHOOK_PRIVATE")
	allowPrivate, err := webhookPrivateAllowed(wp, public)
	if err != nil {
		fatal("-webhook-private", "err", err)
	}
	if allowPrivate && a.exposed.Public() {
		slog.Warn("UNSAFE: notification webhooks of this internet-facing hub may reach private, loopback and link-local addresses " +
			"(-webhook-private=" + wp + "): whoever takes over an account can probe this hub's network")
	}
	perms := a.perms
	notifier := &notify.Notifier{
		Store:        st,
		Origin:       linkOrigin(st, authn.Domains, o.publicURL, o.addr, a.useTLS),
		AllowPrivate: allowPrivate,
		Actions: notify.NewActions(func(id, decision, message string) error {
			err := perms.Decide(id, decision, message)
			if errors.Is(err, perm.ErrNotFound) && apiSrv.Dispatch != nil {
				// A request of a hub's task, waiting on its runner.
				dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				err = apiSrv.Dispatch.DecidePermission(dctx, id, decision, message)
			}
			return err
		}),
	}
	// Web Push to the browsers that turned notifications on, signed
	// with the hub's VAPID key (made on first start, then kept).
	if key, err := st.PushVAPIDKey(notify.NewVAPIDPrivateKey); err != nil {
		slog.Warn("web push off: VAPID key", "err", err)
	} else if notifier.VAPID, err = notify.ParseVAPIDKey(key); err != nil {
		slog.Warn("web push off: VAPID key", "err", err)
	} else {
		notifier.Push = st
	}
	events.Observe = notifier.Observe
	notifier.Start(ctx)
	apiSrv.Notify = notifier

	// Projects whose board is an external tracker (Favro…) are synced
	// with it, one worker each; a runner's projects are its hub's.
	// Only dash's own end-to-end tests set it: Favro's API at a fake.
	if v := os.Getenv("DASH_TEST_FAVRO_URL"); v != "" {
		favro.SetDefaultBaseURL(v)
	}
	a.boards = &boardsync.Manager{Store: st, Kick: todos.Kick, PublicURL: o.publicURL}
	a.storeEvents.chain(a.boards.Observe)
	if err := a.boards.Start(ctx); err != nil {
		slog.Warn("board backends", "err", err)
	}
	apiSrv.Trackers = a.boards
	if !allowPrivate {
		slog.Info("notification webhooks may not reach private addresses (-webhook-private=on allows them)")
	}

	runners := &tunnel.Hub{Pub: events, Taken: apiSrv.Peers.Taken, PreviewURL: a.previews.URLTemplate, Accounts: st.Accounts()}
	apiSrv.Peers.Runners = runners
	a.previews.Remote = runners // runners' previews, served here
	if err := runners.Keep(filepath.Join(o.dataDir, "runners.json")); err != nil {
		slog.Warn("read runners", "err", err)
	}
	// Tasks of projects linked to runners go to them; the hub only
	// coordinates.
	todoLog := a.todoLog
	d := &dispatch.Dispatcher{Store: st, Fleet: runners, Grace: o.runnerGrace, Kick: todos.Kick, Note: todoLog.Line, Pub: events}
	d.Undo = func(t model.Todo, files []string) {
		nt, err := queue.UndoTodo(st, t, t.MergedCommit, files)
		if err != nil {
			todoLog.Line(t, "✖ revert: "+err.Error())
			return
		}
		todoLog.Line(t, "⚠ reverting on the runner conflicts with later work in "+strings.Join(files, ", ")+"; queued \""+nt.Title+"\" for Claude")
		todos.Kick()
	}
	runners.OnState, runners.OnRevoke, runners.OnReport = d.State, d.Revoked, d.Report
	todos.Remote, apiSrv.Dispatch = d, d
	// While the hub doesn't run Claude, scans run their Claude part on
	// a runner linked to the project (the hub keeps the runs and issues).
	a.scans.Remote = d
	// The hub never runs Claude (the chat and short calls go to the
	// caller's runner, tasks of projects not linked to a runner wait,
	// saying one must be linked), nor anything else: a project's git,
	// files, releases and commands are forwarded to its runners,
	// GitHub's builds read with their gh. localrun refuses whatever
	// would still try to run here.
	a.builds.Git = api.RunnerGitHub{S: apiSrv}
	a.merges.CleanupEvery, a.merges.PRPoll = 0, 0 // the runners clean up and follow pull requests
	slog.Info("this hub runs nothing itself: tasks, scans, chats, git and project commands run on runners. Start `dash runner` on a computer with git and Claude Code (account menu → Runners → Add a runner)")
	warnUnlinked(st.ListProjects())
	d.Start()
}

// setupRunner registers with the hub (or reads the registration) and tells
// it of this computer's tasks, pushes, processes and permission requests.
func (a *app) setupRunner() {
	o, st, events, ctx, apiSrv, cli, merges := a.o, a.st, a.events, a.ctx, a.apiSrv, a.cli, a.merges
	apiSrv.HubCommands = o.hubCommands
	var err error
	if apiSrv.GitHubProtocol, err = gitops.ParseGitHubProtocol(o.githubProtocol); err != nil {
		slog.Error("runner: -github-protocol", "err", err)
		os.Exit(2)
	}
	credsPath := filepath.Join(o.dataDir, "runner.json")
	if a.runnerCreds, err = runnerCredentials(ctx, credsPath, o.hubServer, o.regCode, o.machine); err != nil {
		fatal("runner", "err", err)
	}
	creds := a.runnerCreds
	apiSrv.HubOrg = creds.OrgID // projects of other orgs are refused
	// The hub may run the general chat and Claude's short calls here for
	// this runner's owner only, with this computer's `claude` login.
	apiSrv.HubUser, apiSrv.Small, apiSrv.Refiner, apiSrv.Splitter = creds.UserID, a.small, a.small, a.small
	apiSrv.ChatDir = filepath.Join(o.dataDir, "chat")
	apiSrv.ChatCommandTimeout = o.chatCommandTimeout
	// The board tools of the hub's tasks and chats act on the hub's board.
	apiSrv.HubBoard = func(ctx context.Context, hubTodo, hubProject string, body []byte) (*http.Response, error) {
		return tunnel.Board(ctx, creds, hubTodo, hubProject, body)
	}
	// The hub hears where its tasks are, and this computer's usage.
	reporter := &dispatch.Reporter{Store: st, Usage: a.tracker, Parallel: o.parallel, Terminals: o.hubCommands, Claude: cli,
		ChatCommands: !(o.chatBackend == "claude-code" && o.chatTools == agent.ChatReadOnly),
		Send: func(ctx context.Context, r dispatch.Report) error {
			err := tunnel.Report(ctx, creds, r)
			if errors.Is(err, tunnel.ErrRevoked) {
				return dispatch.ErrStop
			}
			return err
		}}
	a.reporter = reporter
	a.storeEvents.chain(reporter.Observe)
	// Claude's progress on the hub's tasks goes to the hub's board too.
	a.todoLog.Pub = &teePublisher{Publisher: events, also: reporter.Observe}
	// Pushes to origin go to the hub, which tells the project's other
	// runners at once; and the hub hears which checkouts are behind.
	merges.OnPushed = func(p model.Project, branch, sha string) {
		if p.HubProjectID != "" {
			reporter.Pushed(dispatch.Push{HubProjectID: p.HubProjectID, Branch: branch, SHA: sha})
		}
	}
	merges.Remote.Pub = &teePublisher{Publisher: events, also: reporter.Observe}
	reporter.Checkouts = func() []dispatch.CheckoutState {
		var out []dispatch.CheckoutState
		for _, rs := range merges.Remote.Behind() {
			if p, err := st.GetProject(rs.ProjectID); err == nil && p.HubProjectID != "" {
				out = append(out, dispatch.CheckoutState{HubProjectID: p.HubProjectID, Branch: rs.Branch,
					Behind: rs.Behind, Ahead: rs.Ahead, Stuck: rs.Stuck})
			}
		}
		return out
	}
	// …and when its processes start or end, so its dashboards list them again.
	a.procEvents.also = reporter.Observe
	// Claude's permission requests in the hub's tasks are answered on
	// the hub's board.
	perms := a.perms
	reporter.Perms = func() []perm.Request { return perms.Pending("") }
	a.permEvents.also = reporter.Observe
	go reporter.Run(ctx)
	// The hub hears of the Claude Code version and warnings at once.
	cli.OnChange(func() {
		for _, w := range cli.Warnings() {
			slog.Warn("claude code cli", "warning", w)
		}
		reporter.Nudge()
	})
	go cli.Run(ctx, time.Hour) // Claude Code updates itself
}

// wireOwners makes every per-run MCP token act as its task's (or chat's)
// user, in its org, and only while they belong to it.
func (a *app) wireOwners() {
	apiSrv := a.apiSrv
	a.todos.Owner = apiSrv.TaskOwner
	a.perms.Member = apiSrv.Member
	// Revoking a runner's token in Settings disconnects the runner.
	a.authn.OnRevoke = func(t store.Token) {
		if t.Kind == store.TokenRunner && apiSrv.Peers.Runners != nil {
			_ = apiSrv.Peers.Runners.Revoke(t.RunnerID)
		}
	}
}

// startMCPListener serves Dash's MCP servers to Claude Code (permission
// prompts, the chat's task tools, the tasks' issue tools) on a
// loopback-only plain-HTTP listener, whatever -addr and TLS are set to.
func (a *app) startMCPListener() {
	mcp := http.NewServeMux()
	if a.o.askPerms {
		mcp.HandleFunc("/mcp/permissions", a.perms.ServeMCP)
	}
	mcp.Handle("/mcp/dash", a.apiSrv.BoardMCP())
	mcp.Handle("/mcp/scan-git", a.scans.GitTools)
	var err error
	if a.mcpURL, err = serveLoopback(a.ctx, mcp); err != nil {
		fatal("MCP listener", "err", err)
	}
	if a.o.askPerms {
		a.permURL = a.mcpURL
		a.todos.Perms, a.todos.BaseURL = a.perms, a.mcpURL
	}
	a.todos.Board, a.todos.BoardURL = a.apiSrv.Board, a.mcpURL
	a.scans.GitURL = strings.TrimRight(a.mcpURL, "/") + "/mcp/scan-git"
	// Once scans have their git tools, and before the scheduler's first
	// check: a scan Dash's last stop cut off starts over, and the
	// scheduler's catch-up then finds it running.
	a.scans.ResumeInterrupted()
	a.merges.Perms, a.merges.Kick = a.perms, a.todos.Kick
}

// fixProjects brings projects saved by older versions up to date.
func (a *app) fixProjects() {
	o, st, git, ctx := a.o, a.st, a.git, a.ctx
	// A runner finds its hub's projects by id: the clones made under
	// <projects>/hub/ before that stay where they are (with their worktrees
	// and task branches), marked once as the hub's.
	if o.runnerMode && a.apiSrv.ProjectRoot != "" {
		if names, err := st.BackfillHubCheckouts(api.LegacyHubClone(a.apiSrv.ProjectRoot)); err != nil {
			slog.Warn("hub projects under hub/", "err", err)
		} else if len(names) > 0 {
			slog.Info("the hub's projects under hub/ stay there, now found by id", "projects", names)
		}
	}

	// Projects adopted with a default branch the repo doesn't have (e.g.
	// "main" in a master repo) get the repo's real main line. The hub has no
	// checkouts: a runner's clone finds origin's main line (see api's linkOn).
	if o.runnerMode {
		for _, p := range st.ListProjects() {
			if _, err := os.Stat(p.Path); err != nil || git.HasBranch(ctx, p.ID, p.Path, p.DefaultBranch) {
				continue
			}
			if b := git.MainBranch(ctx, p.ID, p.Path); b != "" {
				if _, err := st.UpdateProject(p.ID, func(p *model.Project) error { p.DefaultBranch = b; return nil }); err == nil {
					slog.Info("default branch fixed", "project", p.Name, "branch", b)
				}
			}
		}
	}

	// GitHub projects land work through pull requests, so GitHub's checks
	// run before it reaches the default branch, unless their user chose
	// otherwise; projects that can't open pull requests (no GitHub remote,
	// or no gh here) merge locally.
	if names, err := st.DefaultMergeModes(o.runnerMode && git.HasGh()); err != nil {
		slog.Warn("default merge modes", "err", err)
	} else if len(names) > 0 {
		slog.Info("projects switched to their default way of landing work (pull requests on GitHub, else merging)", "projects", names)
	}
}

// startWorkers starts backups, CI, scans, previews, tasks and merges.
func (a *app) startWorkers() {
	o, ctx := a.o, a.ctx
	if dir := envOr(o.backupDir, "DASH_BACKUP_DIR"); dir != "" {
		every, keep, err := backupSchedule(o.backupEvery, o.backupKeep)
		if err != nil {
			fatal("backups", "err", err)
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(o.dataDir, dir)
		}
		slog.Info("scheduled backups", "dir", dir, "every", every, "keep", keep)
		go store.ScheduleBackups(ctx, filepath.Join(o.dataDir, "dash.db"), dir, every, keep)
	}
	go a.builds.Run(ctx)
	if o.scanSchedules {
		go a.scheduler.Run(ctx)
	}
	go a.previews.Run(ctx)
	go a.todos.Run(ctx)
	a.merges.Resume()
	go a.merges.Run(ctx) // one merge worker per project
}

// configureChat picks the chat backend. The chat runs on runners only: the
// hub relays it to the caller's.
func (a *app) configureChat() {
	o := a.o
	switch chat := o.chatBackend; {
	case !o.runnerMode:
	case chat == "claude-code":
		if o.chatTools != agent.ChatReadOnly && o.chatTools != agent.ChatShell {
			fatal("unknown -chat-tools", "value", o.chatTools)
		}
		if o.chatTools == agent.ChatShell && !o.askPerms {
			slog.Warn("-chat-tools shell without -permission-prompts: the chat can only use tools allowed with -chat-allow")
		}
		a.apiSrv.Chat = agent.ClaudeCodeChat{
			Bin: o.claudeBin, Dir: o.home, Tools: o.chatTools, AllowedTools: o.chatAllow,
			Perms: a.perms, PermURL: a.permURL, Board: a.apiSrv.Board, BoardURL: a.mcpURL, OnRateLimit: a.tracker.Observe,
			CLI: a.cli,
		}
	case chat == "api":
		a.apiSrv.Chat = agent.NewChat()
	default:
		fatal("unknown -chat backend", "value", o.chatBackend)
	}
}

// connectHub keeps a runner connected to its hub, writing its state to
// statePath for `dash runner status`. Without a listener of its own (no
// -addr) it serves only the tunnel, returning false once that ends;
// otherwise the tunnel runs in the background and it returns true.
func (a *app) connectHub(statePath string) bool {
	ctx := a.ctx
	offline := offlineWatch(a.o.offlineGrace, a.todos)
	watch := func(connected bool) {
		offline(connected)
		st := runnerctl.State{PID: os.Getpid(), Version: buildinfo.Version, Connected: connected, Since: time.Now()}
		if err := runnerctl.WriteState(statePath, st); err != nil {
			slog.Warn("write runner state", "err", err)
		}
	}
	watch(false) // tasks resumed at startup count as offline until the hub answers
	onState := func(connected bool) {
		watch(connected)
		if connected {
			a.reporter.Nudge() // tell the hub what happened while it couldn't hear
			// Catch up on what the other runners pushed meanwhile.
			go a.merges.Remote.FetchAll(ctx)
		}
	}
	creds := a.runnerCreds
	client := &tunnel.Client{Creds: creds, Handler: a.apiSrv.RunnerRoutes(), OnState: onState, Version: buildinfo.Version,
		Previews: a.previews, OnPreviewURL: func(t string) { a.previews.SetHub(t, creds.Name) }}
	if !a.o.addrSet {
		if err := client.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("runner", "err", err)
			os.Remove(statePath)
			if errors.Is(err, tunnel.ErrRevoked) {
				os.Exit(runnerctl.ExitRevoked) // the service doesn't restart it
			}
			os.Exit(1)
		}
		return false
	}
	go func() {
		if err := client.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("runner", "err", err)
			a.stop()
		}
	}()
	return true
}

// serve starts the metrics listener and serves the dashboard and API on
// -addr until Dash is stopped.
func (a *app) serve() {
	o, ctx := a.o, a.ctx
	if o.metricsAddr != "" {
		if err := serveOps(ctx, o.metricsAddr, a.apiSrv.OpsHandler(a.metricsToken)); err != nil {
			fatal("metrics listener", "err", err)
		}
		slog.Info("metrics listening", "url", "http://"+o.metricsAddr+"/metrics", "token", a.metricsToken != "")
	} else if a.metricsToken != "" {
		slog.Info("metrics on /metrics, for callers with DASH_METRICS_TOKEN")
	}

	srv := &http.Server{
		Addr:              o.addr,
		Handler:           a.apiSrv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	shutDown := make(chan struct{})
	go func() {
		defer close(shutDown)
		<-ctx.Done()
		slog.Info("shutting down", "reason", context.Cause(ctx))
		// Requests in flight (an approval, a runner's report) finish;
		// runners' tunnels drop and they reconnect to the next hub.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	scheme := "http"
	if a.useTLS {
		scheme = "https"
	}
	slog.Info("dash listening", "url", scheme+"://"+o.addr, "data", o.dataDir)
	var err error
	if a.useTLS {
		err = srv.ListenAndServeTLS(o.tlsCert, o.tlsKey)
	} else {
		err = srv.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("server", "err", err)
	}
	<-shutDown // ListenAndServe returns at once; Shutdown waits for the requests
}
