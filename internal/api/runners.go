package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/buildinfo"
	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/runnerctl"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// A hub's runners (`dash runner`) belong to the user who registered them,
// in the org they were working in. Everyone in that org sees them; members
// link projects to their own runners, admins to any of the org's (a
// runner's tasks use its owner's Claude subscription). The owner and the
// org's admins pause, limit and revoke it. Other orgs never see it.

// runnerView is a runner as the org settings page shows it.
type runnerView struct {
	tunnel.Runner
	Mine      bool `json:"mine"`       // the caller registered it
	CanManage bool `json:"can_manage"` // the caller may pause, limit, revoke and link it
}

func (s *Server) fleet() *tunnel.Hub {
	if s.Peers == nil {
		return nil
	}
	return s.Peers.Runners
}

// orgAdmin reports whether c is an admin (or owner) of their org.
func (s *Server) orgAdmin(c Caller) bool {
	if c.Machine {
		return true
	}
	m, err := s.Store.Accounts().Membership(c.User, c.Org)
	return err == nil && m.Role.AtLeast(store.RoleAdmin)
}

// canManageRunner reports whether c may pause, limit, revoke and link r:
// its owner or an admin of its org.
func (s *Server) canManageRunner(c Caller, r tunnel.Runner) bool {
	if r.OrgID() != c.Org || r.Owner == nil {
		return false
	}
	return r.Owner.UserID == c.User || s.orgAdmin(c) || c.PlatformAdmin
}

// orgRunner is runner id if it belongs to c's org.
func (s *Server) orgRunner(c Caller, id string) (tunnel.Runner, bool) {
	fl := s.fleet()
	if fl == nil {
		return tunnel.Runner{}, false
	}
	r, ok := fl.Get(id)
	if !ok || r.OrgID() != c.Org {
		return tunnel.Runner{}, false
	}
	return r, true
}

func (s *Server) view(c Caller, r tunnel.Runner) runnerView {
	if s.Dispatch != nil {
		r.Running = s.Dispatch.Busy(r.ID)
	}
	return runnerView{Runner: r, Mine: r.Owner != nil && r.Owner.UserID == c.User, CanManage: s.canManageRunner(c, r)}
}

var errNoRunner = errors.New("no such runner in your organization")

func (s *Server) runnerRoutes(mux *http.ServeMux, rh *tunnel.Hub) {
	mux.HandleFunc("GET /api/runners", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.caller(r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}
		out := []runnerView{}
		for _, x := range rh.List() {
			if x.OrgID() == c.Org {
				out = append(out, s.view(c, x))
			}
		}
		// hub and version: for the command that updates an outdated runner.
		hubURL, _ := s.hubURL(r)
		writeJSON(w, http.StatusOK, map[string]any{"runners": out, "hub": hubURL, "version": buildinfo.Version})
	})
	// A code registers a runner as the caller's, in their active org; only
	// a signed-in browser makes one (not an API token).
	mux.Handle("POST /api/runners/codes", auth.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.caller(r); err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		code, expires, err := rh.NewCode(s.requestOwner(r))
		if err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		hubURL, _ := s.hubURL(r)
		writeJSON(w, http.StatusOK, map[string]any{"code": code, "expires": expires, "hub": hubURL})
	})))
	mux.Handle("DELETE /api/runners/{id}", auth.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, x, ok := s.managedRunner(w, r)
		if !ok {
			return
		}
		if err := rh.Revoke(x.ID); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.HandleFunc("PATCH /api/runners/{id}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Parallel *int  `json:"parallel"`
			Paused   *bool `json:"paused"`
		}
		c, x, ok := s.managedRunner(w, r)
		if !ok {
			return
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		if req.Parallel != nil && (*req.Parallel < 0 || *req.Parallel > 64) {
			writeErr(w, fmt.Errorf("%w: parallel is 0 (the runner's own limit) to 64", errBadRequest))
			return
		}
		if err := rh.Configure(x.ID, req.Parallel, req.Paused); err != nil {
			writeErr(w, err)
			return
		}
		s.Todos.Kick()
		x, _ = rh.Get(x.ID)
		writeJSON(w, http.StatusOK, s.view(c, x))
	})
	// The repositories on the caller's own runner, to add as its projects (repos.go).
	mux.HandleFunc("GET /api/runners/{id}/repos", s.runnerRepos)
	mux.HandleFunc("POST /api/runners/{id}/repos", s.addRunnerRepos)
	mux.HandleFunc("POST /runner/register", rh.ServeRegister)
	mux.HandleFunc("GET /runner/connect", rh.ServeConnect)
	mux.HandleFunc("POST /runner/report", rh.ServeReport)
	mux.HandleFunc("POST "+tunnel.BoardPath, s.runnerBoard(rh))
	// The one-line installer, and the version it installs (no sign-in:
	// neither holds a secret).
	mux.HandleFunc("GET /runner/install.sh", runnerctl.ScriptHandlerFor(s.hubURL, buildinfo.Version))
	mux.HandleFunc("GET /runner/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": buildinfo.Version})
	})
}

