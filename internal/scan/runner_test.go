package scan

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/testgit"
)

// TestMain keeps the git the tests start off this computer's repositories
// and settings (see testgit).
func TestMain(m *testing.M) {
	store.UseMigratedTemplateInTests() // new databases copy one migrated once
	os.Exit(testgit.Run(m))
}

// fakeExec answers every run with answer(), after waiting for release if set.
type fakeExec struct {
	mu      sync.Mutex
	answer  string
	release chan struct{}
	started chan struct{}
	reqs    []agent.ExecRequest
	onRun   func(agent.ExecRequest) // called during the run, if set
}

// fakeExec takes MCP servers, as Claude Code does (agent.MCPConfigurer).
func (*fakeExec) CanMCP() bool { return true }

func (f *fakeExec) Run(ctx context.Context, req agent.ExecRequest, emit func(model.RunEvent)) (agent.ExecResult, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	answer, release, started, onRun := f.answer, f.release, f.started, f.onRun
	f.mu.Unlock()
	if onRun != nil {
		onRun(req)
	}
	if started != nil {
		started <- struct{}{}
	}
	if _, err := os.Stat(filepath.Join(req.Dir, "main.go")); err != nil {
		return agent.ExecResult{}, errors.New("scan does not run in a checkout: " + err.Error())
	}
	emit(model.RunEvent{Kind: "tool_use", Tool: "Read"})
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return agent.ExecResult{}, ctx.Err()
		}
	}
	return agent.ExecResult{Result: answer, CostUSD: 0.12, Turns: 3, Stop: "success"}, nil
}

func (f *fakeExec) setAnswer(s string) {
	f.mu.Lock()
	f.answer = s
	f.mu.Unlock()
}

func (f *fakeExec) lastPrompt() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1].Prompt
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFile(t *testing.T, repo, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", name)
	gitRun(t, repo, "commit", "-qm", "change "+name)
	return gitRun(t, repo, "rev-parse", "HEAD")
}

type fixture struct {
	st     *store.Store
	state  string // the store's file
	p      model.Project
	repo   string
	exec   *fakeExec
	runner *Runner
}

