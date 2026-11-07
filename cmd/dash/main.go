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
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/api"
	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/ci"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/preview"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/runner"
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
	runnerMode := len(args) > 0 && args[0] == "runner"
	if runnerMode {
		args = args[1:]
	}
	home, _ := os.UserHomeDir()
	addr := flag.String("addr", "127.0.0.1:7788", "listen address; to reach Dash from other devices use HTTPS (-tls-cert, or a proxy with -trust-proxy)")
	tlsCert := flag.String("tls-cert", "", "serve HTTPS with this certificate (PEM), e.g. from certbot or `tailscale cert`")
	tlsKey := flag.String("tls-key", "", "private key for -tls-cert")
	trustProxy := flag.Bool("trust-proxy", false, "trust X-Forwarded-Proto/-For/-Host from a reverse proxy that terminates TLS (Caddy, nginx, a Cloudflare Tunnel, Tailscale Serve)")
	cleanupDays := flag.Int("cleanup-days", 14, "daily cleanup: remove worktrees of tasks that failed this many days ago, plus orphaned worktrees, branches, logs and uploads (0 = off)")
	dataDir := flag.String("data", filepath.Join(home, ".local", "share", "dash"), "state, logs and worktrees")
	projectRoot := flag.String("projects", "", "where new projects are created (default $DASH_PROJECTS, else ~/dash-projects)")
	parallel := flag.Int("parallel", 3, "max tasks executed concurrently")
	claudeBin := flag.String("claude", "claude", "Claude Code binary used to execute tasks")
	claudeArgs := flag.String("claude-args", "--permission-mode acceptEdits --allowedTools Bash",
		"extra flags for `claude -p` (space separated), e.g. --allowedTools / --model")
	signCommits := flag.Bool("sign-commits", false, "GPG/SSH-sign commits Dash makes (needs a non-interactive signing agent)")
	chatBackend := flag.String("chat", "claude-code",
		"chat backend: claude-code (uses the claude CLI login, e.g. a subscription) or api (Anthropic API key)")
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
	var peers []peer.Peer
	flag.Func("peer", "another computer running Dash to show in this dashboard, as name=https://host (repeatable; needs DASH_PEER_TOKEN on both)", func(s string) error {
		p, err := peer.Parse(s)
		peers = append(peers, p)
		return err
	})
	publicURL := flag.String("public-url", "", "public HTTPS origin (e.g. https://dash.example.com) that serves /mcp with OAuth for claude.ai custom connectors; empty = off (or $DASH_PUBLIC_URL)")
	previewDomain := flag.String("preview-domain", "", "serve each task's live preview at https://<task>.<this domain>, e.g. preview.dash.example.com; needs a wildcard DNS record and certificate (see docs/previews.md; or $DASH_PREVIEW_DOMAIN)")
	previewIdle := flag.Duration("preview-idle", preview.DefaultIdle, "stop a live preview after this long without traffic")
	scanTimeout := flag.Duration("scan-timeout", scan.DefaultRunTimeout, "stop a scan's Claude run after this long and mark it failed")
	scanSchedules := flag.Bool("scan-schedules", true, "run scans (.dash/scans) on their schedules; off = only when started by hand")
	envFile := flag.String("env", ".env", "file with DASH_USERNAME / DASH_PASSWORD (real env vars take precedence)")
	var hubServer, regCode *string
	var offlineGrace *time.Duration
	var hubCommands *bool
	if runnerMode {
		flag.CommandLine.Init("dash runner", flag.ExitOnError)
		hubServer = flag.String("server", "", "the hub's URL, e.g. https://dash.example.com (saved after registering)")
		regCode = flag.String("code", "", "registration code from the hub's Runners page (only the first time, or to register again)")
		offlineGrace = flag.Duration("offline-grace", 10*time.Minute, "when the hub has been unreachable this long, stop running tasks and mark them interrupted (0 = never)")
		hubCommands = flag.Bool("hub-commands", false, "let the hub set the projects' run/test/deploy/setup commands and run ad-hoc commands; off = only commands set on this computer run")
	}
	_ = flag.CommandLine.Parse(args)
	addrSet := false
	flag.Visit(func(f *flag.Flag) { addrSet = addrSet || f.Name == "addr" })

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

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
	authn, err := auth.New(user, pass)
	if err != nil {
		slog.Error("auth", "err", err) // refuse to run open: Dash executes commands
		os.Exit(1)
	}
	authn.TrustProxy, hub.TrustProxy = *trustProxy, *trustProxy
	// A shared secret lets the computers in one dashboard call each other.
	peerToken := os.Getenv("DASH_PEER_TOKEN")
	if peerToken != "" && len(peerToken) < 24 {
		slog.Error("DASH_PEER_TOKEN must be at least 24 characters (try: openssl rand -hex 24)")
		os.Exit(1)
	}
	if len(peers) > 0 {
		if runnerMode {
			slog.Error("-peer is for the hub; a runner shows in the hub's dashboard by itself")
			os.Exit(1)
		}
		slog.Warn("-peer is deprecated and will be removed in the next release: run `dash runner` on the other computers instead (see docs/several-computers.md)")
	}
	if len(peers) > 0 && peerToken == "" {
		slog.Error("-peer needs DASH_PEER_TOKEN, set to the same value on every computer")
		os.Exit(1)
	}
	authn.PeerToken = peerToken
	if err := authn.KeepSessions(filepath.Join(*dataDir, "sessions.json")); err != nil {
		slog.Warn("read sessions", "err", err)
	}
	if *publicURL == "" {
		*publicURL = os.Getenv("DASH_PUBLIC_URL")
	}
	if *previewDomain == "" {
		*previewDomain = os.Getenv("DASH_PREVIEW_DOMAIN")
	}
	if *projectRoot == "" {
		*projectRoot = filepath.Join(home, "dash-projects")
		if v := os.Getenv("DASH_PROJECTS"); v != "" {
			*projectRoot = store.CleanPath(v)
		}
	}
	var oauth *auth.OAuth
	if *publicURL != "" {
		if oauth, err = auth.NewOAuth(authn, *publicURL); err != nil {
			slog.Error("oauth", "err", err)
			os.Exit(1)
		}
		if err := oauth.Keep(filepath.Join(*dataDir, "oauth.json")); err != nil {
			slog.Warn("read OAuth tokens", "err", err)
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

	// ---- services ----
	events := hub.New()
	storeEvents := &teePublisher{Publisher: events}
	st, err := store.Open(filepath.Join(*dataDir, "state.json"), storeEvents)
	if err != nil {
		slog.Error("open store", "err", err)
		os.Exit(1)
	}
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
	if *autoTitles {
		todos.Summarizer = agent.Titler{Bin: *claudeBin, Model: *titleModel}
	}
	var titler interface {
		Title(ctx context.Context, prompt string) (string, error)
	}
	if *autoTitles {
		titler = agent.Titler{Bin: *claudeBin, Model: *titleModel}
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
		Peers:       &peer.Hub{Self: *machine, Token: peerToken, Peers: peers},
		Titler:      titler,
		Refiner:     agent.Titler{Bin: *claudeBin, Model: *titleModel},
		ProjectRoot: *projectRoot,
		Static:      web.Static,
		Addr:        *addr,
	}
	var runnerCreds tunnel.Credentials
	if runnerMode {
		apiSrv.HubCommands = *hubCommands
		credsPath := filepath.Join(*dataDir, "runner.json")
		if runnerCreds, err = runnerCredentials(ctx, credsPath, *hubServer, *regCode, *machine); err != nil {
			slog.Error("runner", "err", err)
			os.Exit(1)
		}
	} else {
		apiSrv.Peers.Runners = &tunnel.Hub{Pub: events, Taken: apiSrv.Peers.Taken}
		if err := apiSrv.Peers.Runners.Keep(filepath.Join(*dataDir, "runners.json")); err != nil {
			slog.Warn("read runners", "err", err)
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

	// Projects adopted with a default branch the repo doesn't have (e.g. "main"
	// in a master repo) get the repo's real main line.
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

	go builds.Run(ctx)
	if *scanSchedules {
		go scheduler.Run(ctx)
	}
	go previews.Run(ctx)
	go todos.Run(ctx)
	merges.Resume()
	go merges.Run(ctx) // the one and only merge worker
	go merges.Remote.Run(ctx)

	switch *chatBackend {
	case "claude-code":
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
	case "api":
		apiSrv.Chat = agent.NewChat()
	default:
		slog.Error("unknown -chat backend", "value", *chatBackend)
		os.Exit(1)
	}

	if runnerMode {
		watch := offlineWatch(*offlineGrace, todos)
		watch(false) // tasks resumed at startup count as offline until the hub answers
		client := &tunnel.Client{Creds: runnerCreds, Handler: apiSrv.RunnerRoutes(), OnState: watch}
		if !addrSet {
			if err := client.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("runner", "err", err)
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

// runnerCredentials registers with the hub when given a code, and otherwise
// reads the credentials saved by an earlier registration.
func runnerCredentials(ctx context.Context, path, server, code, name string) (tunnel.Credentials, error) {
	if code != "" {
		if server == "" {
			return tunnel.Credentials{}, errors.New("--code needs --server, the hub's URL")
		}
		c, err := tunnel.Register(ctx, server, code, name)
		if err != nil {
			return c, err
		}
		if err := tunnel.SaveCredentials(path, c); err != nil {
			return c, err
		}
		slog.Info("runner registered", "hub", c.Server, "name", c.Name, "saved", path)
		return c, nil
	}
	c, err := tunnel.LoadCredentials(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, errors.New("not registered yet: get a code from the hub's Runners page and run dash runner --server <hub URL> --code <code>")
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
