// Command dash is the Dash orchestration server.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/api"
	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/billing"
	"github.com/klahr/dash/internal/branding"
	"github.com/klahr/dash/internal/buildinfo"
	"github.com/klahr/dash/internal/ci"
	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/mail"
	"github.com/klahr/dash/internal/mcptools"
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
	"github.com/klahr/dash/internal/tunnel"
	"github.com/klahr/dash/internal/usage"
	"github.com/klahr/dash/web"
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
	home, _ := os.UserHomeDir()
	showVersion := flag.Bool("version", false, "print the version and exit (also: dash version)")
	addr := flag.String("addr", "127.0.0.1:7788", "listen address; to reach Dash from other devices use HTTPS (-tls-cert, or a proxy with -trust-proxy)")
	tlsCert := flag.String("tls-cert", "", "serve HTTPS with this certificate (PEM), e.g. from certbot or `tailscale cert`")
	tlsKey := flag.String("tls-key", "", "private key for -tls-cert")
	trustProxy := flag.Bool("trust-proxy", false, "trust X-Forwarded-Proto/-For/-Host from a reverse proxy that terminates TLS (Caddy, nginx, a Cloudflare Tunnel, Tailscale Serve)")
	loginPerIP := flag.Int("login-max-failures", -1, fmt.Sprintf("failed sign-ins from one address within -login-window before it is locked out until the window passes (default $DASH_LOGIN_MAX_FAILURES, else %d)", auth.DefaultLockout.PerIP))
	loginGlobal := flag.Int("login-global-failures", -1, fmt.Sprintf("failed sign-ins from all addresses together within -login-window before every new sign-in is refused until the window passes; signed-in sessions carry on; 0 = no global limit (default $DASH_LOGIN_GLOBAL_FAILURES, else %d)", auth.DefaultLockout.Global))
	loginWindow := flag.Duration("login-window", 0, fmt.Sprintf("the window failed sign-ins are counted in (default $DASH_LOGIN_WINDOW, else %s)", auth.DefaultLockout.Window))
	cleanupDays := flag.Int("cleanup-days", 14, "daily cleanup: remove worktrees of tasks that failed this many days ago, plus orphaned worktrees, branches, logs and uploads (0 = off)")
	defaultData, mode := runnerctl.HubData(home), runnerctl.ModeHub
	if runnerMode {
		defaultData, mode = runnerctl.RunnerData(home), runnerctl.ModeRunner
	}
	dataDir := flag.String("data", defaultData, "state, logs and worktrees (a hub and a runner on one computer need different ones)")
	projectRoot := flag.String("projects", "", "where new projects are created (default $DASH_PROJECTS, else ~/dash-projects)")
	parallel := flag.Int("parallel", 3, "max tasks executed concurrently")
	claudeBin := flag.String("claude", "claude", "Claude Code binary used to execute tasks (dash runner only: the hub never runs Claude)")
	claudeArgs := flag.String("claude-args", "--permission-mode acceptEdits --allowedTools Bash",
		"extra flags for `claude -p` (space separated), e.g. --allowedTools / --model (dash runner only)")
	signCommits := flag.Bool("sign-commits", false, "GPG/SSH-sign commits Dash makes (needs a non-interactive signing agent)")
	chatBackend := flag.String("chat", "claude-code",
		"chat backend (dash runner only): claude-code (uses the claude CLI login, e.g. a subscription) or api (Anthropic API key)")
	chatTools := flag.String("chat-tools", agent.ChatShell,
		"claude-code chat tools: shell (every tool, your Claude Code settings and MCP servers, as in a terminal; you approve calls they don't allow in the chat) or readonly (Read, Grep, Glob)")
	var chatAllow []string
	flag.Func("chat-allow", "a tool the shell chat may use without asking, as for --allowedTools, e.g. \"Bash(ssh user@host *)\" (repeatable)", func(s string) error {
		chatAllow = append(chatAllow, s)
		return nil
	})
	pauseAt := flag.Float64("pause-at", 0.9, "pause the task queue when a subscription usage window reaches this fraction (0 = only when limited)")
	askPerms := flag.Bool("permission-prompts", true, "send Claude's permission requests to the UI instead of denying them")
	editor := flag.String("editor", os.Getenv("DASH_EDITOR"), "command that opens a worktree on this machine, e.g. \"code\" (or $DASH_EDITOR)")
	autoTitles := flag.Bool("auto-titles", true, "name tasks created without a title and summarise finished ones (a short claude call with -title-model)")
	titleModel := flag.String("title-model", "haiku", "model for automatic titles and summaries")
	hostname, _ := os.Hostname()
	machine := flag.String("name", strings.ToLower(strings.Split(hostname, ".")[0]), "this computer's name in the dashboard")
	publicURL := flag.String("public-url", "", "public HTTPS origin (e.g. https://dash.example.com) that serves /mcp with OAuth for claude.ai custom connectors; empty = off (or $DASH_PUBLIC_URL)")
	previewDomain := flag.String("preview-domain", "", "serve each task's live preview at https://<task>.<this domain>, e.g. preview.dash.example.com; needs a wildcard DNS record and certificate (see docs/previews.md; or $DASH_PREVIEW_DOMAIN)")
	orgDomain := flag.String("org-domain", "", "serve each organization at https://<slug>.<this domain>, e.g. dash.example.com; needs a wildcard DNS record and certificate (see docs/deploy.md; or $DASH_ORG_DOMAIN). Empty = off")
	orgReserved := flag.String("org-reserved", "", "comma-separated subdomains no organization may take, replacing the defaults ("+strings.Join(store.DefaultReservedSlugs, ",")+"); the preview domain's label is always reserved (or $DASH_ORG_RESERVED)")
	webhookPrivate := flag.String("webhook-private", "", "let outgoing notification webhooks reach private, loopback and link-local addresses: on, off, or empty = on unless Stripe billing is configured, i.e. on a self-hosted Dash (or $DASH_WEBHOOK_PRIVATE)")
	previewIdle := flag.Duration("preview-idle", preview.DefaultIdle, "stop a live preview after this long without traffic")
	scanTimeout := flag.Duration("scan-timeout", scan.DefaultRunTimeout, "stop a scan's Claude run after this long and mark it failed")
	scanSchedules := flag.Bool("scan-schedules", true, "run scans (.dash/scans) on their schedules; off = only when started by hand")
	envFile := flag.String("env", ".env", "file with DASH_USERNAME / DASH_PASSWORD (real env vars take precedence)")
	signup := flag.Bool("signup", false, "open signup: anyone may create an account with their email address (needs SMTP and -public-url; see docs/signup.md; or DASH_SIGNUP=1)")
	smtpHost := flag.String("smtp-host", "", "SMTP server for signup and password-reset emails (or $DASH_SMTP_HOST)")
	smtpPort := flag.Int("smtp-port", 0, "SMTP port: 587 (STARTTLS) or 465 (TLS) (default $DASH_SMTP_PORT, else 587)")
	smtpUser := flag.String("smtp-user", "", "SMTP username, with $DASH_SMTP_PASSWORD (or $DASH_SMTP_USERNAME)")
	smtpFrom := flag.String("smtp-from", "", "From address of Dash's emails, e.g. \"Dash <dash@example.com>\" (or $DASH_SMTP_FROM)")
	stripeKey := flag.String("stripe-secret-key", "", "Stripe secret key: turns on per-seat billing of orgs with two or more members (see docs/billing.md; or $STRIPE_SECRET_KEY)")
	stripeWebhook := flag.String("stripe-webhook-secret", "", "signing secret of the Stripe webhook endpoint /api/billing/webhook (or $STRIPE_WEBHOOK_SECRET)")
	stripePrice := flag.String("stripe-price", "", "Stripe price id (price_…) of the recurring per-seat price (or $STRIPE_PRICE_ID)")
	trialDays := flag.Int("billing-trial-days", -1, fmt.Sprintf("trial of an org's first subscription, in days; 0 = none (default $DASH_BILLING_TRIAL_DAYS, else %d)", billing.DefaultTrialDays))
	billingGrace := flag.Duration("billing-grace", 0, fmt.Sprintf("how long an org may need a subscription it doesn't have before its board becomes read-only (default $DASH_BILLING_GRACE, else %s)", billing.DefaultGrace))
	brandName := flag.String("brand-name", "", "product name in page titles, the sign-in pages and emails (default \"Dash\"; or $DASH_BRAND_NAME; see docs/branding.md)")
	sourceURL := flag.String("source-url", "", "the \"Source code\" link in every page's footer, for the AGPL (default the upstream repository; or $DASH_SOURCE_URL)")
	termsURL := flag.String("terms-url", "", "terms of service, linked in the footer (or $DASH_TERMS_URL)")
	privacyURL := flag.String("privacy-url", "", "privacy policy, linked in the footer (or $DASH_PRIVACY_URL)")
	contactEmail := flag.String("contact-email", "", "address people can write to, in the footer and emails (or $DASH_CONTACT_EMAIL)")
	logo := flag.String("logo", "", "image file shown instead of Dash's mark on the pages (or $DASH_LOGO)")
	landing := flag.Bool("landing", false, "show a landing page at / to visitors who aren't signed in, instead of the sign-in page (or DASH_LANDING=1)")
	teamPrice := flag.String("team-price", "", "the team plan's price on the landing page, as shown, e.g. \"€8 per seat / month\" (or $DASH_TEAM_PRICE)")
	pricingURL := flag.String("pricing-url", "", "where the landing page's \"see pricing\" links to when -team-price isn't set (or $DASH_PRICING_URL)")
	// Removed flags, still accepted so an old start command keeps working.
	var removed []string
	for _, name := range removedFlags {
		flag.Func(name, "removed and ignored: the hub never runs Claude or tasks itself", func(string) error {
			removed = append(removed, name)
			return nil
		})
	}
	runnerGrace := flag.Duration("runner-grace", dispatch.DefaultGrace, "fail the tasks of a runner that has been offline this long, ready to be queued again (hub)")
	var hubServer, regCode *string
	var offlineGrace *time.Duration
	var hubCommands *bool
	if runnerMode {
		flag.CommandLine.Init("dash runner", flag.ExitOnError)
		flag.CommandLine.Usage = func() {
			fmt.Fprintf(flag.CommandLine.Output(), "usage: dash runner [flags]          run this computer as a runner of a hub\n"+
				"       dash runner %s [-h]\n\nflags:\n", strings.Join(runnerctl.Commands, "|"))
			flag.PrintDefaults()
		}
		hubServer = flag.String("server", "", "the hub's URL, e.g. https://dash.example.com (saved after registering)")
		regCode = flag.String("code", "", "registration code from the hub's Runners page (only the first time, or to register again)")
		offlineGrace = flag.Duration("offline-grace", 10*time.Minute, "when the hub has been unreachable this long, stop running tasks and mark them interrupted (0 = never)")
		hubCommands = flag.Bool("hub-commands", false, "let the hub set the projects' run/test/deploy/setup commands and run ad-hoc commands; off = only commands set on this computer run")
	}
	_ = flag.CommandLine.Parse(args)
	if *showVersion {
		fmt.Println("dash", buildinfo.Version)
		return
	}
	addrSet := false
	flag.Visit(func(f *flag.Flag) { addrSet = addrSet || f.Name == "addr" })

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.Info("dash", "version", buildinfo.Version)
	for _, name := range removed {
		slog.Warn("-" + name + " is gone and ignored: the hub never runs Claude or tasks itself, runners (dash runner) do. Remove it from the start command")
	}

	if err := auth.LoadDotEnv(*envFile); err != nil {
		slog.Error("read env file", "path", *envFile, "err", err)
		os.Exit(1)
	}
	user, pass := os.Getenv("DASH_USERNAME"), os.Getenv("DASH_PASSWORD")
	if runnerMode && !addrSet && user == "" && pass == "" {
		// Nobody signs in to a runner without a listener of its own; the hub
		// reaches it through the tunnel.
		user, pass = "runner", rand.Text()
	}
	// A hub and a runner on one computer each have their own data directory.
	if runnerMode {
		if err := runnerctl.MigrateRunnerData(home, *dataDir); err != nil {
			slog.Error("move the runner's registration", "err", err)
			os.Exit(1)
		}
	}
	if err := runnerctl.ClaimData(*dataDir, mode); err != nil {
		slog.Error("data directory", "err", err)
		os.Exit(1)
	}
	events := hub.New()
	storeEvents := &teePublisher{Publisher: events}
	st, err := store.Open(filepath.Join(*dataDir, "dash.db"), storeEvents)
	if err != nil {
		slog.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	events.ProjectOrg = st.ProjectOrg // dashboards get only their org's events
	// The first start makes DASH_USERNAME / DASH_PASSWORD the first user;
	// later starts keep that user's name and password in step with them.
	if err := st.Accounts().Bootstrap(user, pass); err != nil {
		slog.Error("accounts", "err", err) // refuse to run open: Dash executes commands
		os.Exit(1)
	}
	authn := auth.New(st.Accounts())
	authn.Legacy(user, pass)
	authn.TrustProxy, hub.TrustProxy = *trustProxy, *trustProxy
	if authn.Lockout, err = lockoutConfig(*loginPerIP, *loginGlobal, *loginWindow); err != nil {
		slog.Error("sign-in lockout", "err", err)
		os.Exit(1)
	}
	if err := authn.ImportSessions(filepath.Join(*dataDir, "sessions.json")); err != nil {
		slog.Warn("import sessions.json", "err", err)
	}
	if *publicURL == "" {
		*publicURL = os.Getenv("DASH_PUBLIC_URL")
	}
	if *previewDomain == "" {
		*previewDomain = os.Getenv("DASH_PREVIEW_DOMAIN")
	}
	*orgDomain = envOr(*orgDomain, "DASH_ORG_DOMAIN")
	if *orgDomain != "" && !runnerMode {
		d, err := orgdomain.New(*orgDomain, orgdomain.ParseList(envOr(*orgReserved, "DASH_ORG_RESERVED")), *previewDomain)
		if err != nil {
			slog.Error("org subdomains", "err", err)
			os.Exit(1)
		}
		d.UseDefaults(*publicURL)
		if u, err := url.Parse(*publicURL); *publicURL != "" && (err != nil || !strings.EqualFold(u.Hostname(), d.Base)) {
			slog.Warn("-public-url isn't on the org domain: it should be https://" + d.Base)
		}
		authn.Domains = d
		st.SlugReserved = d.Reserved
		hub.ForwardedHostOK = d.Under
		fixed, err := st.Accounts().FixSlugs(time.Now())
		if err != nil {
			slog.Error("org subdomains", "err", err)
			os.Exit(1)
		}
		for _, o := range fixed {
			slog.Warn("organization's slug couldn't be a subdomain; renamed", "org", o.Name, "slug", o.Slug)
		}
		slog.Info("org subdomains", "hosts", "<org>."+d.Base, "reserved", strings.Join(d.ReservedList(), ","))
	}
	if *projectRoot == "" {
		*projectRoot = filepath.Join(home, "dash-projects")
		if v := os.Getenv("DASH_PROJECTS"); v != "" {
			*projectRoot = store.CleanPath(v)
		} else if c, err := tunnel.LoadCredentials(filepath.Join(*dataDir, "runner.json")); runnerMode && err == nil && c.Projects != "" {
			*projectRoot = c.Projects // from `dash runner setup --projects`
		}
	}
	var oauth *auth.OAuth
	if *publicURL != "" {
		if oauth, err = auth.NewOAuth(authn, *publicURL); err != nil {
			slog.Error("oauth", "err", err)
			os.Exit(1)
		}
		if err := oauth.ImportLegacy(filepath.Join(*dataDir, "oauth.json")); err != nil {
			slog.Warn("import oauth.json", "err", err)
		}
		slog.Info("remote MCP enabled", "url", oauth.Resource())
	}
	useTLS := *tlsCert != "" || *tlsKey != ""
	if useTLS && (*tlsCert == "" || *tlsKey == "") {
		slog.Error("-tls-cert and -tls-key go together")
		os.Exit(1)
	}
	if !useTLS && !*trustProxy && !isLoopback(*addr) {
		slog.Warn("Dash is reachable from the network over plain HTTP: passwords and session cookies travel unencrypted. " +
			"Use -tls-cert/-tls-key, or keep -addr on 127.0.0.1 behind an HTTPS proxy with -trust-proxy.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !runnerMode {
		// The hub runs nothing on its own computer: no git, gh, project
		// commands or terminals, and no working trees. Every way to run one
		// in this process refuses (localrun), behind the API forwarding each
		// project's work to the runners linked to it.
		localrun.Set(func() bool { return false })
	}

	// ---- services ----
	git := gitops.New(events)
	git.SignCommits = *signCommits
	procs := runner.NewManager(events)
	if err := procs.Keep(filepath.Join(*dataDir, "processes.json")); err != nil {
		slog.Warn("read processes", "err", err)
	}
	defer procs.StopAll()
	previews := preview.New(procs, events, st, *previewDomain)
	previews.TrustProxy, previews.Secure, previews.IdleAfter = *trustProxy, authn.Secure, *previewIdle
	storeEvents.also = previews.Observe // a task leaving review stops its preview
	if previews.Domain != "" {
		slog.Info("live previews", "hosts", "<task>."+previews.Domain)
	}

	tracker := usage.New(events, *pauseAt)
	perms := perm.NewBroker(events)
	perms.Saved = st
	todoLog := &queue.TodoLog{Pub: events, Root: filepath.Join(*dataDir, "logs")}
	merges := queue.NewMergeQueue(st, git, events, todoLog, 1024)
	uploads := filepath.Join(*dataDir, "uploads")
	merges.WorktreeRoot, merges.UploadRoot = filepath.Join(*dataDir, "worktrees"), uploads
	if *cleanupDays > 0 {
		merges.CleanupAfter, merges.CleanupEvery = time.Duration(*cleanupDays)*24*time.Hour, 24*time.Hour
	}
	todos := queue.NewTodoEngine(&queue.TodoEngine{
		Store:        st,
		Git:          git,
		Exec:         agent.ClaudeCode{Bin: *claudeBin, Args: strings.Fields(*claudeArgs), OnRateLimit: tracker.Observe},
		Pub:          events,
		Log:          todoLog,
		Merges:       merges,
		Usage:        tracker,
		WorktreeRoot: merges.WorktreeRoot,
		MaxParallel:  *parallel,
	})
	// Claude's short calls. Only a runner makes them: the hub has no tasks
	// of its own to summarise, and asks the caller's runner for titles and
	// clean-ups (Titler only says whether to name untitled tasks there).
	small := agent.Titler{Bin: *claudeBin, Model: *titleModel}
	if *autoTitles && runnerMode {
		todos.Summarizer = small
	}
	var titler interface {
		Title(ctx context.Context, prompt string) (string, error)
	}
	if *autoTitles {
		titler = small
	}
	// Scans read a throwaway checkout and file issues; they get their own
	// claude flags (read-only tools), not -claude-args.
	scans := &scan.Runner{
		Store: st, Git: git, Context: ctx,
		Exec:       agent.ClaudeCode{Bin: *claudeBin, OnRateLimit: tracker.Observe},
		Root:       filepath.Join(*dataDir, "scans"),
		RunTimeout: *scanTimeout,
		GitTools:   &scan.GitMCP{},
	}
	scans.Cleanup(ctx)
	scans.PruneLogs()
	scheduler := &scan.Scheduler{Store: st, Runner: scans, Usage: tracker}
	builds := &ci.Watcher{Store: st, Git: git, Pub: events, Kick: todos.Kick, Every: time.Minute}
	apiSrv := &api.Server{
		Store: st, Git: git, Hub: events, Todos: todos, Merges: merges, Procs: procs, Previews: previews,
		Auth:        authn,
		Perms:       perms,
		Board:       &mcptools.Tokens{},
		OAuth:       oauth,
		Usage:       tracker,
		Scans:       scans,
		Scheduler:   scheduler,
		Editor:      *editor,
		UploadRoot:  uploads,
		CI:          builds,
		Peers:       &peer.Hub{Self: *machine},
		Titler:      titler,
		ProjectRoot: *projectRoot,
		Static:      web.Static,
		Addr:        *addr,
		PublicURL:   strings.TrimRight(*publicURL, "/"),
		Brand: branding.Config{
			Name: envOr(*brandName, "DASH_BRAND_NAME"), SourceURL: envOr(*sourceURL, "DASH_SOURCE_URL"),
			TermsURL: envOr(*termsURL, "DASH_TERMS_URL"), PrivacyURL: envOr(*privacyURL, "DASH_PRIVACY_URL"),
			Contact: envOr(*contactEmail, "DASH_CONTACT_EMAIL"), Logo: envOr(*logo, "DASH_LOGO"),
			Landing: *landing || envBool("DASH_LANDING"), TeamPrice: envOr(*teamPrice, "DASH_TEAM_PRICE"),
			PricingURL: envOr(*pricingURL, "DASH_PRICING_URL"),
		}.WithDefaults(),
	}
	if err := apiSrv.Brand.Check(); err != nil {
		slog.Error("branding", "err", err) // a broken link in every page's footer
		os.Exit(1)
	}
	if apiSrv.Brand.Landing && !runnerMode {
		slog.Info("landing page on", "name", apiSrv.Brand.Name)
	}
	if !runnerMode {
		configureMail(authn, *signup, mail.Config{
			Host: envOr(*smtpHost, "DASH_SMTP_HOST"), Port: *smtpPort, Username: envOr(*smtpUser, "DASH_SMTP_USERNAME"),
			Password: os.Getenv("DASH_SMTP_PASSWORD"), From: envOr(*smtpFrom, "DASH_SMTP_FROM"),
		}, *publicURL)
	}
	if !runnerMode {
		apiSrv.Billing = configureBilling(st.Accounts(), billing.Config{
			SecretKey:     envOr(*stripeKey, "STRIPE_SECRET_KEY"),
			WebhookSecret: envOr(*stripeWebhook, "STRIPE_WEBHOOK_SECRET"),
			PriceID:       envOr(*stripePrice, "STRIPE_PRICE_ID"),
			TrialDays:     *trialDays,
			Grace:         *billingGrace,
			PublicURL:     *publicURL,
		})
		if b := apiSrv.Billing; b != nil {
			authn.OnMembersChanged, authn.InviteNotice = b.MembersChanged, b.InviteNotice
			todos.Hold, scheduler.Hold = b.Hold, b.Hold
			b.OnChange = func(int64) { todos.Kick() }
		}
		// Outgoing notification webhooks; a runner's hub sends its own.
		allowPrivate, err := webhookPrivateAllowed(envOr(*webhookPrivate, "DASH_WEBHOOK_PRIVATE"), apiSrv.Billing != nil)
		if err != nil {
			slog.Error("-webhook-private", "err", err)
			os.Exit(1)
		}
		notifier := &notify.Notifier{
			Store:        st,
			Origin:       linkOrigin(st, authn.Domains, *publicURL, *addr, useTLS),
			AllowPrivate: allowPrivate,
			Actions:      notify.NewActions(perms.Decide),
		}
		events.Observe = notifier.Observe
		notifier.Start(ctx)
		apiSrv.Notify = notifier
		if !allowPrivate {
			slog.Info("notification webhooks may not reach private addresses (-webhook-private=on allows them)")
		}
	}
	var runnerCreds tunnel.Credentials
	var reporter *dispatch.Reporter
	if runnerMode {
		apiSrv.HubCommands = *hubCommands
		credsPath := filepath.Join(*dataDir, "runner.json")
		if runnerCreds, err = runnerCredentials(ctx, credsPath, *hubServer, *regCode, *machine); err != nil {
			slog.Error("runner", "err", err)
			os.Exit(1)
		}
		apiSrv.HubOrg = runnerCreds.OrgID // projects of other orgs are refused
		// The hub may run the general chat and Claude's short calls here for
		// this runner's owner only, with this computer's `claude` login.
		apiSrv.HubUser, apiSrv.Small, apiSrv.Refiner = runnerCreds.UserID, small, small
		apiSrv.ChatDir = filepath.Join(*dataDir, "chat")
		// The board tools of the hub's tasks and chats act on the hub's board.
		apiSrv.HubBoard = func(ctx context.Context, hubTodo, hubProject string, body []byte) (*http.Response, error) {
			return tunnel.Board(ctx, runnerCreds, hubTodo, hubProject, body)
		}
		// The hub hears where its tasks are, and this computer's usage.
		reporter = &dispatch.Reporter{Store: st, Usage: tracker, Parallel: *parallel,
			Send: func(ctx context.Context, r dispatch.Report) error {
				err := tunnel.Report(ctx, runnerCreds, r)
				if errors.Is(err, tunnel.ErrRevoked) {
					return dispatch.ErrStop
				}
				return err
			}}
		also := storeEvents.also
		storeEvents.also = func(ev model.Event) {
			also(ev)
			reporter.Observe(ev)
		}
		// Claude's progress on the hub's tasks goes to the hub's board too.
		todoLog.Pub = &teePublisher{Publisher: events, also: reporter.Observe}
		go reporter.Run(ctx)
	} else {
		runners := &tunnel.Hub{Pub: events, Taken: apiSrv.Peers.Taken, PreviewURL: previews.URLTemplate, Accounts: st.Accounts()}
		apiSrv.Peers.Runners = runners
		previews.Remote = runners // runners' previews, served here
		if err := runners.Keep(filepath.Join(*dataDir, "runners.json")); err != nil {
			slog.Warn("read runners", "err", err)
		}
		// Tasks of projects linked to runners go to them; the hub only
		// coordinates.
		d := &dispatch.Dispatcher{Store: st, Fleet: runners, Grace: *runnerGrace, Kick: todos.Kick, Note: todoLog.Line, Pub: events}
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
		scans.Remote = d
		// The hub never runs Claude: every way to Claude in this process
		// refuses (agent.SetLocal), the chat and short calls go to the
		// caller's runner, and tasks of projects not linked to a runner
		// wait, saying one must be linked.
		never := func() bool { return false }
		agent.SetLocal(never)
		apiSrv.LocalClaude, todos.LocalTasks = never, never
		// Nor anything else: a project's git, files, releases and commands
		// are forwarded to its runners, GitHub's builds read with their gh.
		apiSrv.RunnerOnly = true
		builds.Git = api.RunnerGitHub{S: apiSrv}
		merges.CleanupEvery, merges.PRPoll = 0, 0 // the runners clean up and follow pull requests
		slog.Info("this hub runs nothing itself: tasks, scans, chats, git and project commands run on runners. Start `dash runner` on a computer with git and Claude Code (account menu → Runners → Add a runner)")
		warnUnlinked(st.ListProjects())
		d.Start()
	}
	// Every per-run MCP token acts as its task's (or chat's) user, in its org,
	// and only while they belong to it.
	todos.Owner = apiSrv.TaskOwner
	perms.Member = apiSrv.Member
	// Revoking a runner's token in Settings disconnects the runner.
	authn.OnRevoke = func(t store.Token) {
		if t.Kind == store.TokenRunner && apiSrv.Peers.Runners != nil {
			_ = apiSrv.Peers.Runners.Revoke(t.RunnerID)
		}
	}

	var mcpURL, permURL string
	{
		// Claude Code reaches Dash's MCP servers (permission prompts, the chat's
		// task tools, the tasks' issue tools) on a loopback-only plain-HTTP
		// listener, whatever -addr and TLS are set to.
		mcp := http.NewServeMux()
		if *askPerms {
			mcp.HandleFunc("/mcp/permissions", perms.ServeMCP)
		}
		mcp.Handle("/mcp/dash", apiSrv.BoardMCP())
		mcp.Handle("/mcp/scan-git", scans.GitTools)
		mcpURL, err = serveLoopback(ctx, mcp)
		if err != nil {
			slog.Error("MCP listener", "err", err)
			os.Exit(1)
		}
		if *askPerms {
			permURL = mcpURL
			todos.Perms, todos.BaseURL = perms, mcpURL
		}
		todos.Board, todos.BoardURL = apiSrv.Board, mcpURL
		scans.GitURL = strings.TrimRight(mcpURL, "/") + "/mcp/scan-git"
	}
	// Once scans have their git tools, and before the scheduler's first
	// check: a scan Dash's last stop cut off starts over, and the
	// scheduler's catch-up then finds it running.
	scans.ResumeInterrupted()
	merges.Perms, merges.Kick = perms, todos.Kick

	// A runner finds its hub's projects by id: the clones made under
	// <projects>/hub/ before that stay where they are (with their worktrees
	// and task branches), marked once as the hub's.
	if runnerMode && apiSrv.ProjectRoot != "" {
		if names, err := st.BackfillHubCheckouts(api.LegacyHubClone(apiSrv.ProjectRoot)); err != nil {
			slog.Warn("hub projects under hub/", "err", err)
		} else if len(names) > 0 {
			slog.Info("the hub's projects under hub/ stay there, now found by id", "projects", names)
		}
	}

	// Projects adopted with a default branch the repo doesn't have (e.g. "main"
	// in a master repo) get the repo's real main line.
	for _, p := range st.ListProjects() {
		if !runnerMode {
			break // the hub has no checkouts: a runner's clone finds origin's main line (see api's linkOn)
		}
		if _, err := os.Stat(p.Path); err != nil || git.HasBranch(ctx, p.ID, p.Path, p.DefaultBranch) {
			continue
		}
		if b := git.MainBranch(ctx, p.ID, p.Path); b != "" {
			if _, err := st.UpdateProject(p.ID, func(p *model.Project) error { p.DefaultBranch = b; return nil }); err == nil {
				slog.Info("default branch fixed", "project", p.Name, "branch", b)
			}
		}
	}

	// GitHub projects land work through pull requests, so GitHub's checks
	// run before it reaches the default branch, unless their user chose
	// otherwise; projects that can't open pull requests (no GitHub remote,
	// or no gh here) merge locally.
	if names, err := st.DefaultMergeModes(runnerMode && git.HasGh()); err != nil {
		slog.Warn("default merge modes", "err", err)
	} else if len(names) > 0 {
		slog.Info("projects switched to their default way of landing work (pull requests on GitHub, else merging)", "projects", names)
	}

	go builds.Run(ctx)
	if *scanSchedules {
		go scheduler.Run(ctx)
	}
	go previews.Run(ctx)
	go todos.Run(ctx)
	merges.Resume()
	go merges.Run(ctx) // the one and only merge worker
	if runnerMode {
		go merges.Remote.Run(ctx) // the hub fetches nothing: its projects' clones are on runners
	}

	// The chat runs on runners only: the hub relays it to the caller's.
	switch chat := *chatBackend; {
	case !runnerMode:
	case chat == "claude-code":
		if *chatTools != agent.ChatReadOnly && *chatTools != agent.ChatShell {
			slog.Error("unknown -chat-tools", "value", *chatTools)
			os.Exit(1)
		}
		if *chatTools == agent.ChatShell && !*askPerms {
			slog.Warn("-chat-tools shell without -permission-prompts: the chat can only use tools allowed with -chat-allow")
		}
		apiSrv.Chat = agent.ClaudeCodeChat{
			Bin: *claudeBin, Dir: home, Tools: *chatTools, AllowedTools: chatAllow,
			Perms: perms, PermURL: permURL, Board: apiSrv.Board, BoardURL: mcpURL, OnRateLimit: tracker.Observe,
		}
	case chat == "api":
		apiSrv.Chat = agent.NewChat()
	default:
		slog.Error("unknown -chat backend", "value", *chatBackend)
		os.Exit(1)
	}

	if runnerMode {
		offline := offlineWatch(*offlineGrace, todos)
		statePath := runnerctl.StatePath(*dataDir)
		watch := func(connected bool) {
			offline(connected)
			// For `dash runner status`.
			st := runnerctl.State{PID: os.Getpid(), Version: buildinfo.Version, Connected: connected, Since: time.Now()}
			if err := runnerctl.WriteState(statePath, st); err != nil {
				slog.Warn("write runner state", "err", err)
			}
		}
		watch(false) // tasks resumed at startup count as offline until the hub answers
		onState := func(connected bool) {
			watch(connected)
			if connected {
				reporter.Nudge() // tell the hub what happened while it couldn't hear
			}
		}
		defer os.Remove(statePath)
		client := &tunnel.Client{Creds: runnerCreds, Handler: apiSrv.RunnerRoutes(), OnState: onState, Version: buildinfo.Version,
			Previews: previews, OnPreviewURL: func(t string) { previews.SetHub(t, runnerCreds.Name) }}
		if !addrSet {
			if err := client.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("runner", "err", err)
				os.Remove(statePath)
				if errors.Is(err, tunnel.ErrRevoked) {
					os.Exit(runnerctl.ExitRevoked) // the service doesn't restart it
				}
				os.Exit(1)
			}
			return
		}
		go func() {
			if err := client.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("runner", "err", err)
				stop()
			}
		}()
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           apiSrv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	slog.Info("dash listening", "url", scheme+"://"+*addr, "data", *dataDir)
	if useTLS {
		err = srv.ListenAndServeTLS(*tlsCert, *tlsKey)
	} else {
		err = srv.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}

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
// private addresses. Unset, a self-hosted Dash (no billing) allows it and
// a hosted one doesn't.
func webhookPrivateAllowed(v string, hosted bool) (bool, error) {
	switch strings.ToLower(v) {
	case "":
		return !hosted, nil
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
	dataDir := fs.String("data", filepath.Join(home, ".local", "share", "dash"), "the server's -data directory")
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
