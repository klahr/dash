package api

// Deploys: a project's environments (model.Environment, project settings)
// and each deploy to one (model.Deploy, kept in the store), with the
// Deploys page's routes.
//
// A deploy is queued on the hub (or a Dash that isn't one), and runs when
// its environment has none running: one at a time per environment, the
// others wait their turn (deployEngine). It runs where the project's code
// is: on a hub, on a runner of the project with the labels the
// environment deploys on; elsewhere, on this computer. There, in a
// checkout of the commit kept per environment under DeployRoot, the
// environment's build command and then its deploy command run with the
// DASH_* variables (model.DeployVar…) and the secrets of the runner's
// ~/.dash/env/<environment>.env (package deploy), and the environment's
// URL is checked. The hub asks how it goes every so often, saving its
// status and its output (secrets masked by the runner) in the record.
//
// The hub's routes (/api/projects/{pid}/deploys…, /api/deploys/{id}) are
// the UI's; a runner's (/api/hub/projects/{hpid}/deploys…) are its hub's
// alone, for projects linked to it, running only the commands the hub
// linked it with (held ones are empty, see dispatch.HeldCommands).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/deploy"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

func (s *Server) deployRoutes(mux routeMux) {
	// The UI's.
	mux.HandleFunc("GET /api/projects/{pid}/environments", s.listEnvironments)
	mux.HandleFunc("GET /api/projects/{pid}/deploys", s.listDeploys)
	mux.HandleFunc("POST /api/projects/{pid}/deploys", s.createDeploy)
	mux.HandleFunc("GET /api/deploys/{id}", s.getDeploy)
	mux.HandleFunc("DELETE /api/deploys/{id}", s.cancelDeploy)
	// A runner's: only its hub asks.
	mux.HandleFunc("POST /api/hub/projects/{hpid}/deploys", s.hubDeployStart)
	mux.HandleFunc("GET /api/hub/projects/{hpid}/deploys/{id}", s.hubDeployState)
	mux.HandleFunc("DELETE /api/hub/projects/{hpid}/deploys/{id}", s.hubDeployStop)
	mux.HandleFunc("POST /api/hub/projects/{hpid}/deploys/compare", s.hubDeployCompare)
}

// ---- settings ----

// deployScripts is what req changes the deploy command to (nil: it
// doesn't send environments).
func (req projectUpdate) deployScripts() *string {
	if req.Environments == nil {
		return nil
	}
	s := model.DeployScripts(*req.Environments)
	return &s
}

// checkEnvironments checks a project's environments and returns them clean
// (model.CleanEnvironments). Only a hub's environments run on labels.
func (sv *service) checkEnvironments(envs []model.Environment) ([]model.Environment, error) {
	out, err := model.CleanEnvironments(envs)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errBadRequest, err)
	}
	for i, e := range out {
		labels, err := sv.checkDeployRunsOn(e.RunsOn)
		if err != nil {
			return nil, fmt.Errorf("%w (environment %s)", err, e.Name)
		}
		out[i].RunsOn = labels
	}
	return out, nil
}

// requestEnvironments are the environments a new project asks for: its
// environments, or its deploy command (on its labels) as "production".
func (sv *service) requestEnvironments(req projectRequest) ([]model.Environment, error) {
	envs := req.Environments
	if cmd := model.CleanScript(req.DeployCommand); cmd != "" {
		if len(envs) > 0 {
			return nil, fmt.Errorf("%w: give environments or a deploy command, not both", errBadRequest)
		}
		envs = []model.Environment{{Name: model.DefaultEnvironment, DeployCommand: cmd, RunsOn: req.DeployRunsOn}}
	}
	return sv.checkEnvironments(envs)
}

// ---- the UI's routes ----

// envStatus is an environment on the Deploys page: what is live there,
// what is on its way, and where its deploys run.
type envStatus struct {
	model.Environment
	Live   *model.Deploy `json:"live,omitempty"`
	Active *model.Deploy `json:"active,omitempty"`
	Queued int           `json:"queued"`
	// Ahead counts, for each other environment whose live commit differs,
	// the commits live here that aren't live there ("staging is 3 commits
	// ahead of production"). Missing when it couldn't be counted.
	Ahead map[string]int `json:"ahead,omitempty"`
	// Runners are, on a hub, the runners its deploys may run on, and
	// whether each has its secrets file.
	Runners []envRunner `json:"runners,omitempty"`
	// EnvFile: whether this computer has its secrets file (not a hub).
	EnvFile bool `json:"env_file,omitempty"`
	// Warning says what keeps its deploys from going well, if anything.
	Warning string `json:"warning,omitempty"`
}

