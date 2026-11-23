package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
)

// An Issue is something wrong with a project — a bug, a vulnerability or
// another problem — as it was reported: by a scan, by Claude during a task
// (report_issue) or by hand. Dash doesn't keep issues apart: each becomes a
// draft task on the Board (see Finding), or bumps the task that already has
// it, found by its fingerprint.
type Issue struct {
	ProjectID string    `json:"project_id,omitempty"`
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
	// Source is who reported it first: "manual", "scan:<name>", "agent:chat"
	// or "agent:<todo id>".
	Source string `json:"source"`
	// Reports is how many times it was reported, counting a report again only
	// when it comes from another reporter than the last one (so a scan that
	// finds it on every run counts once). 0 means 1; see ReportCount.
	Reports int `json:"reports,omitempty"`
	// LastSource is who reported it last, in Source's form; "" when only
	// Source did.
	LastSource string `json:"last_source,omitempty"`
	// Sources are everyone who reported it, each once, first reporter first.
	Sources []string `json:"sources,omitempty"`
	// ScanKind is the kind of scan that reported it (ScanKinds: "security",
	// "tests", "performance", …); "" when no scan did.
	ScanKind string `json:"scan_kind,omitempty"`
	// Fingerprint identifies the same issue across reports (see ComputeFingerprint).
	Fingerprint string `json:"fingerprint,omitempty"`
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

// HasGeneratedPrompt reports whether t's prompt is still the one Dash wrote
// from its Finding's issues (see Finding.PromptSum): nobody edited it.
func (t *Todo) HasGeneratedPrompt() bool {
	return t.Finding != nil && t.Finding.PromptSum != "" && t.Finding.PromptSum == PromptSum(t.Prompt)
}

// ReportCount is how many times the issue was reported: at least 1.
func (i Issue) ReportCount() int {
	return max(i.Reports, 1)
}

// LatestSource is who reported the issue last.
func (i Issue) LatestSource() string {
	if i.LastSource != "" {
		return i.LastSource
	}
	return i.Source
}

// AddReport counts a report of the issue from source, unless source is the
// last reporter again. It reports whether it counted one.
func (i *Issue) AddReport(source string) bool {
	i.noteSource(i.Source)
	if source == "" || source == i.LatestSource() {
		return false
	}
	i.Reports = i.ReportCount() + 1
	i.LastSource = source
	i.noteSource(source)
	return true
}

// noteSource adds source to Sources, once.
func (i *Issue) noteSource(source string) {
	if source != "" && !slices.Contains(i.Sources, source) {
		i.Sources = append(i.Sources, source)
	}
}

// Location is "file:line", "file" or "".
func (i Issue) Location() string {
	if i.File != "" && i.Line > 0 {
		return fmt.Sprintf("%s:%d", i.File, i.Line)
	}
	return i.File
}

// UpsertCounts is what reporting a batch of issues did, by outcome.
type UpsertCounts struct {
	New       int `json:"new"`       // a new draft
	Updated   int `json:"updated"`   // already on a task, which was bumped or changed
	Known     int `json:"known"`     // already on a task, unchanged
	Dismissed int `json:"dismissed"` // dismissed before, left out
	Reopened  int `json:"reopened"`  // a merged task had fixed it, and it came back: a new draft
}

// UpsertResult is the outcome of reporting one issue.
type UpsertResult string

const (
	UpsertNew       UpsertResult = "new"
	UpsertUpdated   UpsertResult = "updated"
	UpsertKnown     UpsertResult = "known"
	UpsertDismissed UpsertResult = "dismissed" // its fingerprint was dismissed: no draft
	UpsertReopened  UpsertResult = "reopened"  // a merged task had it: a new draft that names that task
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
	case UpsertReopened:
		c.Reopened++
	}
}

// TaskFromIssues is the title and prompt of a task that fixes issues, with
// everything known about each so the task can run without scanning again.
// reporter names a source ("agent:<todo id>" as its task, say) for the
// prompt; nil, or "" from it, leaves the source as it is.
func TaskFromIssues(issues []Issue, reporter func(source string) string) (title, prompt string) {
	who := func(src string) string {
		if reporter != nil {
			if s := reporter(src); s != "" {
				return s
			}
		}
		return src
	}
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
			fmt.Fprintf(&b, "- Reported by: %s\n", who(i.Source))
		}
		if n := i.ReportCount(); n > 1 {
			fmt.Fprintf(&b, "- Reported %d times, last by: %s\n", n, who(i.LatestSource()))
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

// IssueTypeTag is the tag of an issue type: bug, security or issue.
func IssueTypeTag(t IssueType) string {
	if t == IssueVuln {
		return "security"
	}
	return string(t)
}

// SourceTag is the tag of an issue's source: "scan:<name>" for a scan's,
// "reported" for anyone else's (Claude, or by hand).
func SourceTag(source string) string {
	if strings.HasPrefix(source, "scan:") {
		return strings.ToLower(source)
	}
	return "reported"
}

// Severities are the severities, most severe first.
var Severities = []Severity{SevCritical, SevHigh, SevMedium, SevLow, SevInfo}

// A Finding is what a task made from reported issues knows about them, kept
// apart from its prompt so later reports find the task by fingerprint and
// the Board can filter by severity. A task merged from several drafts has
// all of their issues.
type Finding struct {
	// Issues are the task's issues, one per fingerprint.
	Issues []Issue `json:"issues"`
	// Earlier is a merged task that had fixed one of them before they came
	// back (empty: none).
	Earlier string `json:"earlier,omitempty"`
	// LegacyIDs are the ids the issues had on the Issues page, before they
	// became tasks, so old links still find the task.
	LegacyIDs []string `json:"legacy_ids,omitempty"`
	// PromptSum is the checksum of the prompt Dash wrote from the issues
	// (PromptSum). While the task's prompt is still that one, a new report
	// writes it again; once someone edited it, it stays theirs. Empty: the
	// prompt isn't Dash's.
	PromptSum string `json:"prompt_sum,omitempty"`
}

// PromptSum is the checksum Finding.PromptSum keeps of a prompt.
func PromptSum(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:8])
}

