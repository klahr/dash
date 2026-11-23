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
	"github.com/klahr/dash/internal/disk"
	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/runnerctl"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// A hub's runners (`dash runner`) belong to the user who registered them,
// in the org they were working in. Everyone in that org sees them. Projects
// don't name runners: a project runs on runner labels, and its work goes
// to the org's runners that have them (model.LabelsMatch) and that do the
// work of whoever it is for: a runner does only its owner's work, unless
// it is a shared runner (Take other members' tasks, with an API key and no
// subscription login; see tunnel/shared.go). The owner and the org's
// admins label, pause, limit, share and revoke a runner. Members may only
// set a project's labels so that it runs on their own runners or shared
// ones, admins on any of the org's (checkRunsOn: a runner clones the
// project and does its git work with its owner's git login). Other orgs
// never see a runner.

// runnerView is a runner as the org settings page shows it.
type runnerView struct {
	tunnel.Runner
	Mine      bool `json:"mine"`       // the caller registered it
	CanManage bool `json:"can_manage"` // the caller may label, pause, limit and revoke it
	// SignIn is how Claude signs in on it, for people: "an API key", "a
	// Claude Max subscription", … ("unknown" from a runner too old to say).
	SignIn string `json:"sign_in"`
	// TakesOthers: it takes other members' tasks now (Shared, with an API
	// key and no subscription login). CantShare is why Take other
	// members' tasks can't be turned on, or has no effect ("": it can).
	TakesOthers bool   `json:"takes_others"`
	CantShare   string `json:"cant_share,omitempty"`
	// ClaudeOnly: a planner's runner, which runs only its owner's general
	// chat and feature brief interviews, never the org's work.
	ClaudeOnly bool `json:"claude_only,omitempty"`
}

func (s *Server) fleet() *tunnel.Hub {
	if s.Peers == nil {
		return nil
	}
	return s.Peers.Runners
}

// hub reports whether this Server is the hub: the one runners connect to
// (Peers.Runners). The hub runs nothing on its own computer (no git, gh,
// Claude, shell commands or terminals, no working trees): its projects'
// work is forwarded to the runners linked to them (see hubproxy.go), and
// it never makes projects on its own disk. Every other Server is a
// runner's, which does that work itself.
func (s *Server) hub() bool { return s.fleet() != nil }

// orgAdmin reports whether c is an admin (or owner) of their org.
func (s *Server) orgAdmin(c Caller) bool {
	if c.Machine {
		return true
	}
	m, err := s.Store.Accounts().Membership(c.User, c.Org)
	return err == nil && m.Role.AtLeast(store.RoleAdmin)
}

// canManageRunner reports whether c may label, pause, limit and revoke r,
// and send a project's work to it:
// its owner or an admin of its org.
func (s *Server) canManageRunner(c Caller, r tunnel.Runner) bool {
	if r.OrgID() != c.Org || r.Owner == nil {
		return false
	}
	return ownedBy(r, c) || s.orgAdmin(c) || c.PlatformAdmin
}

// ownedBy reports whether c registered r. A caller with no user (another
// Dash, or Dash itself) owns no runner.
func ownedBy(r tunnel.Runner, c Caller) bool {
	return c.User != 0 && r.OwnerID() == c.User
}

// orgRunner is runner id if it belongs to c's org (for a planner, if it is
// their own: they don't see anyone else's).
func (s *Server) orgRunner(c Caller, id string) (tunnel.Runner, bool) {
	fl := s.fleet()
	if fl == nil {
		return tunnel.Runner{}, false
	}
	r, ok := fl.Get(id)
	if !ok || !sees(c, r) {
		return tunnel.Runner{}, false
	}
	return r, true
}

// sees reports whether c sees runner r: everyone sees their org's runners,
// except a planner, who sees only their own (see plannerRoutes).
func sees(c Caller, r tunnel.Runner) bool {
	return r.OrgID() == c.Org && (!c.Planner || ownedBy(r, c))
}

// youHaveNoRunner is the advice for a member without a runner of their own.
const youHaveNoRunner = "You have no runner: Runners → Add a runner, or ask an admin for a shared one"

// hasOwnRunner reports whether c registered a runner in their org.
func (s *Server) hasOwnRunner(c Caller) bool {
	fl := s.fleet()
	return fl != nil && slices.ContainsFunc(fl.List(), func(r tunnel.Runner) bool {
		return r.OrgID() == c.Org && ownedBy(r, c)
	})
}