// newFixture is a project with one commit and the scan template src as
// .dash/scans/<name>.md (an untracked file in the checkout).
func newFixture(t *testing.T, name, src string) *fixture {
	t.Helper()
	repo := t.TempDir()
	gitRun(t, repo, "init", "-q", "-b", "main")
	commitFile(t, repo, "main.go", "package main\n")
	if src != "" {
		dir := filepath.Join(repo, ".dash", "scans")
		_ = os.MkdirAll(dir, 0o755)
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	state := filepath.Join(t.TempDir(), "dash.db")
	st, err := store.Open(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() }) // before TempDir is removed: Windows can't remove an open file
	p, err := st.CreateProject(model.Project{Name: "p", Path: repo, DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	fx := &fixture{st: st, state: state, p: p, repo: repo, exec: &fakeExec{answer: `{"findings": []}`}}
	fx.runner = &Runner{Store: st, Git: gitops.New(nil), Exec: fx.exec, Root: t.TempDir()}
	t.Cleanup(fx.runner.Wait)
	return fx
}

// writeTemplate changes a template in the checkout, as a merge would.
func writeTemplate(t *testing.T, fx *fixture, name, src string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fx.repo, ".dash", "scans", name+".md"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (fx *fixture) run(t *testing.T, name string) model.ScanRun {
	t.Helper()
	r, err := fx.runner.Start(fx.p.ID, name, "manual")
	if err != nil {
		t.Fatal(err)
	}
	fx.runner.Wait()
	r, err = fx.st.GetScanRun(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const findingsAB = "Done.\n```json\n{\"findings\": [" +
	`{"title":"Hardcoded key","type":"vuln","severity":"high","file":"main.go","line":1,"fingerprint":"hardcoded-key"},` +
	`{"title":"Unchecked error","type":"bug","severity":"low","file":"main.go","fingerprint":"unchecked-err:main"},` +
	`{"title":"","severity":"high"}` +
	"]}\n```"

func TestRunRecordsFindings(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, "sec", "---\nkind: security\nscope: repo\n---\nScan {{path}}.")
	fx.exec.setAnswer(findingsAB)
	r := fx.run(t, "sec")
	if r.Status != model.ScanSucceeded || r.Error != "" {
		t.Fatalf("run: %+v", r)
	}
	if r.Counts != (model.UpsertCounts{New: 2}) || r.Findings != 2 || r.Invalid != 1 || len(r.NewTasks) != 2 || r.Urgent != 1 || r.High != 1 || r.Critical != 0 {
		t.Fatalf("counts: %+v", r)
	}
	if r.Commit == "" || r.FinishedAt == nil || r.CostUSD != 0.12 || r.Turns != 3 {
		t.Fatalf("run record: %+v", r)
	}
	drafts := findingDrafts(fx.st, fx.p.ID)
	if len(drafts) != 2 || drafts[0].Status != model.StatusDraft {
		t.Fatalf("drafts: %+v", drafts)
	}
	high := drafts[0]
	if high.Finding.Severity() != model.SevHigh {
		high = drafts[1]
	}
	if is := high.Finding.Issues[0]; is.Source != "scan:sec" || is.ScanKind != "security" || !slices.Contains(high.Tags, "scan:sec") || !slices.Contains(high.Tags, "high") {
		t.Fatalf("draft: %+v, tags %q", is, high.Tags)
	}
	// The scan is read-only: no branch, and its worktree is gone.
	if b := gitRun(t, fx.repo, "branch", "--list"); strings.Count(b, "\n") != 0 || !strings.Contains(b, "main") {
		t.Fatalf("branches: %q", b)
	}
	if wts := gitRun(t, fx.repo, "worktree", "list"); strings.Count(wts, "\n") != 0 {
		t.Fatalf("worktrees left: %s", wts)
	}
	prompt := fx.exec.lastPrompt()
	if !strings.Contains(prompt, "Scan .") || !strings.Contains(prompt, `"findings"`) {
		t.Fatalf("prompt: %s", prompt)
	}
	if args := strings.Join((agent.ClaudeCode{}).RunArgs(fx.exec.reqs[0]), " "); !strings.Contains(args, "--disallowedTools Edit,Write,NotebookEdit") {
		t.Fatalf("args: %s", args)
	}
	if _, err := os.Stat(fx.runner.LogPath(r.ID)); err != nil {
		t.Fatalf("log: %v", err)
	}
}

func TestRunCountsKnownDismissedReopened(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, "sec", "---\nkind: security\n---\nScan.")
	fx.exec.setAnswer(findingsAB)
	fx.run(t, "sec")
	var key, unchecked model.Todo
	for _, d := range findingDrafts(fx.st, fx.p.ID) {
		if d.Finding.Issues[0].Rule == "hardcoded-key" {
			key = d
		} else {
			unchecked = d
		}
	}
	// The key's draft is deleted (dismissed); the unchecked error's task is merged.
	if _, err := fx.st.DeleteTodo(key.ID); err != nil {
		t.Fatal(err)
	}
	for _, st := range []model.TodoStatus{model.StatusQueued, model.StatusInProgress, model.StatusReview, model.StatusMerging, model.StatusMerged} {
		if _, err := fx.st.SetStatus(unchecked.ID, st, ""); err != nil {
			t.Fatal(err)
		}
	}
	// Same problems, reworded, on other lines, plus a new one.
	fx.exec.setAnswer(`{"findings": [
		{"title":"API key in source","type":"vuln","severity":"critical","file":"main.go","line":4,"fingerprint":"hardcoded-key"},
		{"title":"Unchecked error","type":"bug","severity":"low","file":"main.go","fingerprint":"unchecked-err:main"},
		{"title":"Race on map","type":"bug","severity":"medium","file":"main.go","fingerprint":"race:m"}
	]}`)
	r := fx.run(t, "sec")
	if r.Counts != (model.UpsertCounts{New: 1, Dismissed: 1, Reopened: 1}) || len(r.ReopenedTasks) != 1 || r.Urgent != 0 {
		t.Fatalf("counts: %+v", r)
	}
	if back, _ := fx.st.GetTodo(r.ReopenedTasks[0]); back.ID == unchecked.ID || back.Finding.Earlier != unchecked.ID {
		t.Fatalf("came back: %+v", back)
	}
	if ds := fx.st.ListDismissed(fx.p.ID); len(ds) != 1 || ds[0].Severity != model.SevHigh || ds[0].Rule != "hardcoded-key" {
		t.Fatalf("dismissed issue changed: %+v", ds)
	}
	// The agent was told what it reported before, dismissed ones included.
	if p := fx.exec.lastPrompt(); !strings.Contains(p, "`hardcoded-key` · dismissed") {
		t.Fatalf("prompt lacks known issues:\n%s", p)
	}
	// Reported again unchanged: already known.
	r = fx.run(t, "sec")
	if r.Counts != (model.UpsertCounts{Known: 2, Dismissed: 1}) || len(r.NewTasks) != 0 {
		t.Fatalf("third run: %+v", r.Counts)
	}
	if n := len(findingDrafts(fx.st, fx.p.ID)); n != 3 { // the merged task, and two drafts
		t.Fatalf("%d tasks with issues, want 3", n)
	}
}

func TestRunSinceLastScan(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, "daily", "---\nscope: since_last_scan\n---\nCheck {{since}}.")
	first := fx.run(t, "daily")
	if first.Status != model.ScanSucceeded || first.Incremental || first.Since != "" || first.Note == "" {
		t.Fatalf("first run should scan everything: %+v", first)
	}
	// Nothing new: skipped without calling Claude.
	calls := len(fx.exec.reqs)
	r := fx.run(t, "daily")
	if r.Status != model.ScanSkipped || len(fx.exec.reqs) != calls {
		t.Fatalf("unchanged run: %+v (%d calls)", r, len(fx.exec.reqs)-calls)
	}
	// A commit: only it is scanned.
	commitFile(t, fx.repo, "pkg/new.go", "package pkg\n")
	r = fx.run(t, "daily")
	if r.Status != model.ScanSucceeded || !r.Incremental || r.Since != first.Commit || r.Commit == first.Commit {
		t.Fatalf("incremental run: %+v", r)
	}
	p := fx.exec.lastPrompt()
	if !strings.Contains(p, "Check "+first.Commit+".") || !strings.Contains(p, "- pkg/new.go") || strings.Contains(p, "- main.go") {
		t.Fatalf("prompt:\n%s", p)
	}
	// A failed run is not a baseline.
	commitFile(t, fx.repo, "pkg/other.go", "package pkg\n")
	fx.exec.setAnswer("I could not finish.")
	if r2 := fx.run(t, "daily"); r2.Status != model.ScanFailed || !strings.Contains(r2.Error, "findings JSON") || r2.Answer != "I could not finish." {
		t.Fatalf("bad answer: %+v", r2)
	}
	fx.exec.setAnswer(`{"findings": []}`)
	if r3 := fx.run(t, "daily"); r3.Since != r.Commit || r3.Answer != "" {
		t.Fatalf("baseline after failure: since %s, want %s", r3.Since, r.Commit)
	}
}

// A since_last_scan scan with full_scan_days looks at everything again
// once its last full pass is that old, and a full pass can be asked for.
func TestRunPeriodicAndRequestedFullScan(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, "daily", "---\nscope: since_last_scan\nfull_scan_days: 30\n---\nCheck.")
	now := time.Now()
	fx.runner.Now = func() time.Time { return now }
	first := fx.run(t, "daily")
	if first.Status != model.ScanSucceeded || first.Incremental || !strings.Contains(first.Note, "First run") {
		t.Fatalf("first run should scan everything: %+v", first)
	}
	commitFile(t, fx.repo, "pkg/a.go", "package pkg\n")
	if r := fx.run(t, "daily"); r.Status != model.ScanSucceeded || !r.Incremental {
		t.Fatalf("a run within 30 days is incremental: %+v", r)
	}
	// 31 days on, a full pass is due, even with nothing new.
	now = now.Add(31 * 24 * time.Hour)
	full := fx.run(t, "daily")
	if full.Status != model.ScanSucceeded || full.Incremental || !strings.Contains(full.Note, "Periodic full scan") {
		t.Fatalf("periodic full scan: %+v", full)
	}
	if p := fx.exec.lastPrompt(); !strings.Contains(p, "Look at the whole repository") {
		t.Fatalf("full scan prompt:\n%s", p)
	}
	if r := fx.run(t, "daily"); r.Status != model.ScanSkipped {
		t.Fatalf("after a full pass, nothing new is skipped again: %+v", r)
	}
	// Asked for by hand: everything, though nothing changed.
	r, err := fx.runner.StartFullFor(fx.p.ID, "daily", "manual", 0)
	if err != nil {
		t.Fatal(err)
	}
	fx.runner.Wait()
	if r, _ = fx.st.GetScanRun(r.ID); r.Status != model.ScanSucceeded || r.Incremental || !strings.Contains(r.Note, "asked for") {
		t.Fatalf("requested full scan: %+v", r)
	}
}

func TestRunSinceMainAndPath(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, "branch", "---\nscope: since_main\npath: web\n---\nGo.")
	if r := fx.run(t, "branch"); r.Status != model.ScanSkipped {
		t.Fatalf("on main: %+v", r)
	}
	gitRun(t, fx.repo, "checkout", "-qb", "feature")
	commitFile(t, fx.repo, "other.go", "package main\n")
	if r := fx.run(t, "branch"); r.Status != model.ScanSkipped || !strings.Contains(r.Note, "web") {
		t.Fatalf("change outside path: %+v", r)
	}
	commitFile(t, fx.repo, "web/app.js", "x\n")
	r := fx.run(t, "branch")
	if r.Status != model.ScanSucceeded || !r.Incremental {
		t.Fatalf("feature branch: %+v", r)
	}
	if p := fx.exec.lastPrompt(); !strings.Contains(p, "- web/app.js") || strings.Contains(p, "- other.go") {
		t.Fatalf("prompt:\n%s", p)
	}
}

