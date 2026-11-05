// Command dash is the Dash orchestration server.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
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
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/runner"
	"github.com/klahr/dash/internal/store"
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

	todos := queue.NewTodoEngine(&queue.TodoEngine{
		Store:        st,
		Git:          git,
		Exec:         agent.ClaudeCode{Bin: *claudeBin, Args: strings.Fields(*claudeArgs)},
		Pub:          events,
		WorktreeRoot: filepath.Join(*dataDir, "worktrees"),
		LogRoot:      filepath.Join(*dataDir, "logs"),
		MaxParallel:  *parallel,
	})
	merges := queue.NewMergeQueue(st, git, events, 1024)

	go todos.Run(ctx)
	go merges.Run(ctx) // the one and only merge worker

	var chat agent.Chatter
	switch *chatBackend {
	case "claude-code":
		chat = agent.ClaudeCodeChat{Bin: *claudeBin}
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
