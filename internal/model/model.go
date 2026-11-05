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
	StatusInProgress: {StatusReview, StatusFailed, StatusQueued},
	StatusReview:     {StatusMerging, StatusQueued, StatusDraft},
	StatusMerging:    {StatusMerged, StatusFailed, StatusReview},
	StatusFailed:     {StatusQueued, StatusDraft, StatusReview},
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

// Project is a local Git repository managed by Dash. Its main checkout at
// Path always has DefaultBranch checked out and is only written to by the
// merge worker; all AI work happens in worktrees.
type Project struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Path          string    `json:"path"`
	DefaultBranch string    `json:"default_branch"`
	RemoteURL     string    `json:"remote_url,omitempty"`
	AutoPush      bool      `json:"auto_push"`                // push DefaultBranch after every merge
	RunCommand    string    `json:"run_command,omitempty"`    // e.g. "npm run dev" — gets $PORT
	TestCommand   string    `json:"test_command,omitempty"`   // e.g. "go test ./..."
	DeployCommand string    `json:"deploy_command,omitempty"` // e.g. "./deploy.sh" or "adb install …"
	CreatedAt     time.Time `json:"created_at"`
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
	ID         string     `json:"id"`
	ProjectID  string     `json:"project_id"`
	Title      string     `json:"title"`
	Prompt     string     `json:"prompt"`
	Goal       bool       `json:"goal"`     // major milestone
	Priority   int        `json:"priority"` // lower runs first; ties broken by CreatedAt
	Status     TodoStatus `json:"status"`
	Worktree   *Worktree  `json:"worktree,omitempty"`
	Feedback   []string   `json:"feedback,omitempty"` // reviewer notes from "rework" rounds
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// MergeJob is a request to land a reviewed Todo's branch on the default branch.
type MergeJob struct {
	TodoID    string
	ProjectID string
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
