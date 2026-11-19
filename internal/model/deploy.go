package model

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Deploys: a project's environments say where it is deployed (production,
// staging, …) and how; each deploy to one is a Deploy, kept on the hub so
// it knows what is live where. The project's own scripts still do the
// pushing: Dash runs them on a runner, tells them what they deploy (see
// DeployVars) and records what happened. Nothing Dash-specific goes in the
// repository: environments are project settings.

// Environment is a place a project is deployed to.
type Environment struct {
	// Name is how it is known: a lowercase word ("production", "staging"),
	// also the name of the runners' secrets file for it
	// (~/.dash/env/<name>.env). See ValidEnvName.
	Name string `json:"name"`
	// DeployCommand deploys: a bash script, one line or several, run in a
	// checkout of the commit it deploys.
	DeployCommand string `json:"deploy_command"`
	// BuildCommand, if any, runs first, in the same place: a deploy whose
	// build fails stops there.
	BuildCommand string `json:"build_command,omitempty"`
	// RunsOn are the runner labels (see labels.go) its deploys run on: of
	// the runners that work on the project, only those with every one of
	// them. Empty: any of them. A deploy never runs elsewhere.
	RunsOn []string `json:"runs_on,omitempty"`
	// URL is where it is reached, if anywhere: shown with what is live,
	// and checked once a deploy is done (verifying). A deploy's script may
	// report another (DeployOutputs).
	URL string `json:"url,omitempty"`

	// The checks that decide whether a deploy went live, once its deploy
	// command exited 0 ("deployed"): the health check, the verify command,
	// then the soak time. Any one failing fails the deploy.
	//
	// HealthPath is the path on the URL the health check asks for
	// ("/healthz"; "": the URL itself). HealthStatus is the status it wants
	// (0: any below 500). HealthTimeout is how long, in seconds, it keeps
	// trying while the deploy comes up (0: DefaultHealthTimeout).
	HealthPath    string `json:"health_path,omitempty"`
	HealthStatus  int    `json:"health_status,omitempty"`
	HealthTimeout int    `json:"health_timeout,omitempty"`
	// VerifyCommand, if any, runs after the health check, in the same
	// checkout: smoke tests, a bash script given the URL as DASH_URL. Exit
	// code 0 passes.
	VerifyCommand string `json:"verify_command,omitempty"`
	// Soak is how long, in seconds, a deploy that passed its checks is
	// watched before it is live: its health is checked again every so
	// often (with a URL). 0: none.
	Soak int `json:"soak,omitempty"`
	// AutoRollback rolls a deploy whose checks failed back to the last good
	// deploy at once (see RollbackTarget).
	AutoRollback bool `json:"auto_rollback,omitempty"`

	// Who may do what (see deploypolicy.go): the lowest org role that may
	// deploy (DeployRole), that must approve a deploy before it runs
	// (ApproveRole; RoleNone: no approval) and that may change these
	// settings (SettingsRole). "": the default (DefaultRoles).
	DeployRole   string `json:"deploy_role,omitempty"`
	ApproveRole  string `json:"approve_role,omitempty"`
	SettingsRole string `json:"settings_role,omitempty"`
	// RequireTests: a deploy runs only once the project's tests passed on
	// its commit (Dash runs them first when it doesn't know they did).
	RequireTests bool `json:"require_tests,omitempty"`
	// RequireLiveOn names another environment a deploy's commit must have
	// been live on first ("staging"), "" none.
	RequireLiveOn string `json:"require_live_on,omitempty"`
	// Freezes are when nothing deploys here (rollbacks still do), read in
	// TimeZone (an IANA name; "": UTC).
	Freezes  []Freeze `json:"freezes,omitempty"`
	TimeZone string   `json:"time_zone,omitempty"`
	// Trigger is what deploys here on its own besides a person pressing
	// Deploy: EnvTriggerManual (nothing), EnvTriggerMerge (each merge to
	// the default branch) or EnvTriggerRelease (each release tag). A
	// triggered deploy goes through the queue, the requirements, the
	// approvals and the freezes like any other. "": manual.
	Trigger string `json:"trigger,omitempty"`
}

// Environment.Trigger.
const (
	EnvTriggerManual  = "manual"
	EnvTriggerMerge   = "merge"
	EnvTriggerRelease = "release"
)

