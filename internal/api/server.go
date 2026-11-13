// Package api exposes Dash over REST (state/CRUD) and WebSockets (live events,
// chat streaming, terminals). Routing uses the Go 1.22+ standard mux.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/billing"
	"github.com/klahr/dash/internal/branding"
	"github.com/klahr/dash/internal/buildinfo"
	"github.com/klahr/dash/internal/ci"
	"github.com/klahr/dash/internal/claudestatus"
	"github.com/klahr/dash/internal/disk"
	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/httpjson"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/metrics"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/notes"
	"github.com/klahr/dash/internal/notify"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/preview"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/runner"
	"github.com/klahr/dash/internal/scan"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tracker"
	"github.com/klahr/dash/internal/tunnel"
	"github.com/klahr/dash/internal/usage"
)

type Server struct {
	// Store is reached one org at a time (see Data); the engines below keep
	// the whole store.
	Store     Data
	Git       *gitops.Git
	Hub       *hub.Hub
	Todos     *queue.TodoEngine
	Merges    *queue.MergeQueue
	Procs     *runner.Manager
	Previews  *preview.Manager // live previews of todos in review (optional)
	Chat      agent.Chatter
	Auth      *auth.Auth
	Perms     *perm.Broker
	Board     *mcptools.Tokens // chat runs' tokens for /mcp/dash
	OAuth     *auth.OAuth      // remote MCP clients (claude.ai) on /mcp; nil = off
	Usage     *usage.Tracker
	Scans     *scan.Runner    // read-only scans that file issues (optional)
	Scheduler *scan.Scheduler // runs scans on their schedules (optional)
	// Trackers syncs projects whose board is an external tracker (nil:
	// every board is Dash's own, and the backend can't be changed).
	Trackers *tracker.Manager
	// Billing charges orgs of two or more members per seat (nil: off, as
	// on a self-hosted Dash).
	Billing     *billing.Service
	ProjectRoot string // where new projects without an explicit path are created
	// Titler names todos created without a title (optional).
	Titler interface {
		Title(ctx context.Context, prompt string) (string, error)
	}
	// Refiner cleans up pasted instructions in the todo form (optional).
	Refiner interface {
		Refine(ctx context.Context, prompt string) (string, error)
	}
	// Splitter proposes how to split a large task into todos (optional).
	Splitter interface {
		Split(ctx context.Context, prompt string) ([]agent.Part, error)
	}
	// Small runs Claude's short calls for this runner's owner when the hub
	// asks (a runner's; optional).
	Small SmallClaude
	// HubUser is, on a runner, the hub's user who registered it: the only
	// one the hub may run the general chat and short calls for (0: not
	// known, an older registration).
	HubUser int64
	// ChatDir is where a runner runs the general chat its hub asks for
	// (default: the temp dir).
	ChatDir string
	// ChatCommandTimeout stops a "!" command run from a chat (default
	// agent.DefaultShellTimeout).
	ChatCommandTimeout time.Duration
	Editor             string // command that opens a directory, e.g. "code" (optional)
	UploadRoot         string // where attached files are stored
	CI                 *ci.Watcher
	// Notify sends events to the projects' outgoing webhooks (nil: off,
	// as on a runner, whose hub notifies).
	Notify *notify.Notifier
	// ClaudeStatus keeps Claude's status page's incidents (nil: off, as on
	// a runner; see claudestatus.go).
	ClaudeStatus *claudestatus.Service
	Peers        *peer.Hub // other computers shown in this dashboard: runners
	// Dispatch sends tasks of projects linked to runners to them (a hub's;
	// optional).
	Dispatch *dispatch.Dispatcher
	// HubOrg is, on a runner, the hub's org it was registered for: it takes
	// projects of no other org (0: not checked, an older registration).
	HubOrg int64
	// HubBoard, on a runner, posts an MCP request of the hub's work (see
	// tunnel.Board) to the hub's board: the board tools of the hub's tasks
	// and of the chats the hub asks for act there, never on this Dash.
	HubBoard func(ctx context.Context, hubTodo, hubProject string, body []byte) (*http.Response, error)
	// NotesPeer, on a runner, takes the shared notes messages its hub
	// relays from the project's other runners (notes.HubTransport; nil:
	// none, e.g. on the hub, which keeps no notes).
	NotesPeer http.Handler
	// Notes, on a runner, is its copy of its hub projects' shared notes,
	// which the tasks it runs reach with the notes tools (/mcp/notes), and
	// which the hub's UI asks for counts and removals (notes.go; nil: none,
	// as on the hub or a Dash without one).
	Notes *notes.Service
	// GitHubProtocol is how this runner clones the hub's GitHub projects:
	// gitops.ProtocolAuto, gitops.ProtocolSSH ("" too, without the
	// fallback) or gitops.ProtocolHTTPS (-github-protocol).
	GitHubProtocol string
	// SelfUpdate, on a runner, installs Dash release version in place of
	// this binary and restarts on it once the hub has its answer (nil: it
	// can't update itself).
	SelfUpdate func(ctx context.Context, version string) error
	// DiskRoom, on a runner, is nil when it has the disk space to start a
	// task under the hub's thresholds s (nil: the runner's last ones), or
	// why it hasn't (disk.Monitor.CanStart; nil: not checked).
	DiskRoom  func(s *disk.Settings) error
	Static    fs.FS  // frontend assets
	PublicURL string // -public-url: the hub's address runners are told (optional; else the request's)
	// MetricsToken, if set, serves /metrics on this listener to callers
	// with it as their bearer token (empty: /metrics isn't served here).
	MetricsToken string
	// Brand is how this Dash presents itself: name, legal links, logo, the
	// landing page (empty fields: the defaults).
	Brand branding.Config

	// TryRoot is where a runner makes its try sessions' worktrees
	// (<data>/try; see try.go).
	TryRoot string
	tries   trySessions // on a hub: which runner each project's try session runs on
	// notesDeliver: one runner at a time takes the pending removals of
	// shared notes (notes.go).
	notesDeliver sync.Mutex

	reviewMu  sync.Mutex
	reviewGen uint64
	reviews   map[string]uint64 // todo id → the review turn that owns its Reviewing flag

	turnsMu      sync.Mutex
	generalTurns map[string]int // general chat turns running, by "<org>:<user>"

	muxOnce sync.Once

	// goalEngine runs the goals' coordinators (goalcoord.go), made once.
	goalsOnce  sync.Once
	goalEngine *goalEngine
	// following counts the fetches "origin moved" news started (tests wait).
	following sync.WaitGroup
	muxv      *http.ServeMux
}

