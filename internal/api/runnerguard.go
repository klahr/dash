package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/notes"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// hubRoutes are the routes a hub may call on a runner (`dash runner`): the
// dashboard's normal work on the runner's own projects and tasks. Missing on
// purpose: the MCP endpoints (local Claude runs use
// those on loopback), other computers (/peer/…), the static UI and login.
// Terminals, ad-hoc commands and the general chat are here but checked
// (hubCheck, forOwner).
// The guard compares against the pattern the mux actually picked, so a path
// can't reach a handler not listed here by matching a looser pattern.
var hubRoutes = map[string]bool{}

func init() {
	for _, p := range []string{
		"GET /api/me",
		"GET /api/version",

		"GET /api/projects", "POST /api/projects", "GET /api/projects/{pid}", "PATCH /api/projects/{pid}",
		"DELETE /api/projects/{pid}",
		"POST /api/projects/{pid}/push", "GET /api/projects/{pid}/unpushed", "GET /api/projects/{pid}/remote",
		"POST /api/projects/{pid}/remote/fetch", "POST /api/projects/{pid}/remote/pull", "GET /api/projects/{pid}/graph", "GET /api/projects/{pid}/files",
		"GET /api/projects/{pid}/claude-files",
		"GET /api/projects/{pid}/commits/{sha}", "GET /api/projects/{pid}/commits/{sha}/diff",
		"GET /api/projects/{pid}/file", "PUT /api/projects/{pid}/file", "POST /api/projects/{pid}/edits",
		// The checklist (TODO.md), read and saved in the runner's checkout (checklist.go).
		"GET /api/projects/{pid}/checklist", "POST /api/projects/{pid}/checklist", "POST /api/projects/{pid}/checklist/move",
		"GET /api/projects/{pid}/branches", "PUT /api/projects/{pid}/branch", "POST /api/projects/{pid}/branch/pr",
		// Release tags: made and pushed on the runner, in its clone (release.go).
		"GET /api/projects/{pid}/release", "POST /api/projects/{pid}/tags", "GET /api/projects/{pid}/tag-format",
		// What GitHub says about the repository, for the project settings (repoinfo.go).
		"GET /api/projects/{pid}/repo-info",

		"GET /api/projects/{pid}/todos", "POST /api/projects/{pid}/todos", "GET /api/projects/{pid}/queues",
		"GET /api/todos/{tid}", "PATCH /api/todos/{tid}", "DELETE /api/todos/{tid}",
		"POST /api/todos/{tid}/enqueue", "POST /api/todos/{tid}/cancel", "POST /api/todos/{tid}/pause",
		"POST /api/todos/{tid}/resume", "GET /api/todos/{tid}/diff", "GET /api/todos/{tid}/log",
		"GET /api/todos/{tid}/events", "POST /api/todos/{tid}/approve", "POST /api/todos/{tid}/rework",
		"POST /api/todos/{tid}/test", "POST /api/todos/{tid}/duplicate", "POST /api/todos/{tid}/open",
		"POST /api/todos/{tid}/revert",
		// Editing a task's changes in review (todofile.go).
		"GET /api/todos/{tid}/file", "PUT /api/todos/{tid}/file",

		"GET /api/projects/{pid}/issues", "POST /api/projects/{pid}/issues", "POST /api/projects/{pid}/issues/batch",
		"POST /api/projects/{pid}/issues/tasks", "POST /api/projects/{pid}/issues/dismiss",
		"PATCH /api/issues/{iid}", "DELETE /api/issues/{iid}",

		"GET /api/projects/{pid}/scans", "POST /api/projects/{pid}/scans/{name}/run", "PATCH /api/projects/{pid}/scans/{name}",
		"GET /api/projects/{pid}/scan-runs", "GET /api/scan-runs/{rid}", "GET /api/scan-runs/{rid}/log",
		"POST /api/scan-runs/{rid}/cancel",

		"GET /api/todos/{tid}/chat", "DELETE /api/todos/{tid}/chat", "GET /api/projects/{pid}/chat",
		"DELETE /api/projects/{pid}/chat", "POST /api/projects/{pid}/cleanup",
		"GET /ws/chat", // the general chat only for the runner's owner (see forOwner)

		"GET /api/projects/{pid}/ci", "POST /api/projects/{pid}/ci/refresh", "POST /api/projects/{pid}/ci/investigate",
		"POST /api/projects/{pid}/ci/retry",

		"POST /api/uploads", "GET /api/uploads/{id}/{name}",

		// The project's own run/test/deploy commands; checked in hubGuard.
		"GET /api/projects/{pid}/processes", "POST /api/projects/{pid}/processes", "DELETE /api/processes/{procid}",
		"GET /ws/proc/{procid}", // terminals only for the owner (hubTerminal)

		// Terminal previews are terminals: checked in hubCheck.
		"POST /api/todos/{tid}/preview", "DELETE /api/todos/{tid}/preview", "GET /api/todos/{tid}/preview/log",
		"GET /ws/todos/{tid}/preview",
		"GET /api/previews", "DELETE /api/previews", "GET /api/qr", "GET /preview/{tid}",

		"GET /api/permissions", "POST /api/permissions/{id}", "GET /api/usage",
		"POST /api/queue/pause", "POST /api/queue/resume", "PUT /api/queue/pace",

		"GET /ws/events",

		// The hub's projects linked to this runner and their tasks; checked
		// in hubtasks.go.
		"PUT /api/hub/projects/{hpid}", "DELETE /api/hub/projects/{hpid}", "POST /api/hub/tasks",
		"POST " + dispatch.OriginMovedPath, // another runner pushed: fetch, and follow in a clean checkout
		"DELETE /api/hub/tasks/{htid}", "POST /api/hub/tasks/{htid}/approve", "POST /api/hub/tasks/{htid}/cancel",
		"POST /api/hub/tasks/{htid}/revert", "POST /api/hub/tasks/{htid}/test", "POST /api/hub/tasks/{htid}/open",
		"GET /api/hub/tasks/{htid}/diff", "GET /api/hub/tasks/{htid}/log", "GET /api/hub/tasks/{htid}/events",
		"GET /api/hub/tasks/{htid}/file", "PUT /api/hub/tasks/{htid}/file",
		"POST " + dispatch.PermissionPath, // the hub's board answers its task's permission request
		// The Claude part of the hub's scans of linked projects; checked in hubtasks.go.
		"GET /ws/hub/scan",
		// Try sessions of linked projects: their commits, and a terminal in a
		// worktree at one of them; checked in try.go and (terminals) hubCheck.
		"GET /api/hub/projects/{hpid}/commits", "GET /api/hub/projects/{hpid}/try", "POST /api/hub/projects/{hpid}/try",
		"DELETE /api/hub/projects/{hpid}/try", "GET /api/hub/projects/{hpid}/try/log", "GET /ws/hub/projects/{hpid}/try",
		// gh for the hub's CI view of linked projects; checked in hubtasks.go.
		"POST /api/hub/github/{hpid}",
		// Shared notes from the project's other runners, relayed by the hub
		// (notes.go); its proxy (/peer/…) can't reach them.
		"POST " + notes.PeerPath,

		// Claude's short calls (titles, summaries, clean-up) for the
		// runner's owner; checked in claude.go.
		"POST /api/hub/claude/{kind}",

		// The repositories in the projects directory, for the runner's
		// owner to pick its projects from; checked in repos.go (only
		// paths it lists, inside -projects, not the old hub/ clones).
		"GET /api/hub/repos", "POST /api/hub/repos",
		// The runner owner's GitHub repositories, through its gh, for the
		// hub's new-project form; checked in ghrepos.go.
		"GET /api/hub/github-repos", "POST /api/hub/github-repos",
		// The hub updates the runner to its release; checked in hubtasks.go.
		"POST " + tunnel.UpdatePath,
	} {
		hubRoutes[p] = true
	}
}