func TestRunRequiresFiles(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, "web", "---\nscope: since_last_scan\nrequires_files: *.tsx, web/**/*.{html,vue}\n---\nGo.")
	// No front-end files: skipped without calling Claude.
	r := fx.run(t, "web")
	if r.Status != model.ScanSkipped || !strings.Contains(r.Note, "*.tsx") || len(fx.exec.reqs) != 0 {
		t.Fatalf("no web files: %+v (%d calls)", r, len(fx.exec.reqs))
	}
	if wts := gitRun(t, fx.repo, "worktree", "list"); strings.Count(wts, "\n") != 0 {
		t.Fatalf("worktrees left: %s", wts)
	}
	// One outside the glob's directory doesn't count; one in it does.
	commitFile(t, fx.repo, "docs/page.html", "<p>\n")
	if r := fx.run(t, "web"); r.Status != model.ScanSkipped {
		t.Fatalf("html outside web/: %+v", r)
	}
	commitFile(t, fx.repo, "web/app/App.vue", "<template/>\n")
	if r := fx.run(t, "web"); r.Status != model.ScanSucceeded || len(fx.exec.reqs) != 1 {
		t.Fatalf("with a vue file: %+v", r)
	}
}

func TestRunNoOverlap(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, "slow", "Go.")
	fx.exec.release, fx.exec.started = make(chan struct{}), make(chan struct{}, 4)
	r, err := fx.runner.Start(fx.p.ID, "slow", "manual")
	if err != nil {
		t.Fatal(err)
	}
	<-fx.exec.started
	if _, err := fx.runner.Start(fx.p.ID, "slow", "schedule"); !errors.Is(err, ErrRunning) {
		t.Fatalf("second copy: %v", err)
	}
	if id, ok := fx.runner.Running(fx.p.ID, "slow"); !ok || id != r.ID {
		t.Fatalf("running: %q %v", id, ok)
	}
	close(fx.exec.release)
	fx.runner.Wait()
	if _, ok := fx.runner.Running(fx.p.ID, "slow"); ok {
		t.Fatal("still running")
	}
	// Free again once finished.
	fx.exec.release = nil
	fx.run(t, "slow")

	// Cancel stops a run.
	fx.exec.release = make(chan struct{})
	r, _ = fx.runner.Start(fx.p.ID, "slow", "manual")
	<-fx.exec.started
	if !fx.runner.Cancel(r.ID) {
		t.Fatal("cancel")
	}
	fx.runner.Wait()
	if got, _ := fx.st.GetScanRun(r.ID); got.Status != model.ScanCanceled {
		t.Fatalf("canceled run: %+v", got)
	}
}

