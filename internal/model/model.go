// Package model holds Dash's core domain types. It has no dependencies on
// other Dash packages so every layer can import it.
package model

import (
	"fmt"
	"time"
)

// TodoStatus is the lifecycle state of a Todo.
//
//	Draft ──enqueue──▶ Queued ──worker──▶ InProgress ──done──▶ Review
//	  ▲                  ▲                    │                  │  │
//	  └──────cancel──────┴──────rework────────┼──────────────────┘  │approve
//	                                          ▼                     ▼
//	                                        Failed ◀──conflict── Merging ──▶ Merged
type TodoStatus string

const (
	StatusDraft      TodoStatus = "draft"
	StatusQueued     TodoStatus = "queued"
	StatusInProgress TodoStatus = "in_progress"
	StatusReview     TodoStatus = "review"
	StatusMerging    TodoStatus = "merging" // approved, waiting in / being processed by the merge queue
	StatusMerged     TodoStatus = "merged"
	StatusFailed     TodoStatus = "failed"
)

var transitions = map[TodoStatus][]TodoStatus{
	StatusDraft:      {StatusQueued},
	StatusQueued:     {StatusDraft, StatusInProgress},
	StatusInProgress: {StatusReview, StatusFailed, StatusQueued, StatusMerging}, // → merging: auto-merge after a conflict fix
	StatusReview:     {StatusMerging, StatusQueued, StatusDraft, StatusMerged},  // → merged: its PR was merged on GitHub
	StatusMerging:    {StatusMerged, StatusFailed, StatusReview, StatusQueued},  // → queued: conflict sent back to Claude
	StatusFailed:     {StatusQueued, StatusDraft, StatusReview, StatusMerged, StatusMerging},
	StatusMerged:     {},
}

// CanTransition reports whether a Todo may move from one status to another.
func CanTransition(from, to TodoStatus) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Project is a local Git repository managed by Dash. Path is the user's own
// checkout: Dash merges in a checkout of its own and only moves Path's files
// along when it has DefaultBranch checked out. All AI work happens in worktrees.
type Project struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	DefaultBranch string `json:"default_branch"`
	RemoteURL     string `json:"remote_url,omitempty"`
	AutoPush      bool   `json:"auto_push"`                // push DefaultBranch after every merge
	RunCommand    string `json:"run_command,omitempty"`    // e.g. "npm run dev" — gets $PORT
	TestCommand   string `json:"test_command,omitempty"`   // e.g. "go test ./..."
	DeployCommand string `json:"deploy_command,omitempty"` // e.g. "./deploy.sh" or "adb install …"
	// SetupCommand runs once in every new worktree before Claude starts,
	// e.g. "npm ci". CopyFiles are copied from the main checkout first
	// (untracked files such as ".env" that worktrees don't get from git).
	SetupCommand string   `json:"setup_command,omitempty"`
	CopyFiles    []string `json:"copy_files,omitempty"`
	// AllowedTools run without a permission prompt in every todo, e.g. "Bash"
	// or "WebFetch". "Always allow" on a prompt adds to it.
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// MaxAutoFixes is how many times Dash sends failing tests or merge
	// conflicts back to Claude on its own before asking a human.
	MaxAutoFixes int `json:"max_auto_fixes"`
	// MergeMode is how approved work lands: "local" (squash onto the default
	// branch here, then push if AutoPush) or "pr" (push the branch and open a
	// GitHub pull request; Dash marks the todo merged when the PR is merged).
	MergeMode string `json:"merge_mode,omitempty"`
	// Limits for one Claude run; 0 means the default. Time spent waiting
	// for a human to answer a permission prompt doesn't count.
	MaxRunMinutes int `json:"max_run_minutes,omitempty"`
	MaxTurns      int `json:"max_turns,omitempty"`
	// SyncError is set while the default branch can't be synced with or
	// pushed to origin (e.g. it diverged); cleared on the next success.
	SyncError string `json:"sync_error,omitempty"`
	// Color is an optional "#rrggbb" the UI marks the project with, so it's
	// clear at a glance which one is open.
	Color string `json:"color,omitempty"`
	// InvestigateCI queues a todo for Claude when a GitHub Actions build on
	// the default branch fails (nil means on). CIHandledRun is the newest
	// run already looked at, so a failure is only investigated once.
	InvestigateCI *bool     `json:"investigate_ci,omitempty"`
	CIHandledRun  int64     `json:"ci_handled_run,omitempty"`
	CreatedAt     time.Time `json:"created_at"`

	// Machine is the computer the project lives on; filled in by clients
	// that show several computers in one dashboard, never stored.
	Machine string `json:"machine,omitempty"`
}

