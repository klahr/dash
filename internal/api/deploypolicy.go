package api

// Deploy policy: who may deploy to an environment, approve its deploys and
// change its settings (the org roles, store.Role: owner > admin > member;
// a planner none of it), what a deploy needs first (passing tests, being
// live on another environment), its freeze windows, and the history of
// its settings. See model/deploypolicy.go for the settings themselves.

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// orgRole is c's role in their org. Dash itself (no user) and the hub
// calling a runner act with every right: the hub checked its user's.
func (s *Server) orgRole(c Caller) store.Role {
	if c.Machine || c.User == 0 {
		return store.RoleOwner
	}
	if c.Planner {
		return store.RolePlanner
	}
	m, err := s.Store.Accounts().Membership(c.User, c.Org)
	if err != nil {
		return ""
	}
	return m.Role
}

// hasRole reports whether c's role is at least role (a model.Role…).
func (s *Server) hasRole(c Caller, role string) bool {
	r := s.orgRole(c)
	return !r.Planner() && r.AtLeast(store.Role(role))
}

// roleWord names role for a message: "an admin (or owner)".
func roleWord(role string) string {
	switch role {
	case model.RoleOwner:
		return "an owner"
	case model.RoleAdmin:
		return "an admin (or owner)"
	}
	return "a member (or admin)"
}

// mayDeploy says why c may not deploy to e, roll back included ("": they may).
func (s *Server) mayDeploy(c Caller, e model.Environment) error {
	if s.hasRole(c, e.WhoDeploys()) {
		return nil
	}
	return fmt.Errorf("%w: only %s of the org may deploy to %s", errForbidden, roleWord(e.WhoDeploys()), e.Name)
}

// checkEnvSettings refuses a change from cur to next environments that c's
// role doesn't allow: changing or removing an environment needs the role
// its "who may change settings" names (a new one's own), and no role an
// environment names may be above c's.
func (s *Server) checkEnvSettings(c Caller, cur, next []model.Environment) error {
	mine := s.orgRole(c)
	for _, ch := range model.DiffEnvironments(cur, next) {
		need := ""
		if e, ok := envNamed(cur, ch.Environment); ok {
			need = e.WhoChangesSettings()
		} else if e, ok := envNamed(next, ch.Environment); ok {
			need = e.WithDefaults().WhoChangesSettings()
		}
		if !s.hasRole(c, need) {
			return fmt.Errorf("%w: only %s of the org may change the settings of %s", errForbidden, roleWord(need), ch.Environment)
		}
		e, ok := envNamed(next, ch.Environment)
		if !ok {
			continue
		}
		for _, r := range []string{e.WhoDeploys(), e.WhoApproves(), e.WhoChangesSettings()} {
			if r != "" && !mine.AtLeast(store.Role(r)) {
				return fmt.Errorf("%w: %s can't name a role above your own (%s)", errForbidden, ch.Environment, mine)
			}
		}
	}
	return nil
}

func envNamed(envs []model.Environment, name string) (model.Environment, bool) {
	for _, e := range envs {
		if e.Name == name {
			return e, true
		}
	}
	return model.Environment{}, false
}

// recordEnvChanges keeps, as the settings history, what changed from
// before to after, by c.
func (s *Server) recordEnvChanges(c Caller, projectID string, before, after []model.Environment) {
	changes := model.DiffEnvironments(before, after)
	if len(changes) == 0 {
		return
	}
	name := ""
	if c.User != 0 {
		if u, err := s.Store.Accounts().User(c.User); err == nil {
			name = u.Username
		}
	}
	if err := s.Store.AddEnvChanges(projectID, c.author(), name, time.Now(), changes); err != nil {
		slog.Error("deploy: record environment changes", "project", projectID, "err", err)
	}
}