func TestRunUnknownScan(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, "a", "Go.")
	if _, err := fx.runner.Start(fx.p.ID, "nope", "manual"); !errors.Is(err, ErrNoScan) {
		t.Fatalf("unknown scan: %v", err)
	}
}

func TestRecordCounts(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, "x", "Go.")
	run, err := fx.st.CreateScanRun(model.ScanRun{ProjectID: fx.p.ID, Scan: "x", Status: model.ScanRunning})
	if err != nil {
		t.Fatal(err)
	}
	in := []model.Issue{
		{Title: "A", Type: model.IssueBug, Severity: model.SevCritical, File: "a.go", Rule: "a"},
		{Title: "B", Type: model.IssueBug, Severity: model.SevLow, File: "b.go", Rule: "b"},
	}
	out, err := Record(fx.st, run, in)
	if err != nil || out.Counts != (model.UpsertCounts{New: 2}) || out.Urgent != 1 || out.Critical != 1 || out.High != 0 || len(out.NewTasks) != 2 {
		t.Fatalf("first: %+v %v", out, err)
	}
	in[1].Severity = model.SevHigh
	out, err = Record(fx.st, run, in)
	if err != nil || out.Counts != (model.UpsertCounts{Known: 1, Updated: 1}) || out.Urgent != 0 || out.Critical != 0 || out.High != 0 || out.NewTasks != nil {
		t.Fatalf("second: %+v %v", out, err)
	}
	for _, d := range findingDrafts(fx.st, fx.p.ID) {
		if is := d.Finding.Issues[0]; is.Source != "scan:x" {
			t.Fatalf("source %q", is.Source)
		}
	}
}

