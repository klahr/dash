// Package api exposes Dash over REST (state/CRUD) and WebSockets (live events,
// chat streaming, terminals). Routing uses the Go 1.22+ standard mux.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klahr/dash/internal/agent"
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
	Board       *mcptools.Tokens // chat runs' tokens for /mcp/dash
	OAuth       *auth.OAuth      // remote MCP clients (claude.ai) on /mcp; nil = off
	Usage       *usage.Tracker
	ProjectRoot string // where new projects without an explicit path are created
	// Titler names todos created without a title (optional).
	Titler interface {
		Title(ctx context.Context, prompt string) (string, error)
	}
	// Refiner cleans up pasted instructions in the todo form (optional).
	Refiner interface {
		Refine(ctx context.Context, prompt string) (string, error)
	}
	Editor     string // command that opens a directory, e.g. "code" (optional)
	UploadRoot string // where attached files are stored
	CI         *ci.Watcher
	Peers      *peer.Hub // other computers shown in this dashboard
	Static     fs.FS     // frontend assets
	Addr       string    // the listen address (-addr), for the connector setup guide

	reviewMu  sync.Mutex
	reviewGen uint64
	reviews   map[string]uint64 // todo id → the review turn that owns its Reviewing flag

	generalTurns atomic.Int32 // global chat turns running
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Auth (the middleware below lets /api/login through without a session)
	mux.HandleFunc("POST /api/login", s.Auth.Login)
	mux.HandleFunc("POST /api/logout", s.Auth.Logout)
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "username": s.Auth.Username()})
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
	mux.HandleFunc("DELETE /api/projects/{pid}", s.deleteProject)
	// A peer can't check this dashboard's password, so the hub does it first.
	mux.HandleFunc("DELETE /peer/{name}/api/projects/{pid}", s.confirmThenForward)
	mux.HandleFunc("POST /api/projects/{pid}/github", s.createGitHubRepo)
	mux.HandleFunc("POST /api/projects/{pid}/push", s.pushProject)
	mux.HandleFunc("GET /api/projects/{pid}/unpushed", s.unpushed)
	mux.HandleFunc("GET /api/projects/{pid}/remote", s.remoteStatus(false))
	mux.HandleFunc("POST /api/projects/{pid}/remote/fetch", s.remoteStatus(true))
	mux.HandleFunc("GET /api/projects/{pid}/graph", s.gitGraph)
	mux.HandleFunc("GET /api/projects/{pid}/files", s.listFiles)
	mux.HandleFunc("GET /api/projects/{pid}/file", s.readFile)
	mux.HandleFunc("PUT /api/projects/{pid}/file", s.saveLocalFile)
	mux.HandleFunc("POST /api/projects/{pid}/edits", s.saveEdit)

	// Todos & queues
	mux.HandleFunc("GET /api/projects/{pid}/todos", s.listTodos)
	mux.HandleFunc("POST /api/projects/{pid}/todos", s.createTodo)
	mux.HandleFunc("GET /api/projects/{pid}/queues", s.getQueues)
	mux.HandleFunc("GET /api/todos/{tid}", s.getTodo)
	mux.HandleFunc("PATCH /api/todos/{tid}", s.updateTodo)
	mux.HandleFunc("DELETE /api/todos/{tid}", s.deleteTodo)
	mux.HandleFunc("POST /api/todos/{tid}/enqueue", s.enqueueTodo)
	mux.HandleFunc("POST /api/todos/{tid}/cancel", s.cancelTodo)
	mux.HandleFunc("POST /api/todos/{tid}/pause", s.pauseTodo)
	mux.HandleFunc("POST /api/todos/{tid}/resume", s.resumeTodo)
	mux.HandleFunc("GET /api/todos/{tid}/diff", s.todoDiff)
	mux.HandleFunc("GET /api/todos/{tid}/log", s.todoLog)
	mux.HandleFunc("GET /api/todos/{tid}/events", s.todoEvents)
	mux.HandleFunc("POST /api/todos/{tid}/approve", s.approveTodo)
	mux.HandleFunc("POST /api/todos/{tid}/rework", s.reworkTodo)
	mux.HandleFunc("POST /api/todos/{tid}/test", s.retestTodo)
	mux.HandleFunc("POST /api/todos/{tid}/duplicate", s.duplicateTodo)
	mux.HandleFunc("POST /api/todos/{tid}/open", s.openTodo)
	mux.HandleFunc("POST /api/todos/{tid}/revert", s.revertTodo)
	mux.HandleFunc("POST /api/refine", s.refinePrompt)
	mux.HandleFunc("GET /api/todos/{tid}/chat", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.Store.TodoChat(r.PathValue("tid")))
	})
	mux.HandleFunc("DELETE /api/todos/{tid}/chat", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Store.SetTodoChat(r.PathValue("tid"), nil); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/projects/{pid}/chat", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.Store.ProjectChat(r.PathValue("pid")))
	})
	mux.HandleFunc("DELETE /api/projects/{pid}/chat", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Store.SetProjectChat(r.PathValue("pid"), nil); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
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
	mux.Handle("POST /mcp/dash", s.BoardMCP())
	mux.Handle("GET /mcp/dash", s.BoardMCP())
	// What the "Connect Claude" guide needs: is /mcp on, where, and who signed in.
	mux.HandleFunc("GET /api/connector", s.connectorStatus)
	if s.OAuth != nil {
		// Remote MCP clients: the whole dashboard, with OAuth access tokens.
		s.OAuth.Routes(mux)
		mux.Handle("POST /mcp", s.OAuth.Bearer(s.remoteMCP(mux)))
		mux.Handle("GET /mcp", s.OAuth.Bearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", "POST")
			http.Error(w, "this MCP server only accepts POST", http.StatusMethodNotAllowed)
		})))
	}

	// WebSockets
	mux.HandleFunc("GET /ws/events", s.Hub.ServeWS)
	mux.HandleFunc("GET /ws/chat", s.chatWS)
	mux.HandleFunc("GET /api/chat", func(w http.ResponseWriter, r *http.Request) {
		if s.generalTurns.Load() > 0 {
			w.Header().Set("X-Chat-Busy", "1")
		}
		writeJSON(w, http.StatusOK, s.Store.Chat())
	})
	mux.HandleFunc("DELETE /api/chat", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Store.SetChat(nil); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
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
	case errors.As(err, &inv), errors.Is(err, store.ErrReviewing):
		code = http.StatusConflict
	case errors.Is(err, queue.ErrMergeQueueFull):
		code = http.StatusServiceUnavailable
	case errors.As(err, &gitErr):
		code = http.StatusBadGateway
	case errors.Is(err, auth.ErrBadPassword):
		code = http.StatusForbidden // not 401: that would sign the browser out
	case errors.Is(err, auth.ErrLockedOut):
		code = http.StatusTooManyRequests
	case errors.Is(err, queue.ErrProjectBusy):
		code = http.StatusConflict
	case errors.Is(err, errBadRequest), errors.Is(err, store.ErrBadDependency):
		code = http.StatusBadRequest
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

var errBadRequest = errors.New("bad request")

// confirmThenForward checks the password in a delete-project request for a
// peer here, then passes it on unchanged (the peer trusts the hub's token).
func (s *Server) confirmThenForward(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, errors.Join(errBadRequest, err))
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeErr(w, errors.Join(errBadRequest, err))
		return
	}
	if err := s.Auth.ConfirmPassword(r, req.Password); err != nil {
		writeErr(w, err)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	s.Peers.ServeHTTP(w, r)
}

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