// RunnerRoutes is what a runner serves to its hub over the tunnel: the
// routes in hubRoutes, without a login (the tunnel is authenticated), and
// a project's commands (run, test, deploy, setup) the hub sets; terminals and
// "!" commands open only for the runner's owner.
func (s *Server) RunnerRoutes() http.Handler {
	mux := s.mux()
	guard := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(auth.WithHub(r.Context()))
		if _, pattern := mux.Handler(r); !hubRoutes[pattern] {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "a runner doesn't offer this to the hub"})
			return
		}
		if err := s.hubCheck(r); err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		mux.ServeHTTP(w, r)
	})
	return logRequests(guard)
}

// hubTerminal reports whether the hub may open (or attach to) a terminal
// on this runner for r: only for the user who registered the runner (a
// shell can do whatever a deploy command can).
// It starts in the project's checkout or a task's worktree here, never a
// folder the hub names (see startProcess).
func (s *Server) hubTerminal(r *http.Request) error {
	if s.HubUser == 0 {
		// Registered with an older hub: it can't tell its owner from the
		// org's other members.
		return errors.New("this runner doesn't know who registered it, so it opens no terminals for the hub; register it again with a new code")
	}
	return s.onlyForOwner(r, "opens terminals")
}

// hubCheck refuses requests that are allowed routes but would run something
// the hub made up, or reach outside the runner's projects.
func (s *Server) hubCheck(r *http.Request) error {
	_, pattern := s.mux().Handler(r)
	switch pattern {
	case "POST /api/projects":
		var req struct {
			Path string `json:"path"`
		}
		if err := peekJSON(r, &req); err != nil {
			return nil // the handler reports the bad body
		}
		if req.Path != "" && !within(s.ProjectRoot, store.CleanPath(req.Path)) {
			return fmt.Errorf("the hub can only add projects inside %s on this runner", s.ProjectRoot)
		}
	case "POST /api/projects/{pid}/processes":
		var req struct {
			Kind    model.ProcessKind `json:"kind"`
			Command string            `json:"command"`
		}
		if err := peekJSON(r, &req); err != nil {
			return nil
		}
		if req.Kind == model.ProcPTY {
			return s.hubTerminal(r)
		}
	case "GET /ws/proc/{procid}":
		if p, err := s.Procs.Get(segment(r, 2)); err == nil && p.Kind == model.ProcPTY {
			return s.hubTerminal(r)
		}
	case "GET /ws/todos/{tid}/preview":
		return s.hubTerminal(r)
	case "GET /api/hub/projects/{hpid}/try", "POST /api/hub/projects/{hpid}/try", "DELETE /api/hub/projects/{hpid}/try",
		"GET /api/hub/projects/{hpid}/try/log", "GET /ws/hub/projects/{hpid}/try":
		// A try session is a terminal in a worktree of the project here.
		return s.hubTerminal(r)
	case "POST /api/todos/{tid}/preview":
		// A terminal preview is a shell in the task's worktree.
		c, err := s.caller(r)
		if err != nil {
			return nil // the handler refuses it
		}
		sv := s.as(c)
		if t, err := sv.data.GetTodo(segment(r, 2)); err == nil {
			if p, err := sv.data.GetProject(t.ProjectID); err == nil && p.TerminalPreview() {
				return s.hubTerminal(r)
			}
		}
	}
	return nil
}

// segment is the i-th (0-based) segment of r's path, decoded, as the mux's
// {wildcard} would be. (r.PathValue is empty until the mux has routed r.)
func segment(r *http.Request, i int) string {
	parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	if i >= len(parts) {
		return ""
	}
	v, err := url.PathUnescape(parts[i])
	if err != nil {
		return ""
	}
	return v
}

// commandFields are the project settings Dash runs as shell commands.
type commandFields struct {
	Run    *string `json:"run_command"`
	Test   *string `json:"test_command"`
	Deploy *string `json:"deploy_command"`
	Setup  *string `json:"setup_command"`
	// A terminal preview runs preview_command, or a shell without one.
	Preview     *string `json:"preview_command"`
	PreviewKind *string `json:"preview_kind"`
}

// peekJSON decodes r's body into v and puts the body back for the handler.
func peekJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(b))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// within reports whether path is root or inside it.
func within(root, path string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