func TestRunToolsNeedApproval(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, "deps", "---\ntools: Read, Bash\n---\nAudit.")
	var dir string
	allowed := func() string {
		t.Helper()
		req := fx.exec.reqs[len(fx.exec.reqs)-1]
		dir = req.Dir
		// The run removed its worktree, but Confine resolves the dir's
		// symlinks (/var -> /private/var on macOS) as the runner did while
		// it existed: bring it back so the expected rules match.
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i, a := range (agent.ClaudeCode{}).RunArgs(req) {
			if a == "--allowedTools" {
				return (agent.ClaudeCode{}).RunArgs(req)[i+1]
			}
		}
		t.Fatalf("no --allowedTools in %q", (agent.ClaudeCode{}).RunArgs(req))
		return ""
	}
	readOnly := func() string { return strings.Join(Confine(DefaultTools, dir), ",") }
	// Not approved: read-only tools.
	if r := fx.run(t, "deps"); r.Status != model.ScanSucceeded {
		t.Fatalf("run: %+v", r)
	}
	if got := allowed(); got != readOnly() {
		t.Fatalf("unapproved tools used: %s", got)
	}
	// Approved: the template's.
	if _, err := fx.st.SetScanSetting(fx.p.ID, "deps", func(st *model.ScanSetting) { st.ApprovedTools = []string{"Read", "Bash"} }); err != nil {
		t.Fatal(err)
	}
	fx.run(t, "deps")
	if got := allowed(); got != strings.Join(Confine([]string{"Read", "Bash"}, dir), ",") || strings.Contains(got, "Read,") {
		t.Fatalf("approved tools: %s", got)
	}
	// The template asks for more: the approval no longer applies.
	writeTemplate(t, fx, "deps", "---\ntools: Read, Bash, WebFetch\n---\nAudit.")
	fx.run(t, "deps")
	if got := allowed(); got != readOnly() {
		t.Fatalf("approval applied to changed tools: %s", got)
	}
	tm := mustFind(t, fx, "deps")
	set, has := fx.st.ScanSetting(fx.p.ID, "deps")
	if _, ok := Tools(tm, set, has); ok {
		t.Fatal("changed tools reported approved")
	}
	// A built-in's tools need no approval.
	if tools, ok := Tools(Template{Builtin: true, Tools: []string{"Bash(npm audit:*)"}}, model.ScanSetting{}, false); !ok || tools[0] != "Bash(npm audit:*)" {
		t.Fatalf("built-in tools: %v %v", tools, ok)
	}
}