func (s *Server) view(c Caller, r tunnel.Runner) runnerView {
	if s.Dispatch != nil {
		r.Running = s.Dispatch.Busy(r.ID)
	}
	v := runnerView{Runner: r, Mine: ownedBy(r, c), CanManage: s.canManageRunner(c, r),
		SignIn: r.Auth().Plan(), CantShare: r.Auth().NotShared(), ClaudeOnly: r.ClaudeOnly()}
	v.TakesOthers, _ = r.TakesOthers()
	if v.ClaudeOnly {
		v.CantShare = "it is a planner's runner"
	}
	return v
}

var errNoRunner = errors.New("no such runner in your organization")

func (s *Server) runnerRoutes(mux routeMux, rh *tunnel.Hub) {
	mux.HandleFunc("GET /api/runners", func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.callerOr(w, r)
		if !ok {
			return
		}
		out := []runnerView{}
		for _, x := range rh.List() {
			if sees(c, x) {
				out = append(out, s.view(c, x))
			}
		}
		// hub and version: for the command that updates an outdated runner.
		hubURL, _ := s.hubURL(r)
		// can_update: runners can update themselves to the hub's release (not
		// for a hub built from source).
		writeJSON(w, http.StatusOK, map[string]any{"runners": out, "hub": hubURL, "version": buildinfo.Version, "can_update": rh.Release != ""})
	})
	// A code registers a runner as the caller's, in their active org; only
	// a signed-in browser makes one (not an API token).
	mux.Handle("POST /api/runners/codes", auth.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.caller(r); err != nil {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		code, expires, err := rh.NewCode(s.requestOwner(r))
		if err != nil {
			writeError(w, http.StatusForbidden, err.Error())
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
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.HandleFunc("PATCH /api/runners/{id}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name       *string        `json:"name"`
			Parallel   *int           `json:"parallel"`
			Paused     *bool          `json:"paused"`
			AutoUpdate *bool          `json:"auto_update"`
			Disk       *disk.Settings `json:"disk"`
			Labels     *[]string      `json:"labels"`
			// Shared is Take other members' tasks (tunnel.Runner.Shared).
			Shared *bool `json:"shared"`
		}
		c, x, ok := s.managedRunner(w, r)
		if !ok {
			return
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		if c.Planner && (req.Parallel != nil || req.Paused != nil || req.AutoUpdate != nil || req.Disk != nil || req.Labels != nil || req.Shared != nil) {
			// A planner's runner takes no work: there is nothing to tune.
			writeErr(w, fmt.Errorf("%w: a planner can rename and remove their runner only", errBadRequest))
			return
		}
		if req.Parallel != nil && (*req.Parallel < 0 || *req.Parallel > 64) {
			writeErr(w, fmt.Errorf("%w: parallel is 0 (the runner's own limit) to 64", errBadRequest))
			return
		}
		var gone []model.Project
		if req.Labels != nil {
			var err error
			if gone, err = s.as(c).checkRelabel(x, *req.Labels); err != nil {
				writeErr(w, err)
				return
			}
		}
		if req.Shared != nil && *req.Shared && !x.Shared {
			// Refused here, before anything else changes, for a runner
			// that signs in to Claude with a subscription (or doesn't say).
			if why := x.CanShare(rh.Release); why != "" {
				writeErr(w, fmt.Errorf("%w: %s", errBadRequest, why))
				return
			}
		}
		if req.Name != nil {
			switch err := rh.Rename(x.ID, *req.Name); {
			case errors.Is(err, tunnel.ErrNameTaken):
				writeErr(w, fmt.Errorf("%w: another runner is called %s: pick another name", errConflict, strings.ToLower(strings.TrimSpace(*req.Name))))
				return
			case errors.Is(err, tunnel.ErrNameHub):
				writeErr(w, fmt.Errorf("%w: that name is the hub's own: pick another", errConflict))
				return
			case err != nil:
				writeErr(w, fmt.Errorf("%w: %w", errBadRequest, err))
				return
			}
		}
		if err := rh.Configure(x.ID, req.Parallel, req.Paused); err != nil {
			writeErr(w, err)
			return
		}
		if req.AutoUpdate != nil {
			if err := rh.SetAutoUpdate(x.ID, *req.AutoUpdate); err != nil {
				writeErr(w, err)
				return
			}
		}
		if req.Disk != nil {
			if err := rh.SetDisk(x.ID, *req.Disk); err != nil {
				writeErr(w, err)
				return
			}
		}
		if req.Shared != nil {
			if err := rh.SetShared(x.ID, *req.Shared); err != nil {
				writeErr(w, fmt.Errorf("%w: %w", errBadRequest, err))
				return
			}
			if !*req.Shared && s.Dispatch != nil {
				s.Dispatch.StopOthersWork(x.ID) // its other members' tasks wait for another runner
			}
		}
		if req.Labels != nil {
			if err := rh.SetLabels(x.ID, *req.Labels); err != nil {
				writeErr(w, fmt.Errorf("%w: %w", errBadRequest, err))
				return
			}
			s.unlinkLater(gone, []string{x.ID})
		}
		s.Todos.Kick()
		x, _ = rh.Get(x.ID)
		writeJSON(w, http.StatusOK, s.view(c, x))
	})
	// Updating a runner to the hub's release: it takes no new tasks, finishes
	// its running ones, then updates itself and restarts (tunnel/update.go).
	// While it waits for its tasks, the update can be cancelled.
	mux.HandleFunc("POST /api/runners/{id}/update", func(w http.ResponseWriter, r *http.Request) {
		c, x, ok := s.managedRunner(w, r)
		if !ok {
			return
		}
		switch err := rh.StartUpdate(x.ID); {
		case errors.Is(err, tunnel.ErrNoRelease), errors.Is(err, tunnel.ErrUpToDate):
			writeError(w, http.StatusBadRequest, err.Error())
			return
		case err != nil:
			writeErr(w, err)
			return
		}
		x, _ = rh.Get(x.ID)
		writeJSON(w, http.StatusOK, s.view(c, x))
	})
	mux.HandleFunc("DELETE /api/runners/{id}/update", func(w http.ResponseWriter, r *http.Request) {
		c, x, ok := s.managedRunner(w, r)
		if !ok {
			return
		}
		switch err := rh.CancelUpdate(x.ID); {
		case errors.Is(err, tunnel.ErrNoUpdate), errors.Is(err, tunnel.ErrUpdating):
			writeError(w, http.StatusConflict, err.Error())
			return
		case err != nil:
			writeErr(w, err)
			return
		}
		s.Todos.Kick() // it takes tasks again
		x, _ = rh.Get(x.ID)
		writeJSON(w, http.StatusOK, s.view(c, x))
	})
	// The repositories on the caller's own runner, to add as its projects (repos.go).
	mux.HandleFunc("GET /api/runners/{id}/repos", s.runnerRepos)
	mux.HandleFunc("POST /api/runners/{id}/repos", s.addRunnerRepos)
	mux.HandleFunc("POST /api/runners/{id}/projects", s.createRunnerProject)
	mux.HandleFunc("GET /api/runners/{id}/projects/{pid}", s.runnerProject)
	mux.HandleFunc("PATCH /api/runners/{id}/projects/{pid}", s.updateRunnerProject)
	mux.HandleFunc("GET /api/runners/{id}/github-repos", s.runnerGitHubRepos)
	mux.HandleFunc("POST /api/runners/{id}/github-repos", s.createRunnerGitHubRepo)
	s.runnerLinkRoutes(mux) // the runner's Projects screen (runnerlinks.go)
	mux.HandleFunc("POST /runner/register", rh.ServeRegister)
	mux.HandleFunc("GET /runner/connect", rh.ServeConnect)
	mux.HandleFunc("POST /runner/report", rh.ServeReport)
	mux.HandleFunc("POST "+tunnel.BoardPath, s.runnerBoard(rh))
	// The one-line installers (Linux and macOS; Windows), and the version it installs (no sign-in:
	// neither holds a secret).
	mux.HandleFunc("GET /runner/install.sh", runnerctl.ScriptHandlerFor(s.hubURL, buildinfo.Version))
	mux.HandleFunc("GET /runner/install.ps1", runnerctl.PowerShellHandlerFor(s.hubURL, buildinfo.Version))
	mux.HandleFunc("GET /runner/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": buildinfo.Version})
	})
}

