package api

// Deploy triggers: an environment may deploy on its own, besides someone
// pressing Deploy (model.Environment.Trigger): each merge to the default
// branch (a task merged, here or on GitHub), or each release tag Dash
// pushes. A triggered deploy goes through what a manual one does: the
// queue, approvals, requirements and freezes. It is recorded with what
// started it (Trigger, TriggerRef: the task or the tag) and who, when
// someone did (the one who tagged; a merge is Dash's).
//
// Only the Dash that keeps the deploy records triggers: a hub, or a Dash
// that isn't a runner of one (never a runner, for its hub's projects).

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/model"
)

// mergeWatch remembers which tasks were merged already, so only a task
// that becomes merged triggers deploys (once).
type mergeWatch struct {
	mu     sync.Mutex
	merged map[string]bool
	primed bool
}

// primeMerges notes the tasks merged before Dash started: they deploy
// nothing now.
func (s *Server) primeMerges() {
	w := &s.merged
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.merged == nil {
		w.merged = map[string]bool{}
	}
	for _, t := range s.Store.ListTodos("", model.StatusMerged) {
		w.merged[t.ID] = true
	}
	w.primed = true
}

// ObserveDeploys is fed every store event: a task newly merged deploys to
// the environments of its project that deploy on merge. The store
// publishes holding its lock, so the deploys are queued from another
// goroutine.
func (s *Server) ObserveDeploys(ev model.Event) {
	if ev.Type != model.EvTodoUpdated {
		return
	}
	t, ok := ev.Data.(model.Todo)
	if !ok || t.Status != model.StatusMerged || t.MergedCommit == "" {
		return
	}
	w := &s.merged
	w.mu.Lock()
	seen := w.merged[t.ID] || !w.primed
	if w.merged == nil {
		w.merged = map[string]bool{}
	}
	w.merged[t.ID] = true
	w.mu.Unlock()
	if seen {
		return
	}
	go s.deployMerge(t)
}

// deployMerge queues the deploys merged task t triggers.
func (s *Server) deployMerge(t model.Todo) {
	p, err := s.deployProject(t.ProjectID)
	if err != nil || p.LinkedFromHub() {
		return
	}
	for _, e := range p.Environments {
		if e.DeploysOnMerge() {
			s.triggerDeploy(p, e, model.DeployByMerge, t.MergedCommit, t.ID, Caller{Org: p.OrgID})
		}
	}
}

// deployRelease queues the deploys a release tag of p at sha, pushed for
// c, triggers.
func (s *Server) deployRelease(p model.Project, tag, sha string, c Caller) {
	if p.LinkedFromHub() || tag == "" || sha == "" {
		return
	}
	for _, e := range p.Environments {
		if e.DeploysOnRelease() {
			s.triggerDeploy(p, e, model.DeployByRelease, sha, tag, c)
		}
	}
}

// triggerDeploy queues a deploy of commit to e of p, started by trigger
// (ref: the task merged, the tag) for c (Dash itself when c has no user).
// A merge's deploy still waiting to start (queued, or awaiting an
// approval) is moved on to the newer commit instead of queueing another.
// One that a freeze or a requirement blocks is recorded failed, with the
// reason, so the Deploys page shows that it didn't go.
func (s *Server) triggerDeploy(p model.Project, e model.Environment, trigger, commit, ref string, c Caller) {
	c.Org = p.OrgID
	c.Agent = false
	if trigger == model.DeployByMerge {
		for _, d := range s.Store.ListDeploys(p.ID, e.Name) {
			if d.Trigger != model.DeployByMerge || d.Status != model.DeployQueued && d.Status != model.DeployAwaiting || d.Approval != nil {
				continue
			}
			if _, err := s.Store.UpdateDeploy(d.ID, func(x *model.Deploy) error {
				if x.Status != model.DeployQueued && x.Status != model.DeployAwaiting {
					return errUnchanged // it started meanwhile
				}
				x.Ref, x.TriggerRef = commit, ref
				return nil
			}); err == nil {
				slog.Info("deploy: merge trigger moved on", "project", p.Name, "environment", e.Name, "id", d.ID, "commit", commit)
				return
			}
		}
	}
	d, err := s.as(c).requestDeploy(p, deployAsk{Environment: e.Name, Commit: commit, Trigger: trigger, TriggerRef: ref})
	if err == nil {
		slog.Info("deploy: triggered", "project", p.Name, "environment", e.Name, "trigger", trigger, "ref", ref, "id", d.ID)
		return
	}
	why, blocked := strings.CutPrefix(err.Error(), errConflict.Error()+": blocked: ")
	if !blocked {
		slog.Warn("deploy: trigger", "project", p.Name, "environment", e.Name, "trigger", trigger, "ref", ref, "err", err)
		return
	}
	// Recorded as the queue records a deploy blocked at its turn: failed,
	// with the reason (and so notified like one). Made awaiting, which the
	// queue never runs, then failed at once.
	d, err = s.Store.CreateDeploy(model.Deploy{ProjectID: p.ID, Environment: e.Name, Ref: commit, Trigger: trigger, TriggerRef: ref,
		By: c.User, ByName: s.username(c.User), Status: model.DeployAwaiting})
	if err != nil {
		return
	}
	_, _ = s.Store.UpdateDeploy(d.ID, func(x *model.Deploy) error {
		x.Error = "blocked: " + why
		x.SetStatus(model.DeployFailed, time.Now())
		return nil
	})
	slog.Info("deploy: triggered, blocked", "project", p.Name, "environment", e.Name, "trigger", trigger, "ref", ref, "why", why)
}

// username is user id's name ("" for none, or Dash itself).
func (s *Server) username(id int64) string {
	if id == 0 || s.Store == nil {
		return ""
	}
	if u, err := s.Store.Accounts().User(id); err == nil {
		return u.Username
	}
	return ""
}
