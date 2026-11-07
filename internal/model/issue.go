package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
	"time"
)

// An Issue is something wrong with a project — a bug, a vulnerability or
// another problem — that nobody has decided to work on yet. Issues are cheap
// to add (by hand, by a scan, or by an agent that noticed something unrelated
// to its task) and may never be fixed. Turning one into a Todo is triage.
//
//	Open ──create task──▶ TaskCreated ──task merged──▶ Resolved
//	 │ ▲                       │                          │
//	 │ └────────reopen─────────┴──────────────────────────┘
//	 └──dismiss──▶ Dismissed ──reopen──▶ Open
type Issue struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Title     string    `json:"title"`
	Type      IssueType `json:"type"`
	Severity  Severity  `json:"severity"`
	// File and Line locate the issue in the repository; both optional.
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Details string `json:"details,omitempty"` // markdown
	Fix     string `json:"fix,omitempty"`     // suggested fix, markdown
	// Rule is the check that found it (e.g. a scanner's rule id); it stands
	// in for the title in the fingerprint so rewording doesn't duplicate.
	Rule string `json:"rule,omitempty"`
	// Source is who reported it: "manual", "scan:<name>" or "agent:<todo id>".
	Source string      `json:"source"`
	Status IssueStatus `json:"status"`
	// Fingerprint identifies the same issue across reports (see ComputeFingerprint).
	Fingerprint   string    `json:"fingerprint"`
	TodoIDs       []string  `json:"todo_ids,omitempty"` // tasks created for it
	DismissReason string    `json:"dismiss_reason,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type IssueType string

const (
	IssueBug   IssueType = "bug"
	IssueVuln  IssueType = "vuln"
	IssueOther IssueType = "issue"
)

type Severity string

const (
	SevCritical Severity = "critical"
	SevHigh     Severity = "high"
	SevMedium   Severity = "medium"
	SevLow      Severity = "low"
	SevInfo     Severity = "info"
)

// Rank orders severities, most severe first (0); unknown ones sort last.
func (s Severity) Rank() int {
	switch s {
	case SevCritical:
		return 0
	case SevHigh:
		return 1
	case SevMedium:
		return 2
	case SevLow:
		return 3
	case SevInfo:
		return 4
	}
	return 5
}

type IssueStatus string

const (
	IssueOpen        IssueStatus = "open"
	IssueTaskCreated IssueStatus = "task_created"
	IssueResolved    IssueStatus = "resolved"
	IssueDismissed   IssueStatus = "dismissed"
)

var issueTransitions = map[IssueStatus][]IssueStatus{
	IssueOpen:        {IssueTaskCreated, IssueDismissed, IssueResolved},
	IssueTaskCreated: {IssueResolved, IssueOpen, IssueDismissed}, // → open: its tasks were deleted
	IssueResolved:    {IssueOpen},
	IssueDismissed:   {IssueOpen},
}

// CanTransitionIssue reports whether an issue may move between statuses.
func CanTransitionIssue(from, to IssueStatus) bool {
	for _, s := range issueTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// ErrInvalidIssueTransition is returned when an issue status change is not allowed.
type ErrInvalidIssueTransition struct{ From, To IssueStatus }

func (e ErrInvalidIssueTransition) Error() string {
	return fmt.Sprintf("invalid issue status change %s → %s", e.From, e.To)
}

// Normalize trims the issue's fields, fills in defaults (type "issue",
// severity "medium", source "manual") and checks the enums. It does not touch
// Status or Fingerprint.
func (i *Issue) Normalize() error {
	i.Title = strings.TrimSpace(i.Title)
	i.Details = strings.TrimSpace(i.Details)
	i.Fix = strings.TrimSpace(i.Fix)
	i.Rule = strings.TrimSpace(i.Rule)
	i.Source = strings.TrimSpace(i.Source)
	i.File = NormalizePath(i.File)
	if i.Line < 0 {
		i.Line = 0
	}
	if i.Title == "" {
		return fmt.Errorf("an issue needs a title")
	}
	switch i.Type {
	case "":
		i.Type = IssueOther
	case IssueBug, IssueVuln, IssueOther:
	default:
		return fmt.Errorf("unknown issue type %q (bug, vuln or issue)", i.Type)
	}
	if i.Severity == "" {
		i.Severity = SevMedium
	} else if i.Severity.Rank() > 4 {
		return fmt.Errorf("unknown severity %q (critical, high, medium, low or info)", i.Severity)
	}
	if i.Source == "" {
		i.Source = "manual"
	}
	return nil
}

// NormalizePath makes repository paths that name the same file compare equal:
// slashes, no leading "./" or "/", cleaned.
func NormalizePath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" {
		return ""
	}
	p = strings.TrimLeft(path.Clean(p), "/")
	if p == "." {
		return ""
	}
	return p
}

// ComputeFingerprint is the stable key of an issue: its type, file and rule (or,
// without a rule, its title, case- and space-insensitively). The line is left
// out so the issue keeps its identity when code above it moves.
func (i Issue) ComputeFingerprint() string {
	what := i.Rule
	if what == "" {
		what = strings.Join(strings.Fields(strings.ToLower(i.Title)), " ")
	}
	sum := sha256.Sum256([]byte(string(i.Type) + "\x00" + NormalizePath(i.File) + "\x00" + what))
	return hex.EncodeToString(sum[:8])
}

// Location is "file:line", "file" or "".
func (i Issue) Location() string {
	if i.File != "" && i.Line > 0 {
		return fmt.Sprintf("%s:%d", i.File, i.Line)
	}
	return i.File
}

// UpsertCounts is what adding a batch of issues did, by outcome.
type UpsertCounts struct {
	New       int `json:"new"`
	Updated   int `json:"updated"` // changed, or reopened after being resolved
	Known     int `json:"known"`   // already there, unchanged
	Dismissed int `json:"dismissed"`
}

// UpsertResult is the outcome of adding one issue.
type UpsertResult string

const (
	UpsertNew       UpsertResult = "new"
	UpsertUpdated   UpsertResult = "updated"
	UpsertKnown     UpsertResult = "known"
	UpsertDismissed UpsertResult = "dismissed" // matched a dismissed issue, which stays dismissed
)

// Add counts one result.
func (c *UpsertCounts) Add(r UpsertResult) {
	switch r {
	case UpsertNew:
		c.New++
	case UpsertUpdated:
		c.Updated++
	case UpsertKnown:
		c.Known++
	case UpsertDismissed:
		c.Dismissed++
	}
}

// TaskFromIssues is the title and prompt of a task that fixes issues, with
// everything known about each so the task can run without scanning again.
func TaskFromIssues(issues []Issue) (title, prompt string) {
	var b strings.Builder
	switch len(issues) {
	case 0:
		return "", ""
	case 1:
		title = "Fix: " + issues[0].Title
		b.WriteString("Fix the following issue.\n")
	default:
		title = fmt.Sprintf("Fix %d issues: %s", len(issues), issues[0].Title)
		b.WriteString("Fix the following issues. They may be instances of one pattern; fix them consistently.\n")
	}
	for n, i := range issues {
		if len(issues) == 1 {
			fmt.Fprintf(&b, "\n## %s\n\n", i.Title)
		} else {
			fmt.Fprintf(&b, "\n## %d. %s\n\n", n+1, i.Title)
		}
		fmt.Fprintf(&b, "- Type: %s\n- Severity: %s\n", i.Type, i.Severity)
		if loc := i.Location(); loc != "" {
			fmt.Fprintf(&b, "- Location: %s\n", loc)
		}
		if i.Rule != "" {
			fmt.Fprintf(&b, "- Rule: %s\n", i.Rule)
		}
		if i.Source != "" && i.Source != "manual" {
			fmt.Fprintf(&b, "- Reported by: %s\n", i.Source)
		}
		if i.Details != "" {
			fmt.Fprintf(&b, "\n### Details\n\n%s\n", i.Details)
		}
		if i.Fix != "" {
			fmt.Fprintf(&b, "\n### Suggested fix\n\n%s\n", i.Fix)
		}
	}
	b.WriteString("\nIf an issue turns out not to be real, say so in your final message instead of changing code for it.")
	return title, b.String()
}
