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
// work lands through the remote (a pull request), never by merging on the
// hub.
//
// The runner checks every task against the projects linked to it (see the
// api package's hub task routes): it takes work only for a project the hub
// linked to it, in the org it was registered for, cloned inside its own
// projects root, and ignores anything else.
package dispatch

import (
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/usage"
)

// Link is PUT /api/hub/projects/{hub project id} on a runner: the hub
// links one of its projects to the runner, which clones RemoteURL into its
// projects root (or finds its clone from an earlier link).
type Link struct {
	Name          string `json:"name"`
	RemoteURL     string `json:"remote_url"`
	DefaultBranch string `json:"default_branch"`
	OrgID         int64  `json:"org_id"`
}

// Linked is the runner's answer to Link: its project for the hub's.
type Linked struct {
	ProjectID string `json:"project_id"`
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
	Parallel int             `json:"parallel"` // the runner's own -parallel
	Usage    *usage.Snapshot `json:"usage,omitempty"`
	Tasks    []TaskState     `json:"tasks,omitempty"`
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
	// Session: the runner still has the task's worktree and Claude
	// session, so it can continue the task.
	Session   bool      `json:"session,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// StateOf is what a runner reports about its copy t of a hub's task.
func StateOf(t model.Todo) TaskState {
	s := TaskState{
		HubTodoID: t.HubTodoID, ID: t.ID, Status: t.Status, Paused: t.Paused, Error: t.Error, Summary: t.Summary,
		DiffFiles: t.DiffFiles, DiffAdd: t.DiffAdd, DiffDel: t.DiffDel, CostUSD: t.CostUSD, Runs: t.Runs,
		Checks: t.Checks, PR: t.PR, Session: t.Worktree != nil && t.SessionID != "", UpdatedAt: t.UpdatedAt,
	}
	if t.Worktree != nil {
		s.Branch = t.Worktree.Branch
	}
	return s
}
