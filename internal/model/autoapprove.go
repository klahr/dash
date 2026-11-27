package model

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
)

// AutoApproveRules are the conditions a finished task must meet for Dash to
// approve it on its own (Project.AutoApprove on): its tests passed, its
// diff is small, it changed only some paths and, if asked, Claude's review
// found no issues. Every one must hold. See EvaluateAutoApprove.
type AutoApproveRules struct {
	// MaxLines is the most changed lines (added plus deleted) a task may
	// have; 0 means DefaultAutoApproveMaxLines.
	MaxLines int `json:"max_lines,omitempty"`
	// Paths, if any, are globs every changed file must match, e.g.
	// "docs/**" or "**/*_test.go" (see MatchPath). Empty: any file.
	Paths []string `json:"paths,omitempty"`
	// Review runs Claude's review (the task chat's Review, with the
	// project's ReviewPrompt) when the task finishes, and asks that it
	// found no issues.
	Review bool `json:"review,omitempty"`
}

const (
	// DefaultAutoApproveMaxLines is AutoApproveRules.MaxLines when unset.
	DefaultAutoApproveMaxLines = 200
	// MaxAutoApproveLines bounds AutoApproveRules.MaxLines.
	MaxAutoApproveLines = 100_000
	// maxAutoApprovePaths bounds AutoApproveRules.Paths.
	maxAutoApprovePaths = 50
)

// LineLimit is the most changed lines a task may have (MaxLines, else the
// default).
func (r AutoApproveRules) LineLimit() int {
	if r.MaxLines <= 0 {
		return DefaultAutoApproveMaxLines
	}
	return min(r.MaxLines, MaxAutoApproveLines)
}