// Routes is the dashboard's handler: every route, behind the login.
func (s *Server) Routes() http.Handler {
	// Preview hosts never reach Dash's own routes, nor get its security
	// headers (a task's app keeps its own).
	// /healthz and /metrics answer before sign-in (see ops.go); every request
	// is counted and timed for /metrics, by the route pattern that served it.
	return s.Previews.Wrap(s.Auth.Headers(metrics.Instrument(s.ops(
		logRequests(s.Auth.Middleware(s.billingGate(metrics.Route(s.mux()))))))))
}

func (s *Server) mux() *http.ServeMux {
	s.muxOnce.Do(func() { s.muxv = s.buildMux() })
	return s.muxv
}

func (s *Server) buildMux() *http.ServeMux {
	mux := http.NewServeMux()
	if s.Hub != nil && s.Hub.ProjectOrg == nil && s.Store != nil {
		s.Hub.ProjectOrg = s.Store.ProjectOrg // events go to their project's org
	}

	// Emails and the connector's page name the same Dash; signed-out
	// visitors to / see the landing page when it's on.
	s.Auth.Brand = s.Brand
	if s.Brand.Landing {
		s.Auth.Landing = s.landing()
	}
	mux.HandleFunc("GET "+branding.LogoPath, s.logo)
	mux.HandleFunc("GET "+branding.TermsPath, s.legal("Terms of service", func(b branding.Config) string { return b.TermsFile }))
	mux.HandleFunc("GET "+branding.PrivacyPath, s.legal("Privacy policy", func(b branding.Config) string { return b.PrivacyFile }))
	// With org subdomains, a preview signs in at its todo's org's subdomain.
	if s.Previews != nil && s.Auth.Domains != nil && s.Previews.OriginFor == nil {
		s.Previews.OriginFor = s.previewOrigin
	}

	// Sign-in, users, orgs and invitations (the middleware lets /api/login
	// and the invitation-link calls through without a session)
	s.Auth.Routes(mux)
	// Billing (answers "off" unless Stripe is configured)
	s.Billing.Routes(mux)

	// Computers: this one plus runners, whose API is forwarded under /peer/{name}/
	// The other computers belong to the whole Dash: only platform admins
	// see and use them; everyone else sees this computer.
	mux.HandleFunc("GET /api/machines", func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.callerOr(w, r)
		if !ok {
			return
		}
		ms := s.Peers.Machines(r.Context())
		if !c.PlatformAdmin {
			ms = ms[:1]
		}
		writeJSON(w, http.StatusOK, ms)
	})
	for _, m := range []string{"GET", "POST", "PATCH", "PUT", "DELETE"} {
		mux.HandleFunc(m+" /peer/{name}/", s.forwardToPeer)
	}
	mux.HandleFunc("GET /peer/{name}/preview/{tid}", s.openRunnerPreview)
	// Runners: computers that connect out to this one (`dash runner`)
	if rh := s.fleet(); rh != nil {
		s.runnerRoutes(mux, rh)
	}
	// A runner's side: tasks of its hub's projects linked to it (hub only)
	s.hubTaskRoutes(mux)
	// Try: a terminal on a project at a commit, on a runner (try.go)
	s.tryRoutes(mux)

	// Projects & git
	mux.HandleFunc("GET /api/projects", s.listProjects)
	mux.HandleFunc("POST /api/projects", s.createProject)
	mux.HandleFunc("GET /api/projects/{pid}", s.getProject)
	mux.HandleFunc("PATCH /api/projects/{pid}", s.updateProject)
	mux.HandleFunc("DELETE /api/projects/{pid}", s.deleteProject)
	// Shared notes of a hub's project: counts and removals, never a note (notes.go)
	mux.HandleFunc("GET /api/projects/{pid}/notes", s.getNotes)
	mux.HandleFunc("POST /api/projects/{pid}/notes/remove", s.removeNotes)
	mux.HandleFunc("DELETE /api/projects/{pid}/notes/pending/{rid}", s.cancelNotesRemoval)
	mux.HandleFunc("POST /api/projects/{pid}/push", s.viaRunner(s.pushProject))
	mux.HandleFunc("GET /api/projects/{pid}/unpushed", s.viaRunner(s.unpushed))
	mux.HandleFunc("GET /api/projects/{pid}/remote", s.viaRunner(s.remoteStatus(false)))
	mux.HandleFunc("POST /api/projects/{pid}/remote/fetch", s.viaRunner(s.remoteStatus(true)))
	mux.HandleFunc("POST /api/projects/{pid}/remote/pull", s.viaRunner(s.pullRemote))
	mux.HandleFunc("GET /api/projects/{pid}/checkouts", s.listCheckouts)
	mux.HandleFunc("GET /api/projects/{pid}/graph", s.viaRunner(s.gitGraph))
	mux.HandleFunc("GET /api/projects/{pid}/commits/{sha}", s.viaRunner(s.getCommit))
	mux.HandleFunc("GET /api/projects/{pid}/commits/{sha}/diff", s.viaRunner(s.commitDiff))
	mux.HandleFunc("GET /api/projects/{pid}/commits/{sha}/image", s.viaRunner(s.commitImage))
	mux.HandleFunc("GET /api/projects/{pid}/branches", s.viaRunner(s.listBranches))
	mux.HandleFunc("PUT /api/projects/{pid}/branch", s.viaRunner(s.switchBranch))
	mux.HandleFunc("POST /api/projects/{pid}/branch/pr", s.viaRunner(s.branchPR))
	mux.HandleFunc("GET /api/projects/{pid}/release", s.viaRunner(s.getRelease))
	mux.HandleFunc("POST /api/projects/{pid}/tags", s.viaRunner(s.createTag))
	mux.HandleFunc("GET /api/projects/{pid}/tag-format", s.viaRunner(s.tagFormatInfo))
	mux.HandleFunc("GET /api/projects/{pid}/repo-info", s.viaRunner(s.repoInfo))
	mux.HandleFunc("GET /api/projects/{pid}/files", s.viaRunner(s.listFiles))
	mux.HandleFunc("GET /api/projects/{pid}/claude-files", s.viaRunner(s.listClaudeFiles))
	mux.HandleFunc("GET /api/projects/{pid}/file", s.viaRunner(s.readFile))
	mux.HandleFunc("PUT /api/projects/{pid}/file", s.viaRunner(s.saveLocalFile))
	// The checklist (TODO.md). Saving and moving pass the file's part on to
	// a runner themselves (see checklist.go): a task they make belongs on
	// this Dash's board, not the runner's.
	mux.HandleFunc("GET /api/projects/{pid}/checklist", s.viaRunner(s.getChecklist))
	mux.HandleFunc("POST /api/projects/{pid}/checklist", s.postChecklist)
	mux.HandleFunc("POST /api/projects/{pid}/checklist/move", s.moveChecklist)
	mux.HandleFunc("POST /api/projects/{pid}/edits", s.saveEdit)

	// Todos
	mux.HandleFunc("GET /api/projects/{pid}/todos", s.listTodos)
	mux.HandleFunc("POST /api/projects/{pid}/todos", s.createTodo)
	mux.HandleFunc("GET /api/todos/{tid}", s.getTodo)
	mux.HandleFunc("PATCH /api/todos/{tid}", s.updateTodo)
	mux.HandleFunc("DELETE /api/todos/{tid}", s.deleteTodo)
	mux.HandleFunc("POST /api/todos/{tid}/enqueue", s.enqueueTodo)
	mux.HandleFunc("POST /api/todos/{tid}/start-now", s.startNowTodo)
	mux.HandleFunc("POST /api/todos/{tid}/cancel", s.cancelTodo)
	mux.HandleFunc("POST /api/todos/{tid}/pause", s.pauseTodo)
	mux.HandleFunc("POST /api/todos/{tid}/resume", s.resumeTodo)
	mux.HandleFunc("GET /api/todos/{tid}/diff", s.todoDiff)
	mux.HandleFunc("GET /api/todos/{tid}/image", s.todoImage)
	mux.HandleFunc("GET /api/todos/{tid}/log", s.todoLog)
	mux.HandleFunc("GET /api/todos/{tid}/events", s.todoEvents)
	mux.HandleFunc("GET /api/todos/{tid}/file", s.todoFile)
	mux.HandleFunc("PUT /api/todos/{tid}/file", s.saveTodoFile)
	mux.HandleFunc("POST /api/todos/{tid}/approve", s.approveTodo)
	mux.HandleFunc("POST /api/todos/{tid}/rework", s.reworkTodo)
	mux.HandleFunc("POST /api/todos/{tid}/test", s.retestTodo)
	mux.HandleFunc("POST /api/todos/{tid}/duplicate", s.duplicateTodo)
	mux.HandleFunc("POST /api/todos/{tid}/open", s.openTodo)
	mux.HandleFunc("POST /api/todos/{tid}/revert", s.revertTodo)
	mux.HandleFunc("POST /api/refine", s.refinePrompt)
	mux.HandleFunc("POST /api/split", s.splitPrompt)
	mux.HandleFunc("GET /api/claude", s.claudeInfo)
	mux.HandleFunc("POST /api/hub/claude/{kind}", s.hubClaude)
	mux.HandleFunc("GET /api/hub/repos", s.hubRepos)
	mux.HandleFunc("POST /api/hub/repos", s.hubAddRepos)
	mux.HandleFunc("GET /api/hub/github-repos", s.hubGitHubRepos)
	mux.HandleFunc("POST /api/hub/github-repos", s.hubCreateGitHubRepo)

	// Issues: known bugs, vulnerabilities and other problems, triaged into tasks
	mux.HandleFunc("GET /api/projects/{pid}/issues", s.listIssues)
	mux.HandleFunc("POST /api/projects/{pid}/issues", s.addIssue)
	mux.HandleFunc("POST /api/projects/{pid}/issues/tasks", s.issueTasks)
	mux.HandleFunc("POST /api/projects/{pid}/issues/dismiss", s.dismissIssues)
	mux.HandleFunc("PATCH /api/issues/{iid}", s.updateIssue)
	mux.HandleFunc("DELETE /api/issues/{iid}", s.deleteIssue)

	// Goals: a coordinator plans and dispatches tasks towards each
	s.goalRoutes(mux)

	// Scans: preconfigured read-only reviews that file their findings as issues
	mux.HandleFunc("GET /api/projects/{pid}/scans", s.listScans)
	mux.HandleFunc("POST /api/projects/{pid}/scans/{name}/run", s.runScan)
	mux.HandleFunc("PATCH /api/projects/{pid}/scans/{name}", s.updateScan)
	mux.HandleFunc("POST /api/projects/{pid}/scans", s.createScan)
	mux.HandleFunc("PUT /api/projects/{pid}/scans/{name}", s.editScan)
	mux.HandleFunc("DELETE /api/projects/{pid}/scans/{name}", s.deleteScan)
	mux.HandleFunc("GET /api/projects/{pid}/scan-runs", s.listScanRuns)
	mux.HandleFunc("GET /api/scan-runs/{rid}", s.getScanRun)
	mux.HandleFunc("GET /api/scan-runs/{rid}/log", s.scanRunLog)
	mux.HandleFunc("POST /api/scan-runs/{rid}/cancel", s.cancelScanRun)
	mux.HandleFunc("GET /api/todos/{tid}/chat", func(w http.ResponseWriter, r *http.Request) {
		sv, ok := s.service(w, r)
		if !ok {
			return
		}
		msgs, err := sv.data.TodoChat(r.PathValue("tid"))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, msgs)
	})
	mux.HandleFunc("DELETE /api/todos/{tid}/chat", func(w http.ResponseWriter, r *http.Request) {
		sv, ok := s.service(w, r)
		if !ok {
			return
		}
		if err := sv.data.SetTodoChat(r.PathValue("tid"), nil); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/projects/{pid}/chat", func(w http.ResponseWriter, r *http.Request) {
		sv, ok := s.service(w, r)
		if !ok {
			return
		}
		msgs, err := sv.data.ProjectChat(r.PathValue("pid"))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, msgs)
	})
	mux.HandleFunc("DELETE /api/projects/{pid}/chat", func(w http.ResponseWriter, r *http.Request) {
		sv, ok := s.service(w, r)
		if !ok {
			return
		}
		if err := sv.data.SetProjectChat(r.PathValue("pid"), nil); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/projects/{pid}/cleanup", s.viaRunner(s.cleanupProject))

	// GitHub Actions builds of the default branch
	mux.HandleFunc("GET /api/projects/{pid}/ci", s.getCI)
	mux.HandleFunc("POST /api/projects/{pid}/ci/refresh", s.refreshCI)
	mux.HandleFunc("POST /api/projects/{pid}/ci/investigate", s.investigateCI)
	mux.HandleFunc("POST /api/projects/{pid}/ci/retry", s.retryCI)

	// Outgoing notification webhooks (generic JSON, Slack, ntfy)
	mux.HandleFunc("GET /api/projects/{pid}/webhooks", s.listWebhooks)
	mux.HandleFunc("POST /api/projects/{pid}/webhooks", s.createWebhook)
	mux.HandleFunc("PATCH /api/webhooks/{wid}", s.updateWebhook)
	mux.HandleFunc("DELETE /api/webhooks/{wid}", s.deleteWebhook)
	mux.HandleFunc("POST /api/webhooks/{wid}/rotate", s.rotateWebhook)
	mux.HandleFunc("POST /api/webhooks/{wid}/test", s.testWebhook)
	mux.HandleFunc("GET /api/webhooks/{wid}/deliveries", s.webhookDeliveries)
	mux.HandleFunc("GET /api/me/webhooks", s.listPersonalWebhooks)
	mux.HandleFunc("POST /api/me/webhooks", s.createPersonalWebhook)
	// Claude's status page: its webhook (public, the token is its
	// credential), the status for everyone, the settings for platform admins
	s.claudeStatusRoutes(mux)
	// Web Push to this browser (the installed app, while it's closed)
	mux.HandleFunc("GET /api/push", s.pushInfo)
	mux.HandleFunc("PUT /api/push/subscription", s.subscribePush)
	mux.HandleFunc("DELETE /api/push/subscription", s.unsubscribePush)
	mux.HandleFunc("POST /api/push/test", s.testPush)
	if s.Notify != nil && s.Notify.Actions != nil {
		mux.Handle("POST "+notify.ActionPath+"{token}", s.Notify.Actions) // public: see auth.publicPath
	}

	// The project's board backend (Dash's own, or an external tracker)
	mux.HandleFunc("GET /api/projects/{pid}/tracker", s.getTracker)
	mux.HandleFunc("PUT /api/projects/{pid}/tracker", s.putTracker)
	mux.HandleFunc("POST /api/projects/{pid}/tracker/test", s.testTracker)
	mux.HandleFunc("POST /api/projects/{pid}/tracker/options", s.trackerOptions)
	mux.HandleFunc("POST /api/projects/{pid}/tracker/sync", s.syncTracker)
	mux.HandleFunc("POST /api/projects/{pid}/tracker/webhook-secret", s.rotateTrackerSecret)
	mux.HandleFunc("POST /api/projects/{pid}/tracker/webhook", s.trackerWebhook) // public: see auth.TrackerWebhookPath
	mux.HandleFunc("HEAD /api/projects/{pid}/tracker/webhook", s.trackerWebhook)

	// Attachments (screenshots for Claude)
	mux.HandleFunc("POST /api/uploads", s.upload)
	mux.HandleFunc("GET /api/uploads/{id}/{name}", s.getUpload)

	// Execution, testing, deployment
	mux.HandleFunc("GET /api/projects/{pid}/processes", s.viaRunner(s.listProcesses))
	mux.HandleFunc("POST /api/projects/{pid}/processes", s.viaRunner(s.startProcess))
	mux.HandleFunc("DELETE /api/processes/{procid}", s.processViaRunner(s.stopProcess))

	// Live previews: a todo's server, proxied at <todo>.<preview domain>
	mux.HandleFunc("POST /api/todos/{tid}/preview", s.startPreview)
	mux.HandleFunc("DELETE /api/todos/{tid}/preview", s.stopPreview)
	mux.HandleFunc("GET /api/todos/{tid}/preview/log", s.previewLog)
	mux.HandleFunc("GET /api/previews", s.listPreviews)
	mux.HandleFunc("DELETE /api/previews", s.stopPreviews)
	mux.HandleFunc("GET /api/qr", s.qrCode)
	mux.HandleFunc("GET /preview/{tid}", s.openPreview)

	// Permission prompts from Claude, subscription usage, queue pause
	mux.HandleFunc("GET /api/permissions", s.listPermissions)
	mux.HandleFunc("POST /api/permissions/{id}", s.decidePermission)
	mux.HandleFunc("GET /api/usage", s.getUsage)
	mux.HandleFunc("POST /api/queue/pause", s.pauseQueue(true))
	mux.HandleFunc("POST /api/queue/resume", s.pauseQueue(false))
	mux.HandleFunc("PUT /api/queue/pace", s.putQueuePace)
	// Claude Code connects here (bearer token per run, not the session cookie)
	mux.HandleFunc("POST /mcp/permissions", s.Perms.ServeMCP)
	mux.HandleFunc("GET /mcp/permissions", s.Perms.ServeMCP) // answers 405, as MCP clients expect
	mux.Handle("POST /mcp/dash", s.BoardMCP())
	mux.Handle("GET /mcp/dash", s.BoardMCP())
	mux.Handle("POST /mcp/notes", s.NotesMCP())
	mux.Handle("GET /mcp/notes", s.NotesMCP())
	// What the "Connect Claude" guide needs: is /mcp on, where, and who signed in.
	mux.HandleFunc("GET /api/connector", s.connectorStatus)
	if s.OAuth != nil {
		// Remote MCP clients: the whole dashboard, with OAuth access tokens.
		s.OAuth.Routes(mux)
		mux.Handle("POST /mcp", s.OAuth.Bearer(s.remoteMCP()))
		mux.Handle("GET /mcp", s.OAuth.Bearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", "POST")
			http.Error(w, "this MCP server only accepts POST", http.StatusMethodNotAllowed)
		})))
	}

	// WebSockets
	mux.HandleFunc("GET /ws/events", s.eventsWS)
	mux.HandleFunc("GET /ws/chat", s.chatWS)
	// The general chat is each user's own, in each org.
	mux.HandleFunc("GET /api/chat", func(w http.ResponseWriter, r *http.Request) {
		sv, ok := s.service(w, r)
		if !ok {
			return
		}
		if s.generalBusy(sv.c) {
			w.Header().Set("X-Chat-Busy", "1")
		}
		writeJSON(w, http.StatusOK, sv.data.Chat(sv.c.User))
	})
	mux.HandleFunc("DELETE /api/chat", func(w http.ResponseWriter, r *http.Request) {
		sv, ok := s.service(w, r)
		if !ok {
			return
		}
		if err := sv.data.SetChat(sv.c.User, nil); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /ws/proc/{procid}", s.processViaRunner(s.procWS))
	// A task's terminal preview (see preview.TerminalPath).
	mux.HandleFunc("GET /ws/todos/{tid}/preview", s.previewTerminalWS)

	// The wall view: a read-only TV dashboard (wall.go)
	mux.HandleFunc("GET /wall", s.wallPage)
	mux.HandleFunc("GET /api/wall", s.getWall)
	s.wallTokenRoutes(mux)

	// Frontend
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": s.version(), "release": buildinfo.Version})
	})
	mux.Handle("GET /", s.static())
	return mux
}

