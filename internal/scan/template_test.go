package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

func TestParseTemplate(t *testing.T) {
	src := "---\r\n" +
		"# a comment\r\n" +
		"name: sec\r\n" +
		"kind: security\r\n" +
		"scope: since-last-scan\r\n" +
		"path: ./internal/\r\n" +
		"model: \"opus\"\r\n" +
		"effort: High\r\n" +
		"schedule: '0 6 * * 1-5'\r\n" +
		"tools: [Read, Grep, Bash(npm audit:*, --json)]\r\n" +
		"---\r\n" +
		"Scan {{path}} since {{ since }}.\r\n"
	tm, err := ParseTemplate("file-name", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := Template{
		Name: "sec", Title: "sec", Kind: KindSecurity, Scope: model.ScopeSinceLastScan, Path: "internal",
		Model: "opus", Effort: "high", Schedule: "0 6 * * 1-5",
		Tools:  []string{"Read", "Grep", "Bash(npm audit:*, --json)"},
		Prompt: "Scan {{path}} since {{ since }}.",
	}
	if strings.Join(tm.Tools, "|") != strings.Join(want.Tools, "|") {
		t.Fatalf("tools %q", tm.Tools)
	}
	tm.Tools, want.Tools = nil, nil
	if tm.Name != want.Name || tm.Kind != want.Kind || tm.Scope != want.Scope || tm.Path != want.Path ||
		tm.Model != want.Model || tm.Effort != want.Effort || tm.Schedule != want.Schedule || tm.Prompt != want.Prompt {
		t.Fatalf("got %+v\nwant %+v", tm, want)
	}
	if got := tm.Render("internal", "abc123"); got != "Scan internal since abc123." {
		t.Fatalf("render: %q", got)
	}
	if got := tm.Render("", ""); got != "Scan . since ." {
		t.Fatalf("render defaults: %q", got)
	}
	if tm.FindingType() != model.IssueVuln || tm.Source() != "scan:sec" {
		t.Fatalf("type %s source %s", tm.FindingType(), tm.Source())
	}
}

func TestParseTemplateDefaults(t *testing.T) {
	tm, err := ParseTemplate("lint", []byte("Find problems."))
	if err != nil {
		t.Fatal(err)
	}
	if tm.Name != "lint" || tm.Kind != KindCustom || tm.Scope != model.ScopeRepo || tm.Prompt != "Find problems." || tm.FindingType() != model.IssueOther {
		t.Fatalf("defaults: %+v", tm)
	}
	// A path without a scope scans the path.
	tm, err = ParseTemplate("x", []byte("---\npath: web\n---\nGo."))
	if err != nil || tm.Scope != model.ScopePath {
		t.Fatalf("path scope: %+v %v", tm, err)
	}
	if tm, _ := ParseTemplate("x", []byte("---\nkind: review\n---\nGo.")); tm.Kind != KindCodeReview || tm.FindingType() != model.IssueBug {
		t.Fatalf("review alias: %+v", tm)
	}
}

func TestParseTemplateErrors(t *testing.T) {
	for name, src := range map[string]string{
		"unclosed":      "---\nname: x\nGo.",
		"unknown key":   "---\nshedule: 0 6 * * *\n---\nGo.",
		"no colon":      "---\nname x\n---\nGo.",
		"bad scope":     "---\nscope: everything\n---\nGo.",
		"path scope":    "---\nscope: path\n---\nGo.",
		"outside":       "---\npath: ../other\n---\nGo.",
		"bad kind":      "---\nkind: vibes\n---\nGo.",
		"bad effort":    "---\neffort: huge\n---\nGo.",
		"bad cron":      "---\nschedule: every day\n---\nGo.",
		"no prompt":     "---\nname: x\n---\n\n",
		"bad name":      "---\nname: ../x\n---\nGo.",
		"trailing text": "---\nname: x\n---junk\nGo.",
	} {
		if _, err := ParseTemplate("ok", []byte(src)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestBuiltins(t *testing.T) {
	bs := Builtins()
	names := map[string]Template{}
	for _, b := range bs {
		names[b.Name] = b
		if !b.Builtin || b.Schedule == "" {
			t.Errorf("%s: builtin %v schedule %q", b.Name, b.Builtin, b.Schedule)
		}
	}
	for _, n := range []string{"security", "code-review", "dependencies"} {
		if _, ok := names[n]; !ok {
			t.Errorf("no built-in %s (have %v)", n, names)
		}
	}
	if names["security"].Kind != KindSecurity || names["dependencies"].Kind != KindDependencies {
		t.Errorf("kinds: %+v", names)
	}
}

func TestLoad(t *testing.T) {
	repo := t.TempDir()
	// No templates: the built-ins.
	ts, problems := Load(repo)
	if len(ts) != len(Builtins()) || len(problems) != 0 || !ts[0].Builtin {
		t.Fatalf("empty repo: %d templates, %v", len(ts), problems)
	}
	dir := filepath.Join(repo, ".dash", "scans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("b.md", "---\nkind: security\n---\nB.")
	write("a.md", "A.")
	write("broken.md", "---\nscope: nope\n---\nX.")
	write("dup.md", "---\nname: a\n---\nAgain.")
	write("notes.txt", "ignored")
	ts, problems = Load(repo)
	if len(ts) != 2 || ts[0].Name != "a" || ts[1].Name != "b" || ts[0].Builtin || ts[1].File != ".dash/scans/b.md" {
		t.Fatalf("templates: %+v", ts)
	}
	if len(problems) != 2 {
		t.Fatalf("problems: %+v", problems)
	}
	if _, ok := Find(repo, "security"); ok {
		t.Fatal("built-ins used although the repo has templates")
	}
	if tm, ok := Find(repo, "b"); !ok || tm.Kind != KindSecurity {
		t.Fatalf("find: %+v %v", tm, ok)
	}
}