type envRunner struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Online  bool   `json:"online"`
	EnvFile bool   `json:"env_file"`
}

// listEnvironments handles GET /api/projects/{pid}/environments.
func (s *Server) listEnvironments(w http.ResponseWriter, r *http.Request) {
	sv, p, ok := s.project(w, r)
	if !ok {
		return
	}
	out := make([]envStatus, 0, len(p.Environments))
	var have []string
	if !s.hub() {
		have = deploy.EnvFiles(s.envDir())
	}
	for _, e := range p.Environments {
		st := envStatus{Environment: e}
		for _, d := range sv.data.ListDeploys(p.ID, e.Name) {
			switch {
			case d.Status == model.DeployLive && st.Live == nil:
				st.Live = &d
			case d.Status.Active() && st.Active == nil:
				st.Active = &d
			case d.Status == model.DeployQueued:
				st.Queued++
			}
		}
		if s.hub() {
			st.Runners, st.Warning = s.envRunners(p, e)
		} else {
			st.EnvFile = slices.Contains(have, e.Name)
		}
		out = append(out, st)
	}
	s.countAhead(r.Context(), sv, p, out)
	writeJSON(w, http.StatusOK, out)
}

// envRunners are the runners of p that deploy to e, and a warning when
// none of them can or none has e's secrets file.
func (s *Server) envRunners(p model.Project, e model.Environment) ([]envRunner, string) {
	out := []envRunner{}
	files := 0
	for _, r := range s.projectRunners(p) {
		if !r.Deploys(p, e) {
			continue
		}
		has := r.HasEnvFile(e.Name)
		if has {
			files++
		}
		out = append(out, envRunner{ID: r.ID, Name: r.Name, Online: r.Online, EnvFile: has})
	}
	where := "this project"
	if len(e.RunsOn) > 0 {
		where = strings.Join(e.RunsOn, ", ")
	}
	switch {
	case len(out) == 0 && len(e.RunsOn) > 0:
		return out, fmt.Sprintf("No runner of this project has the labels %s: deploys to %s fail.", where, e.Name)
	case len(out) == 0:
		return out, "No runner works on this project: deploys fail."
	case files == 0:
		return out, fmt.Sprintf("No runner with %s has a %s env file (~/.dash/env/%s.env): deploys run without its secrets.", where, e.Name, e.Name)
	}
	return out, ""
}