// runnerFor is the runner in r's path if allowed lets the caller at it;
// otherwise it answers r (403 with denied) and returns false. Another org's
// runner is "not found".
func (s *Server) runnerFor(w http.ResponseWriter, r *http.Request, allowed func(Caller, tunnel.Runner) bool, denied func(Caller) string) (Caller, tunnel.Runner, bool) {
	c, ok := s.callerOr(w, r)
	if !ok {
		return c, tunnel.Runner{}, false
	}
	x, ok := s.orgRunner(c, r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errNoRunner.Error())
		return c, x, false
	}
	if !allowed(c, x) {
		writeError(w, http.StatusForbidden, denied(c))
		return c, x, false
	}
	return c, x, true
}

// deny is a refusal for runnerFor that is the same for every caller.
func deny(msg string) func(Caller) string { return func(Caller) string { return msg } }

// managedRunner is the runner in r's path if the caller may manage it, or
// answers r and returns false.
func (s *Server) managedRunner(w http.ResponseWriter, r *http.Request) (Caller, tunnel.Runner, bool) {
	return s.runnerFor(w, r, s.canManageRunner, deny("only the runner's owner or an admin of the organization can do that"))
}

// checkRunsOn checks a project's new runner labels for c and returns them
// tidied (model.CleanLabels): p needs a git remote to run on runners, and
// every runner the labels newly link p to must be c's own, a shared runner,
// or c an admin of the org. Whose tasks a runner runs no longer depends on
// labels (a runner runs only its owner's, unless shared: see
// dispatch.Dispatcher.pick); this guards the rest of a project's work on a
// runner, which uses its owner's git login: its clone, pushes and tags.
func (sv *service) checkRunsOn(p model.Project, labels []string) ([]string, error) {
	labels, err := model.CleanLabels(labels)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errBadRequest, err)
	}
	if len(labels) > 0 && p.RemoteURL == "" {
		return nil, fmt.Errorf("%w: a project that runs on runners needs a git remote (each runner clones it); connect one first", errBadRequest)
	}
	np := p
	np.RunsOn = labels
	if err := sv.checkNewRunners(p, np); err != nil {
		return nil, err
	}
	return labels, nil
}

