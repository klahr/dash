// Package api exposes Dash over REST (state/CRUD) and WebSockets (live events,
// chat streaming, terminals). Routing uses the Go 1.22+ standard mux.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/ci"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/runner"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/usage"
)

type Server struct {
	Store       *store.Store
	Git         *gitops.Git
	Hub         *hub.Hub
	Todos       *queue.TodoEngine
	Merges      *queue.MergeQueue
	Procs       *runner.Manager
	Chat        agent.Chatter
	Auth        *auth.Auth
	Perms       *perm.Broker
	Usage       *usage.Tracker
	ProjectRoot string // where new projects without an explicit path are created
	// Titler names todos created without a title (optional).
	Titler interface {
		Title(ctx context.Context, prompt string) (string, error)
	}
	Editor     string // command that opens a directory, e.g. "code" (optional)
	UploadRoot string // where attached files are stored
	CI         *ci.Watcher
	Peers      *peer.Hub // other computers shown in this dashboard
	Static     fs.FS     // frontend assets
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Auth (the middleware below lets /api/login through without a session)
	mux.HandleFunc("POST /api/login", s.Auth.Login)
	mux.HandleFunc("POST /api/logout", s.Auth.Logout)
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	// Computers: this one plus peers, whose API is forwarded under /peer/{name}/
	mux.HandleFunc("GET /api/machines", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.Peers.Machines(r.Context()))
	})
	for _, m := range []string{"GET", "POST", "PATCH", "PUT", "DELETE"} {
		mux.Handle(m+" /peer/{name}/", s.Peers)
	}

	// Projects & git
	mux.HandleFunc("GET /api/projects", s.listProjects)
	mux.HandleFunc("POST /api/projects", s.createProject)
	mux.HandleFunc("GET /api/projects/{pid}", s.getProject)
	mux.HandleFunc("PATCH /api/projects/{pid}", s.updateProject)
	mux.HandleFunc("POST /api/projects/{pid}/github", s.createGitHubRepo)
	mux.HandleFunc("POST /api/projects/{pid}/push", s.pushProject)

	// Todos & queues
	mux.HandleFunc("GET /api/projects/{pid}/todos", s.listTodos)
	mux.HandleFunc("POST /api/projects/{pid}/todos", s.createTodo)
	mux.HandleFunc("GET /api/projects/{pid}/queues", s.getQueues)
	mux.HandleFunc("GET /api/todos/{tid}", s.getTodo)
	mux.HandleFunc("PATCH /api/todos/{tid}", s.updateTodo)
	mux.HandleFunc("DELETE /api/todos/{tid}", s.deleteTodo)
	mux.HandleFunc("POST /api/todos/{tid}/enqueue", s.enqueueTodo)
	mux.HandleFunc("POST /api/todos/{tid}/cancel", s.cancelTodo)
	mux.HandleFunc("GET /api/todos/{tid}/diff", s.todoDiff)
	mux.HandleFunc("GET /api/todos/{tid}/log", s.todoLog)
	mux.HandleFunc("POST /api/todos/{tid}/approve", s.approveTodo)
	mux.HandleFunc("POST /api/todos/{tid}/rework", s.reworkTodo)
	mux.HandleFunc("POST /api/todos/{tid}/test", s.retestTodo)
	mux.HandleFunc("POST /api/todos/{tid}/duplicate", s.duplicateTodo)
	mux.HandleFunc("POST /api/todos/{tid}/open", s.openTodo)
	mux.HandleFunc("POST /api/todos/{tid}/revert", s.revertTodo)
	mux.HandleFunc("POST /api/projects/{pid}/cleanup", s.cleanupProject)

	// GitHub Actions builds of the default branch
	mux.HandleFunc("GET /api/projects/{pid}/ci", s.getCI)
	mux.HandleFunc("POST /api/projects/{pid}/ci/refresh", s.refreshCI)
	mux.HandleFunc("POST /api/projects/{pid}/ci/investigate", s.investigateCI)

	// Attachments (screenshots for Claude)
	mux.HandleFunc("POST /api/uploads", s.upload)
	mux.HandleFunc("GET /api/uploads/{id}/{name}", s.getUpload)

	// Execution, testing, deployment
	mux.HandleFunc("GET /api/projects/{pid}/processes", s.listProcesses)
	mux.HandleFunc("POST /api/projects/{pid}/processes", s.startProcess)
	mux.HandleFunc("DELETE /api/processes/{procid}", s.stopProcess)

	// Permission prompts from Claude, subscription usage, queue pause
	mux.HandleFunc("GET /api/permissions", s.listPermissions)
	mux.HandleFunc("POST /api/permissions/{id}", s.decidePermission)
	mux.HandleFunc("GET /api/usage", s.getUsage)
	mux.HandleFunc("POST /api/queue/pause", s.pauseQueue(true))
	mux.HandleFunc("POST /api/queue/resume", s.pauseQueue(false))
	// Claude Code connects here (bearer token per run, not the session cookie)
	mux.HandleFunc("POST /mcp/permissions", s.Perms.ServeMCP)
	mux.HandleFunc("GET /mcp/permissions", s.Perms.ServeMCP) // answers 405, as MCP clients expect

	// WebSockets
	mux.HandleFunc("GET /ws/events", s.Hub.ServeWS)
	mux.HandleFunc("GET /ws/chat", s.chatWS)
	mux.HandleFunc("GET /ws/proc/{procid}", s.procWS)

	// Frontend
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": s.version()})
	})
	mux.Handle("GET /", s.static())

	return logRequests(s.Auth.Middleware(mux))
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	var inv model.ErrInvalidTransition
	var gitErr *gitops.CmdError
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, runner.ErrNotFound):
		code = http.StatusNotFound
	case errors.As(err, &inv):
		code = http.StatusConflict
	case errors.Is(err, queue.ErrMergeQueueFull):
		code = http.StatusServiceUnavailable
	case errors.As(err, &gitErr):
		code = http.StatusBadGateway
	case errors.Is(err, errBadRequest):
		code = http.StatusBadRequest
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

var errBadRequest = errors.New("bad request")

func decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return errors.Join(errBadRequest, err)
	}
	return nil
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(c int) { r.code = c; r.ResponseWriter.WriteHeader(c) }

// Unwrap lets http.ResponseController (and the WebSocket upgrader's hijack) reach the real writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) >= 3 && r.URL.Path[:3] == "/ws" {
			next.ServeHTTP(w, r) // hijacked connections must get the raw writer
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		next.ServeHTTP(rec, r)
		if len(r.URL.Path) >= 4 && r.URL.Path[:4] == "/api" {
			slog.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.code, "dur", time.Since(start))
		}
	})
}