func flagValue(args []string, name string) (string, bool) {
	i := slices.Index(args, name)
	if i < 0 || i+1 >= len(args) {
		return "", i >= 0
	}
	return args[i+1], true
}

// Scans run unattended: no user/project settings or hooks, and anything
// outside the allowed tools is denied at once instead of asking.
func TestArgsNeverAsk(t *testing.T) {
	t.Parallel()

	// -claude-args are for tasks: a scan's run goes without them.
	claude := agent.ClaudeCode{Args: []string{"--permission-mode", "acceptEdits", "--allowedTools", "Bash"}}
	args := claude.RunArgs(RunRequest(Template{Model: "sonnet"}, []string{"Read", "Bash(npm audit:*)"}, ""))
	if slices.Contains(args, "acceptEdits") || slices.Contains(args, "Bash") {
		t.Errorf("task flags in a scan's run: %q", args)
	}
	if v, ok := flagValue(args, "--setting-sources"); !ok || v != "" {
		t.Errorf("--setting-sources %q %v, want empty", v, ok)
	}
	if v, _ := flagValue(args, "--permission-mode"); v != "dontAsk" {
		t.Errorf("--permission-mode %q, want dontAsk", v)
	}
	if v, _ := flagValue(args, "--permission-prompts"); v != "none" {
		t.Errorf("--permission-prompts %q, want none", v)
	}
	if _, ok := flagValue(args, "--permission-prompt-tool"); ok {
		t.Error("scan should not pass --permission-prompt-tool")
	}
	for _, a := range args {
		if a == "default" || a == "manual" || a == "--dangerously-skip-permissions" || a == "bypassPermissions" {
			t.Errorf("interactive or bypassing permission flag %q in %q", a, args)
		}
	}
	if v, _ := flagValue(args, "--allowedTools"); v != "Read,Bash(npm audit:*)" {
		t.Errorf("--allowedTools %q", v)
	}
	if v, _ := flagValue(args, "--disallowedTools"); v != "Edit,Write,NotebookEdit" {
		t.Errorf("--disallowedTools %q", v)
	}
	if v, _ := flagValue(args, "--model"); v != "sonnet" {
		t.Errorf("--model %q", v)
	}
}