// DeploysOnMerge reports whether e deploys each merge to the default branch.
func (e Environment) DeploysOnMerge() bool { return e.Trigger == EnvTriggerMerge }

// DeploysOnRelease reports whether e deploys each release tag.
func (e Environment) DeploysOnRelease() bool { return e.Trigger == EnvTriggerRelease }

// Limits of an environment's checks.
const (
	DefaultHealthTimeout = 60
	MaxHealthTimeout     = 600
	MaxSoak              = 3600
)

// HasCommands reports whether e runs anything.
func (e Environment) HasCommands() bool {
	return e.DeployCommand != "" || e.BuildCommand != "" || e.VerifyCommand != ""
}

// ClearCommands empties what e runs (held on a runner that mustn't run it).
func (e *Environment) ClearCommands() { e.DeployCommand, e.BuildCommand, e.VerifyCommand = "", "", "" }

// HealthURL is what e's health check asks for at url (a deploy's, else
// e's own): url with HealthPath on it ("" without a url).
func (e Environment) HealthURL(url string) string {
	if url == "" || e.HealthPath == "" {
		return url
	}
	return strings.TrimRight(url, "/") + e.HealthPath
}

// HealthWait is how long e's health check keeps trying.
func (e Environment) HealthWait() time.Duration {
	if e.HealthTimeout <= 0 {
		return DefaultHealthTimeout * time.Second
	}
	return time.Duration(e.HealthTimeout) * time.Second
}

// DeploysOn reports whether a runner with the labels have may run e's
// deploys: e names no labels, or have has every one of them.
func (e Environment) DeploysOn(have []string) bool {
	return len(e.RunsOn) == 0 || LabelsMatch(have, e.RunsOn)
}

// DefaultEnvironment is the environment a project's single deploy command
// (from before environments) becomes.
const DefaultEnvironment = "production"

// MaxEnvironments caps a project's environments.
const MaxEnvironments = 16

var envNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ValidEnvName reports whether name may name an environment: 1 to 32 of
// a-z, 0-9, "-" and "_", starting with a letter or digit. It names a file
// on the runners, so nothing else goes.
func ValidEnvName(name string) bool { return envNameRe.MatchString(name) }

// CleanEnvironments checks a project's environments and returns them in
// their stored form: names trimmed and lowercased, scripts cleaned
// (CleanScript), labels cleaned. Names must be valid and unique.
func CleanEnvironments(envs []Environment) ([]Environment, error) {
	if len(envs) > MaxEnvironments {
		return nil, fmt.Errorf("a project has at most %d environments", MaxEnvironments)
	}
	out := make([]Environment, 0, len(envs))
	seen := map[string]bool{}
	for _, e := range envs {
		e.Name = strings.ToLower(strings.TrimSpace(e.Name))
		if !ValidEnvName(e.Name) {
			return nil, fmt.Errorf("environment name %q: use 1 to 32 lowercase letters, digits, - and _", e.Name)
		}
		if seen[e.Name] {
			return nil, fmt.Errorf("two environments are named %q", e.Name)
		}
		seen[e.Name] = true
		e.DeployCommand, e.BuildCommand, e.VerifyCommand = CleanScript(e.DeployCommand), CleanScript(e.BuildCommand), CleanScript(e.VerifyCommand)
		if e.DeployCommand == "" {
			return nil, fmt.Errorf("environment %s: give it a deploy command", e.Name)
		}
		e.URL = strings.TrimSpace(e.URL)
		if e.URL != "" && !strings.HasPrefix(e.URL, "https://") && !strings.HasPrefix(e.URL, "http://") {
			return nil, fmt.Errorf("environment %s: the URL must start with https:// or http://", e.Name)
		}
		if err := e.cleanChecks(); err != nil {
			return nil, fmt.Errorf("environment %s: %v", e.Name, err)
		}
		if err := e.cleanPolicy(); err != nil {
			return nil, fmt.Errorf("environment %s: %v", e.Name, err)
		}
		labels := make([]string, 0, len(e.RunsOn))
		for _, l := range e.RunsOn {
			if l = strings.TrimSpace(l); l != "" && !strings.EqualFold(l, LabelAny) && !slices.ContainsFunc(labels, func(x string) bool { return strings.EqualFold(x, l) }) {
				labels = append(labels, l)
			}
		}
		if len(labels) > MaxLabels {
			return nil, fmt.Errorf("environment %s: at most %d labels", e.Name, MaxLabels)
		}
		e.RunsOn = labels
		if len(e.RunsOn) == 0 {
			e.RunsOn = nil
		}
		out = append(out, e)
	}
	for _, e := range out {
		if e.RequireLiveOn == "" {
			continue
		}
		if e.RequireLiveOn == e.Name || !seen[e.RequireLiveOn] {
			return nil, fmt.Errorf("environment %s: \"live on first\" must name another of the project's environments", e.Name)
		}
	}
	return out, nil
}

