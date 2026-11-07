package model

import "time"

// Scan scopes: what part of the repository a scan looks at.
const (
	ScopeRepo          = "repo"            // the whole repository
	ScopeSinceLastScan = "since_last_scan" // what changed since the previous run of the same scan
	ScopeSinceMain     = "since_main"      // the checkout's branch compared with the default branch
	ScopePath          = "path"            // one directory or file
)

// ScanRun statuses.
const (
	ScanRunning   = "running"
	ScanSucceeded = "succeeded"
	ScanFailed    = "failed"
	ScanSkipped   = "skipped"  // nothing changed since the last run
	ScanCanceled  = "canceled" // stopped by the user; never restarted
	// ScanInterrupted: Dash stopped (shut down or crashed) during the run.
	// It is started over once at the next startup; see ResumedAs.
	ScanInterrupted = "interrupted"
)

// TriggerResume is the trigger of a run that starts over an interrupted one.
const TriggerResume = "resume"

// ScanRun is one run of a scan template on a project. A scan only reads the
// code; what it finds goes into the project's Issues, never onto a branch.
type ScanRun struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Scan      string `json:"scan"`    // template name; its issues have source "scan:<name>"
	Trigger   string `json:"trigger"` // "manual", "schedule" or "resume"
	// ResumedFrom is the interrupted run this one starts over (trigger
	// "resume"); ResumedAs, on the interrupted run, the run that did.
	ResumedFrom string `json:"resumed_from,omitempty"`
	ResumedAs   string `json:"resumed_as,omitempty"`
	Scope       string `json:"scope"`
	Path        string `json:"path,omitempty"`  // for ScopePath
	Commit      string `json:"commit"`          // what was scanned
	Since       string `json:"since,omitempty"` // for incremental scopes: the commit diffed against
	// Incremental says the run looked only at Since..Commit; false for a
	// since_last_scan run without a previous run, which scans everything.
	Incremental bool   `json:"incremental,omitempty"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	// Answer is the agent's final message, kept when no findings could be
	// read from it so they aren't lost and the failure can be looked into.
	Answer     string     `json:"answer,omitempty"`
	Note       string     `json:"note,omitempty"` // e.g. why it was skipped
	Activity   string     `json:"activity,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	CostUSD    float64    `json:"cost_usd,omitempty"`
	Turns      int        `json:"turns,omitempty"`
	// Findings is how many valid findings the agent returned; Invalid how
	// many it returned that were dropped (no title, unknown severity, …).
	Findings int          `json:"findings"`
	Invalid  int          `json:"invalid,omitempty"`
	Counts   UpsertCounts `json:"counts"`
	// NewIssues and ReopenedIssues are the ids of the issues this run added
	// or brought back, for "show me what's new".
	NewIssues      []string `json:"new_issues,omitempty"`
	ReopenedIssues []string `json:"reopened_issues,omitempty"`
	// Urgent counts new and reopened issues of critical or high severity.
	Urgent int `json:"urgent,omitempty"`
}

// ScanSetting is what a user changed about a scan in the UI: its schedule,
// and what they approved of a repository template. A repository template is
// code anyone with merge rights can change, so its extra tools and its
// schedule only take effect once a user approves them in Dash.
type ScanSetting struct {
	ProjectID string `json:"project_id"`
	Scan      string `json:"scan"`
	// Enabled overrides whether the schedule runs; nil = the default.
	Enabled *bool `json:"enabled,omitempty"`
	// Schedule overrides the template's cron expression; "" = the template's.
	Schedule string `json:"schedule,omitempty"`
	// ApprovedSchedule is the repository template's cron expression the
	// user approved; it keeps running even if the template's changes.
	ApprovedSchedule string `json:"approved_schedule,omitempty"`
	// ApprovedTools is the exact tool list of the repository template the
	// user approved; once the template asks for other tools, it no longer
	// applies and the scan runs with read-only tools.
	ApprovedTools []string  `json:"approved_tools,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ScanTemplate is a custom scan a user wrote in Dash, kept per project next
// to the repository's .dash/scans templates. Unlike those, it is trusted like
// a built-in: its tools and schedule need no separate approval, since only a
// Dash user can change it. The scan package validates it (scan.Custom).
type ScanTemplate struct {
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	Title     string    `json:"title,omitempty"`
	Kind      string    `json:"kind"`
	Scope     string    `json:"scope"`
	Path      string    `json:"path,omitempty"`
	Model     string    `json:"model,omitempty"`
	Effort    string    `json:"effort,omitempty"`
	Schedule  string    `json:"schedule,omitempty"`
	Tools     []string  `json:"tools,omitempty"`
	Prompt    string    `json:"prompt"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
