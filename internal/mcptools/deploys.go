package mcptools

import (
	"errors"
	"time"
)

// The deploy tools: a project's environments (docs/deploys.md), what is
// live on each, and deploys, rollbacks and promotions. Dash checks every
// call as the run's user, and more: Claude may deploy, roll back or
// promote only to an environment whose "Who may deploy" is member, never
// approve a deploy, and its deploys wait for an approval, and are held by
// freezes and requirements, like anyone's. A chat asks the user before it
// deploys, rolls back or promotes (AskTools).

// EnvRow is an environment as list_deploys shows it.
type EnvRow struct {
	Name string `json:"name"`
	// Live is what runs there now, if anything.
	Live *DeployRow `json:"live,omitempty"`
	// Trigger: manual, merge (each merge to the default branch) or release
	// (each release tag).
	Trigger string `json:"trigger"`
	// WhoDeploys is the lowest org role that may deploy there; Claude may
	// only where it is member (AgentMayDeploy).
	WhoDeploys     string `json:"who_may_deploy"`
	AgentMayDeploy bool   `json:"claude_may_deploy"`
	// NeedsApproval is the role that must approve a deploy before it runs
	// ("" none): Claude's deploys wait for a person.
	NeedsApproval string `json:"needs_approval,omitempty"`
	// Needs lists what a deploy needs first ("passing tests", "live on
	// staging first"); Blocked why a deploy of the branch's tip would be
	// refused now (a freeze, a requirement).
	Needs   []string `json:"needs,omitempty"`
	Blocked []string `json:"blocked,omitempty"`
	// PromoteTo is where promote sends what is live here by default.
	PromoteTo string `json:"promote_to,omitempty"`
	// RollbackTo is the commit rollback would go back to now, if any.
	RollbackTo string `json:"rollback_to,omitempty"`
	// Running and Queued are the deploys on their way.
	Running string `json:"running,omitempty"`
	Queued  int    `json:"queued,omitempty"`
}

// DeployRow is a deploy as the deploy tools show it.
type DeployRow struct {
	ID          string     `json:"id"`
	Environment string     `json:"environment"`
	Status      string     `json:"status"`
	Commit      string     `json:"commit,omitempty"`
	Subject     string     `json:"subject,omitempty"`
	Version     string     `json:"version,omitempty"`
	Trigger     string     `json:"trigger"`
	TriggerRef  string     `json:"trigger_ref,omitempty"`
	By          string     `json:"by,omitempty"`
	Rollback    bool       `json:"rollback,omitempty"`
	PromotedEnv string     `json:"promoted_from,omitempty"`
	Needs       string     `json:"needs_approval,omitempty"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	LiveSince   *time.Time `json:"live_since,omitempty"`
	// Shipped are the titles of the tasks it shipped.
	Shipped []string `json:"shipped,omitempty"`
}

// DeployRequest is a deploy Claude asks for: of Commit ("" for the tip of
// the default branch, as origin has it) to Environment. For is the task
// the run is for ("" for a chat), recorded as what started it.
type DeployRequest struct {
	Environment, Commit, For string
}

// Deployer is a Board with a project's deploys: Tools then has the deploy
// tools (see DeployTools).
type Deployer interface {
	// Environments lists a project's environments, as the run may use them.
	Environments(projectID string) ([]EnvRow, error)
	// Deploys lists a project's deploys, newest first, of one environment
	// ("" all), at most limit.
	Deploys(projectID, env string, limit int) ([]DeployRow, error)
	// Deploy queues a deploy (or one awaiting approval).
	Deploy(projectID string, r DeployRequest) (DeployRow, error)
	// Rollback rolls env back from what it runs now to the last good deploy.
	Rollback(projectID, env, forTodo string) (DeployRow, error)
	// Promote deploys what is live on from to to ("": from's next one).
	Promote(projectID, from, to, forTodo string) (DeployRow, error)
}

// DeployTools returns list_deploys, deploy, rollback and promote on d.
func DeployTools(b Board, d Deployer, o Options) []Tool {
	pProject, project := projectParam(o)
	pEnv := Param{"environment", "string", "The environment's name, e.g. staging (see list_deploys).", true}
	agentRule := " Claude may only deploy to environments whose \"Who may deploy\" is member (claude_may_deploy in list_deploys); " +
		"elsewhere, tell the user to do it on the Deploys page. Only when the user asks for it."
	return []Tool{
		{"list_deploys", "List a project's environments (what is live on each and since when, its trigger, who may deploy, " +
			"what a deploy needs and what blocks one now, where promote goes, what rollback would go back to) and its latest deploys, newest first.",
			[]Param{pProject, {"environment", "string", "Only this environment's deploys.", false},
				{"limit", "integer", "At most this many deploys (default 20).", false}},
			func(a Args) (any, error) {
				p, err := project(b, a)
				if err != nil {
					return nil, err
				}
				envs, err := d.Environments(p.ID)
				if err != nil {
					return nil, err
				}
				limit := a.Num("limit", 20)
				if limit <= 0 {
					limit = 20
				}
				ds, err := d.Deploys(p.ID, a.Str("environment"), limit)
				if err != nil {
					return nil, err
				}
				return struct {
					Environments []EnvRow    `json:"environments"`
					Deploys      []DeployRow `json:"deploys"`
				}{envs, ds}, nil
			}},
		{"deploy", "Deploy a commit of the project to one of its environments, with the environment's own scripts. " +
			"It is queued behind other deploys there; where the environment needs an approval it waits for a person to approve it (Claude can't), " +
			"and a freeze or an unmet requirement refuses it, with the reason." + agentRule,
			[]Param{pProject, pEnv, {"commit", "string", "The commit to deploy (default: the tip of the default branch, as origin has it).", false}},
			func(a Args) (any, error) {
				p, err := project(b, a)
				if err != nil {
					return nil, err
				}
				if a.Str("environment") == "" {
					return nil, errors.New("environment is required")
				}
				return d.Deploy(p.ID, DeployRequest{Environment: a.Str("environment"), Commit: a.Str("commit"), For: o.Todo})
			}},
		{"rollback", "Roll an environment back from what it runs now (the live deploy, or a newer one that failed its checks) " +
			"to its last good deploy (rollback_to in list_deploys). It goes ahead of queued deploys and needs no approval." + agentRule,
			[]Param{pProject, pEnv},
			func(a Args) (any, error) {
				p, err := project(b, a)
				if err != nil {
					return nil, err
				}
				if a.Str("environment") == "" {
					return nil, errors.New("environment is required")
				}
				return d.Rollback(p.ID, a.Str("environment"), o.Todo)
			}},
		{"promote", "Promote what is live on one environment to another: the same commit and artifact, without building again. " +
			"Like deploy, it may wait for an approval, and a freeze or an unmet requirement refuses it." + agentRule,
			[]Param{pProject, {"from", "string", "The environment whose live deploy to promote, e.g. staging.", true},
				{"to", "string", "The environment to deploy it to (default: from's promote_to in list_deploys).", false}},
			func(a Args) (any, error) {
				p, err := project(b, a)
				if err != nil {
					return nil, err
				}
				if a.Str("from") == "" {
					return nil, errors.New("from is required")
				}
				return d.Promote(p.ID, a.Str("from"), a.Str("to"), o.Todo)
			}},
	}
}
