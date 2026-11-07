// Package model holds Dash's core domain types. It has no dependencies on
// other Dash packages so every layer can import it.
package model

import (
	"encoding/json"
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
	ID string `json:"id"`
	// OrgID is the organization the project belongs to; its todos, issues,
	// scans and chats belong there with it. Set when it is created, never
	// changed.
	OrgID         int64  `json:"org_id,omitempty"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	DefaultBranch string `json:"default_branch"`
	RemoteURL     string `json:"remote_url,omitempty"`
	AutoPush      bool   `json:"auto_push"`                // push DefaultBranch after every merge
	AutoApprove   bool   `json:"auto_approve"`             // finished todos whose tests pass skip review
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
	// ChatPermissionMode is passed as --permission-mode to the project's chats
	// in shell mode: "" (ask for everything not allowed), ChatModeAuto or
	// ChatModeAcceptEdits. What the mode doesn't approve still asks the user.
	ChatPermissionMode string `json:"chat_permission_mode,omitempty"`
	// ReviewPrompt is what the task chat's Review button asks Claude; empty
	// means the web UI's default.
	ReviewPrompt string `json:"review_prompt,omitempty"`
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
	// Icon is an optional image upload ("<upload id>/<name>") shown in place
	// of the project's initial; served at /api/uploads/<Icon>.
	Icon string `json:"icon,omitempty"`
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

	ChatModeAuto        = "auto"
	ChatModeAcceptEdits = "acceptEdits"
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
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	// CreatedBy is the user who created the task (0: Dash itself, from
	// before users, or made on a runner by its hub). Its runs act as them.
	CreatedBy int64  `json:"created_by,omitempty"`
	Title     string `json:"title"`
	Prompt    string `json:"prompt"`
	Priority  int    `json:"priority"` // lower runs first; ties broken by CreatedAt
	// DependsOn are ids of other todos in the same project that must be
	// merged before this one starts: its worktree branches from the default
	// branch then, so it builds on their code. Until then it waits queued.
	DependsOn []string   `json:"depends_on"`
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
	// SentBack is why the merge queue last returned the todo to Claude
	// (a conflict or failing tests), shown on the card while it is fixed.
	SentBack string `json:"sent_back,omitempty"`
	// Paused holds a queued todo back from the dispatcher. Pausing a running
	// todo stops Claude and requeues it paused, keeping its worktree and
	// session so resuming continues where it stopped.
	Paused bool `json:"paused,omitempty"`
	// Reviewing is set while Claude reviews the todo from its chat; its
	// status can't change until the review finishes.
	Reviewing bool `json:"reviewing,omitempty"`
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
	// Edits are file changes made in the web editor. The first run commits
	// them in the worktree instead of running Claude and sets EditsApplied;
	// later runs (rework, failing tests) are Claude's as usual.
	Edits        []FileEdit `json:"edits,omitempty"`
	EditsApplied bool       `json:"edits_applied,omitempty"`
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

// Blockers returns the todos among all that t depends on and that aren't
// merged yet, in t.DependsOn order. Ids not in all (deleted) don't block.
func (t Todo) Blockers(all []Todo) []Todo {
	if len(t.DependsOn) == 0 {
		return nil
	}
	byID := make(map[string]Todo, len(all))
	for _, x := range all {
		byID[x.ID] = x
	}
	var out []Todo
	for _, id := range t.DependsOn {
		if d, ok := byID[id]; ok && d.Status != StatusMerged {
			out = append(out, d)
		}
	}
	return out
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

// FileEdit is a file's new content, saved in the web editor. Blob is the git
// object the editor started from, so a file changed since isn't overwritten.
type FileEdit struct {
	Path    string `json:"path"`
	Blob    string `json:"blob"`
	Content string `json:"content"`
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
	Port      int         `json:"port,omitempty"`    // web servers: their $PORT
	TodoID    string      `json:"todo_id,omitempty"` // set for a todo's live preview
	Running   bool        `json:"running"`
	ExitCode  *int        `json:"exit_code,omitempty"`
	StartedAt time.Time   `json:"started_at"`
}

// PreviewState is where a todo's live preview is in its life.
type PreviewState string

const (
	PreviewStarting PreviewState = "starting" // server started, its port doesn't answer yet
	PreviewLive     PreviewState = "live"     // the port answers; the proxy serves it
	PreviewCrashed  PreviewState = "crashed"  // the server exited on its own
	PreviewStopped  PreviewState = "stopped"  // stopped (by hand, idle, or the todo left review); only in events
)

// Preview is a todo's project run_command running in its worktree, served
// at URL through Dash's reverse proxy (a subdomain per todo).
type Preview struct {
	TodoID    string       `json:"todo_id"`
	ProjectID string       `json:"project_id"`
	ProcessID string       `json:"process_id"`
	Title     string       `json:"title"` // the todo's, for lists of previews
	Port      int          `json:"port"`
	State     PreviewState `json:"state"`
	// URL is the proxied address, e.g. https://<todo>.preview.example.com/;
	// empty when no preview domain is configured. LocalURL is the server
	// itself, reachable from this computer only. OpenURL is a path on Dash
	// that signs the browser in to the preview and redirects there.
	URL       string    `json:"url,omitempty"`
	LocalURL  string    `json:"local_url"`
	OpenURL   string    `json:"open_url,omitempty"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	Reason    string    `json:"reason,omitempty"` // why it stopped (events only)
	StartedAt time.Time `json:"started_at"`
	LastSeen  time.Time `json:"last_seen"` // last proxied request or WebSocket traffic
}

