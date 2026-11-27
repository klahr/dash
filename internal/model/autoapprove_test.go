package model

import (
	"strings"
	"testing"
	"time"
)

// passing are the facts of a small task whose tests passed, which every
// rule lets through unless a case changes them.
func passing() AutoApproveFacts {
	return AutoApproveFacts{
		TestCommand: "go test ./...",
		Checks:      &Checks{Command: "go test ./...", Passed: true},
		Files:       []string{"docs/guide.md", "internal/x/x_test.go"},
		Lines:       12,
	}
}

func TestEvaluateAutoApprove(t *testing.T) {
	review := func(ok bool, line string) func(*AutoApproveFacts) {
		return func(f *AutoApproveFacts) { f.ReviewRan, f.ReviewOK, f.ReviewLine = true, ok, line }
	}
	cases := []struct {
		name  string
		rules AutoApproveRules
		facts func(*AutoApproveFacts)
		// fail is a part of the rule that failed; "" wants it approved.
		fail string
	}{
		{name: "defaults: tests passed, small diff", fail: ""},
		{name: "tests failed", facts: func(f *AutoApproveFacts) { f.Checks.Passed = false }, fail: "tests failed"},
		{name: "tests didn't run", facts: func(f *AutoApproveFacts) { f.Checks = nil }, fail: "didn't run"},
		{name: "no test command: tests skipped", facts: func(f *AutoApproveFacts) { f.TestCommand, f.Checks = "", nil }, fail: "no test command"},
		{name: "tests ran with another command", facts: func(f *AutoApproveFacts) { f.Checks.Command = "true" }, fail: "current test command"},
		{name: "diff at the limit", rules: AutoApproveRules{MaxLines: 12}},
		{name: "diff over the limit", rules: AutoApproveRules{MaxLines: 11}, fail: "12 lines, more than 11"},
		{name: "diff over the default limit", facts: func(f *AutoApproveFacts) { f.Lines = DefaultAutoApproveMaxLines + 1 }, fail: "more than 200"},
		{name: "only matching paths", rules: AutoApproveRules{Paths: []string{"docs/**", "**/*_test.go"}}},
		{name: "a path outside the globs", rules: AutoApproveRules{Paths: []string{"docs/**"}}, fail: "internal/x/x_test.go, outside docs/**"},
		{name: "review found no issues", rules: AutoApproveRules{Review: true}, facts: review(true, "Ready to merge")},
		{name: "review found issues", rules: AutoApproveRules{Review: true}, facts: review(false, "Needs changes"), fail: "review found issues: Needs changes"},
		{name: "review didn't run", rules: AutoApproveRules{Review: true}, fail: "review didn't run"},
		{name: "review off: its verdict doesn't count", facts: review(false, "Needs changes")},

		// Never, whatever the rules.
		{name: "never: a merge conflict", facts: func(f *AutoApproveFacts) { f.Conflict = true }, fail: "merge conflict"},
		{name: "never: the settings changed while it ran", facts: func(f *AutoApproveFacts) { f.SettingsChanged = true }, fail: "settings changed"},
		{name: "never: CLAUDE.md changed", rules: AutoApproveRules{Paths: []string{"**"}},
			facts: func(f *AutoApproveFacts) { f.Files = append(f.Files, "web/CLAUDE.md") }, fail: "web/CLAUDE.md"},
		{name: "never: .claude settings changed", facts: func(f *AutoApproveFacts) { f.Files = []string{".claude/settings.json"} }, fail: ".claude/settings.json"},
		{name: "never: .dash changed", facts: func(f *AutoApproveFacts) { f.Files = []string{".dash/scans/x.md"} }, fail: ".dash/scans/x.md"},
		{name: "never: the CI workflow changed", facts: func(f *AutoApproveFacts) { f.Files = []string{".github/workflows/ci.yml"} }, fail: "ci.yml"},
		{name: "never: the test script changed", facts: func(f *AutoApproveFacts) {
			f.TestCommand, f.Checks.Command = "./scripts/check.sh", "./scripts/check.sh"
			f.Files = []string{"scripts/check.sh"}
		}, fail: "scripts/check.sh"},
		{name: "never: a conflict, even with a review that passed", rules: AutoApproveRules{Review: true},
			facts: func(f *AutoApproveFacts) { review(true, "Ready to merge")(f); f.Conflict = true }, fail: "merge conflict"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := passing()
			if c.facts != nil {
				c.facts(&f)
			}
			got := EvaluateAutoApprove(c.rules, f)
			if c.fail == "" {
				if !got.Approved || got.Failed != "" {
					t.Fatalf("want approved, got %+v", got)
				}
				if len(got.Matched) == 0 || got.Matched[0] != "tests passed" {
					t.Errorf("matched rules = %q, want tests passed first", got.Matched)
				}
				return
			}
			if got.Approved || !strings.Contains(got.Failed, c.fail) {
				t.Fatalf("want refused for %q, got %+v", c.fail, got)
			}
		})
	}
}

// Every rule that held is recorded on an approved task.
func TestEvaluateAutoApproveMatched(t *testing.T) {
	f := passing()
	f.ReviewRan, f.ReviewOK = true, true
	got := EvaluateAutoApprove(AutoApproveRules{MaxLines: 50, Paths: []string{"docs/**", "**/*_test.go"}, Review: true}, f)
	want := []string{"tests passed", "at most 50 changed lines (12)", "only docs/**, **/*_test.go changed", "Claude's review found no issues"}
	if !got.Approved || strings.Join(got.Matched, "|") != strings.Join(want, "|") {
		t.Fatalf("got %+v, want approved with %q", got, want)
	}
}