// managedRunner is the runner in r's path if the caller may manage it, or
// answers r and returns false. Another org's runner is "not found".
func (s *Server) managedRunner(w http.ResponseWriter, r *http.Request) (Caller, tunnel.Runner, bool) {
	c, err := s.caller(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return c, tunnel.Runner{}, false
	}
	x, ok := s.orgRunner(c, r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": errNoRunner.Error()})
		return c, x, false
	}
	if !s.canManageRunner(c, x) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the runner's owner or an admin of the organization can do that"})
		return c, x, false
	}
	return c, x, true
}

// linkRunners checks a project's new runner list for c: every runner in
// c's org (and p's), the newly added ones c's own or c an org admin, and p
// with a git remote. It returns the list cleaned up.
func (sv *service) linkRunners(p model.Project, ids []string) ([]string, error) {
	var out []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || slices.Contains(out, id) {
			continue
		}
		r, ok := sv.s.orgRunner(sv.c, id)
		if !ok || r.OrgID() != p.OrgID {
			return nil, fmt.Errorf("%w: %s", errBadRequest, errNoRunner)
		}
		if !slices.Contains(p.Runners, id) && !sv.s.canManageRunner(sv.c, r) {
			return nil, fmt.Errorf("%w: %s is %s's runner: only they or an admin of the organization can link it", errBadRequest, r.Name, r.Owner.Username)
		}
		out = append(out, id)
	}
	if len(out) > 0 && p.RemoteURL == "" {
		return nil, fmt.Errorf("%w: a project shared with runners needs a git remote (each runner clones it); connect one first", errBadRequest)
	}
	return out, nil
}

// runnerTask returns the dispatcher for t, a task on a runner, or an
// error if this Dash has none.
func (sv *service) runnerTask(t model.Todo) (*dispatch.Dispatcher, error) {
	if sv.s.Dispatch == nil {
		return nil, fmt.Errorf("%w: this task ran on a runner, and this Dash doesn't reach runners", errBadRequest)
	}
	return sv.s.Dispatch, nil
}

// approveOnRunner asks t's runner to land it: the runner squashes it onto
// the default branch, runs the checks and pushes it to origin (or opens a
// pull request, if the project opted into them), then reports it merged.
func (sv *service) approveOnRunner(ctx context.Context, t model.Todo) (model.Todo, error) {
	if t.Status != model.StatusReview && t.Status != model.StatusFailed {
		return t, fmt.Errorf("%w: only tasks in review (or a failed merge) can be approved", errBadRequest)
	}
	d, err := sv.runnerTask(t)
	if err != nil {
		return t, err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := d.Approve(cctx, t); err != nil {
		return t, err
	}
	return sv.data.UpdateTodo(t.ID, func(x *model.Todo) error {
		if x.Status == model.StatusReview || x.Status == model.StatusFailed {
			x.Status, x.Error, x.Resumable = model.StatusMerging, "", false
		}
		return nil
	})
}

// revertOnRunner asks the runner that landed t to undo it; the task
// updates when the runner reports the revert.
func (sv *service) revertOnRunner(ctx context.Context, t model.Todo) error {
	d, err := sv.runnerTask(t)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := d.Revert(cctx, t); err != nil {
		return err
	}
	_, err = sv.data.UpdateTodo(t.ID, func(x *model.Todo) error { x.Error, x.RevertConflict = "", nil; return nil })
	return err
}

// fetchFromRunner gets a task's diff, log or events from its runner.
func (sv *service) fetchFromRunner(ctx context.Context, t model.Todo, what string) ([]byte, error) {
	d, err := sv.runnerTask(t)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return d.Fetch(cctx, t, what)
}

// sendable refuses files for a task of a project linked to runners: they
// are on the hub, out of the runners' reach.
func (sv *service) sendable(projectID string, files bool) error {
	if !files {
		return nil
	}
	if p, err := sv.data.GetProject(projectID); err == nil && p.OnRunners() {
		return fmt.Errorf("%w: attachments and editor changes can't be sent to runners; describe them in the instructions instead", errBadRequest)
	}
	return nil
}

// notOnRunner refuses what only works for a task whose files are here.
func notOnRunner(t model.Todo) error {
	if t.OnRunner() {
		return fmt.Errorf("%w: this task's worktree is on its runner, not on the hub", errBadRequest)
	}
	return nil
}
