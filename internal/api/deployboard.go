package api

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
)

// The deploy tools (mcptools.Deployer) on the board. The board's caller is
// an agent (Caller.Agent): mayDeploy lets it deploy, roll back and promote
// only where members may, and decide never lets it approve.

// deployRow is d as the deploy tools show it.
func deployRow(d model.Deploy) mcptools.DeployRow {
	r := mcptools.DeployRow{ID: d.ID, Environment: d.Environment, Status: string(d.Status), Commit: d.Commit, Tag: d.Tag, Subject: d.Subject,
		Version: d.Version, Trigger: d.Trigger, TriggerRef: d.TriggerRef, By: d.ByName, Rollback: d.Rollback, PromotedEnv: d.PromotedEnv,
		Error: d.Error, CreatedAt: d.CreatedAt}
	if d.Status == model.DeployAwaiting {
		r.Needs = d.NeedsApproval
	}
	if t := d.LiveSince(); !t.IsZero() {
		r.LiveSince = &t
	}
	if r.Commit == "" {
		r.Commit = d.Ref
	}
	for _, s := range d.Shipped {
		if s.Kind == model.LinkTask {
			r.Shipped = append(r.Shipped, s.Title)
		}
	}
	return r
}

// agentRef is what an agent's deploy records as asking for it: its task,
// else the chat.
func agentRef(todo string) string {
	if todo == "" {
		return "chat"
	}
	return todo
}

func (b board) deployProject(projectID string) (model.Project, error) {
	p, err := b.sv.data.GetProject(projectID)
	if err != nil {
		return p, fmt.Errorf("no project %q (see list_projects)", projectID)
	}
	return p, nil
}

func (b board) Environments(projectID string) ([]mcptools.EnvRow, error) {
	p, err := b.deployProject(projectID)
	if err != nil {
		return nil, err
	}
	s, now := b.sv.s, time.Now()
	out := []mcptools.EnvRow{}
	for i, e := range p.Environments {
		deploys := b.sv.data.ListDeploys(p.ID, e.Name)
		row := mcptools.EnvRow{Name: e.Name, Trigger: e.WithDefaults().Trigger, ReleaseTagsOnly: e.ReleaseTagsOnly, WaitForBuild: e.WaitsForBuild(), WhoDeploys: e.WhoDeploys(), NeedsApproval: e.WhoApproves(),
			AgentMayDeploy: s.mayDeploy(b.sv.c, e) == nil}
		var live *model.Deploy
		for _, d := range deploys {
			switch {
			case d.Status == model.DeployLive && live == nil:
				live = &d
				lr := deployRow(d)
				row.Live = &lr
			case d.Status.Active() && row.Running == "":
				row.Running = d.ID
			case d.Status == model.DeployQueued:
				row.Queued++
			}
		}
		pol := s.policyFor(b.sv.c, p, i, deploys, live, now)
		row.Needs, row.Blocked, row.PromoteTo = pol.Needs, pol.Blocked, pol.PromoteTo
		if rb := rollbackFor(deploys); rb != nil {
			row.RollbackTo = rb.To.Commit
		}
		out = append(out, row)
	}
	return out, nil
}

func (b board) Deploys(projectID, env string, limit int) ([]mcptools.DeployRow, error) {
	p, err := b.deployProject(projectID)
	if err != nil {
		return nil, err
	}
	if env != "" {
		if _, ok := p.Environment(env); !ok {
			return nil, fmt.Errorf("the project has no environment %q (see list_deploys)", env)
		}
	}
	out := []mcptools.DeployRow{}
	for _, d := range b.sv.data.ListDeploys(p.ID, env) {
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, deployRow(d))
	}
	return out, nil
}

func (b board) Deploy(projectID string, r mcptools.DeployRequest) (mcptools.DeployRow, error) {
	if err := b.sv.writable(); err != nil {
		return mcptools.DeployRow{}, err
	}
	p, err := b.deployProject(projectID)
	if err != nil {
		return mcptools.DeployRow{}, err
	}
	d, err := b.sv.requestDeploy(p, deployAsk{Environment: r.Environment, Commit: r.Commit, Trigger: model.DeployByAgent, TriggerRef: agentRef(r.For)})
	return deployRow(d), err
}

func (b board) Rollback(projectID, env, forTodo string) (mcptools.DeployRow, error) {
	if err := b.sv.writable(); err != nil {
		return mcptools.DeployRow{}, err
	}
	p, err := b.deployProject(projectID)
	if err != nil {
		return mcptools.DeployRow{}, err
	}
	e, ok := p.Environment(env)
	if !ok {
		return mcptools.DeployRow{}, fmt.Errorf("the project has no environment %q (see list_deploys)", env)
	}
	if err := b.sv.s.mayDeploy(b.sv.c, e); err != nil {
		return mcptools.DeployRow{}, err
	}
	bad, ok := model.RollbackFrom(b.sv.data.ListDeploys(p.ID, e.Name))
	if !ok {
		return mcptools.DeployRow{}, fmt.Errorf("%s runs nothing Dash deployed: there is nothing to roll back", e.Name)
	}
	d, err := b.sv.requestRollback(p, bad, model.DeployByAgent, agentRef(forTodo))
	return deployRow(d), err
}

func (b board) Promote(projectID, from, to, forTodo string) (mcptools.DeployRow, error) {
	if err := b.sv.writable(); err != nil {
		return mcptools.DeployRow{}, err
	}
	p, err := b.deployProject(projectID)
	if err != nil {
		return mcptools.DeployRow{}, err
	}
	i := slices.IndexFunc(p.Environments, func(e model.Environment) bool { return e.Name == strings.TrimSpace(from) })
	if i < 0 {
		return mcptools.DeployRow{}, fmt.Errorf("the project has no environment %q (see list_deploys)", from)
	}
	if to == "" {
		if to = promoteTarget(p.Environments, i); to == "" {
			return mcptools.DeployRow{}, fmt.Errorf("nothing is promoted from %s by default: name the environment to promote to", from)
		}
	}
	d, err := b.sv.requestDeploy(p, deployAsk{Environment: to, PromoteFrom: p.Environments[i].Name, Trigger: model.DeployByAgent, TriggerRef: agentRef(forTodo)})
	return deployRow(d), err
}

// ---- a project's run: its own project's deploys only ----

func (pb projectBoard) Environments(projectID string) ([]mcptools.EnvRow, error) {
	if err := pb.check(projectID); err != nil {
		return nil, err
	}
	return pb.b.Environments(pb.project)
}

func (pb projectBoard) Deploys(projectID, env string, limit int) ([]mcptools.DeployRow, error) {
	if err := pb.check(projectID); err != nil {
		return nil, err
	}
	return pb.b.Deploys(pb.project, env, limit)
}

func (pb projectBoard) Deploy(projectID string, r mcptools.DeployRequest) (mcptools.DeployRow, error) {
	if err := pb.check(projectID); err != nil {
		return mcptools.DeployRow{}, err
	}
	return pb.b.Deploy(pb.project, r)
}

func (pb projectBoard) Rollback(projectID, env, forTodo string) (mcptools.DeployRow, error) {
	if err := pb.check(projectID); err != nil {
		return mcptools.DeployRow{}, err
	}
	return pb.b.Rollback(pb.project, env, forTodo)
}

func (pb projectBoard) Promote(projectID, from, to, forTodo string) (mcptools.DeployRow, error) {
	if err := pb.check(projectID); err != nil {
		return mcptools.DeployRow{}, err
	}
	return pb.b.Promote(pb.project, from, to, forTodo)
}
