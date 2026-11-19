package model

import (
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
}

// HasCommands reports whether e runs anything.
func (e Environment) HasCommands() bool { return e.DeployCommand != "" || e.BuildCommand != "" }

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
		e.DeployCommand, e.BuildCommand = CleanScript(e.DeployCommand), CleanScript(e.BuildCommand)
		if e.DeployCommand == "" {
			return nil, fmt.Errorf("environment %s: give it a deploy command", e.Name)
		}
		e.URL = strings.TrimSpace(e.URL)
		if e.URL != "" && !strings.HasPrefix(e.URL, "https://") && !strings.HasPrefix(e.URL, "http://") {
			return nil, fmt.Errorf("environment %s: the URL must start with https:// or http://", e.Name)
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
	return out, nil
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
//	queued ──▶ building ──▶ deploying ──▶ verifying ──▶ live ──▶ superseded
//	   │           │            │             │           │
//	   └───────────┴────────────┴─────────────┴──▶ failed └──▶ rolled_back
//
// A deploy with no build command skips building; one whose environment has
// no URL (and whose script reported none) skips verifying.
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
)

// Active reports whether a deploy in status s holds its environment: it
// is on its way (one at a time per environment; the others queue).
func (s DeployStatus) Active() bool {
	return s == DeployBuilding || s == DeployDeploying || s == DeployVerifying
}

// Done reports whether a deploy in status s has finished, one way or another.
func (s DeployStatus) Done() bool { return s != DeployQueued && !s.Active() }

// What started a deploy (Deploy.Trigger).
const (
	DeployByUser    = "user"
	DeployByMerge   = "merge"
	DeployByRelease = "release"
	DeployByGoal    = "goal"
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
	// Rollback: it deploys an earlier deploy again (DASH_ROLLBACK=1).
	Rollback bool `json:"rollback,omitempty"`
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

// LiveSince is when d went live (zero if it never did).
func (d Deploy) LiveSince() time.Time {
	for _, ph := range d.Phases {
		if ph.Status == DeployLive {
			return ph.At
		}
	}
	return time.Time{}
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
	DeployVarOutput   = "DASH_OUTPUT"      // a file the script may write key=value lines to (DeployOutputs)
)

// DeployOutputs are the keys a script may write to $DASH_OUTPUT, one
// key=value per line: where the environment is reached now, the version
// and the artifact it deployed. Other keys are ignored.
var DeployOutputs = []string{"url", "version", "artifact"}

// MaxDeployLog caps a deploy's saved log; the oldest output goes first.
const MaxDeployLog = 256 << 10