// cleanChecks checks e's health check and soak settings, trimming the path.
func (e *Environment) cleanChecks() error {
	e.HealthPath = strings.TrimSpace(e.HealthPath)
	if e.HealthPath != "" {
		if e.URL == "" {
			return errors.New("a health check path needs a URL")
		}
		if !strings.HasPrefix(e.HealthPath, "/") || strings.ContainsAny(e.HealthPath, " \t\r\n#") || len(e.HealthPath) > 200 {
			return errors.New("the health check path must start with / (e.g. /healthz), with no spaces or #")
		}
	}
	if e.HealthStatus != 0 && (e.HealthStatus < 100 || e.HealthStatus > 599) {
		return errors.New("the expected status must be an HTTP status, 100 to 599")
	}
	if e.HealthTimeout < 0 || e.HealthTimeout > MaxHealthTimeout {
		return fmt.Errorf("the health check timeout must be 0 to %d seconds", MaxHealthTimeout)
	}
	if e.Soak < 0 || e.Soak > MaxSoak {
		return fmt.Errorf("the soak time must be 0 to %d seconds", MaxSoak)
	}
	return nil
}

// Environment is p's environment name, if it has one.
func (p Project) Environment(name string) (Environment, bool) {
	for _, e := range p.Environments {
		if e.Name == name {
			return e, true
		}
	}
	return Environment{}, false
}

// CloneEnvironments copies envs deeply.
func CloneEnvironments(envs []Environment) []Environment {
	if envs == nil {
		return nil
	}
	out := slices.Clone(envs)
	for i := range out {
		out[i].RunsOn = slices.Clone(out[i].RunsOn)
		out[i].Freezes = slices.Clone(out[i].Freezes)
	}
	return out
}