// eventsWS handles GET /ws/events: the caller receives their org's events
// (and those about Dash as a whole); another Dash receives all of them.
func (s *Server) eventsWS(w http.ResponseWriter, r *http.Request) {
	c, ok := s.callerOr(w, r)
	if !ok {
		return
	}
	s.Hub.ServeWS(w, r, hub.Subscriber{Org: c.Org, All: c.Machine})
}

// chatKey names a user's general chat in their org.
func chatKey(c Caller) string { return fmt.Sprintf("%d:%d", c.Org, c.User) }

// generalTurn counts a general chat turn of c's as running (delta 1) or
// finished (-1).
func (s *Server) generalTurn(c Caller, delta int) {
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	if s.generalTurns == nil {
		s.generalTurns = map[string]int{}
	}
	k := chatKey(c)
	if s.generalTurns[k] += delta; s.generalTurns[k] <= 0 {
		delete(s.generalTurns, k)
	}
}

// generalBusy reports whether one of c's general chat turns is running.
func (s *Server) generalBusy(c Caller) bool {
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	return s.generalTurns[chatKey(c)] > 0
}

// ---- helpers ----

var writeJSON = httpjson.Write

// writeText answers with plain text.
func writeText(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(s))
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	var inv model.ErrInvalidTransition
	var invIssue model.ErrInvalidIssueTransition
	var gitErr *gitops.CmdError
	var callErr *tunnel.CallError
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, runner.ErrNotFound), errors.Is(err, preview.ErrNotFound), errors.Is(err, preview.ErrNoTry),
		errors.Is(err, scan.ErrNoScan), errors.Is(err, gitops.ErrNoCommit), errors.Is(err, gitops.ErrNoFileChange):
		code = http.StatusNotFound
	case errors.As(err, &inv), errors.As(err, &invIssue), errors.Is(err, store.ErrReviewing):
		code = http.StatusConflict
	case errors.As(err, new(*queue.PullRefused)), errors.Is(err, queue.ErrNotQueued):
		code = http.StatusConflict
	case errors.Is(err, queue.ErrMergeQueueFull), errors.Is(err, errScansOff), errors.Is(err, errTrackersOff),
		errors.Is(err, errPushOff), errors.Is(err, errNotifyOff):
		code = http.StatusServiceUnavailable
	case errors.As(err, &gitErr):
		code = http.StatusBadGateway
	case errors.Is(err, auth.ErrBadPassword), errors.Is(err, auth.ErrSessionRequired):
		code = http.StatusForbidden // not 401: that would sign the browser out
	case errors.Is(err, auth.ErrLockedOut):
		code = http.StatusTooManyRequests
	case errors.Is(err, queue.ErrProjectBusy), errors.Is(err, scan.ErrRunning), errors.Is(err, store.ErrScanExists),
		errors.Is(err, queue.ErrOnRunner):
		code = http.StatusConflict
	case errors.Is(err, tunnel.ErrOffline), errors.Is(err, errNoRunnerOnline), errors.Is(err, errNoRunnerLinked),
		errors.Is(err, localrun.ErrHub):
		code = http.StatusServiceUnavailable
	case errors.As(err, &callErr):
		code = http.StatusBadGateway
		if callErr.Status == http.StatusForbidden || callErr.Status == http.StatusConflict || callErr.Status == http.StatusBadRequest {
			code = callErr.Status
		}
	case errors.Is(err, errForbidden):
		code = http.StatusForbidden
	case errors.Is(err, errConflict):
		code = http.StatusConflict
	case errors.Is(err, errBadRequest), errors.Is(err, store.ErrBadDependency), errors.Is(err, store.ErrBadIssue),
		errors.Is(err, preview.ErrNoRunCommand), errors.Is(err, preview.ErrNotInReview), errors.Is(err, preview.ErrNotTerminal):
		code = http.StatusBadRequest
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