// Clone copies f deeply.
func (f *Finding) Clone() *Finding {
	if f == nil {
		return nil
	}
	cp := *f
	cp.Issues = make([]Issue, len(f.Issues))
	for n, i := range f.Issues {
		i.Sources = slices.Clone(i.Sources)
		cp.Issues[n] = i
	}
	cp.LegacyIDs = slices.Clone(f.LegacyIDs)
	return &cp
}

// Index is the index of the issue with fingerprint fp, or -1.
func (f *Finding) Index(fp string) int {
	if f == nil {
		return -1
	}
	return slices.IndexFunc(f.Issues, func(i Issue) bool { return i.Fingerprint == fp })
}

// Fingerprints are the fingerprints of f's issues.
func (f *Finding) Fingerprints() []string {
	if f == nil {
		return nil
	}
	out := make([]string, len(f.Issues))
	for n, i := range f.Issues {
		out[n] = i.Fingerprint
	}
	return out
}

// Severity is the worst severity of f's issues ("" for none).
func (f *Finding) Severity() Severity {
	var worst Severity
	if f == nil {
		return worst
	}
	for _, i := range f.Issues {
		if worst == "" || i.Severity.Rank() < worst.Rank() {
			worst = i.Severity
		}
	}
	return worst
}

// Reports is how many times f's issues were reported, all together.
func (f *Finding) Reports() int {
	n := 0
	if f != nil {
		for _, i := range f.Issues {
			n += i.ReportCount()
		}
	}
	return n
}

// Tags are the tags f's issues give a task: each one's type and source, and
// the worst severity.
func (f *Finding) Tags() []string {
	var out []string
	if f == nil {
		return out
	}
	for _, i := range f.Issues {
		out = append(out, IssueTypeTag(i.Type))
	}
	if sev := f.Severity(); sev != "" {
		out = append(out, string(sev))
	}
	for _, i := range f.Issues {
		out = append(out, SourceTag(i.Source))
	}
	return out
}

// WithFindingTags is tags with f's tags added: a severity tag that isn't
// f's worst severity any more is taken out, and the other tags stay.
func WithFindingTags(tags []string, f *Finding) []string {
	sev := f.Severity()
	out := []string{}
	for _, t := range tags {
		if s := Severity(strings.ToLower(t)); sev != "" && s != sev && slices.Contains(Severities, s) {
			continue
		}
		out = append(out, t)
	}
	out = append(out, f.Tags()...)
	clean, err := CleanTags(out)
	if err != nil { // too many: keep the first that fit
		clean, _ = CleanTags(out[:min(len(out), MaxCardTags)])
	}
	return clean
}

// A DismissedFinding is an issue the user dismissed: they deleted the draft
// that had it. Reports of its fingerprint add no draft until it is undone.
type DismissedFinding struct {
	ProjectID   string    `json:"project_id"`
	Fingerprint string    `json:"fingerprint"`
	Title       string    `json:"title"`
	Type        IssueType `json:"type,omitempty"`
	Severity    Severity  `json:"severity,omitempty"`
	File        string    `json:"file,omitempty"`
	Rule        string    `json:"rule,omitempty"`
	Source      string    `json:"source,omitempty"`
	ScanKind    string    `json:"scan_kind,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	// TodoID is the task that was deleted, and TodoTitle its title.
	TodoID    string    `json:"todo_id,omitempty"`
	TodoTitle string    `json:"todo_title,omitempty"`
	By        int64     `json:"by,omitempty"` // who dismissed it (0: unknown)
	At        time.Time `json:"at"`
}

// Dismissal is the record of dismissing issue i of task t.
func Dismissal(t Todo, i Issue, reason string, by int64, at time.Time) DismissedFinding {
	return DismissedFinding{
		ProjectID: t.ProjectID, Fingerprint: i.Fingerprint, Title: i.Title, Type: i.Type, Severity: i.Severity,
		File: i.File, Rule: i.Rule, Source: i.Source, ScanKind: i.ScanKind, Reason: reason,
		TodoID: t.ID, TodoTitle: t.Title, By: by, At: at,
	}
}
