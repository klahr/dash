// Command dash is the Dash orchestration server.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/runner"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/usage"
	"github.com/klahr/dash/web"
)

func main() {
	home, _ := os.UserHomeDir()
	addr := flag.String("addr", "127.0.0.1:7788", "listen address; to reach Dash from other devices use HTTPS (-tls-cert, or a proxy with -trust-proxy)")
	tlsCert := flag.String("tls-cert", "", "serve HTTPS with this certificate (PEM), e.g. from `tailscale cert`")
	tlsKey := flag.String("tls-key", "", "private key for -tls-cert")
	trustProxy := flag.Bool("trust-proxy", false, "trust X-Forwarded-Proto/-For/-Host from a reverse proxy that terminates TLS (Caddy, nginx, Tailscale Serve)")
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
	publicURL := flag.String("public-url", "", "public HTTPS origin (e.g. https://host.tailnet.ts.net:8443) that serves /mcp with OAuth for claude.ai custom connectors; empty = off (or $DASH_PUBLIC_URL)")
	envFile := flag.String("env", ".env", "file with DASH_USERNAME / DASH_PASSWORD (real env vars take precedence)")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if err := auth.LoadDotEnv(*envFile); err != nil {
		slog.Error("read env file", "path", *envFile, "err", err)
		os.Exit(1)
	}
	authn, err := auth.New(os.Getenv("DASH_USERNAME"), os.Getenv("DASH_PASSWORD"))
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
	st, err := store.Open(filepath.Join(*dataDir, "state.json"), events)
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
	builds := &ci.Watcher{Store: st, Git: git, Pub: events, Kick: todos.Kick, Every: time.Minute}
	apiSrv := &api.Server{
		Store: st, Git: git, Hub: events, Todos: todos, Merges: merges, Procs: procs,
		Auth:        authn,
		Perms:       perms,
		Board:       &mcptools.Tokens{},
		OAuth:       oauth,
		Usage:       tracker,
		Editor:      *editor,
		UploadRoot:  uploads,
		CI:          builds,
		Peers:       &peer.Hub{Self: *machine, Token: peerToken, Peers: peers},
		Titler:      titler,
		Refiner:     agent.Titler{Bin: *claudeBin, Model: *titleModel},
		ProjectRoot: *projectRoot,
		Static:      web.Static,
	}

	var mcpURL, permURL string
	if *askPerms || *chatBackend == "claude-code" {
		// Claude Code reaches Dash's MCP servers (permission prompts, the chat's
		// task tools) on a loopback-only plain-HTTP listener, whatever -addr and
		// TLS are set to.
		mcp := http.NewServeMux()
		if *askPerms {
			mcp.HandleFunc("/mcp/permissions", perms.ServeMCP)
		}
		mcp.Handle("/mcp/dash", apiSrv.BoardMCP())
		mcpURL, err = serveLoopback(ctx, mcp)
		if err != nil {
			slog.Error("MCP listener", "err", err)
			os.Exit(1)
		}
		if *askPerms {
			permURL = mcpURL
			todos.Perms, todos.BaseURL = perms, mcpURL
		}
	}
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
	go todos.Run(ctx)
	merges.Resume()
	go merges.Run(ctx) // the one and only merge worker

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
