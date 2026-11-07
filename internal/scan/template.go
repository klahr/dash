// Package scan runs preconfigured, read-only Claude Code scans of a project
// (security, code review, dependency audit, or the team's own) on demand or
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
	"sort"
	"strings"

	"github.com/klahr/dash/internal/model"
)

// Dir is where a repository keeps its scan templates.
const Dir = ".dash/scans"

// Kinds of scan; a kind sets the type a finding gets when it names none.
const (
	KindSecurity     = "security"
	KindCodeReview   = "code_review"
	KindDependencies = "dependencies"
	KindCustom       = "custom"
)

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
	// empty means read-only ones (DefaultTools).
	Tools  []string `json:"tools,omitempty"`
	Prompt string   `json:"prompt"`
	// File is the template's path in the repository; "" for a built-in.
	File    string `json:"file,omitempty"`
	Builtin bool   `json:"builtin,omitempty"`
}

// DefaultTools only read: files, and git's view of what changed.
var DefaultTools = []string{"Read", "Grep", "Glob", "Task", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)", "Bash(git blame:*)"}

var (
	nameRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	efforts = []string{"low", "medium", "high", "xhigh", "max"}
	scopes  = []string{model.ScopeRepo, model.ScopeSinceLastScan, model.ScopeSinceMain, model.ScopePath}
	kinds   = []string{KindSecurity, KindCodeReview, KindDependencies, KindCustom}
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
			t.Kind = strings.ToLower(val)
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
			t.Tools = nil
			for _, tool := range splitList(val) {
				t.Tools = append(t.Tools, tool)
			}
		default:
			return fmt.Errorf("frontmatter line %d: unknown setting %q (name, title, kind, scope, path, model, effort, schedule or tools)", n, key)
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
	t.Kind = strings.ReplaceAll(t.Kind, "-", "_")
	if t.Kind == "review" {
		t.Kind = KindCodeReview
	}
	if t.Kind == "dependency" || t.Kind == "deps" {
		t.Kind = KindDependencies
	}
	if !contains(kinds, t.Kind) {
		return fmt.Errorf("unknown kind %q (%s)", t.Kind, strings.Join(kinds, ", "))
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
	if t.Prompt == "" {
		return fmt.Errorf("the template has no prompt (the text after the frontmatter)")
	}
	return nil
}

// FindingType is the type of a finding that names none.
func (t Template) FindingType() model.IssueType {
	switch t.Kind {
	case KindSecurity, KindDependencies:
		return model.IssueVuln
	case KindCodeReview:
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

// Builtins are the templates used for a repository without its own.
func Builtins() []Template {
	entries, _ := fs.ReadDir(builtinFS, "builtin")
	var out []Template
	for _, e := range entries {
		b, _ := builtinFS.ReadFile("builtin/" + e.Name())
		t, err := ParseTemplate(strings.TrimSuffix(e.Name(), ".md"), b)
		if err != nil {
			panic(fmt.Sprintf("built-in scan %s: %v", e.Name(), err))
		}
		t.Builtin = true
		out = append(out, t)
	}
	return out
}

// Problem is a template file that could not be read.
type Problem struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

// Load reads the templates in repo/.dash/scans; a repository without any
// gets the built-ins. Files that don't parse are reported, not fatal.
func Load(repo string) ([]Template, []Problem) {
	dir := filepath.Join(repo, filepath.FromSlash(Dir))
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Builtins(), []Problem{{File: Dir, Error: err.Error()}}
	}
	var out []Template
	var problems []Problem
	seen := map[string]string{}
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
		t.File = rel
		out = append(out, t)
	}
	if len(out) == 0 && len(problems) == 0 {
		return Builtins(), nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, problems
}

// Find is the template called name in repo.
func Find(repo, name string) (Template, bool) {
	ts, _ := Load(repo)
	for _, t := range ts {
		if t.Name == name {
			return t, true
		}
	}
	return Template{}, false
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

// splitList splits "a, b" or "[a, b]" on commas outside parentheses, so
// "Bash(npm audit:*)" stays whole.
func splitList(s string) []string {
	s = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s), "["), "]")
	var out []string
	depth, start := 0, 0
	for i, r := range s + "," {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth <= 0 {
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