// ChatMessage is one turn of the general chat.
type ChatMessage struct {
	Role    string `json:"role"` // "user" | "assistant"
	Content string `json:"content"`
	// Attachments are files dropped or pasted into a user message.
	Attachments []Attachment `json:"attachments,omitempty"`
	// At is when the message was sent, to place it in a task's timeline.
	At *time.Time `json:"at,omitempty"`
	// CreatedBy is the user who wrote a user message (0 for Claude's
	// answers and messages from before users).
	CreatedBy int64 `json:"created_by,omitempty"`
}

// RunEvent is one step of a todo's worker: a Claude run's text, tool calls
// and results, its boundaries, and Dash's own progress notes.
type RunEvent struct {
	// "run_start" | "run_end" | "prompt" (notes sent to Claude) | "text" |
	// "tool_use" | "tool_result" | "result" | "status" (a Dash note)
	Kind    string          `json:"kind"`
	Text    string          `json:"text,omitempty"`
	ID      string          `json:"id,omitempty"` // tool_use id (tool_use, tool_result)
	Tool    string          `json:"tool,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
	Turns   int             `json:"turns,omitempty"`    // result
	CostUSD float64         `json:"cost_usd,omitempty"` // result
	At      time.Time       `json:"at"`
}

// EventType names a WebSocket event broadcast to dashboards.
type EventType string

const (
	EvProjectUpdated EventType = "project.updated"
	EvProjectDeleted EventType = "project.deleted"
	EvTodoUpdated    EventType = "todo.updated"
	EvTodoDeleted    EventType = "todo.deleted"
	EvTodoOutput     EventType = "todo.output" // agent progress line
	EvTodoEvent      EventType = "todo.event"  // a structured RunEvent of the worker
	EvGitOp          EventType = "git.op"      // a git/gh command ran
	EvMergeQueue     EventType = "merge.queue" // merge queue depth / current job
	EvProcUpdated    EventType = "process.updated"
	EvPermission     EventType = "permission.updated" // Claude asked to use a tool, or a request was answered
	EvUsage          EventType = "usage.updated"      // subscription rate-limit usage / queue pause state
	EvCI             EventType = "ci.updated"         // GitHub Actions status of a project's default branch
	EvRemote         EventType = "remote.updated"     // how the default branch compares with origin's, after a fetch
	EvPreview        EventType = "preview.updated"    // a todo's live preview started, came up, crashed or stopped
	EvIssueUpdated   EventType = "issue.updated"      // an issue was added or changed; Data is the Issue
	EvIssueDeleted   EventType = "issue.deleted"      // Data is {"id": …}
	EvScanRun        EventType = "scan.run"           // a scan started, progressed or finished; Data is the ScanRun
	EvScanSettings   EventType = "scan.settings"      // a scan's schedule was turned on/off or edited; Data is the ScanSetting
	EvScanTemplate   EventType = "scan.template"      // a Dash scan template was created, edited or deleted; Data is {"name": …, "deleted": bool}
)

// Event is the envelope for everything pushed over /ws/events.
type Event struct {
	Type      EventType `json:"type"`
	ProjectID string    `json:"project_id,omitempty"`
	TodoID    string    `json:"todo_id,omitempty"`
	Data      any       `json:"data,omitempty"`
	Time      time.Time `json:"time"`
	// OrgID is the organization the event belongs to, when the publisher
	// knows it; otherwise the event hub finds it from ProjectID. Events with
	// neither are about Dash as a whole (usage, the merge queue's depth,
	// runners). Never sent to clients.
	OrgID int64 `json:"-"`
}

// ErrInvalidTransition is returned when a status change is not allowed.
type ErrInvalidTransition struct{ From, To TodoStatus }

func (e ErrInvalidTransition) Error() string {
	return fmt.Sprintf("invalid status transition %s → %s", e.From, e.To)
}

// Owner is who a credential acts as: a user, in one org. Both are required.
type Owner struct {
	User int64 `json:"user_id"`
	Org  int64 `json:"org_id"`
}

// Valid reports whether o names both a user and an org.
func (o Owner) Valid() bool { return o.User > 0 && o.Org > 0 }