// With a checkout, the file tools read only it, and never the runner's
// token or the user's credentials, even in a data directory that holds the
// checkout.
func TestArgsConfineFileTools(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	data := filepath.Join(home, ".local", "share", "dash-runner")
	root := filepath.Join(data, "scans")
	dir := filepath.Join(root, "worktrees", "p", "run")
	args := (agent.ClaudeCode{}).RunArgs(RunRequest(Template{}, nil, dir, data, root))
	allow, _ := flagValue(args, "--allowedTools")
	for _, tool := range []string{"Read", "Grep", "Glob"} {
		if slices.Contains(strings.Split(allow, ","), tool) {
			t.Errorf("unscoped %s in %q", tool, allow)
		}
		if want := tool + "(/" + rulePath(dir) + "/**)"; !strings.Contains(allow, want) {
			t.Errorf("no %s in %q", want, allow)
		}
	}
	if !strings.Contains(allow, "Task") || !strings.Contains(allow, "mcp__") {
		t.Errorf("other tools dropped: %q", allow)
	}
	deny, _ := flagValue(args, "--disallowedTools")
	for _, want := range []string{
		"Edit,Write,NotebookEdit",
		"Read(/" + rulePath(filepath.Join(home, ".ssh")) + "/**)",
		"Grep(/" + rulePath(filepath.Join(home, ".config", "gh")) + "/**)",
		"Glob(/" + rulePath(filepath.Join(home, ".claude")) + "/**)",
		"Read(/" + rulePath(filepath.Join(data, "*.json")) + ")",
		"Read(/" + rulePath(filepath.Join(root, "logs")) + "/**)",
	} {
		if !strings.Contains(deny, want) {
			t.Errorf("no %s in %q", want, deny)
		}
	}
	// A deny rule beats an allow rule: nothing holding the checkout is denied whole.
	for _, p := range []string{data, root, filepath.Join(root, "worktrees")} {
		if strings.Contains(deny, "(/"+rulePath(p)+"/**)") {
			t.Errorf("%s denied whole: %q", p, deny)
		}
	}
}

// Claude Code matches permission rules against POSIX paths, with a Windows
// drive as /c.
func TestRulePath(t *testing.T) {
	t.Parallel()

	if got := pathRule("Read", "/home/a/.ssh", true); runtime.GOOS != "windows" && got != "Read(//home/a/.ssh/**)" {
		t.Errorf("pathRule = %q", got)
	}
	if runtime.GOOS == "windows" {
		for p, want := range map[string]string{
			`C:\Users\a\.ssh`: "/c/Users/a/.ssh",
			`d:\w\repo`:       "/d/w/repo",
		} {
			if got := rulePath(p); got != want {
				t.Errorf("rulePath(%q) = %q, want %q", p, got, want)
			}
		}
	}
}

func TestPromptSaysToolsAreFixed(t *testing.T) {
	t.Parallel()

	for _, tools := range [][]string{nil, DefaultTools} {
		p := BuildPrompt(Template{Prompt: "Go."}, tools, target{}, nil)
		for _, want := range []string{"Verify findings by reading the code", "do not change, create or delete files", "builds, tests, network) is refused"} {
			if !strings.Contains(p, want) {
				t.Errorf("tools %v: prompt does not say %q:\n%s", tools, want, p)
			}
		}
		if strings.Contains(p, "mktemp") {
			t.Errorf("tools %v: read-only prompt offers a temp directory:\n%s", tools, p)
		}
	}
}

