// Package dispatch sends a hub's tasks to the runners (`dash runner`) their
// projects are linked to, and keeps the hub's copy of each task in step with
// what its runner reports.
//
// The hub coordinates: it stores the projects and their tasks, picks a
// runner for each queued task (one that is linked to its project, in the
// project's org, online, not paused, not at its usage limit and below its
// parallel limit) and sends it there over the runner's tunnel. The runner
// runs the task in its own clone of the project's git remote, with its
// owner's Claude login, and reports back on POST /runner/report. Approved
// work lands through the remote: in "pr" mode (the default on GitHub) the
// runner pushes the task's branch and opens a pull request, which it merges
// once its checks pass (unless the project turned that off); in "local"
// mode it squashes the task onto the default branch in its clone, runs the
// checks and pushes it to origin, merging again on top of origin when
// another runner (or anyone) pushed first. The hub marks the task merged
// once the runner reports it. Nothing merges on the hub.
//
// The runner checks every task against the projects linked to it (see the
// api package's hub task routes): it takes work only for a project the hub
// linked to it (by the hub's project id, which it keeps with its checkout),
// in the org it was registered for, cloned or adopted inside its own
// projects root, and ignores anything else.
package dispatch

import (
	"encoding/json"
	"time"

	"github.com/klahr/dash/internal/claudecli"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/usage"
)

// Link is PUT /api/hub/projects/{hub project id} on a runner: the hub
// links one of its projects to the runner, which clones RemoteURL into
// <its projects root>/<repo>, adopts a clone of RemoteURL already there, or
// finds its checkout from an earlier link by the hub's project id.
type Link struct {
	Name          string `json:"name"`
	RemoteURL     string `json:"remote_url"`
	DefaultBranch string `json:"default_branch"`
	// WorkBranch is the branch the hub's tasks start from and land on when
	// it isn't DefaultBranch (the hub's branch switcher); "" means DefaultBranch.
	WorkBranch string `json:"work_branch,omitempty"`
	OrgID      int64  `json:"org_id"`
	// MergeMode is the hub project's model.Project.MergeMode: "" or "local"
	// lands approved work by pushing it to the default branch, "pr" through
	// a pull request.
	MergeMode string `json:"merge_mode,omitempty"`
	// MergeModeSet is whether the hub's user chose MergeMode. A default
	// "pr" falls back to "local" on a runner without gh, so runners don't
	// need gh unless someone asked for pull requests.
	MergeModeSet bool `json:"merge_mode_set,omitempty"`
	// MergeGreenPRs is the hub project's model.Project.MergeGreenPRs: the
	// runner merges a task's pull request once its checks pass, unless false.
	MergeGreenPRs *bool `json:"merge_green_prs,omitempty"`
	// TaskInstructions are the hub project's extra instructions for Claude
	// (model.Project.TaskInstructions). The link is sent before every task,
	// so the runner's copy follows the hub's.
	TaskInstructions string `json:"task_instructions,omitempty"`
	// AutoApprove is the hub project's model.Project.AutoApprove: finished
	// tasks whose tests pass skip review and go straight to merging. It is
	// nil from hubs that predate it; the runner then keeps its own setting.
	AutoApprove *bool `json:"auto_approve,omitempty"`
	// MaxAutoFixes is the hub project's model.Project.MaxAutoFixes: how many
	// times failing tests or merge conflicts go back to Claude on their own.
	// Nil (older hubs) keeps the runner's setting.
	MaxAutoFixes *int `json:"max_auto_fixes,omitempty"`
	// The hub project's shell commands (setup, test, run, deploy). A
	// runner takes them only when it was started with -hub-commands;
	// otherwise its clone keeps the commands set on the runner.
	SetupCommand  string `json:"setup_command,omitempty"`
	TestCommand   string `json:"test_command,omitempty"`
	RunCommand    string `json:"run_command,omitempty"`
	DeployCommand string `json:"deploy_command,omitempty"`
	// The hub project's preview settings (model.Project.PreviewKind and
	// PreviewCommand), taken like its commands: a terminal preview is a shell.
	PreviewKind    string `json:"preview_kind,omitempty"`
	PreviewCommand string `json:"preview_command,omitempty"`
}