// DeployScripts is what CmdDeploy stands for (Project.Command): every
// environment's commands and where they run, as one text, so changing any
// of them changes "the deploy command" a runner must have approved.
func DeployScripts(envs []Environment) string {
	var b strings.Builder
	for _, e := range envs {
		if !e.HasCommands() {
			continue
		}
		fmt.Fprintf(&b, "[%s]", e.Name)
		if len(e.RunsOn) > 0 {
			fmt.Fprintf(&b, " on %s", strings.Join(e.RunsOn, ","))
		}
		b.WriteString("\n")
		if e.BuildCommand != "" {
			b.WriteString("build: " + e.BuildCommand + "\n")
		}
		b.WriteString(e.DeployCommand + "\n")
		if e.VerifyCommand != "" {
			b.WriteString("verify: " + e.VerifyCommand + "\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// MigrateDeploy moves the single deploy command of a project from before
// environments (DeployCommand, DeployRunsOn) into an environment named
// DefaultEnvironment, unless it has environments already, and clears the
// old fields. It reports whether p changed.
func (p *Project) MigrateDeploy() bool {
	if p.DeployCommand == "" && p.DeployRunsOn == nil {
		return false
	}
	if p.DeployCommand != "" && len(p.Environments) == 0 {
		p.Environments = []Environment{{Name: DefaultEnvironment, DeployCommand: p.DeployCommand, RunsOn: slices.Clone(p.DeployRunsOn)}}
	}
	p.DeployCommand, p.DeployRunsOn = "", nil
	return true
}

// HeldEnvKind is the HeldCommands key of an environment held on a runner
// for itself (its labels), as opposed to all of them (CmdDeploy).
func HeldEnvKind(env string) string { return CmdDeploy + ":" + env }

// DeployHeld is, on a runner, why p's deploys to env may not run here ("":
// they may).
func (p Project) DeployHeld(env string) string {
	if why := p.HeldCommands[CmdDeploy]; why != "" {
		return why
	}
	return p.HeldCommands[HeldEnvKind(env)]
}

// DeployStatus is where a deploy is in its life:
//
//	awaiting_approval ──▶ queued (approved), or ──▶ rejected
//
//	queued ──▶ building ──▶ deploying ──▶ verifying ──▶ live ──▶ superseded
//	   │           │            │             │           │
//	   └───────────┴────────────┴─────────────┴──▶ failed └──▶ rolled_back
//
// A deploy with no build command skips building; one with no checks (no
// URL, verify command or soak time) skips verifying. The deploy command
// exiting 0 means "deployed" (Deploy.Deployed); the checks decide "live".
type DeployStatus string

const (
	DeployQueued     DeployStatus = "queued"
	DeployBuilding   DeployStatus = "building"
	DeployDeploying  DeployStatus = "deploying"
	DeployVerifying  DeployStatus = "verifying"
	DeployLive       DeployStatus = "live"
	DeployFailed     DeployStatus = "failed"
	DeployRolledBack DeployStatus = "rolled_back" // it was live and a rollback replaced it
	DeploySuperseded DeployStatus = "superseded"  // it was live and a newer deploy replaced it
	// DeployAwaiting: its environment needs an approval before it is
	// queued (Environment.ApproveRole); DeployRejected: it was refused.
	DeployAwaiting DeployStatus = "awaiting_approval"
	DeployRejected DeployStatus = "rejected"
)

// Active reports whether a deploy in status s holds its environment: it
// is on its way (one at a time per environment; the others queue).
func (s DeployStatus) Active() bool {
	return s == DeployBuilding || s == DeployDeploying || s == DeployVerifying
}

// Done reports whether a deploy in status s has finished, one way or another.
func (s DeployStatus) Done() bool { return s != DeployQueued && s != DeployAwaiting && !s.Active() }

// What started a deploy (Deploy.Trigger).
const (
	DeployByUser    = "user"
	DeployByMerge   = "merge"
	DeployByRelease = "release"
	DeployByGoal    = "goal"
	// DeployByAutoRollback: Dash rolled back a deploy whose checks failed
	// (Environment.AutoRollback).
	DeployByAutoRollback = "auto_rollback"
	// DeployByAgent: Claude, with the deploy tools, on behalf of By
	// (TriggerRef: the task it ran for, or "chat").
	DeployByAgent = "agent"
)

// DeployPhase is when a deploy entered a status.
type DeployPhase struct {
	Status DeployStatus `json:"status"`
	At     time.Time    `json:"at"`
}

// DeployShipped is a task, or a planning card, a deploy shipped: one of
// the commits between it and the previous deploy names it.
type DeployShipped struct {
	Kind  string `json:"kind"` // LinkTask or LinkCard
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

// LinkCard is DeployShipped.Kind of a planning card.
const LinkCard = "card"

// Deploy is one deploy of a project to one of its environments.
type Deploy struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	Environment string `json:"environment"`
	// Commit is what it deploys (resolved on the runner when the deploy
	// starts, if it was asked for by branch), Subject its first line.
	Commit  string `json:"commit,omitempty"`
	Subject string `json:"subject,omitempty"`
	// Ref is what was asked for: a commit, or "" for the tip of the
	// project's branch as origin has it when the deploy starts.
	Ref string `json:"ref,omitempty"`
	// PrevCommit and PrevDeploy are the deploy that was live in the
	// environment when this one started: what it replaces.
	PrevCommit string `json:"prev_commit,omitempty"`
	PrevDeploy string `json:"prev_deploy,omitempty"`
	// Artifact and Version are what the script reported it deployed
	// (DeployOutputs), or the artifact it was given (a rollback's).
	Artifact string `json:"artifact,omitempty"`
	Version  string `json:"version,omitempty"`
	// URL is where it is reached: the script's, else the environment's.
	URL string `json:"url,omitempty"`
	// Rollback: it deploys an earlier deploy again (DASH_ROLLBACK=1):
	// RollbackTo, the last good deploy before RollbackOf, the one it
	// rolls back.
	Rollback   bool   `json:"rollback,omitempty"`
	RollbackOf string `json:"rollback_of,omitempty"`
	RollbackTo string `json:"rollback_to,omitempty"`
	// RolledBackBy is the rollback that replaced it, once that is live.
	RolledBackBy string `json:"rolled_back_by,omitempty"`
	// Trigger is what started it (DeployByUser, …); By the user who did
	// or on whose behalf (0: Dash), ByName their name then; TriggerRef
	// the task, release tag or goal behind it.
	Trigger    string `json:"trigger"`
	By         int64  `json:"by,omitempty"`
	ByName     string `json:"by_name,omitempty"`
	TriggerRef string `json:"trigger_ref,omitempty"`
	// Runner and RunnerID are where it ran (on a hub: a runner; else
	// this Dash, by its machine name).
	Runner   string       `json:"runner,omitempty"`
	RunnerID string       `json:"runner_id,omitempty"`
	Status   DeployStatus `json:"status"`
	// Error says why it failed, when the exit code doesn't.
	Error    string `json:"error,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	// Deployed: its deploy command exited 0. A deploy that failed after
	// that failed its checks (Checks), and the environment may run it.
	Deployed bool `json:"deployed,omitempty"`
	// Checks are how its checks went, in the order they ran.
	Checks []DeployCheck `json:"checks,omitempty"`
	// Cancelled: someone took it off the queue or stopped it.
	Cancelled bool `json:"cancelled,omitempty"`
	// Promote: it deploys what is live on another environment (PromotedFrom,
	// that deploy; PromotedEnv, its environment) again here: the same
	// commit and artifact, with no build.
	Promote      bool   `json:"promote,omitempty"`
	PromotedFrom string `json:"promoted_from,omitempty"`
	PromotedEnv  string `json:"promoted_env,omitempty"`
	// Approval is who approved it, or rejected it, when its environment
	// needs that (nil: it doesn't, or nobody has yet).
	Approval *DeployApproval `json:"approval,omitempty"`
	// NeedsApproval is the role that must approve it (set when it waits).
	NeedsApproval string `json:"needs_approval,omitempty"`
	// TestsPassed: the project's tests passed on its commit (Dash ran them
	// with the deploy, or knew they had passed).
	TestsPassed bool `json:"tests_passed,omitempty"`
	// FixTask is the draft task made when it failed.
	FixTask string `json:"fix_task,omitempty"`
	// Phases are when it entered each status, in order: its timings.
	Phases     []DeployPhase `json:"phases,omitempty"`
	CreatedAt  time.Time     `json:"created_at"`
	StartedAt  *time.Time    `json:"started_at,omitempty"`
	FinishedAt *time.Time    `json:"finished_at,omitempty"`
	// Shipped are the tasks and planning cards it shipped, from the
	// commits between PrevCommit and Commit ("Dash-Todo:" trailers);
	// Commits is how many commits that is.
	Shipped []DeployShipped `json:"shipped,omitempty"`
	Commits int             `json:"commits,omitempty"`
	// Log is its output, secrets masked by the runner (only read with the
	// deploy itself: lists and events leave it out).
	Log string `json:"log,omitempty"`
}

// SetStatus moves d to s at at, recording the phase, and its start and
// finish times.
func (d *Deploy) SetStatus(s DeployStatus, at time.Time) {
	if d.Status == s && len(d.Phases) > 0 {
		return
	}
	d.Status = s
	d.Phases = append(d.Phases, DeployPhase{Status: s, At: at})
	if s.Active() && d.StartedAt == nil {
		d.StartedAt = &at
	}
	if s.Done() && d.FinishedAt == nil && s != DeploySuperseded && s != DeployRolledBack {
		d.FinishedAt = &at
	}
}

// The checks a deploy runs once deployed (DeployCheck.Name).
const (
	CheckHealth = "health"
	CheckVerify = "verify"
	CheckSoak   = "soak"
)

// DeployCheck is how one of a deploy's checks went.
type DeployCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// ChecksFailed reports whether d was deployed and then failed a check.
func (d Deploy) ChecksFailed() bool {
	if d.Status != DeployFailed || !d.Deployed {
		return false
	}
	for _, c := range d.Checks {
		if !c.OK {
			return true
		}
	}
	return false
}

// WentLive reports whether d was ever live.
func (d Deploy) WentLive() bool { return !d.LiveSince().IsZero() }

// RollbackFrom is, of an environment's deploys (newest first), the one a
// rollback would replace: the newest deploy done that the environment may
// be running, live or deployed and failed (and not cancelled or rolled
// back already).
func RollbackFrom(deploys []Deploy) (Deploy, bool) {
	for _, d := range deploys {
		switch {
		case d.Status == DeployLive:
			return d, true
		case d.Status == DeployFailed && d.Deployed && !d.Cancelled && d.RolledBackBy == "":
			return d, true
		}
	}
	return Deploy{}, false
}

// RollbackTarget is, of an environment's deploys (newest first), the last
// good deploy before bad: the newest older one that went live, of a commit
// never rolled back, and another than bad's (the same commit again would
// change nothing).
func RollbackTarget(deploys []Deploy, bad Deploy) (Deploy, bool) {
	rolledBack := map[string]bool{}
	for _, d := range deploys {
		if d.Status == DeployRolledBack || d.RolledBackBy != "" {
			rolledBack[d.Commit] = true
		}
	}
	older := false
	for _, d := range deploys {
		if d.ID == bad.ID {
			older = true
			continue
		}
		if !older || !d.WentLive() || rolledBack[d.Commit] || d.Commit == "" || d.Commit == bad.Commit {
			continue
		}
		return d, true
	}
	return Deploy{}, false
}

// LiveSince is when d went live (zero if it never did).
func (d Deploy) LiveSince() time.Time {
	for _, ph := range d.Phases {
		if ph.Status == DeployLive {
			return ph.At
		}
	}
	return time.Time{}
}

// ShippedKey names a task or planning card a deploy shipped, in LiveShipped.
func ShippedKey(kind, id string) string { return kind + ":" + id }

// LiveShipped is what one environment runs now, of the tasks and cards its
// deploys (any order) shipped: by ShippedKey, since when each has been
// live there. It is worked out from the records alone, without the
// repository (a hub has none): each deploy that went live adds what it
// shipped to what was live before it, and a rollback goes back to what
// was live with the deploy it rolled back to. Nil when nothing is live.
func LiveShipped(deploys []Deploy) map[string]time.Time {
	var went []Deploy
	for _, d := range deploys {
		if d.WentLive() {
			went = append(went, d)
		}
	}
	slices.SortStableFunc(went, func(a, b Deploy) int { return a.LiveSince().Compare(b.LiveSince()) })
	after := map[string]map[string]time.Time{} // deploy id → what was live with it
	var cur map[string]time.Time
	live := ""
	for _, d := range went {
		next := map[string]time.Time{}
		base := cur
		if d.Rollback {
			if snap, ok := after[d.RollbackTo]; ok {
				base = snap
			}
		}
		for k, at := range base {
			next[k] = at
		}
		if !d.Rollback {
			for _, s := range d.Shipped {
				if _, ok := next[ShippedKey(s.Kind, s.ID)]; !ok {
					next[ShippedKey(s.Kind, s.ID)] = d.LiveSince()
				}
			}
		}
		after[d.ID], cur = next, next
		if d.Status == DeployLive {
			live = d.ID
		}
	}
	if live == "" {
		return nil
	}
	return after[live]
}

// The environment variables a deploy's build and deploy commands get (the
// script contract, see docs/deploys.md). A script that ignores them all
// still works.
const (
	DeployVarEnv      = "DASH_ENV"         // the environment's name
	DeployVarCommit   = "DASH_COMMIT"      // the commit deployed
	DeployVarPrev     = "DASH_PREV_COMMIT" // the commit live before ("" for the first deploy)
	DeployVarArtifact = "DASH_ARTIFACT"    // the artifact to deploy again (a rollback), else ""
	DeployVarID       = "DASH_DEPLOY_ID"   // the deploy's id
	DeployVarRollback = "DASH_ROLLBACK"    // "1" on a rollback, else unset
	DeployVarURL      = "DASH_URL"         // where the environment is reached (the verify command: the deploy's)
	DeployVarOutput   = "DASH_OUTPUT"      // a file the script may write key=value lines to (DeployOutputs)
)

// DeployOutputs are the keys a script may write to $DASH_OUTPUT, one
// key=value per line: where the environment is reached now, the version
// and the artifact it deployed. Other keys are ignored.
var DeployOutputs = []string{"url", "version", "artifact"}

// MaxDeployLog caps a deploy's saved log; the oldest output goes first.
const MaxDeployLog = 256 << 10
