package scan

import (
	"os"
	"path/filepath"
	"slices"
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

func TestKinds(t *testing.T) {
	for _, c := range []struct {
		kind string
		want string
		typ  model.IssueType
	}{
		{"security", KindSecurity, model.IssueVuln},
		{"dependencies", KindDependencies, model.IssueVuln},
		{"deps", KindDependencies, model.IssueVuln},
		{"code_review", KindCodeReview, model.IssueBug},
		{"code-review", KindCodeReview, model.IssueBug},
		{"tests", KindTests, model.IssueOther},
		{"test", KindTests, model.IssueOther},
		{"Testing", KindTests, model.IssueOther},
		{"docs", KindDocs, model.IssueOther},
		{"doc", KindDocs, model.IssueOther},
		{"documentation", KindDocs, model.IssueOther},
		{"performance", KindPerformance, model.IssueOther},
		{"perf", KindPerformance, model.IssueOther},
		{"dead_code", KindDeadCode, model.IssueOther},
		{"dead-code", KindDeadCode, model.IssueOther},
		{"\"dead code\"", KindDeadCode, model.IssueOther},
		{"deadcode", KindDeadCode, model.IssueOther},
		{"unused", KindDeadCode, model.IssueOther},
		{"flaky_tests", KindFlakyTests, model.IssueBug},
		{"flaky", KindFlakyTests, model.IssueBug},
		{"flaky-test", KindFlakyTests, model.IssueBug},
		{"accessibility", KindAccessibility, model.IssueBug},
		{"a11y", KindAccessibility, model.IssueBug},
		{"A11Y", KindAccessibility, model.IssueBug},
		{"breaking_changes", KindBreakingChanges, model.IssueBug},
		{"breaking", KindBreakingChanges, model.IssueBug},
		{"breaking-change", KindBreakingChanges, model.IssueBug},
		{"custom", KindCustom, model.IssueOther},
	} {
		tm, err := ParseTemplate("x", []byte("---\nkind: "+c.kind+"\n---\nGo."))
		if err != nil {
			t.Errorf("%s: %v", c.kind, err)
			continue
		}
		if tm.Kind != c.want || tm.FindingType() != c.typ {
			t.Errorf("%s: kind %q type %q, want %q %q", c.kind, tm.Kind, tm.FindingType(), c.want, c.typ)
		}
		// The canonical name survives a round trip.
		back, err := ParseTemplate("x", []byte(tm.Markdown()))
		if err != nil || back.Kind != c.want {
			t.Errorf("%s: round trip %q %v", c.kind, back.Kind, err)
		}
	}
	if got := len(model.ScanKinds()); got != 11 {
		t.Errorf("Kinds: %d %v", got, model.ScanKinds())
	}
	// The error names every kind.
	_, err := ParseTemplate("x", []byte("---\nkind: vibes\n---\nGo."))
	if err == nil {
		t.Fatal("vibes accepted")
	}
	for _, k := range model.ScanKinds() {
		if !strings.Contains(err.Error(), k) {
			t.Errorf("error %q does not name %s", err, k)
		}
	}
}

func TestIssuesCarryScanKind(t *testing.T) {
	tm, err := ParseTemplate("a11y-check", []byte("---\nkind: a11y\n---\nGo."))
	if err != nil {
		t.Fatal(err)
	}
	is, errs := Issues(tm, "", []Finding{{Title: "Button has no label"}, {Title: "XSS", Type: "vuln"}})
	if len(errs) != 0 || len(is) != 2 {
		t.Fatalf("issues: %+v %v", is, errs)
	}
	for _, i := range is {
		if i.ScanKind != KindAccessibility || i.Source != "scan:a11y-check" {
			t.Errorf("issue: %+v", i)
		}
	}
	if is[0].Type != model.IssueBug || is[1].Type != model.IssueVuln {
		t.Errorf("types: %q %q", is[0].Type, is[1].Type)
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
		// Breaking changes compares a branch with the default branch, so it
		// suggests no schedule.
		if !b.Builtin || (b.Schedule == "") != (b.Name == "breaking-changes") {
			t.Errorf("%s: builtin %v schedule %q", b.Name, b.Builtin, b.Schedule)
		}
	}
	for _, n := range []string{"security", "code-review", "dependencies", "test-gaps", "docs-drift", "performance", "dead-code", "flaky-tests", "accessibility", "breaking-changes"} {
		if _, ok := names[n]; !ok {
			t.Errorf("no built-in %s (have %v)", n, names)
		}
	}
	if names["security"].Kind != KindSecurity || names["dependencies"].Kind != KindDependencies ||
		names["test-gaps"].Kind != KindTests || names["test-gaps"].Scope != model.ScopeSinceLastScan ||
		names["docs-drift"].Kind != KindDocs || names["docs-drift"].Scope != model.ScopeSinceLastScan ||
		names["performance"].Kind != KindPerformance || names["performance"].Scope != model.ScopeSinceLastScan ||
		names["dead-code"].Kind != KindDeadCode ||
		names["flaky-tests"].Kind != KindFlakyTests || names["flaky-tests"].Scope != model.ScopeRepo ||
		names["accessibility"].Kind != KindAccessibility || names["accessibility"].Scope != model.ScopeSinceLastScan ||
		names["breaking-changes"].Kind != KindBreakingChanges || names["breaking-changes"].Scope != model.ScopeSinceMain {
		t.Errorf("kinds: %+v", names)
	}
	// Only the accessibility scan is limited to some files: web front ends.
	for _, b := range bs {
		if (len(b.RequiresFiles) > 0) != (b.Name == "accessibility") {
			t.Errorf("%s: requires_files %v", b.Name, b.RequiresFiles)
		}
	}
	a11y := names["accessibility"]
	for _, f := range []string{"index.html", "web/src/App.tsx", "ui/Button.jsx", "src/App.vue", "src/routes/+page.svelte", "internal/web/templates/page.gohtml", "tmpl/base.tmpl"} {
		if !requiresMatch(t, a11y.RequiresFiles, f) {
			t.Errorf("accessibility scan doesn't apply to %s", f)
		}
	}
	for _, f := range []string{"main.go", "internal/api/server.ts", "README.md", "styles.css"} {
		if requiresMatch(t, a11y.RequiresFiles, f) {
			t.Errorf("accessibility scan applies to %s", f)
		}
	}
	// The performance review covers N+1 queries, unbounded work, indexes and
	// slow handlers, and asks for the cost and the fix.
	perf := names["performance"].Render("", "")
	for _, want := range []string{"N+1", "Unbounded", "indexes", "request handlers", "plausible real cost", "suggested fix"} {
		if !strings.Contains(perf, want) {
			t.Errorf("performance prompt lacks %q", want)
		}
	}
	// The flaky-test scan runs the suite, so it needs a shell, and it repeats
	// the runs within a time cap.
	flaky := names["flaky-tests"]
	if !slices.Contains(flaky.Tools, "Bash") {
		t.Errorf("flaky-tests tools: %q", flaky.Tools)
	}
	fp := flaky.Render("", "")
	for _, want := range []string{"3 to 5 runs", "-shuffle=on", "25 minutes", "timeout", "timing", "ordering", "shared state", "network", "Do not change any files"} {
		if !strings.Contains(fp, want) {
			t.Errorf("flaky-tests prompt lacks %q", want)
		}
	}
	// The dependency audit covers vulnerabilities, outdated packages and licences.
	deps := names["dependencies"].Render("", "")
	for _, want := range []string{"vulnerable", "abandoned", "project's own", "clashes", "no licence", "licence changed", "Do not change any files"} {
		if !strings.Contains(deps, want) {
			t.Errorf("dependency audit prompt lacks %q", want)
		}
	}
	// The dead code scan covers the whole repository once a month and checks
	// "unused" against indirect references; old TODOs are dated with blame.
	dc := names["dead-code"]
	if dc.Scope != model.ScopeRepo || dc.Schedule != "0 6 1 * *" {
		t.Errorf("dead-code: scope %q schedule %q", dc.Scope, dc.Schedule)
	}
	dead := dc.Render("", "")
	for _, want := range []string{"whole", "reflection", "templates", "as a string", "exported API of a library", "git_blame", "90 days", "FIXME", "Do not change any files"} {
		if !strings.Contains(dead, want) {
			t.Errorf("dead code prompt lacks %q", want)
		}
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
	if tm, ok := Pick(ts, "security"); !ok || !tm.Builtin || tm.Origin != SourceBuiltin {
		t.Fatalf("security: %+v %v", tm, ok)
	}
	for i := 1; i < len(ts); i++ {
		if ts[i-1].Name >= ts[i].Name {
			t.Fatalf("not sorted by name: %+v", ts)
		}
	}
	if tm, ok := Pick(ts, "b"); !ok || tm.Kind != KindSecurity {
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
	tm, ok := Pick(ts, "security")
	if !ok || tm.Builtin || tm.Origin != SourceRepo || tm.File == "" || tm.Trusted() {
		t.Fatalf("security: %+v %v", tm, ok)
	}
	if tm, _ := Pick(ts, "code-review"); !tm.Builtin {
		t.Fatalf("code-review: %+v", tm)
	}
}

func requiresMatch(t *testing.T, globs []string, file string) bool {
	t.Helper()
	for _, g := range globs {
		re, err := globRE(g)
		if err != nil {
			t.Fatalf("%s: %v", g, err)
		}
		if re.MatchString(file) {
			return true
		}
	}
	return false
}

func TestRequiresFiles(t *testing.T) {
	tm, err := ParseTemplate("w", []byte("---\nrequires_files: [*.html, \"src/**/*.{ts,tsx}\", web/*.vue]\n---\nGo."))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"*.html", "src/**/*.{ts,tsx}", "web/*.vue"}; !slices.Equal(tm.RequiresFiles, want) {
		t.Fatalf("requires_files %q, want %q", tm.RequiresFiles, want)
	}
	if !strings.Contains(tm.Markdown(), "requires_files: *.html, src/**/*.{ts,tsx}, web/*.vue\n") {
		t.Errorf("markdown:\n%s", tm.Markdown())
	}
	for file, want := range map[string]bool{
		"index.html": true, "a/b/index.html": true, "index.htmlx": false,
		"src/x.ts": true, "src/a/b/x.tsx": true, "lib/x.ts": false, "src/x.js": false,
		"web/App.vue": true, "web/sub/App.vue": false, "x/web/App.vue": false,
	} {
		if got := requiresMatch(t, tm.RequiresFiles, file); got != want {
			t.Errorf("%s: match %v, want %v", file, got, want)
		}
	}
	for _, bad := range []string{"*.{html", "*.html}", "{a,{b}}"} {
		if _, err := ParseTemplate("w", []byte("---\nrequires_files: x, "+bad+"\n---\nGo.")); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}

	// In a checkout: under the template's path, not in node_modules.
	dir := t.TempDir()
	for _, f := range []string{"main.go", "node_modules/pkg/index.html", "web/app.vue"} {
		_ = os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		globs []string
		path  string
		want  bool
	}{
		{nil, "", true},
		{[]string{"*.html"}, "", false},
		{[]string{"*.vue"}, "", true},
		{[]string{"*.vue"}, "web", true},
		{[]string{"*.vue"}, "internal", false},
		{[]string{"web/*.vue"}, "web", true},
	} {
		got, err := requiresMet(Template{RequiresFiles: c.globs, Path: c.path}, dir)
		if err != nil || got != c.want {
			t.Errorf("%v under %q: %v %v, want %v", c.globs, c.path, got, err, c.want)
		}
	}
}