var errBadRequest = errors.New("bad request")

// forwardToPeer handles /peer/{name}/…. A runner can't check this
// dashboard's password and skips its own check for requests over its tunnel,
// so a project delete is confirmed here first. The decision uses the path as
// the runner will see it: a route pattern like "DELETE /peer/{name}/api/projects/{pid}" matches
// escaped segments and would miss "/api/projects%2F<pid>", which the proxy
// forwards decoded as "/api/projects/<pid>".
func (s *Server) forwardToPeer(w http.ResponseWriter, r *http.Request) {
	if !s.adminOnly(w, r) { // other computers belong to the whole Dash
		return
	}
	if r.Method == http.MethodDelete && isProjectPath(peer.ForwardedPath(r)) {
		s.confirmThenForward(w, r)
		return
	}
	// Say whom this is for: a runner runs Claude (chats included), opens
	// terminals and lists repositories only for the user who registered it
	// (onlyForOwner). The proxy drops whatever X-Dash-User the browser sent
	// and sets this one, the logged-in user's; a machine caller (User 0)
	// names nobody.
	if c, err := s.caller(r); err == nil && !c.Machine && c.User != 0 {
		r = r.WithContext(tunnel.WithUser(r.Context(), c.User))
	}
	s.Peers.ServeHTTP(w, r)
}

// isProjectPath reports whether p, once cleaned, is /api/projects/{pid}.
func isProjectPath(p string) bool {
	rest, ok := strings.CutPrefix(path.Clean("/"+p), "/api/projects/")
	return ok && rest != "" && !strings.Contains(rest, "/")
}

// confirmThenForward checks the password in a delete-project request for a
// runner here, then passes it on unchanged (the runner trusts its tunnel).
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
		if strings.HasPrefix(r.URL.Path, "/ws") || r.URL.Path == "/runner/connect" {
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
