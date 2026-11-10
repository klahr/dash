// Package scan runs preconfigured, read-only Claude Code scans of a project
// (security, code review, dependency audit, tests, docs, performance, dead
// code, flaky tests, accessibility, breaking changes, or the team's own) on demand or
// on a schedule, and files what they find in the project's Issues.
//
// A scan template is a markdown file in the repository, .dash/scans/<name>.md:
//
//	---
//	name: security
//	kind: security
//	scope: since_last_scan
//	schedule: 0 6 * * *
//	effort: high
//	---
//	Look for vulnerabilities in {{path}} …
//
// It is the command presets (run | test | deploy) one level up: a prompt the
// team versions with the code instead of a shell command.
package scan

import (
	"bufio"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/klahr/dash/internal/model"

	"github.com/klahr/dash/internal/localrun"
)

// Dir is where a repository keeps its scan templates.
const Dir = ".dash/scans"

// Kinds of scan; a kind sets the type a finding gets when it names none
// (FindingType), and its findings' issues carry it (model.Issue.ScanKind).
const (
	KindSecurity        = model.ScanKindSecurity
	KindCodeReview      = model.ScanKindCodeReview
	KindDependencies    = model.ScanKindDependencies
	KindTests           = model.ScanKindTests
	KindDocs            = model.ScanKindDocs
	KindPerformance     = model.ScanKindPerformance
	KindDeadCode        = model.ScanKindDeadCode
	KindFlakyTests      = model.ScanKindFlakyTests
	KindAccessibility   = model.ScanKindAccessibility
	KindBreakingChanges = model.ScanKindBreakingChanges
	KindMaintainability = model.ScanKindMaintainability
	KindCustom          = model.ScanKindCustom
)

// NormalizeKind is kind's canonical name (see model.NormalizeScanKind):
// "perf" is performance, "a11y" accessibility, "dead-code" dead_code.
func NormalizeKind(kind string) string { return model.NormalizeScanKind(kind) }

// Template is one scan: its settings (the frontmatter) and its prompt (the body).
type Template struct {
	Name     string `json:"name"`
	Title    string `json:"title,omitempty"` // display name; defaults to Name
	Kind     string `json:"kind"`
	Scope    string `json:"scope"`
	Path     string `json:"path,omitempty"`     // limits the scan to a directory or file
	Model    string `json:"model,omitempty"`    // claude --model
	Effort   string `json:"effort,omitempty"`   // claude --effort
	Schedule string `json:"schedule,omitempty"` // cron expression; the default schedule
	// Tools are the Claude Code tools the scan may use (--allowedTools);
	// empty means read-only ones (DefaultTools). A repository template's
	// apply only once a user approves them in Dash (see Tools).
	Tools []string `json:"tools,omitempty"`
	// RequiresFiles are globs (see globRE) of which the scanned commit
	// must have a file for the scan to run; empty: it always runs. A run of
	// a repository without one is skipped before Claude starts.
	RequiresFiles []string `json:"requires_files,omitempty"`
	Prompt        string   `json:"prompt"`
	// File is the template's path in the repository; "" for a built-in.
	File    string `json:"file,omitempty"`
	Builtin bool   `json:"builtin,omitempty"`
	// Origin is where the template comes from: SourceBuiltin, SourceRepo
	// or SourceDash. "" (a template parsed on its own) counts as a
	// repository one.
	Origin string `json:"source,omitempty"`
	// ShadowedBy, on a repository template, is where the template of the
	// same name that is used instead comes from: SourceDash.
	ShadowedBy string `json:"shadowed_by,omitempty"`
}

// Where a template comes from.
const (
	SourceBuiltin = "builtin" // shipped with Dash
	SourceRepo    = "repo"    // .dash/scans/<name>.md in the repository
	SourceDash    = "dash"    // written in Dash (model.ScanTemplate)
)

// Trusted says the template's tools and schedule apply without approval: a
// built-in, or one a user wrote in Dash. A repository template is code
// anyone who can merge may change, so it needs approval.
func (t Template) Trusted() bool { return t.Builtin || t.Origin == SourceDash }

// DefaultTools only read: files, and git's view of what changed through the
// scan's git tools (see GitTools). Not Bash git rules: "Bash(git log:*)"
// allows any arguments, and git log/diff/show --output=<file> writes a file.
var DefaultTools = append([]string{"Read", "Grep", "Glob", "Task"}, gitAllowed()...)