// checkNewRunners checks that every runner project p newly runs on as np
// (its labels or its agent changed) is c's own, a shared runner, or c an
// admin of the org (see checkRunsOn).
func (sv *service) checkNewRunners(p, np model.Project) error {
	before := sv.s.projectRunners(p)
	for _, r := range sv.s.projectRunners(np) {
		if slices.ContainsFunc(before, func(b tunnel.Runner) bool { return b.ID == r.ID }) || sv.s.canManageRunner(sv.c, r) {
			continue
		}
		if shared, _ := r.TakesOthers(); shared {
			continue // it takes the org's work: what it is shared for
		}
		fix := "Pick labels it lacks (give your own runners a label of their own in Runners), or ask them"
		if !sv.s.hasOwnRunner(sv.c) {
			fix = youHaveNoRunner + ", then pick labels only that one has, or ask them"
		}
		on := "the labels " + model.ShowLabels(np.RunnerLabels())
		if len(model.MissingLabels(nil, np.RunsOn)) == 0 {
			on = "any runner" // LabelAny is no label it has
		}
		return fmt.Errorf("%w: %s is %s's runner, and this project would run on it (it runs on %s): only they or an admin of the organization can link projects to it. %s",
			errBadRequest, r.Name, r.Owner.Username, on, fix)
	}
	return nil
}

// checkDeployRunsOn checks the runner labels a project's deploy command
// runs on (model.Project.DeployRunsOn) and returns them clean, nil for
// none. LabelAny, which every runner matches, adds nothing and goes. Only
// a hub's projects run on labels.
func (sv *service) checkDeployRunsOn(labels []string) ([]string, error) {
	labels, err := model.CleanLabels(labels)
	if err != nil {
		return nil, fmt.Errorf("%w: deploy runs on: %v", errBadRequest, err)
	}
	labels = slices.DeleteFunc(labels, func(l string) bool { return strings.EqualFold(l, model.LabelAny) })
	if len(labels) == 0 {
		return nil, nil
	}
	if !sv.s.hub() {
		return nil, fmt.Errorf("%w: deploy_runs_on is a hub's project's setting: this Dash deploys on its own computer", errBadRequest)
	}
	return labels, nil
}