func TestMatchPath(t *testing.T) {
	cases := []struct {
		glob, file string
		want       bool
	}{
		{"docs/**", "docs/a.md", true},
		{"docs/**", "docs/deep/b/c.md", true},
		{"docs/**", "web/docs/a.md", false},
		{"**/*_test.go", "a_test.go", true},
		{"**/*_test.go", "internal/x/a_test.go", true},
		{"**/*_test.go", "internal/x/a.go", false},
		{"*.md", "README.md", true},
		{"*.md", "docs/x/guide.md", true}, // no slash: any folder
		{"docs/*.md", "docs/x/guide.md", false},
		{"./docs/*.md", "docs/guide.md", true},
		{"**", "anything/at/all", true},
		{"web/**/x.js", "web/x.js", true},
		{"web/**/x.js", "web/a/b/x.js", true},
		{"[", "[", false}, // a bad pattern matches nothing
	}
	for _, c := range cases {
		if got := MatchPath(c.glob, c.file); got != c.want {
			t.Errorf("MatchPath(%q, %q) = %v, want %v", c.glob, c.file, got, c.want)
		}
	}
}

func TestTestScript(t *testing.T) {
	cases := map[string]string{
		"./scripts/check.sh":    "scripts/check.sh",
		"sh scripts/test.sh -v": "scripts/test.sh",
		"bash ./ci/run.sh":      "ci/run.sh",
		"go test ./...":         "",
		"npm test":              "",
		"/usr/bin/make test":    "",
		"../elsewhere/test.sh":  "",
		"$HOME/bin/t.sh":        "",
		"make -C sub/dir test":  "",
		`"./scripts/check.sh"`:  "scripts/check.sh",
	}
	for cmd, want := range cases {
		got := strings.Join(testScript(cmd), "")
		if got != want {
			t.Errorf("testScript(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestAutoApproveRulesClean(t *testing.T) {
	r, err := AutoApproveRules{MaxLines: -3, Paths: []string{" docs/** ", "", "./docs/**", "*.md"}, Review: true}.Clean()
	if err != nil {
		t.Fatal(err)
	}
	if r.MaxLines != 0 || strings.Join(r.Paths, ",") != "docs/**,*.md" || !r.Review || r.LineLimit() != DefaultAutoApproveMaxLines {
		t.Fatalf("cleaned = %+v", r)
	}
	for _, bad := range []string{"/etc/**", `docs\x`, "[a-"} {
		if _, err := (AutoApproveRules{Paths: []string{bad}}).Clean(); err == nil {
			t.Errorf("Clean accepted %q", bad)
		}
	}
}

// Changing the switch or the rules records when; saving them unchanged doesn't.
func TestSetAutoApproveRecordsChanges(t *testing.T) {
	var p Project
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p.SetAutoApprove(nil, &AutoApproveRules{}, t0)
	if p.AutoApproveChanged != nil {
		t.Fatal("the default rules saved again changed nothing")
	}
	on := true
	p.SetAutoApprove(&on, nil, t0)
	if p.AutoApproveChanged == nil || !p.AutoApproveChanged.Equal(t0) {
		t.Fatalf("turning it on: changed = %v", p.AutoApproveChanged)
	}
	t1 := t0.Add(time.Hour)
	p.SetAutoApprove(&on, &AutoApproveRules{Paths: []string{"docs/**"}}, t1)
	if !p.AutoApproveChanged.Equal(t1) || p.Rules().Paths[0] != "docs/**" {
		t.Fatalf("new paths: %+v, changed %v", p.Rules(), p.AutoApproveChanged)
	}
	p.SetAutoApprove(&on, &AutoApproveRules{Paths: []string{"docs/**"}}, t1.Add(time.Hour))
	if !p.AutoApproveChanged.Equal(t1) {
		t.Fatal("the same rules saved again moved the change time")
	}
}

func TestReviewVerdict(t *testing.T) {
	cases := []struct {
		answer string
		ok     bool
	}{
		{"Ready to merge\n\nNice-to-haves: …", true},
		{"\n  **Ready to merge.**\n", true},
		{"## Verdict: Ready to merge", true},
		{"Ready to merge!", true},
		{"Needs changes\n1. x.go:3 …", false},
		{"Ready to merge once the test is fixed", false},
		{"Ready to merge? No.", false},
		{"I think it's ready to merge", false},
		{"", false},
	}
	for _, c := range cases {
		if ok, _ := ReviewVerdict(c.answer); ok != c.ok {
			t.Errorf("ReviewVerdict(%q) = %v, want %v", c.answer, ok, c.ok)
		}
	}
	if _, line := ReviewVerdict("\n\nNeeds changes\nmore"); line != "Needs changes" {
		t.Errorf("line = %q", line)
	}
}

func TestAutoReviewPromptAsksForTheVerdict(t *testing.T) {
	for _, p := range []Project{{}, {ReviewPrompt: "Look for typos only."}} {
		got := AutoReviewPrompt(p)
		if !strings.Contains(got, `"`+VerdictReady+`"`) || !strings.Contains(got, `"`+VerdictChanges+`"`) {
			t.Errorf("prompt lacks the verdicts:\n%s", got)
		}
		if p.ReviewPrompt != "" && !strings.HasPrefix(got, p.ReviewPrompt) || p.ReviewPrompt == "" && !strings.HasPrefix(got, DefaultReviewPrompt) {
			t.Errorf("prompt doesn't start with the project's review prompt:\n%s", got)
		}
	}
}