var (
	nameRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	efforts = []string{"low", "medium", "high", "xhigh", "max"}
	scopes  = []string{model.ScopeRepo, model.ScopeSinceLastScan, model.ScopeSinceMain, model.ScopePath}
	kinds   = model.ScanKinds()
)

// ParseTemplate reads a template file. name is used when the frontmatter
// has none (the file name without .md).
func ParseTemplate(name string, data []byte) (Template, error) {
	t := Template{Name: name}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	body := text
	if rest, ok := strings.CutPrefix(text, "---\n"); ok {
		front, after, found := strings.Cut(rest, "\n---")
		if !found {
			return t, fmt.Errorf("frontmatter not closed with ---")
		}
		// The closing --- ends its line.
		if nl := strings.IndexByte(after, '\n'); nl >= 0 {
			if strings.TrimSpace(after[:nl]) != "" {
				return t, fmt.Errorf("frontmatter not closed with ---")
			}
			body = after[nl+1:]
		} else {
			body = ""
		}
		if err := t.setFields(front); err != nil {
			return t, err
		}
	}
	t.Prompt = strings.TrimSpace(body)
	return t, t.normalize()
}

func (t *Template) setFields(front string) error {
	sc := bufio.NewScanner(strings.NewReader(front))
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return fmt.Errorf("frontmatter line %d: want key: value", n)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = unquote(strings.TrimSpace(val))
		switch key {
		case "name":
			t.Name = val
		case "title":
			t.Title = val
		case "kind":
			t.Kind = val // normalize resolves case and aliases
		case "scope":
			t.Scope = strings.ToLower(val)
		case "path":
			t.Path = val
		case "model":
			t.Model = val
		case "effort":
			t.Effort = strings.ToLower(val)
		case "schedule", "cron":
			t.Schedule = val
		case "tools", "allowed_tools":
			t.Tools = splitList(val)
		case "requires_files":
			t.RequiresFiles = splitList(val)
		default:
			return fmt.Errorf("frontmatter line %d: unknown setting %q (name, title, kind, scope, path, model, effort, schedule, tools or requires_files)", n, key)
		}
	}
	return nil
}

func (t *Template) normalize() error {
	t.Name = strings.TrimSpace(t.Name)
	if !nameRE.MatchString(t.Name) {
		return fmt.Errorf("scan name %q: use letters, digits, '-', '_' and '.'", t.Name)
	}
	if t.Title == "" {
		t.Title = t.Name
	}
	if t.Kind == "" {
		t.Kind = KindCustom
	}
	t.Kind = NormalizeKind(t.Kind)
	if !contains(kinds, t.Kind) {
		return fmt.Errorf("unknown kind %q (%s, %s or %s)", t.Kind,
			strings.Join(kinds[:len(kinds)-2], ", "), kinds[len(kinds)-2], kinds[len(kinds)-1])
	}
	t.Path = model.NormalizePath(t.Path)
	if strings.HasPrefix(t.Path, "../") || t.Path == ".." {
		return fmt.Errorf("path %q is outside the repository", t.Path)
	}
	if t.Scope == "" {
		t.Scope = model.ScopeRepo
		if t.Path != "" {
			t.Scope = model.ScopePath
		}
	}
	t.Scope = strings.ReplaceAll(t.Scope, "-", "_")
	if !contains(scopes, t.Scope) {
		return fmt.Errorf("unknown scope %q (%s)", t.Scope, strings.Join(scopes, ", "))
	}
	if t.Scope == model.ScopePath && t.Path == "" {
		return fmt.Errorf("scope path needs a path")
	}
	if t.Effort != "" && !contains(efforts, t.Effort) {
		return fmt.Errorf("unknown effort %q (%s)", t.Effort, strings.Join(efforts, ", "))
	}
	if t.Schedule != "" {
		if _, err := ParseCron(t.Schedule); err != nil {
			return fmt.Errorf("schedule: %w", err)
		}
	}
	for _, g := range t.RequiresFiles {
		if _, err := globRE(g); err != nil {
			return fmt.Errorf("requires_files: %w", err)
		}
	}
	if t.Prompt == "" {
		return fmt.Errorf("the template has no prompt (the text after the frontmatter)")
	}
	return nil
}