// envHistory handles GET /api/projects/{pid}/environments/history: the
// latest changes to the project's environments, newest first.
func (s *Server) envHistory(w http.ResponseWriter, r *http.Request) {
	sv, p, ok := s.project(w, r)
	if !ok {
		return
	}
	out, err := sv.data.EnvChanges(p.ID, 200)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- what a deploy needs ----

// sameCommit reports whether full is the commit ref names (a full sha, or
// one of at least 7 characters).
func sameCommit(full, ref string) bool {
	return full != "" && len(ref) >= 7 && strings.HasPrefix(full, ref)
}

// testsKnown reports whether the project's tests are known to have passed
// on commit ref: a deploy of it ran them (or knew), or the project's CI
// passed on it.
func (s *Server) testsKnown(p model.Project, ref string) bool {
	if ref == "" {
		return false
	}
	for _, d := range s.Store.ListDeploys(p.ID, "") {
		if d.TestsPassed && sameCommit(d.Commit, ref) {
			return true
		}
	}
	return s.CI != nil && s.CI.Status(p.ID).PassedOn(ref)
}

// deployBlocks say why a deploy of ref ("": the branch's tip) to e of p
// may not run at now (none: it may). Rollbacks skip them.
func (s *Server) deployBlocks(p model.Project, e model.Environment, ref string, now time.Time) []string {
	var out []string
	if why := e.FreezeReason(now); why != "" {
		out = append(out, why)
	}
	if src := e.RequireLiveOn; src != "" {
		switch {
		case ref == "":
			out = append(out, fmt.Sprintf("%s takes only commits that were live on %s: promote %s's, or pick one", e.Name, src, src))
		case !s.wentLiveOn(p, src, ref):
			out = append(out, fmt.Sprintf("%s has not been live on %s, which %s needs first", gitops.ShortSHA(ref), src, e.Name))
		}
	}
	if e.RequireTests && strings.TrimSpace(p.TestCommand) == "" && !s.testsKnown(p, ref) {
		out = append(out, fmt.Sprintf("%s needs passing tests, and the project has no test command (Project settings → Tests) and no passing CI run of the commit", e.Name))
	}
	return out
}

// wentLiveOn reports whether commit ref was ever live on p's environment env.
func (s *Server) wentLiveOn(p model.Project, env, ref string) bool {
	return slices.ContainsFunc(s.Store.ListDeploys(p.ID, env), func(d model.Deploy) bool { return d.WentLive() && sameCommit(d.Commit, ref) })
}

// blockedErr is the error for a deploy blocked for whys.
func blockedErr(whys []string) error {
	return fmt.Errorf("%w: blocked: %s", errConflict, strings.Join(whys, "; "))
}

// ---- approvals ----

// approveDeploy handles POST /api/deploys/{id}/approve: a deploy awaiting
// approval is queued. The approver needs the environment's approve role,
// and may not approve their own deploy unless the org has one admin.
func (s *Server) approveDeploy(w http.ResponseWriter, r *http.Request) { s.decideDeploy(w, r, true) }

// rejectDeploy handles POST /api/deploys/{id}/reject {"note"}.
func (s *Server) rejectDeploy(w http.ResponseWriter, r *http.Request) { s.decideDeploy(w, r, false) }

func (s *Server) decideDeploy(w http.ResponseWriter, r *http.Request, approve bool) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	if r.ContentLength > 0 {
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
	}
	d, err := sv.data.GetDeploy(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	p, err := sv.data.GetProject(d.ProjectID)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.decide(sv.c, p, d, approve, strings.TrimSpace(req.Note))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// decide approves (or rejects) deploy d of p for c.
func (s *Server) decide(c Caller, p model.Project, d model.Deploy, approve bool, note string) (model.Deploy, error) {
	if d.Status != model.DeployAwaiting {
		return d, fmt.Errorf("%w: it isn't waiting for an approval", errConflict)
	}
	e, ok := p.Environment(d.Environment)
	if !ok {
		return d, fmt.Errorf("%w: the project no longer has the environment %s", errConflict, d.Environment)
	}
	need := d.NeedsApproval
	if need == "" {
		need = e.WhoApproves()
	}
	if need != "" && !s.hasRole(c, need) {
		return d, fmt.Errorf("%w: only %s of the org may approve deploys to %s", errForbidden, roleWord(need), e.Name)
	}
	if approve && d.By != 0 && d.By == c.User && s.orgAdmins(c.Org) > 1 {
		return d, fmt.Errorf("%w: someone else must approve your own deploy (the org has more than one admin)", errForbidden)
	}
	if approve {
		if whys := s.deployBlocks(p, e, d.Ref, time.Now()); len(whys) > 0 {
			return d, blockedErr(whys)
		}
	}
	if len(note) > 500 {
		note = note[:500]
	}
	a := model.DeployApproval{Approved: approve, By: c.author(), At: time.Now(), Note: note}
	if u, err := s.Store.Accounts().User(c.User); err == nil {
		a.ByName = u.Username
	}
	out, err := s.Store.UpdateDeploy(d.ID, func(x *model.Deploy) error {
		if x.Status != model.DeployAwaiting {
			return fmt.Errorf("%w: someone decided meanwhile", errConflict)
		}
		x.Approval = &a
		if approve {
			x.SetStatus(model.DeployQueued, a.At)
		} else {
			x.Error = "rejected by " + a.ByName
			if a.ByName == "" {
				x.Error = "rejected"
			}
			if note != "" {
				x.Error += ": " + note
			}
			x.SetStatus(model.DeployRejected, a.At)
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	slog.Info("deploy: decided", "project", p.Name, "environment", d.Environment, "id", d.ID, "approved", approve, "by", a.ByName)
	if approve {
		s.kickDeploys(d.ProjectID, d.Environment)
	}
	return out, nil
}

// orgAdmins counts the org's admins and owners.
func (s *Server) orgAdmins(org int64) int {
	ms, err := s.Store.Accounts().Members(org)
	if err != nil {
		return 0
	}
	n := 0
	for _, m := range ms {
		if m.Role.AtLeast(store.RoleAdmin) {
			n++
		}
	}
	return n
}

// envPolicy is, on the Deploys page, what an environment's policy means
// for the one who looks: whether they may deploy and approve, what a
// deploy needs, why it is blocked now, and where Promote sends what is
// live here.
type envPolicy struct {
	CanDeploy  bool `json:"can_deploy"`
	CanApprove bool `json:"can_approve"`
	// Needs lists what a deploy needs, in words ("approval from an admin").
	Needs []string `json:"needs,omitempty"`
	// Frozen says why nothing deploys now, if so.
	Frozen string `json:"frozen,omitempty"`
	// Blocked says why a deploy of the branch's tip would be refused now.
	Blocked []string `json:"blocked,omitempty"`
	// PromoteTo is the environment Promote deploys what is live here to:
	// the one that needs it live here first, else the next one.
	PromoteTo string `json:"promote_to,omitempty"`
	// PromoteBlocked says why that promotion would be refused now.
	PromoteBlocked []string `json:"promote_blocked,omitempty"`
	// Awaiting are the deploys waiting for an approval.
	Awaiting []model.Deploy `json:"awaiting,omitempty"`
}

// policyFor fills in what e's policy means for c, at now; deploys are e's
// (newest first), live what is live there.
func (s *Server) policyFor(c Caller, p model.Project, i int, deploys []model.Deploy, live *model.Deploy, now time.Time) envPolicy {
	e := p.Environments[i]
	out := envPolicy{CanDeploy: s.mayDeploy(c, e) == nil, Frozen: e.FreezeReason(now), Blocked: s.deployBlocks(p, e, "", now)}
	if r := e.WhoApproves(); r != "" {
		out.CanApprove = s.hasRole(c, r)
		out.Needs = append(out.Needs, "approval from "+roleWord(r))
	}
	if e.RequireTests {
		out.Needs = append(out.Needs, "passing tests")
	}
	if e.RequireLiveOn != "" {
		out.Needs = append(out.Needs, "live on "+e.RequireLiveOn+" first")
	}
	for _, d := range deploys {
		if d.Status == model.DeployAwaiting {
			out.Awaiting = append(out.Awaiting, d)
		}
	}
	out.PromoteTo = promoteTarget(p.Environments, i)
	if live != nil && out.PromoteTo != "" {
		to, _ := p.Environment(out.PromoteTo)
		if err := s.mayDeploy(c, to); err != nil {
			out.PromoteBlocked = append(out.PromoteBlocked, strings.TrimPrefix(err.Error(), errForbidden.Error()+": "))
		}
		out.PromoteBlocked = append(out.PromoteBlocked, s.deployBlocks(p, to, live.Commit, now)...)
	}
	return out
}

// promoteTarget is where Promote sends what is live on envs[i]: the first
// environment that needs it live there first, else the next one ("" for
// the last, and for production: nothing goes on from there).
func promoteTarget(envs []model.Environment, i int) string {
	if envs[i].Production() {
		return ""
	}
	for _, e := range envs {
		if e.RequireLiveOn == envs[i].Name {
			return e.Name
		}
	}
	if i+1 < len(envs) && envs[i+1].RequireLiveOn == "" {
		return envs[i+1].Name
	}
	return ""
}
