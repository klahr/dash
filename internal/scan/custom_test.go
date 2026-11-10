package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

func TestCustomValidation(t *testing.T) {
	tm, err := Custom(model.ScanTemplate{Name: "lint", Kind: "Review", Path: "./web/", Prompt: " Look at {{path}}. "})
	if err != nil {
		t.Fatal(err)
	}
	if tm.Kind != KindCodeReview || tm.Scope != model.ScopePath || tm.Path != "web" || tm.Title != "lint" ||
		tm.Prompt != "Look at {{path}}." || tm.Origin != SourceDash || !tm.Trusted() {
		t.Fatalf("normalized: %+v", tm)
	}
	for kind, want := range map[string]string{
		"tests": KindTests, " Test ": KindTests, "docs": KindDocs, "PERF": KindPerformance, "dead-code": KindDeadCode,
		"flaky": KindFlakyTests, "a11y": KindAccessibility, "breaking": KindBreakingChanges, "": KindCustom,
	} {
		tm, err := Custom(model.ScanTemplate{Name: "k", Kind: kind, Prompt: "x"})
		if err != nil || tm.Kind != want || tm.Record("p").Kind != want {
			t.Errorf("kind %q: %q %v", kind, tm.Kind, err)
		}
	}
	for _, bad := range []model.ScanTemplate{
		{Name: "ok", Kind: "speed", Prompt: "x"},
		{Name: "ok", Kind: "flaky tests and more", Prompt: "x"},
		{Name: "", Prompt: "x"},
		{Name: "a b", Prompt: "x"},
		{Name: "../x", Prompt: "x"},
		{Name: "ok", Kind: "nope", Prompt: "x"},
		{Name: "ok", Scope: "nope", Prompt: "x"},
		{Name: "ok", Scope: "path", Prompt: "x"},
		{Name: "ok", Path: "../out", Prompt: "x"},
		{Name: "ok", Effort: "huge", Prompt: "x"},
		{Name: "ok", Schedule: "every night", Prompt: "x"},
		{Name: "ok", Prompt: "  "},
		{Name: "ok", Title: "two\nlines", Prompt: "x"},
	} {
		if _, err := Custom(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestMarkdownRoundTrip(t *testing.T) {
	tm, err := Custom(model.ScanTemplate{Name: "deps", Title: "Deps", Kind: "dependencies", Scope: "since_last_scan",
		Model: "opus", Effort: "high", Schedule: "0 6 * * 1", Tools: []string{"Read", "Bash(npm audit:*)"}, Prompt: "Audit {{since}}."})
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseTemplate("x", []byte(tm.Markdown()))
	if err != nil {
		t.Fatal(err)
	}
	back.Origin = SourceDash
	if back.Name != tm.Name || back.Title != tm.Title || back.Kind != tm.Kind || back.Scope != tm.Scope || back.Model != tm.Model ||
		back.Effort != tm.Effort || back.Schedule != tm.Schedule || len(back.Tools) != 2 || back.Tools[1] != "Bash(npm audit:*)" || back.Prompt != tm.Prompt {
		t.Fatalf("round trip:\n%+v\n%+v", tm, back)
	}
}

func TestTemplatesMergeAndShadow(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	p, _ := st.CreateProject(model.Project{Name: "p", Path: repo})
	save := func(name string) {
		t.Helper()
		if _, err := st.SaveScanTemplate(model.ScanTemplate{ProjectID: p.ID, Name: name, Prompt: "Dash " + name + "."}, true); err != nil {
			t.Fatal(err)
		}
	}
	save("mine")
	save("security") // the built-in's name

	// No repository templates: the built-ins next to the Dash ones; a Dash
	// scan replaces the built-in of its name, which is not listed.
	active, shadowed, problems := Templates(st, p)
	if len(active) != len(Builtins())+1 || len(problems) != 0 || len(shadowed) != 0 {
		t.Fatalf("active: %+v shadowed: %+v %v", active, shadowed, problems)
	}
	if tm, ok := FindIn(st, p, "mine"); !ok || tm.Origin != SourceDash || tm.Prompt != "Dash mine." {
		t.Fatalf("find mine: %+v", tm)
	}
	if tm, _ := FindIn(st, p, "security"); tm.Builtin || tm.Origin != SourceDash || tm.Prompt != "Dash security." {
		t.Fatalf("security: %+v", tm)
	}
	if tm, _ := FindIn(st, p, "code-review"); !tm.Builtin || tm.Origin != SourceBuiltin {
		t.Fatalf("code-review: %+v", tm)
	}

	// Repository templates: one replaces a built-in, and Dash scans replace
	// repository templates and built-ins alike.
	dir := filepath.Join(repo, ".dash", "scans")
	_ = os.MkdirAll(dir, 0o755)
	for name, src := range map[string]string{"mine.md": "Repo mine.", "security.md": "Repo security.", "dependencies.md": "Repo deps."} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	active, shadowed, problems = Templates(st, p)
	if len(active) != len(Builtins())+1 || len(problems) != 0 {
		t.Fatalf("active: %+v %v", active, problems)
	}
	want := map[string]string{"mine": SourceDash, "security": SourceDash, "dependencies": SourceRepo, "code-review": SourceBuiltin, "test-gaps": SourceBuiltin}
	for i, tm := range active {
		if want[tm.Name] != tm.Origin {
			t.Errorf("%s: origin %q, want %q", tm.Name, tm.Origin, want[tm.Name])
		}
		if i > 0 && active[i-1].Name >= tm.Name {
			t.Errorf("not sorted: %+v", active)
		}
	}
	if tm, _ := FindIn(st, p, "dependencies"); tm.Prompt != "Repo deps." || tm.File != ".dash/scans/dependencies.md" {
		t.Fatalf("dependencies: %+v", tm)
	}
	if tm, _ := FindIn(st, p, "security"); tm.Prompt != "Dash security." {
		t.Fatalf("security: %+v", tm)
	}
	// The repository files the Dash scans replace are listed as shadowed.
	if len(shadowed) != 2 || shadowed[0].Name != "mine" || shadowed[1].Name != "security" ||
		shadowed[0].ShadowedBy != SourceDash || shadowed[1].Origin != SourceRepo || shadowed[1].File != ".dash/scans/security.md" {
		t.Fatalf("shadowed: %+v", shadowed)
	}
}

func TestDashScanTrusted(t *testing.T) {
	tm, _ := Custom(model.ScanTemplate{Name: "x", Schedule: "0 6 * * *", Tools: []string{"Bash(npm audit:*)"}, Prompt: "Go."})
	if tools, ok := Tools(tm, model.ScanSetting{}, false); !ok || tools[0] != "Bash(npm audit:*)" {
		t.Fatalf("tools: %v %v", tools, ok)
	}
	if sch := Effective(tm, model.ScanSetting{}, false); !sch.Enabled || sch.Cron != "0 6 * * *" || sch.Requested != "" {
		t.Fatalf("schedule: %+v", sch)
	}
	off := false
	if sch := Effective(tm, model.ScanSetting{Enabled: &off}, true); sch.Enabled {
		t.Fatalf("turned off: %+v", sch)
	}
	// No tools: read-only ones.
	tm, _ = Custom(model.ScanTemplate{Name: "x", Prompt: "Go."})
	if tools, ok := Tools(tm, model.ScanSetting{}, false); !ok || len(tools) != len(DefaultTools) {
		t.Fatalf("default tools: %v", tools)
	}
	if sch := Effective(tm, model.ScanSetting{}, false); sch.Enabled {
		t.Fatalf("no schedule enabled: %+v", sch)
	}
	// A repository template with the same settings still needs approval.
	repo, _ := ParseTemplate("x", []byte("---\nschedule: 0 6 * * *\ntools: Bash\n---\nGo."))
	repo.Origin = SourceRepo
	if _, ok := Tools(repo, model.ScanSetting{}, false); ok {
		t.Fatal("repo tools trusted")
	}
	if sch := Effective(repo, model.ScanSetting{}, false); sch.Enabled || sch.Requested == "" {
		t.Fatalf("repo schedule: %+v", sch)
	}
}

func TestRunDashScan(t *testing.T) {
	fx := newFixture(t, "", "")
	if _, err := fx.st.SaveScanTemplate(model.ScanTemplate{ProjectID: fx.p.ID, Name: "mine", Kind: KindCustom, Scope: model.ScopeRepo, Prompt: "Dash scan of {{path}}."}, true); err != nil {
		t.Fatal(err)
	}
	r := fx.run(t, "mine")
	if r.Status != model.ScanSucceeded {
		t.Fatalf("run: %+v", r)
	}
	if p := fx.exec.lastPrompt(); !strings.Contains(p, "Dash scan of .") {
		t.Fatalf("prompt: %s", p)
	}
	// Deleting it keeps its runs.
	if err := fx.st.DeleteScanTemplate(fx.p.ID, "mine", false); err != nil {
		t.Fatal(err)
	}
	if runs := fx.st.ListScanRuns(fx.p.ID, "mine"); len(runs) != 1 {
		t.Fatalf("runs: %+v", runs)
	}
	if _, err := fx.runner.Start(fx.p.ID, "mine", "manual"); err == nil {
		t.Fatal("deleted scan started")
	}
}