// FindingType is the type of a finding that names none: a vulnerability
// for security and dependency scans; a bug where what is found is wrong
// behaviour (code review, a flaky test, a change that breaks callers, a UI
// some users can't use); otherwise an issue (a missing test or doc, slow,
// dead, duplicated or overly complex code, a custom scan's finding).
func (t Template) FindingType() model.IssueType {
	switch t.Kind {
	case KindSecurity, KindDependencies:
		return model.IssueVuln
	case KindCodeReview, KindFlakyTests, KindBreakingChanges, KindAccessibility:
		return model.IssueBug
	}
	return model.IssueOther
}

// Source is the issue source of the scan's findings.
func (t Template) Source() string { return "scan:" + t.Name }

var paramRE = regexp.MustCompile(`\{\{\s*(path|since)\s*\}\}`)

// Render is the template's prompt with {{path}} (the path scanned, "." for
// the whole repository) and {{since}} (the commit an incremental scan diffs
// against, else "") filled in.
func (t Template) Render(path, since string) string {
	if path == "" {
		path = "."
	}
	return paramRE.ReplaceAllStringFunc(t.Prompt, func(m string) string {
		if strings.Contains(m, "path") {
			return path
		}
		return since
	})
}

//go:embed builtin/*.md
var builtinFS embed.FS

// Builtins are the templates every project has, unless a repository or
// Dash scan of the same name replaces one.
func Builtins() []Template {
	entries, _ := fs.ReadDir(builtinFS, "builtin")
	var out []Template
	for _, e := range entries {
		b, _ := builtinFS.ReadFile("builtin/" + e.Name())
		t, err := ParseTemplate(strings.TrimSuffix(e.Name(), ".md"), b)
		if err != nil {
			panic(fmt.Sprintf("built-in scan %s: %v", e.Name(), err))
		}
		t.Builtin, t.Origin = true, SourceBuiltin
		out = append(out, t)
	}
	return out
}

// Problem is a template file that could not be read.
type Problem struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