// Linked is the runner's answer to Link: its project for the hub's.
type Linked struct {
	ProjectID string `json:"project_id"`
	// DefaultBranch is the clone's default branch: origin's, which may
	// differ from the one the hub guessed when the project was made.
	DefaultBranch string `json:"default_branch,omitempty"`
	// Path is where the runner keeps its checkout: <its projects
	// folder>/<repo> (or <repo>-2, … when that folder holds something else).
	Path string `json:"path,omitempty"`
	// Adopted is whether a clone of the remote was already there and the
	// runner took it over instead of cloning.
	Adopted bool `json:"adopted,omitempty"`
}

// Task is POST /api/hub/tasks on a runner: run the hub's task.
type Task struct {
	HubTodoID    string `json:"hub_todo_id"`
	HubProjectID string `json:"hub_project_id"`
	Title        string `json:"title"`
	Prompt       string `json:"prompt"`
	// Feedback is all of the hub's notes for Claude (rework requests); the
	// runner adds those it doesn't have yet.
	Feedback []string `json:"feedback,omitempty"`
	// Fresh starts the task over; otherwise a task the runner already has
	// (sent back for rework, or interrupted) continues in its worktree and
	// Claude session.
	Fresh bool `json:"fresh,omitempty"`
}

// Accepted is the runner's answer to Task.
type Accepted struct {
	ID      string `json:"id"`      // the runner's todo id
	Resumed bool   `json:"resumed"` // it continues the runner's earlier run
}

// Report is POST /runner/report: a runner's news for its hub.
type Report struct {
	Parallel int `json:"parallel"` // the runner's own -parallel
	// Terminals: the runner opens terminals for its owner from the hub
	// (it runs with -hub-commands).
	Terminals bool            `json:"terminals,omitempty"`
	Usage     *usage.Snapshot `json:"usage,omitempty"`
	// Claude is the runner's Claude Code CLI: its version and what looks
	// changed about it (nil from a runner older than this field).
	Claude *claudecli.Status `json:"claude,omitempty"`
	Tasks  []TaskState       `json:"tasks,omitempty"`
	// Procs are the hub's projects whose processes (run/test/deploy
	// commands, terminals) on the runner changed since the last report: the
	// hub tells their dashboards to list them again.
	Procs []string `json:"procs,omitempty"`
	// Pushes are the runner's pushes to origin since its last report: the
	// hub tells the project's other runners (OriginMoved).
	Pushes []Push `json:"pushes,omitempty"`
	// Checkouts are the runner's clones of the hub's projects whose
	// checkout is behind origin, all of them every time (none: none is).
	// Nil from a runner older than this field: the hub keeps what it had.
	Checkouts *[]CheckoutState `json:"checkouts,omitempty"`
	// Permissions are the tool calls Claude asks to make in the hub's tasks
	// that wait for a human's answer on the runner, all of them every time
	// (none: none waits). The hub shows them on its board and answers them
	// with PermissionPath.
	Permissions []Permission `json:"permissions,omitempty"`
}

// Permission is a permission request of one of the hub's tasks, pending on
// the runner (perm.Request, with the hub's task id).
type Permission struct {
	ID        string          `json:"id"` // the runner's request id
	HubTodoID string          `json:"hub_todo_id"`
	Tool      string          `json:"tool"`
	Input     json.RawMessage `json:"input,omitempty"`
	Created   time.Time       `json:"created"`
}

// PermissionPath is POST on a runner: the hub answers the permission
// request {id} of its task {htid}; the body is a PermissionAnswer.
const PermissionPath = "/api/hub/tasks/{htid}/permissions/{id}"

// PermissionAnswer is how a permission request was answered: allow,
// always or deny.
type PermissionAnswer struct {
	Decision string `json:"decision"`
	Message  string `json:"message,omitempty"`
}

// Push is a runner's push of a hub's project's branch to origin.
type Push struct {
	HubProjectID string `json:"hub_project_id"`
	Branch       string `json:"branch"`
	SHA          string `json:"sha"`
}