// checkRelabel checks giving runner x the labels: no project it stops
// working on may have a task running on it (checkUnlink). It returns those
// projects, which the runner must be told it left (unlinkLater).
func (sv *service) checkRelabel(x tunnel.Runner, labels []string) ([]model.Project, error) {
	clean, err := model.CleanRunnerLabels(labels)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errBadRequest, err)
	}
	y := x
	y.Labels = clean
	var gone []model.Project
	for _, p := range sv.data.ListProjects() {
		if x.Serves(p) && !y.Serves(p) {
			if err := sv.checkUnlink(p, []string{x.ID}); err != nil {
				return nil, err
			}
			gone = append(gone, p)
		}
	}
	return gone, nil
}

// unlinkLater tells runners ids, in the background, that they no longer
// work on projects (dispatch.Dispatcher.Unlink): each keeps its clone as a
// project of its own.
func (s *Server) unlinkLater(projects []model.Project, ids []string) {
	if len(projects) == 0 || len(ids) == 0 || s.Dispatch == nil {
		return
	}
	go func() {
		for _, p := range projects {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			s.Dispatch.Unlink(ctx, p.ID, ids)
			cancel()
		}
	}()
}

// actionOnRunner refuses c's action on task t that runs a project command
// on t's runner (what: "run its tests"), unless that runner does c's work:
// it is theirs, or a shared runner (tunnel.Runner.WorksFor). The runner
// checks it again.
func (s *Server) actionOnRunner(c Caller, t model.Todo, what string) error {
	fl := s.fleet()
	if fl == nil {
		return nil
	}
	r, ok := fl.Get(t.Runner)
	if !ok {
		return nil // onRunner says it is gone
	}
	if ok, why := r.WorksFor([]int64{c.User}); !ok {
		return fmt.Errorf("%w: %s, so it can't %s for you", errForbidden, why, what)
	}
	return nil
}

// runnerTask returns the dispatcher for t, a task on a runner, or an
// error if this Dash has none.
func (sv *service) runnerTask(t model.Todo) (*dispatch.Dispatcher, error) {
	if sv.s.Dispatch == nil {
		return nil, fmt.Errorf("%w: this task ran on a runner, and this Dash doesn't reach runners", errBadRequest)
	}
	return sv.s.Dispatch, nil
}

// pauseOnRunner pauses t, which runs on a runner: the hub marks it paused
// and has the runner stop it. Once the runner reports it stopped, the hub
// queues it again, still paused (see dispatch.Dispatcher's applyTask);
// resumed, it goes back to a runner, which continues its Claude session.
func (sv *service) pauseOnRunner(ctx context.Context, t model.Todo) error {
	d, err := sv.runnerTask(t)
	if err != nil {
		return err
	}
	if _, err := sv.data.UpdateTodo(t.ID, func(x *model.Todo) error {
		if x.Status != model.StatusInProgress || !x.OnRunner() {
			return model.ErrInvalidTransition{From: x.Status, To: "paused"}
		}
		x.Paused = true
		return nil
	}); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := d.Cancel(cctx, t); err != nil {
		_, _ = sv.data.UpdateTodo(t.ID, func(x *model.Todo) error {
			if x.Status == model.StatusInProgress {
				x.Paused = false
			}
			return nil
		})
		return err
	}
	return nil
}

// approveOnRunner asks t's runner to land it: the runner opens a pull
// request ("pr" mode), which a person merges on GitHub, or squashes it
// onto the default branch, runs the checks and pushes it to origin
// ("local"), then reports it merged.
func (sv *service) approveOnRunner(ctx context.Context, t model.Todo) (model.Todo, error) {
	if t.Status != model.StatusReview && t.Status != model.StatusFailed {
		return t, fmt.Errorf("%w: only tasks in review (or a failed merge) can be approved", errBadRequest)
	}
	if err := reviewedOnGitHub(t); err != nil {
		return t, err
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

// sendable refuses a hub's task editor changes (model.Todo.Edits): they
// would be on the hub, out of the runners' reach. Attachments go to the
// runner with the task (dispatch.Task.Attachments).
func (sv *service) sendable(projectID string, edits bool) error {
	if !edits {
		return nil
	}
	if _, err := sv.data.GetProject(projectID); err == nil && sv.s.hub() {
		return fmt.Errorf("%w: editor changes can't be sent to runners; describe them in the instructions instead", errBadRequest)
	}
	return nil
}
