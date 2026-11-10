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
	// The built-ins stay next to the repository's templates.
	if len(ts) != 2+len(Builtins()) || len(problems) != 2 {
		t.Fatalf("templates: %+v, problems: %+v", ts, problems)
	}
	if a, _ := Pick(ts, "a"); a.Builtin || a.Origin != SourceRepo || a.Prompt != "A." {
		t.Fatalf("a: %+v", a)
	}
	if b, _ := Pick(ts, "b"); b.File != ".dash/scans/b.md" || b.Origin != SourceRepo {
		t.Fatalf("b: %+v", b)
	}
	if tm, ok := Find(repo, "security"); !ok || !tm.Builtin || tm.Origin != SourceBuiltin {
		t.Fatalf("security: %+v %v", tm, ok)
	}
	for i := 1; i < len(ts); i++ {
		if ts[i-1].Name >= ts[i].Name {
			t.Fatalf("not sorted by name: %+v", ts)
		}
	}
	if tm, ok := Find(repo, "b"); !ok || tm.Kind != KindSecurity {
		t.Fatalf("find: %+v %v", tm, ok)
	}
}

func TestLoadUnrelatedRepoScanKeepsBuiltins(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, ".dash", "scans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "licenses.md"), []byte("Check licenses."), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, problems := Load(repo)
	if len(problems) != 0 || len(ts) != len(Builtins())+1 {
		t.Fatalf("templates: %+v, problems: %+v", ts, problems)
	}
	for _, b := range Builtins() {
		if tm, ok := Pick(ts, b.Name); !ok || !tm.Builtin || tm.Origin != SourceBuiltin || tm.File != "" {
			t.Errorf("built-in %s: %+v %v", b.Name, tm, ok)
		}
	}
	if tm, ok := Pick(ts, "licenses"); !ok || tm.Origin != SourceRepo || tm.Builtin {
		t.Fatalf("licenses: %+v %v", tm, ok)
	}
}

func TestLoadRepoScanOverridesBuiltin(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, ".dash", "scans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("security.md", "---\nkind: security\n---\nOur own security scan.")
	write("sec2.md", "---\nname: security\n---\nAnother one.")
	ts, problems := Load(repo)
	// Shadowing a built-in is no problem; a second file of the name is.
	if len(problems) != 1 || problems[0].File != ".dash/scans/security.md" && problems[0].File != ".dash/scans/sec2.md" {
		t.Fatalf("problems: %+v", problems)
	}
	if len(ts) != len(Builtins()) {
		t.Fatalf("templates: %+v", ts)
	}
	n := 0
	for _, tm := range ts {
		if tm.Name == "security" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d scans called security: %+v", n, ts)
	}
	tm, ok := Find(repo, "security")
	if !ok || tm.Builtin || tm.Origin != SourceRepo || tm.File == "" || tm.Trusted() {
		t.Fatalf("security: %+v %v", tm, ok)
	}
	if tm, _ := Find(repo, "code-review"); !tm.Builtin {
		t.Fatalf("code-review: %+v", tm)
	}
}
