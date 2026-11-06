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
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/runner"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/usage"
	"github.com/klahr/dash/web"
)

func main() {
	home, _ := os.UserHomeDir()
	addr := flag.String("addr", "127.0.0.1:7777", "listen address (keep on localhost: Dash runs commands)")
	dataDir := flag.String("data", filepath.Join(home, ".local", "share", "dash"), "state, logs and worktrees")
	projectRoot := flag.String("projects", filepath.Join(home, "dash-projects"), "where new projects are created")
	parallel := flag.Int("parallel", 3, "max todos executed concurrently")
	claudeBin := flag.String("claude", "claude", "Claude Code binary used to execute todos")
	claudeArgs := flag.String("claude-args", "--permission-mode acceptEdits",
		"extra flags for `claude -p` (space separated), e.g. --allowedTools / --model")
	signCommits := flag.Bool("sign-commits", false, "GPG/SSH-sign commits Dash makes (needs a non-interactive signing agent)")
	chatBackend := flag.String("chat", "claude-code",
		"chat backend: claude-code (uses the claude CLI login, e.g. a subscription) or api (Anthropic API key)")
	pauseAt := flag.Float64("pause-at", 0.9, "pause the todo queue when a subscription usage window reaches this fraction (0 = only when limited)")
	askPerms := flag.Bool("permission-prompts", true, "send Claude's permission requests to the UI instead of denying them")
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
	defer procs.StopAll()

	tracker := usage.New(events, *pauseAt)
	perms := perm.NewBroker(events)
	todoLog := &queue.TodoLog{Pub: events, Root: filepath.Join(*dataDir, "logs")}
	merges := queue.NewMergeQueue(st, git, events, todoLog, 1024)
	todos := queue.NewTodoEngine(&queue.TodoEngine{
		Store:        st,
		Git:          git,
		Exec:         agent.ClaudeCode{Bin: *claudeBin, Args: strings.Fields(*claudeArgs), OnRateLimit: tracker.Observe},
		Pub:          events,
		Log:          todoLog,
		Merges:       merges,
		Usage:        tracker,
		WorktreeRoot: filepath.Join(*dataDir, "worktrees"),
		MaxParallel:  *parallel,
	})
	if *askPerms {
		todos.Perms, todos.BaseURL = perms, selfURL(*addr)
	}
	merges.Perms, merges.Kick = perms, todos.Kick

	go todos.Run(ctx)
	go merges.Run(ctx) // the one and only merge worker

	var chat agent.Chatter
	switch *chatBackend {
	case "claude-code":
		chat = agent.ClaudeCodeChat{Bin: *claudeBin, OnRateLimit: tracker.Observe}
	case "api":
		chat = agent.NewChat()
	default:
		slog.Error("unknown -chat backend", "value", *chatBackend)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr: *addr,
		Handler: (&api.Server{
			Store: st, Git: git, Hub: events, Todos: todos, Merges: merges, Procs: procs,
			Chat:        chat,
			Auth:        authn,
			Perms:       perms,
			Usage:       tracker,
			ProjectRoot: *projectRoot,
			Static:      web.Static,
		}).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	slog.Info("dash listening", "url", "http://"+*addr, "data", *dataDir)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}

// selfURL is how child processes on this machine reach Dash.
func selfURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