// InvestigatesCI reports whether failed builds get a todo automatically.
func (p Project) InvestigatesCI() bool { return p.InvestigateCI == nil || *p.InvestigateCI }

const (
	DefaultMaxAutoFixes  = 2
	DefaultMaxRunMinutes = 60
	DefaultMaxTurns      = 200

	MergeLocal = "local"
	MergePR    = "pr"
)

// UsesPR reports whether approved todos are landed through GitHub pull requests.
func (p Project) UsesPR() bool { return p.MergeMode == MergePR }

// SyncsRemote reports whether the default branch is kept in step with origin
// before merging: whenever Dash pushes it or opens pull requests against it.
func (p Project) SyncsRemote() bool { return p.RemoteURL != "" && (p.AutoPush || p.UsesPR()) }

// RunMinutes is the time limit for one Claude run.
func (p Project) RunMinutes() int {
	if p.MaxRunMinutes > 0 {
		return p.MaxRunMinutes
	}
	return DefaultMaxRunMinutes
}

// TurnLimit is the --max-turns for one Claude run.
func (p Project) TurnLimit() int {
	if p.MaxTurns > 0 {
		return p.MaxTurns
	}
	return DefaultMaxTurns
}

// Worktree is the isolated checkout a Todo is executed in.
type Worktree struct {
	Name       string `json:"name"`        // e.g. "todo-3f9a1c"
	Path       string `json:"path"`        // absolute path on disk
	Branch     string `json:"branch"`      // e.g. "dash/todo-3f9a1c"
	BaseCommit string `json:"base_commit"` // DefaultBranch HEAD when created
}

// Todo is a unit of AI work.
type Todo struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"project_id"`
	Title     string     `json:"title"`
	Prompt    string     `json:"prompt"`
	Goal      bool       `json:"goal"`     // major milestone
	Priority  int        `json:"priority"` // lower runs first; ties broken by CreatedAt
	Status    TodoStatus `json:"status"`
	Worktree  *Worktree  `json:"worktree,omitempty"`
	// Feedback holds notes for Claude: reviewer rework notes and automatic
	// ones (failing tests, merge conflicts). FeedbackSent counts how many
	// Claude has already seen, so a resumed session only gets the new ones.
	Feedback     []string `json:"feedback,omitempty"`
	FeedbackSent int      `json:"feedback_sent,omitempty"`
	// SessionID is Claude Code's session for this todo; later runs resume it.
	SessionID string  `json:"session_id,omitempty"`
	Checks    *Checks `json:"checks,omitempty"` // latest test run
	AutoFixes int     `json:"auto_fixes,omitempty"`
	// MergeMain asks the next run to merge the default branch into the
	// worktree first (set when a merge conflicted). AutoMerge sends the todo
	// straight back to the merge queue once it is fixed and tests pass,
	// since a human already approved it.
	MergeMain bool `json:"merge_main,omitempty"`
	AutoMerge bool `json:"auto_merge,omitempty"`
	// Paused holds a queued todo back from the dispatcher. Pausing a running
	// todo stops Claude and requeues it paused, keeping its worktree and
	// session so resuming continues where it stopped.
	Paused bool `json:"paused,omitempty"`
	// Size of the change against the default branch, after the latest run.
	DiffFiles int     `json:"diff_files,omitempty"`
	DiffAdd   int     `json:"diff_add,omitempty"`
	DiffDel   int     `json:"diff_del,omitempty"`
	CostUSD   float64 `json:"cost_usd,omitempty"` // API-equivalent cost reported by Claude Code
	Runs      int     `json:"runs,omitempty"`
	// Summary is one sentence on what Claude did, from its latest run.
	Summary string `json:"summary,omitempty"`
	// Attachments are files (usually screenshots) given to Claude with the task.
	Attachments []Attachment `json:"attachments,omitempty"`
	// MergedCommit is the commit that landed the todo on the default branch.
	MergedCommit string       `json:"merged_commit,omitempty"`
	PR           *PullRequest `json:"pr,omitempty"`     // in "pr" merge mode
	Revert       *Revert      `json:"revert,omitempty"` // set once a merged todo is reverted
	CIRun        int64        `json:"ci_run,omitempty"` // the failed GitHub Actions run this todo investigates
	Error        string       `json:"error,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	StartedAt    *time.Time   `json:"started_at,omitempty"`
	FinishedAt   *time.Time   `json:"finished_at,omitempty"`
}