// Load is the built-ins and the templates in repo/.dash/scans; a repository
// template replaces the built-in of the same name. Files that don't parse,
// and a second file using a name, are reported, not fatal.
func Load(repo string) ([]Template, []Problem) {
	dir := filepath.Join(repo, filepath.FromSlash(Dir))
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Builtins(), []Problem{{File: Dir, Error: err.Error()}}
	}
	var out []Template
	var problems []Problem
	seen := map[string]string{} // name: the repository file that uses it
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		rel := Dir + "/" + e.Name()
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err == nil && len(b) > 256<<10 {
			err = fmt.Errorf("larger than 256 KB")
		}
		if err != nil {
			problems = append(problems, Problem{File: rel, Error: err.Error()})
			continue
		}
		t, err := ParseTemplate(strings.TrimSuffix(e.Name(), ".md"), b)
		if err != nil {
			problems = append(problems, Problem{File: rel, Error: err.Error()})
			continue
		}
		if other, dup := seen[t.Name]; dup {
			problems = append(problems, Problem{File: rel, Error: fmt.Sprintf("scan name %q is also used by %s", t.Name, other)})
			continue
		}
		seen[t.Name] = rel
		t.File, t.Origin = rel, SourceRepo
		out = append(out, t)
	}
	for _, b := range Builtins() {
		if _, replaced := seen[b.Name]; !replaced {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, problems
}

// Pick is the template called name in ts.
func Pick(ts []Template, name string) (Template, bool) {
	for _, t := range ts {
		if t.Name == name {
			return t, true
		}
	}
	return Template{}, false
}

// Custom checks a Dash scan with the rules of a template file and fills in
// its defaults; the result is its template.
func Custom(m model.ScanTemplate) (Template, error) {
	t := Template{
		Name: m.Name, Title: strings.TrimSpace(m.Title), Kind: NormalizeKind(m.Kind),
		Scope: strings.ToLower(strings.TrimSpace(m.Scope)), Path: strings.TrimSpace(m.Path),
		Model: strings.TrimSpace(m.Model), Effort: strings.ToLower(strings.TrimSpace(m.Effort)),
		Schedule: strings.TrimSpace(m.Schedule), Prompt: strings.TrimSpace(m.Prompt),
		Origin: SourceDash,
	}
	for _, tool := range m.Tools {
		if tool = strings.TrimSpace(tool); tool != "" {
			t.Tools = append(t.Tools, tool)
		}
	}
	if len(t.Prompt) > 256<<10 {
		return t, fmt.Errorf("the prompt is larger than 256 KB")
	}
	if strings.ContainsAny(t.Model+t.Title, "\n\r") {
		return t, fmt.Errorf("title and model must be one line")
	}
	return t, t.normalize()
}

// Record is a Dash scan's stored record.
func (t Template) Record(projectID string) model.ScanTemplate {
	return model.ScanTemplate{
		ProjectID: projectID, Name: t.Name, Title: t.Title, Kind: t.Kind, Scope: t.Scope,
		Path: t.Path, Model: t.Model, Effort: t.Effort, Schedule: t.Schedule,
		Tools: append([]string(nil), t.Tools...), Prompt: t.Prompt,
	}
}

// Markdown is the template as a repository file, .dash/scans/<name>.md.
func (t Template) Markdown() string {
	var b strings.Builder
	b.WriteString("---\n")
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	line("name", t.Name)
	if t.Title != t.Name {
		line("title", t.Title)
	}
	line("kind", t.Kind)
	line("scope", t.Scope)
	line("path", t.Path)
	line("model", t.Model)
	line("effort", t.Effort)
	line("schedule", t.Schedule)
	line("tools", strings.Join(t.Tools, ", "))
	line("requires_files", strings.Join(t.RequiresFiles, ", "))
	b.WriteString("---\n")
	b.WriteString(t.Prompt)
	b.WriteString("\n")
	return b.String()
}

// TemplateSource has the Dash scans users wrote (a store, or one org's view of it).
type TemplateSource interface {
	ScanTemplates(projectID string) []model.ScanTemplate
}

// Templates are a project's scans: the built-ins, the repository's templates
// and the ones written in Dash. A name is used by one scan: a Dash scan
// replaces a repository template or built-in of its name, and a repository
// template replaces a built-in. A repository template a Dash scan replaces
// is returned in shadowed, with ShadowedBy SourceDash, and does not run; a
// replaced built-in is left out.
func Templates(st TemplateSource, p model.Project) (active, shadowed []Template, problems []Problem) {
	if p.Path == "" || !localrun.Allowed() {
		// The hub has no working tree of its projects (their clones are on
		// runners): the built-ins and the scans written in Dash.
		active = Builtins()
	} else {
		active, problems = Load(p.Path)
	}
	for _, m := range st.ScanTemplates(p.ID) {
		t, err := Custom(m)
		if err != nil {
			// Saved before a rule changed: show it, so it can be fixed. The
			// scan of its name, if any, keeps running meanwhile.
			problems = append(problems, Problem{File: "Dash scan " + m.Name, Error: err.Error()})
			continue
		}
		if i := slices.IndexFunc(active, func(o Template) bool { return o.Name == t.Name }); i >= 0 {
			if other := active[i]; other.Origin == SourceRepo {
				other.ShadowedBy = SourceDash
				shadowed = append(shadowed, other)
			}
			active[i] = t
			continue
		}
		active = append(active, t)
	}
	sort.SliceStable(active, func(i, j int) bool { return active[i].Name < active[j].Name })
	return active, shadowed, problems
}

// FindIn is the project's scan called name (not a shadowed one).
func FindIn(st TemplateSource, p model.Project, name string) (Template, bool) {
	ts, _, _ := Templates(st, p)
	return Pick(ts, name)
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

// splitList splits "a, b" or "[a, b]" on commas outside parentheses and
// braces, so "Bash(npm audit:*)" and "**/*.{htm,html}" stay whole.
func splitList(s string) []string {
	s = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s), "["), "]")
	var out []string
	depth, start := 0, 0
	for i, r := range s + "," {
		switch r {
		case '(', '{':
			depth++
		case ')', '}':
			depth--
		case ',':
			if depth <= 0 || i == len(s) { // an unclosed ( or { keeps its item
				if v := unquote(strings.TrimSpace(s[start:min(i, len(s))])); v != "" {
					out = append(out, v)
				}
				start = i + 1
			}
		}
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