// CheckoutState is a runner's checkout of a hub's project that is behind
// origin (queue.RemoteStatus).
type CheckoutState struct {
	HubProjectID string `json:"hub_project_id"`
	Branch       string `json:"branch"`
	Behind       int    `json:"behind"`
	Ahead        int    `json:"ahead,omitempty"`
	Stuck        string `json:"stuck,omitempty"`
}

// OriginMovedPath is POST on a runner: the hub tells it that another
// runner pushed a branch of the hub's project {hpid}; the body is an
// OriginMoved. The runner fetches and brings its checkout up to date.
const OriginMovedPath = "/api/hub/projects/{hpid}/origin"

// OriginMoved is "origin's Branch now points at SHA".
type OriginMoved struct {
	Branch string `json:"branch"`
	SHA    string `json:"sha"`
}

// TaskState is where one of the hub's tasks is on the runner.
type TaskState struct {
	HubTodoID string             `json:"hub_todo_id"`
	ID        string             `json:"id"`
	Status    model.TodoStatus   `json:"status"`
	Paused    bool               `json:"paused,omitempty"`
	Error     string             `json:"error,omitempty"`
	Summary   string             `json:"summary,omitempty"`
	Branch    string             `json:"branch,omitempty"`
	DiffFiles int                `json:"diff_files,omitempty"`
	DiffAdd   int                `json:"diff_add,omitempty"`
	DiffDel   int                `json:"diff_del,omitempty"`
	CostUSD   float64            `json:"cost_usd,omitempty"`
	Runs      int                `json:"runs,omitempty"`
	Checks    *model.Checks      `json:"checks,omitempty"`
	PR        *model.PullRequest `json:"pr,omitempty"`
	// MergedCommit is the commit that landed the task on origin's default
	// branch, Revert how it was undone since, and RevertConflict the files
	// a revert conflicted in (the hub then queues a task to undo it by hand).
	MergedCommit   string        `json:"merged_commit,omitempty"`
	Revert         *model.Revert `json:"revert,omitempty"`
	RevertConflict []string      `json:"revert_conflict,omitempty"`
	// Session: the runner still has the task's worktree and Claude
	// session, so it can continue the task.
	Session   bool      `json:"session,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	// Output is the task's latest progress line and Events its worker's
	// events since the runner's last report, for the hub's dashboards.
	Output string           `json:"output,omitempty"`
	Events []model.RunEvent `json:"events,omitempty"`
}

// StateOf is what a runner reports about its copy t of a hub's task.
func StateOf(t model.Todo) TaskState {
	s := TaskState{
		HubTodoID: t.HubTodoID, ID: t.ID, Status: t.Status, Paused: t.Paused, Error: t.Error, Summary: t.Summary,
		DiffFiles: t.DiffFiles, DiffAdd: t.DiffAdd, DiffDel: t.DiffDel, CostUSD: t.CostUSD, Runs: t.Runs,
		Checks: t.Checks, PR: t.PR, MergedCommit: t.MergedCommit, Revert: t.Revert, RevertConflict: t.RevertConflict,
		Session: t.Worktree != nil && t.SessionID != "", UpdatedAt: t.UpdatedAt,
	}
	if t.Worktree != nil {
		s.Branch = t.Worktree.Branch
	}
	return s
}

// GitHubCall is POST /api/hub/github/{hub project id} on a runner: the
// hub, which has no gh and no clone, reads (or re-runs) the project's
// GitHub Actions builds with gh in the runner's clone. Op is one of the
// GitHub* constants.
type GitHubCall struct {
	Op         string `json:"op"`
	Ref        string `json:"ref,omitempty"` // a branch or tag (runs), a commit (commit_runs) or a tag (release_url)
	Limit      int    `json:"limit,omitempty"`
	RunID      int64  `json:"run_id,omitempty"`
	FailedOnly bool   `json:"failed_only,omitempty"`
}

const (
	GitHubRuns       = "runs"
	GitHubCommitRuns = "commit_runs"
	GitHubReleaseURL = "release_url"
	GitHubRerun      = "rerun"
	GitHubFailedLog  = "failed_log"
)

// GitHubAnswer is the runner's answer to a GitHubCall.
type GitHubAnswer struct {
	Runs []gitops.Run `json:"runs,omitempty"`
	URL  string       `json:"url,omitempty"`
	Log  string       `json:"log,omitempty"`
}