// countAhead fills in how many commits each environment's live deploy is
// ahead of the others', asking git where the project's code is. Whatever
// can't be counted (no runner online, commits it hasn't) is left out.
func (s *Server) countAhead(ctx context.Context, sv *service, p model.Project, envs []envStatus) {
	var pairs []commitPair
	var where [][2]int
	for i, a := range envs {
		for j, b := range envs {
			if i != j && a.Live != nil && b.Live != nil && a.Live.Commit != b.Live.Commit && a.Live.Commit != "" && b.Live.Commit != "" {
				pairs = append(pairs, commitPair{Base: b.Live.Commit, Head: a.Live.Commit})
				where = append(where, [2]int{i, j})
			}
		}
	}
	if len(pairs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var counts []int
	if s.hub() {
		run, _, err := sv.projectRunner(ctx, p, "")
		if err != nil {
			return
		}
		if err := s.fleet().Call(ctx, run.ID, http.MethodPost, deployPath(p.ID, "compare"), pairs, &counts); err != nil {
			return
		}
	} else {
		counts = s.compareHere(ctx, p, pairs)
	}
	for k, n := range counts {
		if k >= len(where) || n <= 0 {
			continue
		}
		i, j := where[k][0], where[k][1]
		if envs[i].Ahead == nil {
			envs[i].Ahead = map[string]int{}
		}
		envs[i].Ahead[envs[j].Name] = n
	}
}

// listDeploys handles GET /api/projects/{pid}/deploys?environment=: the
// project's deploys, newest first, without their logs.
func (s *Server) listDeploys(w http.ResponseWriter, r *http.Request) {
	sv, p, ok := s.project(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, sv.data.ListDeploys(p.ID, r.URL.Query().Get("environment")))
}

// createDeploy handles POST /api/projects/{pid}/deploys {"environment":
// name, "commit": sha}: a deploy of commit ("" for the tip of the
// project's branch, as origin has it when the deploy starts), queued
// behind any other deploy to that environment.
func (s *Server) createDeploy(w http.ResponseWriter, r *http.Request) {
	sv, p, ok := s.project(w, r)
	if !ok {
		return
	}
	var req struct {
		Environment string `json:"environment"`
		Commit      string `json:"commit"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	e, ok := p.Environment(strings.TrimSpace(req.Environment))
	if !ok {
		writeErr(w, fmt.Errorf("%w: the project has no environment %q (Project settings → Deploy)", errBadRequest, req.Environment))
		return
	}
	req.Commit = strings.TrimSpace(req.Commit)
	if req.Commit != "" && gitops.CheckRev(req.Commit) != nil {
		writeErr(w, fmt.Errorf("%w: %q isn't a commit", errBadRequest, req.Commit))
		return
	}
	d, err := s.queueDeploy(sv.c, model.Deploy{ProjectID: p.ID, Environment: e.Name, Ref: req.Commit, Trigger: model.DeployByUser})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

// queueDeploy queues d for c (who starts it) and wakes its environment's queue.
func (s *Server) queueDeploy(c Caller, d model.Deploy) (model.Deploy, error) {
	d.By = c.User
	if c.User != 0 && s.Store != nil {
		if u, err := s.Store.Accounts().User(c.User); err == nil {
			d.ByName = u.Username
		}
	}
	d, err := s.Store.CreateDeploy(d)
	if err != nil {
		return d, err
	}
	slog.Info("deploy: queued", "project", d.ProjectID, "environment", d.Environment, "id", d.ID, "by", d.ByName)
	s.kickDeploys(d.ProjectID, d.Environment)
	return d, nil
}

// getDeploy handles GET /api/deploys/{id}: the deploy with its log.
func (s *Server) getDeploy(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	d, err := sv.data.GetDeploy(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// cancelDeploy handles DELETE /api/deploys/{id}: a queued deploy is taken
// off the queue (failed, "cancelled"); one running is stopped where it runs.
func (s *Server) cancelDeploy(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	d, err := sv.data.GetDeploy(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	who := "someone"
	if u, err := s.Store.Accounts().User(sv.c.User); err == nil {
		who = u.Username
	}
	switch {
	case d.Status == model.DeployQueued:
		_, err = s.Store.UpdateDeploy(d.ID, func(x *model.Deploy) error {
			if x.Status != model.DeployQueued {
				return fmt.Errorf("%w: it has started meanwhile; stop it again", errConflict)
			}
			x.Error = "cancelled by " + who
			x.SetStatus(model.DeployFailed, time.Now())
			return nil
		})
	case d.Status.Active():
		err = s.stopDeploy(r.Context(), d)
	default:
		err = fmt.Errorf("%w: it has finished", errConflict)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// ---- the queue ----

// deployEngine runs each environment's deploys one at a time: running
// maps "<project id>/<environment>" to the deploy being run there.
type deployEngine struct {
	mu      sync.Mutex
	ctx     context.Context
	running map[string]string
	wg      sync.WaitGroup
}

func (s *Server) deployer() *deployEngine {
	s.deploysOnce.Do(func() { s.deploys = &deployEngine{ctx: context.Background(), running: map[string]string{}} })
	return s.deploys
}

// deployPoll is how often the hub asks how a running deploy goes.
func (s *Server) deployPoll() time.Duration {
	if s.DeployPoll > 0 {
		return s.DeployPoll
	}
	return 2 * time.Second
}

// StartDeploys picks up, once Dash has started, the deploys a restart cut
// short (asking their runners how they went) and the queued ones.
func (s *Server) StartDeploys(ctx context.Context) {
	e := s.deployer()
	e.mu.Lock()
	e.ctx = ctx
	e.mu.Unlock()
	seen := map[string]bool{}
	for _, d := range s.Store.ListDeploys("", "") {
		key := d.ProjectID + "/" + d.Environment
		if !d.Status.Done() && !seen[key] {
			seen[key] = true
			s.kickDeploys(d.ProjectID, d.Environment)
		}
	}
}

// kickDeploys runs the next deploy of an environment, unless one runs: one
// a restart cut short first, else the oldest queued.
func (s *Server) kickDeploys(projectID, env string) {
	e := s.deployer()
	key := projectID + "/" + env
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running[key] != "" || e.ctx.Err() != nil {
		return
	}
	list := s.Store.ListDeploys(projectID, env) // newest first
	next := ""
	for _, d := range list {
		if d.Status.Active() {
			next = d.ID
			break
		}
		if d.Status == model.DeployQueued {
			next = d.ID // the oldest one wins: keep looking
		}
	}
	if next == "" {
		return
	}
	e.running[key] = next
	e.wg.Add(1)
	ctx := e.ctx
	go func() {
		defer e.wg.Done()
		s.runDeploy(ctx, next)
		e.mu.Lock()
		delete(e.running, key)
		e.mu.Unlock()
		s.kickDeploys(projectID, env)
	}()
}

// deployRun is POST /api/hub/projects/{hpid}/deploys on a runner (and what
// a Dash that isn't a hub starts on its own): run deploy ID to Environment
// of Commit ("" for the tip of the project's default branch on origin).
type deployRun struct {
	ID          string `json:"id"`
	Environment string `json:"environment"`
	Commit      string `json:"commit,omitempty"`
	PrevCommit  string `json:"prev_commit,omitempty"`
	Artifact    string `json:"artifact,omitempty"`
	Rollback    bool   `json:"rollback,omitempty"`
}

// deployStarted is the answer to a deployRun: what it deploys, and the
// commits since the previous deploy (with the tasks they name).
type deployStarted struct {
	Commit  string `json:"commit"`
	Subject string `json:"subject,omitempty"`
	Commits int    `json:"commits"`
	// Todos are the ids in those commits' "Dash-Todo:" trailers.
	Todos []string `json:"todos,omitempty"`
	// EnvFile: the runner has the environment's secrets file.
	EnvFile bool   `json:"env_file"`
	Warning string `json:"warning,omitempty"`
}

// deployWhere is where a deploy runs: a runner (on a hub) or this Dash.
type deployWhere struct {
	run tunnel.Runner // zero: here
	by  int64
}

func (s *Server) deployCall(ctx context.Context, at deployWhere, method, path string, in, out any) error {
	ctx, cancel := context.WithTimeout(tunnel.WithUser(ctx, at.by), 3*time.Minute)
	defer cancel()
	err := s.fleet().Call(ctx, at.run.ID, method, path, in, out)
	if errors.Is(err, tunnel.ErrOffline) || errors.Is(err, tunnel.ErrUnknownName) {
		return errNoRunnerOnline
	}
	return err
}

func deployPath(hubProject string, rest ...string) string {
	return "/api/hub/projects/" + url.PathEscape(hubProject) + "/deploys" + strings.Join(append([]string{""}, rest...), "/")
}

// runDeploy runs deploy id to the end: it starts it where it runs (unless
// a restart found it running already), follows it and records how it went.
func (s *Server) runDeploy(ctx context.Context, id string) {
	d, err := s.Store.GetDeploy(id)
	if err != nil {
		return
	}
	fail := func(msg string) {
		slog.Info("deploy: failed", "id", id, "environment", d.Environment, "err", msg)
		_, _ = s.Store.UpdateDeploy(id, func(x *model.Deploy) error {
			if x.Error == "" {
				x.Error = msg
			}
			x.SetStatus(model.DeployFailed, time.Now())
			return nil
		})
	}
	p, err := s.deployProject(d.ProjectID)
	if err != nil {
		fail("the project is gone")
		return
	}
	env, ok := p.Environment(d.Environment)
	if !ok {
		fail("the project no longer has this environment")
		return
	}
	at := deployWhere{by: d.By}
	if d.Status == model.DeployQueued {
		if at, err = s.startDeploy(ctx, p, env, d); err != nil {
			fail(err.Error())
			return
		}
	} else if s.hub() {
		// Cut short by a restart: its runner may still be running it.
		run, ok := s.fleet().Get(d.RunnerID)
		if !ok {
			fail("Dash restarted, and the runner that ran it is gone")
			return
		}
		at.run = run
	}
	s.followDeploy(ctx, p, d.ID, at)
}

// startDeploy starts queued deploy d of p to env where it runs, and
// records it there with what it deploys.
func (s *Server) startDeploy(ctx context.Context, p model.Project, env model.Environment, d model.Deploy) (deployWhere, error) {
	at := deployWhere{by: d.By}
	prev, hasPrev := s.Store.LiveDeploy(p.ID, env.Name)
	req := deployRun{ID: d.ID, Environment: env.Name, Commit: d.Ref, Artifact: d.Artifact, Rollback: d.Rollback}
	if hasPrev {
		req.PrevCommit = prev.Commit
	}
	var started deployStarted
	runner := ""
	if s.hub() {
		var users []int64
		if d.By != 0 {
			users = []int64{d.By}
		}
		c := Caller{User: d.By, Org: p.OrgID}
		run, _, err := s.as(c).deployRunnerFor(ctx, p, env, users)
		if err != nil {
			return at, err
		}
		at.run, runner = run, run.Name
		if err := s.deployCall(ctx, at, http.MethodPost, deployPath(p.ID), req, &started); err != nil {
			return at, err
		}
	} else {
		var err error
		if started, err = s.startDeployHere(ctx, p, req, false); err != nil {
			return at, err
		}
		runner, _ = os.Hostname()
	}
	shipped := s.shipped(p, started.Todos)
	_, err := s.Store.UpdateDeploy(d.ID, func(x *model.Deploy) error {
		if x.Status != model.DeployQueued {
			return errDeployCancelled // while it was being started
		}
		x.Commit, x.Subject, x.Commits, x.Shipped = started.Commit, started.Subject, started.Commits, shipped
		x.Runner, x.RunnerID = runner, at.run.ID
		if hasPrev {
			x.PrevCommit, x.PrevDeploy = prev.Commit, prev.ID
		}
		first := model.DeployDeploying
		if env.BuildCommand != "" {
			first = model.DeployBuilding
		}
		x.SetStatus(first, time.Now())
		return nil
	})
	if errors.Is(err, errDeployCancelled) {
		// Cancelled meanwhile: what was started stops too.
		if at.run.ID == "" {
			_ = s.deployRuns.Stop(d.ID)
		} else {
			_ = s.deployCall(context.WithoutCancel(ctx), at, http.MethodDelete, deployPath(p.ID, url.PathEscape(d.ID)), nil, nil)
		}
		return at, err
	}
	slog.Info("deploy: started", "project", p.Name, "environment", env.Name, "id", d.ID, "runner", runner, "commit", started.Commit)
	return at, err
}

// errDeployCancelled is why a deploy cancelled while it was being started
// doesn't go on.
var errDeployCancelled = errors.New("cancelled")

// shipped are the tasks (of p, by their id here or their runner's) and
// the planning cards linked to them that the todo ids name.
func (s *Server) shipped(p model.Project, ids []string) []model.DeployShipped {
	if len(ids) == 0 {
		return nil
	}
	byID := map[string]model.Todo{}
	for _, t := range s.Store.ForOrg(p.OrgID).ListTodos(p.ID) {
		byID[t.ID] = t
		if t.RemoteID != "" {
			byID[t.RemoteID] = t
		}
	}
	var out []model.DeployShipped
	var tasks []string
	for _, id := range ids {
		t, ok := byID[id]
		if !ok || slices.Contains(tasks, t.ID) {
			continue
		}
		tasks = append(tasks, t.ID)
		out = append(out, model.DeployShipped{Kind: model.LinkTask, ID: t.ID, Title: t.Title})
	}
	if cards, err := s.Store.CardsLinkedTo(tasks); err == nil {
		out = append(out, cards...)
	}
	return out
}

// deployLost is how long a running deploy's runner may be out of reach
// before the hub gives up on it.
const deployLost = 10 * time.Minute

// followDeploy asks where deploy id runs how it goes until it is done,
// saving its status and output as they change.
func (s *Server) followDeploy(ctx context.Context, p model.Project, id string, at deployWhere) {
	var lastLog string
	var unreachable time.Time
	for {
		var st deploy.State
		var err error
		if at.run.ID != "" {
			err = s.deployCall(ctx, at, http.MethodGet, deployPath(p.ID, url.PathEscape(id)), nil, &st)
		} else {
			st, err = s.deployRuns.State(id)
		}
		var ce *tunnel.CallError
		switch {
		case ctx.Err() != nil:
			return // Dash is stopping: StartDeploys picks it up again
		case errors.Is(err, deploy.ErrNotFound) || errors.As(err, &ce) && ce.Status == http.StatusNotFound:
			s.finishDeploy(id, deploy.State{Phase: model.DeployFailed, Error: "it was cut short: the runner restarted, or Dash did"}, lastLog)
			return
		case err != nil:
			if unreachable.IsZero() {
				unreachable = time.Now()
			}
			if time.Since(unreachable) > deployLost {
				s.finishDeploy(id, deploy.State{Phase: model.DeployFailed, Error: "lost touch with the runner: " + err.Error()}, lastLog)
				return
			}
		default:
			unreachable = time.Time{}
			if st.Log != lastLog {
				if err := s.Store.SetDeployLog(id, st.Log); err == nil {
					lastLog = st.Log
				}
			}
			if !st.Running {
				s.finishDeploy(id, st, lastLog)
				return
			}
			if st.Phase.Active() {
				_, _ = s.Store.UpdateDeploy(id, func(x *model.Deploy) error {
					if x.Status != st.Phase {
						x.SetStatus(st.Phase, time.Now())
					}
					return nil
				})
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.deployPoll()):
		}
	}
}

// finishDeploy records how deploy id ended. One that went live replaces
// (supersedes) the one live before.
func (s *Server) finishDeploy(id string, st deploy.State, log string) {
	if st.Log != "" && st.Log != log {
		_ = s.Store.SetDeployLog(id, st.Log)
	}
	now := time.Now()
	d, err := s.Store.UpdateDeploy(id, func(x *model.Deploy) error {
		x.ExitCode = st.ExitCode
		if st.Error != "" {
			x.Error = st.Error
		}
		if v := st.Outputs["version"]; v != "" {
			x.Version = v
		}
		if a := st.Outputs["artifact"]; a != "" {
			x.Artifact = a
		}
		if st.URL != "" {
			x.URL = st.URL
		}
		if st.Phase == model.DeployLive {
			x.SetStatus(model.DeployLive, now)
		} else {
			x.SetStatus(model.DeployFailed, now)
		}
		return nil
	})
	if err != nil || d.Status != model.DeployLive {
		return
	}
	if d.URL == "" {
		if p, err := s.deployProject(d.ProjectID); err == nil {
			if e, ok := p.Environment(d.Environment); ok && e.URL != "" {
				_, _ = s.Store.UpdateDeploy(id, func(x *model.Deploy) error { x.URL = e.URL; return nil })
			}
		}
	}
	for _, old := range s.Store.ListDeploys(d.ProjectID, d.Environment) {
		if old.ID != d.ID && old.Status == model.DeployLive {
			_, _ = s.Store.UpdateDeploy(old.ID, func(x *model.Deploy) error {
				x.SetStatus(model.DeploySuperseded, now)
				return nil
			})
		}
	}
	slog.Info("deploy: live", "project", d.ProjectID, "environment", d.Environment, "id", d.ID, "commit", d.Commit)
}

// stopDeploy stops running deploy d where it runs; following it records
// that it failed.
func (s *Server) stopDeploy(ctx context.Context, d model.Deploy) error {
	if !s.hub() {
		return s.deployRuns.Stop(d.ID)
	}
	run, ok := s.fleet().Get(d.RunnerID)
	if !ok {
		return errNoRunnerOnline
	}
	return s.deployCall(ctx, deployWhere{run: run, by: d.By}, http.MethodDelete, deployPath(d.ProjectID, url.PathEscape(d.ID)), nil, nil)
}

// ---- where it runs: a runner, or a Dash that isn't a hub ----

// deployRoot is where deploys' checkouts are kept: <root>/<project>/<environment>.
func (s *Server) deployRoot() string {
	if s.DeployRoot != "" {
		return s.DeployRoot
	}
	return filepath.Join(os.TempDir(), "dash-deploy")
}

// envDir is where this computer keeps environments' secrets files.
func (s *Server) envDir() string {
	if s.EnvDir != "" {
		return s.EnvDir
	}
	return deploy.EnvDir()
}

// EnvFiles lists the environments this computer has a secrets file for
// (what a runner reports to its hub).
func (s *Server) EnvFiles() []string { return deploy.EnvFiles(s.envDir()) }

// deployLocks serialise preparing an environment's checkout here.
var deployLocks sync.Map // "<project id>/<environment>" → *sync.Mutex

// maxShipped bounds the commits a deploy looks through for the tasks it ships.
const maxShipped = 1000

// startDeployHere starts deploy req of p (this computer's project) to one
// of its environments: in a checkout of the commit kept for the
// environment, with its secrets file's variables. shared: for another
// member than this runner's owner (their environment hasn't its login).
func (s *Server) startDeployHere(ctx context.Context, p model.Project, req deployRun, shared bool) (deployStarted, error) {
	var out deployStarted
	if err := localrun.Check("deploy"); err != nil {
		return out, err
	}
	e, ok := p.Environment(req.Environment)
	if !ok {
		return out, fmt.Errorf("%w: this project has no environment %q here (it is linked again with the hub's settings soon)", errBadRequest, req.Environment)
	}
	if why := p.DeployHeld(e.Name); why != "" {
		return out, fmt.Errorf("%w: %s", errForbidden, why)
	}
	if e.DeployCommand == "" {
		return out, fmt.Errorf("%w: the %s environment has no deploy command", errBadRequest, e.Name)
	}
	for _, c := range []string{req.Commit, req.PrevCommit} {
		if c != "" && gitops.CheckRev(c) != nil {
			return out, fmt.Errorf("%w: %q isn't a commit", errBadRequest, c)
		}
	}
	key := p.ID + "/" + e.Name
	mu, _ := deployLocks.LoadOrStore(key, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	if prev, ok := s.deployStarts.Load(req.ID); ok {
		// Asked again (the hub stopped waiting): it runs already.
		return prev.(deployStarted), nil
	}

	// What it deploys: the commit asked for, else the tip of the default
	// branch on origin, fetched now.
	ref, warn := s.fetchMain(ctx, p)
	out.Warning = warn
	if req.Commit != "" {
		ref = req.Commit
	}
	c, err := s.Git.CommitOf(ctx, p.ID, p.Path, ref)
	if err != nil {
		return out, fmt.Errorf("%w: no commit %s in %s here", errBadRequest, ref, p.Name)
	}
	out.Commit, out.Subject = c.SHA, c.Subject

	// Its checkout, kept between deploys so ignored files (dependencies,
	// build caches) stay.
	dir := filepath.Join(s.deployRoot(), p.ID, e.Name)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return out, err
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if err := s.Git.CheckoutDetached(ctx, p.ID, dir, c.SHA); err != nil {
			s.Git.RemoveDetachedWorktree(ctx, p.ID, p.Path, dir)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		_ = os.RemoveAll(dir)
		s.Git.PruneWorktrees(ctx, p.ID, p.Path)
		if err := s.Git.AddDetachedWorktree(ctx, p.ID, p.Path, dir, c.SHA); err != nil {
			return out, fmt.Errorf("check out %s for the deploy: %w", c.Short, err)
		}
	}

	// What it ships: the commits since the previous deploy.
	if req.PrevCommit != "" && req.PrevCommit != c.SHA {
		if cs, err := s.Git.CommitsBetween(ctx, p.ID, p.Path, req.PrevCommit, c.SHA, time.Time{}); err == nil {
			out.Commits = len(cs)
			for i, rc := range cs {
				if i >= maxShipped {
					break
				}
				out.Todos = append(out.Todos, rc.Todos...)
			}
		}
	}

	secrets, err := deploy.ReadEnvFile(s.envDir(), e.Name)
	if err != nil {
		return out, fmt.Errorf("%w: the %s env file: %v", errBadRequest, e.Name, err)
	}
	out.EnvFile = slices.Contains(deploy.EnvFiles(s.envDir()), e.Name)
	base := localrun.Environ()
	if shared {
		base = s.sharedEnv()
	}
	vars := []string{
		model.DeployVarEnv + "=" + e.Name,
		model.DeployVarCommit + "=" + c.SHA,
		model.DeployVarPrev + "=" + req.PrevCommit,
		model.DeployVarArtifact + "=" + req.Artifact,
		model.DeployVarID + "=" + req.ID,
	}
	if req.Rollback {
		vars = append(vars, model.DeployVarRollback+"=1")
	}
	env := append(append(base, secrets...), vars...)
	job := deploy.Job{ID: req.ID, Dir: dir, Build: e.BuildCommand, Deploy: e.DeployCommand, Env: env, Secrets: secrets, URL: e.URL}
	// It runs on, whoever asked; Dash stopping stops it.
	if err := s.deployRuns.Start(context.WithoutCancel(ctx), key, job); err != nil {
		if errors.Is(err, deploy.ErrBusy) {
			return out, fmt.Errorf("%w: %v", errConflict, err)
		}
		return out, err
	}
	s.deployStarts.Store(req.ID, out)
	slog.Info("deploy: running", "project", p.Name, "environment", e.Name, "commit", c.Short, "dir", dir)
	return out, nil
}

// commitPair asks how many commits Head has that Base hasn't.
type commitPair struct {
	Base string `json:"base"`
	Head string `json:"head"`
}

// compareHere counts each pair in p's clone here (-1: it couldn't).
func (s *Server) compareHere(ctx context.Context, p model.Project, pairs []commitPair) []int {
	out := make([]int, len(pairs))
	for i, c := range pairs {
		n, err := s.Git.CountBetween(ctx, p.ID, p.Path, c.Base, c.Head)
		if err != nil {
			n = -1
		}
		out[i] = n
	}
	return out
}

// deployHubProject is the runner's clone of the hub's project {hpid}.
func (s *Server) deployHubProject(w http.ResponseWriter, r *http.Request, what string) (*service, model.Project, bool) {
	sv, ok := s.fromHub(w, r)
	if !ok {
		return nil, model.Project{}, false
	}
	p, ok := sv.hubLinked(w, r.PathValue("hpid"), what)
	return sv, p, ok
}

// hubDeployStart handles POST /api/hub/projects/{hpid}/deploys on a
// runner: a deployRun, answered with deployStarted.
func (s *Server) hubDeployStart(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.deployHubProject(w, r, "a deploy")
	if !ok {
		return
	}
	var req deployRun
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.ID == "" {
		writeErr(w, fmt.Errorf("%w: no deploy id", errBadRequest))
		return
	}
	// For another member than this runner's owner: only on a shared
	// runner, without its owner's login (hubshared.go).
	shared, err := s.requestShared(r, "runs a deploy")
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	out, err := s.startDeployHere(r.Context(), p, req, shared)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// hubDeployState handles GET /api/hub/projects/{hpid}/deploys/{id}: how
// the deploy goes (deploy.State), its secrets masked.
func (s *Server) hubDeployState(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.deployHubProject(w, r, ""); !ok {
		return
	}
	st, err := s.deployRuns.State(r.PathValue("id"))
	if err != nil {
		writeErr(w, fmt.Errorf("%w: %v", store.ErrNotFound, err))
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// hubDeployStop handles DELETE /api/hub/projects/{hpid}/deploys/{id}.
func (s *Server) hubDeployStop(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.deployHubProject(w, r, "stopping a deploy"); !ok {
		return
	}
	if err := s.deployRuns.Stop(r.PathValue("id")); err != nil {
		writeErr(w, fmt.Errorf("%w: %v", store.ErrNotFound, err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// hubDeployCompare handles POST /api/hub/projects/{hpid}/deploys/compare
// [{"base", "head"}]: for each pair, the commits head has that base
// hasn't (-1 when that can't be counted here).
func (s *Server) hubDeployCompare(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.deployHubProject(w, r, "")
	if !ok {
		return
	}
	var pairs []commitPair
	if err := decode(r, &pairs); err != nil {
		writeErr(w, err)
		return
	}
	if len(pairs) > model.MaxEnvironments*model.MaxEnvironments {
		writeErr(w, fmt.Errorf("%w: too many pairs", errBadRequest))
		return
	}
	writeJSON(w, http.StatusOK, s.compareHere(r.Context(), p, pairs))
}

// deployProject is project id, whatever its org (for the deploy queue).
func (s *Server) deployProject(id string) (model.Project, error) {
	org, ok := s.Store.ProjectOrg(id)
	if !ok {
		return model.Project{}, store.ErrNotFound
	}
	return s.Store.ForOrg(org).GetProject(id)
}