// A scan with Bash is told it may run commands and write to a temp
// directory, but not change the repository; never that commands are refused.
func TestPromptWithBash(t *testing.T) {
	t.Parallel()

	cases := []struct {
		tools []string
		want  []string
	}{
		{[]string{"Read", "Grep", "Glob", "Bash"}, []string{"You may run shell commands with Bash", "network access they need"}},
		{[]string{"Read", " Bash(npm audit:*)", "Bash(govulncheck ./...)"},
			[]string{"only these shell commands", "commands starting with `npm audit`", "`govulncheck ./...`"}},
	}
	for _, c := range cases {
		p := BuildPrompt(Template{Prompt: "Go."}, c.tools, target{}, nil)
		want := append(c.want, "Do not change, create or delete the repository's files", "write only to a temporary directory", "`mktemp -d`", `{"findings": [`)
		for _, w := range want {
			if !strings.Contains(p, w) {
				t.Errorf("tools %v: prompt does not say %q:\n%s", c.tools, w, p)
			}
		}
		for _, no := range []string{"Verify findings by reading the code", "builds, tests, network) is refused", "This is a read-only scan"} {
			if strings.Contains(p, no) {
				t.Errorf("tools %v: prompt says %q:\n%s", c.tools, no, p)
			}
		}
	}
	if p := BuildPrompt(Template{Prompt: "Go."}, []string{"Read", "Bash"}, target{}, nil); strings.Contains(p, "only these shell commands") {
		t.Errorf("unrestricted Bash is listed as rules:\n%s", p)
	}
}

// A run that takes longer than the limit is stopped and fails, keeping its log.
func TestRunTimeout(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, "slow", "Go.")
	fx.exec.release = make(chan struct{}) // never released
	fx.runner.RunTimeout = 50 * time.Millisecond
	r := fx.run(t, "slow")
	if r.Status != model.ScanFailed || !strings.Contains(r.Error, "limit") {
		t.Fatalf("timed-out run: %+v", r)
	}
	b, err := os.ReadFile(fx.runner.LogPath(r.ID))
	if err != nil || !strings.Contains(string(b), "limit") {
		t.Fatalf("log: %v %q", err, b)
	}
}

// A log past LogMaxBytes keeps its newest lines behind a note, and stays
// under the cap.
func TestRunLogCapped(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, "chatty", "Go.")
	fx.runner.LogMaxBytes = 160
	r := fx.run(t, "chatty")
	if r.Status != model.ScanSucceeded {
		t.Fatalf("run: %+v", r)
	}
	b, err := os.ReadFile(fx.runner.LogPath(r.ID))
	if err != nil {
		t.Fatal(err)
	}
	log := string(b)
	if len(b) > 160 {
		t.Errorf("log is %d bytes, over the cap of 160:\n%s", len(b), log)
	}
	if !strings.Contains(log, "earlier output trimmed") || strings.Contains(log, "▶ scan") {
		t.Errorf("log not trimmed to its newest lines:\n%s", log)
	}
	if !strings.Contains(log, "✔") {
		t.Errorf("log lost its last line:\n%s", log)
	}
}

// A runner refuses a job whose settings would turn into claude flags.
func TestJobCheck(t *testing.T) {
	t.Parallel()

	ok := Job{Template: Template{Name: "security", Scope: model.ScopeRepo, Model: "opus"}, Tools: []string{"Read", "Bash(go test:*)"}}
	if err := ok.Check(); err != nil {
		t.Fatalf("good job refused: %v", err)
	}
	for name, mut := range map[string]func(*Job){
		"name":   func(j *Job) { j.Template.Name = "../x" },
		"scope":  func(j *Job) { j.Template.Scope = "everything" },
		"model":  func(j *Job) { j.Template.Model = "--dangerously-skip-permissions" },
		"effort": func(j *Job) { j.Template.Effort = "-x" },
		"tool":   func(j *Job) { j.Tools = append(j.Tools, "--add-dir=/") },
		"empty":  func(j *Job) { j.Tools = append(j.Tools, " ") },
		"commit": func(j *Job) { j.LastCommit = "--output=/tmp/x" },
	} {
		j := ok
		j.Tools = append([]string(nil), ok.Tools...)
		mut(&j)
		if j.Check() == nil {
			t.Errorf("%s: accepted %+v", name, j)
		}
	}
}

// findingDrafts are the project's tasks made from reported issues.
func findingDrafts(st *store.Store, pid string) []model.Todo {
	var out []model.Todo
	for _, t := range st.ListTodos(pid) {
		if t.Finding != nil {
			out = append(out, t)
		}
	}
	return out
}