// Checks is the outcome of running the project's test command in a worktree.
type Checks struct {
	Command  string    `json:"command"`
	Passed   bool      `json:"passed"`
	ExitCode int       `json:"exit_code"`
	Output   string    `json:"output"` // tail of combined stdout/stderr
	Stage    string    `json:"stage"`  // "review" (before review) | "merge" (after merging the default branch)
	At       time.Time `json:"at"`
	Duration float64   `json:"duration_s"`
}

// Attachment is an uploaded file attached to a todo or a rework note.
type Attachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	Path string `json:"path"` // absolute path Claude reads it from
}

// PullRequest is the GitHub PR a todo was landed through.
type PullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"` // OPEN | MERGED | CLOSED
	// HandledSHA is the head commit whose conflicts or failing checks were
	// already sent back to Claude, so the same failure isn't sent twice.
	HandledSHA string `json:"handled_sha,omitempty"`
}

// Revert records how a merged todo was undone.
type Revert struct {
	Commit string    `json:"commit,omitempty"` // revert commit on the default branch
	PRURL  string    `json:"pr_url,omitempty"` // revert pull request, in "pr" merge mode
	At     time.Time `json:"at"`
}

// MergeJob is a request to land a reviewed Todo's branch on the default branch.
type MergeJob struct {
	TodoID    string
	ProjectID string
	Revert    bool // undo the merged todo instead
}

// ProcessKind distinguishes how a child process is attached.
type ProcessKind string

const (
	ProcWeb  ProcessKind = "web"  // long-running server, piped output, exposes URL
	ProcPTY  ProcessKind = "pty"  // interactive CLI over xterm.js
	ProcTask ProcessKind = "task" // one-shot command (tests, deploy), piped output
)

// Process is a child process started by Dash on behalf of a project.
type Process struct {
	ID        string      `json:"id"`
	ProjectID string      `json:"project_id"`
	Kind      ProcessKind `json:"kind"`
	Command   string      `json:"command"`
	Dir       string      `json:"dir"`
	PID       int         `json:"pid"`
	URL       string      `json:"url,omitempty"`
	Running   bool        `json:"running"`
	ExitCode  *int        `json:"exit_code,omitempty"`
	StartedAt time.Time   `json:"started_at"`
}

// ChatMessage is one turn of the general chat.
type ChatMessage struct {
	Role    string `json:"role"` // "user" | "assistant"
	Content string `json:"content"`
}

// EventType names a WebSocket event broadcast to dashboards.
type EventType string

const (
	EvProjectUpdated EventType = "project.updated"
	EvTodoUpdated    EventType = "todo.updated"
	EvTodoDeleted    EventType = "todo.deleted"
	EvTodoOutput     EventType = "todo.output" // agent progress line
	EvGitOp          EventType = "git.op"      // a git/gh command ran
	EvMergeQueue     EventType = "merge.queue" // merge queue depth / current job
	EvProcUpdated    EventType = "process.updated"
	EvPermission     EventType = "permission.updated" // Claude asked to use a tool, or a request was answered
	EvUsage          EventType = "usage.updated"      // subscription rate-limit usage / queue pause state
	EvCI             EventType = "ci.updated"         // GitHub Actions status of a project's default branch
)

// Event is the envelope for everything pushed over /ws/events.
type Event struct {
	Type      EventType `json:"type"`
	ProjectID string    `json:"project_id,omitempty"`
	TodoID    string    `json:"todo_id,omitempty"`
	Data      any       `json:"data,omitempty"`
	Time      time.Time `json:"time"`
}

// ErrInvalidTransition is returned when a status change is not allowed.
type ErrInvalidTransition struct{ From, To TodoStatus }

func (e ErrInvalidTransition) Error() string {
	return fmt.Sprintf("invalid status transition %s → %s", e.From, e.To)
}