// Clean trims r's globs, drops empty and repeated ones, and checks them.
func (r AutoApproveRules) Clean() (AutoApproveRules, error) {
	out := AutoApproveRules{MaxLines: max(0, min(r.MaxLines, MaxAutoApproveLines)), Review: r.Review}
	for _, g := range r.Paths {
		g = strings.TrimPrefix(strings.TrimSpace(g), "./")
		if g == "" || slices.Contains(out.Paths, g) {
			continue
		}
		if strings.HasPrefix(g, "/") || strings.Contains(g, `\`) {
			return r, fmt.Errorf("path %q: use paths relative to the repository, with forward slashes", g)
		}
		if _, err := path.Match(strings.ReplaceAll(g, "**", "*"), ""); err != nil {
			return r, fmt.Errorf("path %q: %v", g, err)
		}
		out.Paths = append(out.Paths, g)
	}
	if len(out.Paths) > maxAutoApprovePaths {
		return r, fmt.Errorf("at most %d paths", maxAutoApprovePaths)
	}
	return out, nil
}

// Rules is p's auto-approve rules: AutoApproveRules, or the defaults.
func (p Project) Rules() AutoApproveRules {
	if p.AutoApproveRules == nil {
		return AutoApproveRules{}
	}
	return *p.AutoApproveRules
}

// SetAutoApprove sets p's auto-approve switch and rules (nil keeps them),
// and records when either changed (AutoApproveChanged), so a task that ran
// meanwhile is never approved on its own.
func (p *Project) SetAutoApprove(on *bool, rules *AutoApproveRules, now time.Time) {
	before := p.AutoApprove
	beforeRules := p.Rules()
	if on != nil {
		p.AutoApprove = *on
	}
	if rules != nil {
		r := *rules
		p.AutoApproveRules = &r
	}
	after := p.Rules()
	if before != p.AutoApprove || beforeRules.MaxLines != after.MaxLines || beforeRules.Review != after.Review || !slices.Equal(beforeRules.Paths, after.Paths) {
		p.AutoApproveChanged = &now
	}
}

// AutoApproval is what the auto-approve rules made of a finished task.
type AutoApproval struct {
	// Approved: every rule held and the task went on to merge.
	Approved bool `json:"approved"`
	// Matched are the rules that held, as people read them.
	Matched []string `json:"matched,omitempty"`
	// Failed is the rule that didn't, when it wasn't approved.
	Failed string `json:"failed,omitempty"`
	// Review is the first line of Claude's review, when one ran.
	Review string    `json:"review,omitempty"`
	At     time.Time `json:"at"`
}

// AutoApproveFacts are what a finished task's rules are checked against.
type AutoApproveFacts struct {
	// TestCommand is the project's test command; Checks the task's latest
	// test run (nil: none ran).
	TestCommand string
	Checks      *Checks
	// Files are the files the task changed; Lines how many lines it added
	// and deleted.
	Files []string
	Lines int
	// Conflict: the task had a merge conflict (Todo.HadConflict, MergeMain).
	Conflict bool
	// SettingsChanged: the project's auto-approve settings changed while
	// the task ran.
	SettingsChanged bool
	// ReviewRan says Claude's review ran, ReviewOK that it found no issues,
	// and ReviewLine is its verdict (or why it couldn't run).
	ReviewRan  bool
	ReviewOK   bool
	ReviewLine string
}

// RulesFiles are files a task may never change and still be approved on
// its own: what tells Claude and Dash how to work (CLAUDE.md, .claude,
// .dash), and the CI that runs the tests. The script the test command runs
// is added by EvaluateAutoApprove.
var RulesFiles = []string{"**/CLAUDE.md", "**/CLAUDE.local.md", ".claude/**", ".dash/**", ".github/workflows/**"}

// EvaluateAutoApprove checks a finished task against rules: whether it may
// be approved on its own, which rules held and, if not, the first that
// didn't. It is never approved after a merge conflict, a test run that
// failed or didn't run, or a change to the rules (RulesFiles, the test
// script, the project's auto-approve settings).
func EvaluateAutoApprove(rules AutoApproveRules, f AutoApproveFacts) AutoApproval {
	out := AutoApproval{}
	fail := func(why string) AutoApproval {
		out.Failed = why
		return out
	}
	if f.Conflict {
		return fail("it had a merge conflict")
	}
	if f.SettingsChanged {
		return fail("the auto-approve settings changed while it ran")
	}
	switch c := f.Checks; {
	case strings.TrimSpace(f.TestCommand) == "":
		return fail("the project has no test command, so no tests ran")
	case c == nil:
		return fail("its tests didn't run")
	case c.Command != f.TestCommand:
		return fail("its tests didn't run with the current test command")
	case !c.Passed:
		return fail("its tests failed")
	}
	out.Matched = append(out.Matched, "tests passed")
	protected := append(slices.Clone(RulesFiles), testScript(f.TestCommand)...)
	for _, file := range f.Files {
		for _, g := range protected {
			if MatchPath(g, file) {
				return fail("it changed " + file + ", which sets how tasks are checked")
			}
		}
	}
	if limit := rules.LineLimit(); f.Lines > limit {
		return fail(fmt.Sprintf("it changed %d lines, more than %d", f.Lines, limit))
	}
	out.Matched = append(out.Matched, fmt.Sprintf("at most %d changed lines (%d)", rules.LineLimit(), f.Lines))
	if len(rules.Paths) > 0 {
		for _, file := range f.Files {
			if !slices.ContainsFunc(rules.Paths, func(g string) bool { return MatchPath(g, file) }) {
				return fail("it changed " + file + ", outside " + strings.Join(rules.Paths, ", "))
			}
		}
		out.Matched = append(out.Matched, "only "+strings.Join(rules.Paths, ", ")+" changed")
	}
	if rules.Review {
		out.Review = f.ReviewLine
		switch {
		case !f.ReviewRan:
			return fail("Claude's review didn't run" + suffix(f.ReviewLine))
		case !f.ReviewOK:
			return fail("Claude's review found issues" + suffix(f.ReviewLine))
		}
		out.Matched = append(out.Matched, "Claude's review found no issues")
	}
	out.Approved = true
	return out
}

func suffix(s string) string {
	if s == "" {
		return ""
	}
	return ": " + s
}

// testScript is the script in the repository a test command runs, if it
// starts with one (e.g. "./scripts/check.sh" or "sh scripts/check.sh"), as
// a path relative to the repository.
func testScript(cmd string) []string {
	fields := strings.Fields(cmd)
	for i, f := range fields {
		if i > 1 {
			break
		}
		if i == 1 && !slices.Contains([]string{"sh", "bash", "zsh", "python", "python3", "node", "pwsh"}, fields[0]) {
			break
		}
		f = strings.Trim(f, `"'`)
		if !strings.Contains(f, "/") || strings.HasPrefix(f, "/") || strings.HasPrefix(f, "~") || strings.HasPrefix(f, "$") {
			continue
		}
		if f = path.Clean(f); !strings.HasPrefix(f, "../") {
			return []string{f}
		}
	}
	return nil
}

// MatchPath reports whether file, a slash-separated path relative to the
// repository, matches glob: path.Match's patterns per path segment, where
// "**" as a whole segment matches any number of segments (none too), so
// "docs/**" matches every file under docs and "**/*_test.go" every Go
// test file. A glob without a slash matches the file's name in any folder.
func MatchPath(glob, file string) bool {
	glob = strings.TrimPrefix(glob, "./")
	if !strings.Contains(glob, "/") && glob != "**" {
		glob = "**/" + glob
	}
	return matchSegs(strings.Split(glob, "/"), strings.Split(file, "/"))
}

func matchSegs(g, f []string) bool {
	for len(g) > 0 {
		if g[0] == "**" {
			for i := 0; i <= len(f); i++ {
				if matchSegs(g[1:], f[i:]) {
					return true
				}
			}
			return false
		}
		if len(f) == 0 {
			return false
		}
		if ok, err := path.Match(g[0], f[0]); err != nil || !ok {
			return false
		}
		g, f = g[1:], f[1:]
	}
	return len(f) == 0
}
