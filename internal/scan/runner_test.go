package scan

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// TestMain keeps the developer's git config (commit signing) out of the tests.
func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_COUNT", "1")
	os.Setenv("GIT_CONFIG_KEY_0", "commit.gpgsign")
	os.Setenv("GIT_CONFIG_VALUE_0", "false")
	os.Exit(m.Run())
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
	fx := newFixture(t, "sec", "---\nkind: security\nscope: repo\n---\nScan {{path}}.")
	fx.exec.setAnswer(findingsAB)
	r := fx.run(t, "sec")
	if r.Status != model.ScanSucceeded || r.Error != "" {
		t.Fatalf("run: %+v", r)
	}
	if r.Counts != (model.UpsertCounts{New: 2}) || r.Findings != 2 || r.Invalid != 1 || len(r.NewIssues) != 2 || r.Urgent != 1 {
		t.Fatalf("counts: %+v", r)
	}
	if r.Commit == "" || r.FinishedAt == nil || r.CostUSD != 0.12 || r.Turns != 3 {
		t.Fatalf("run record: %+v", r)
	}
	issues := fx.st.ListIssues(fx.p.ID)
	if len(issues) != 2 || issues[0].Source != "scan:sec" || issues[0].Severity != model.SevHigh {
		t.Fatalf("issues: %+v", issues)
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
	if args := strings.Join(fx.exec.reqs[0].ExtraArgs, " "); !strings.Contains(args, "--disallowedTools Edit,Write,NotebookEdit") {
		t.Fatalf("args: %s", args)
	}
	if _, err := os.Stat(fx.runner.LogPath(r.ID)); err != nil {
		t.Fatalf("log: %v", err)
	}
}

func TestRunCountsKnownDismissedReopened(t *testing.T) {
	fx := newFixture(t, "sec", "---\nkind: security\n---\nScan.")
	fx.exec.setAnswer(findingsAB)
	fx.run(t, "sec")
	var key, unchecked model.Issue
	for _, is := range fx.st.ListIssues(fx.p.ID) {
		if is.Rule == "hardcoded-key" {
			key = is
		} else {
			unchecked = is
		}
	}
	if _, err := fx.st.SetIssueStatus(key.ID, model.IssueDismissed, "test key"); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.st.SetIssueStatus(unchecked.ID, model.IssueResolved, ""); err != nil {
		t.Fatal(err)
	}
	// Same problems, reworded, on other lines, plus a new one.
	fx.exec.setAnswer(`{"findings": [
		{"title":"API key in source","type":"vuln","severity":"critical","file":"main.go","line":4,"fingerprint":"hardcoded-key"},
		{"title":"Unchecked error","type":"bug","severity":"low","file":"main.go","fingerprint":"unchecked-err:main"},
		{"title":"Race on map","type":"bug","severity":"medium","file":"main.go","fingerprint":"race:m"}
	]}`)
	r := fx.run(t, "sec")
	if r.Counts != (model.UpsertCounts{New: 1, Dismissed: 1, Reopened: 1}) || len(r.ReopenedIssues) != 1 || r.ReopenedIssues[0] != unchecked.ID || r.Urgent != 0 {
		t.Fatalf("counts: %+v", r)
	}
	if got, _ := fx.st.GetIssue(key.ID); got.Status != model.IssueDismissed || got.Severity != model.SevHigh {
		t.Fatalf("dismissed issue changed: %+v", got)
	}
	// The agent was told what it reported before, dismissed ones included.
	if p := fx.exec.lastPrompt(); !strings.Contains(p, "`hardcoded-key` · dismissed") {
		t.Fatalf("prompt lacks known issues:\n%s", p)
	}
	// Reported again unchanged: already known.
	r = fx.run(t, "sec")
	if r.Counts != (model.UpsertCounts{Known: 2, Dismissed: 1}) || len(r.NewIssues) != 0 {
		t.Fatalf("third run: %+v", r.Counts)
	}
	if n := len(fx.st.ListIssues(fx.p.ID)); n != 3 {
		t.Fatalf("%d issues, want 3", n)
	}
}

func TestRunSinceLastScan(t *testing.T) {
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

func TestRunSinceMainAndPath(t *testing.T) {
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

func TestRunNoOverlap(t *testing.T) {
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
	fx := newFixture(t, "a", "Go.")
	if _, err := fx.runner.Start(fx.p.ID, "nope", "manual"); !errors.Is(err, ErrNoScan) {
		t.Fatalf("unknown scan: %v", err)
	}
}

func TestRecordCounts(t *testing.T) {
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
	if err != nil || out.Counts != (model.UpsertCounts{New: 2}) || out.Urgent != 1 || len(out.NewIssues) != 2 {
		t.Fatalf("first: %+v %v", out, err)
	}
	in[1].Severity = model.SevHigh
	out, err = Record(fx.st, run, in)
	if err != nil || out.Counts != (model.UpsertCounts{Known: 1, Updated: 1}) || out.Urgent != 0 || out.NewIssues != nil {
		t.Fatalf("second: %+v %v", out, err)
	}
	for _, is := range fx.st.ListIssues(fx.p.ID) {
		if is.Source != "scan:x" {
			t.Fatalf("source %q", is.Source)
		}
	}
}

func TestRunToolsNeedApproval(t *testing.T) {
	fx := newFixture(t, "deps", "---\ntools: Read, Bash\n---\nAudit.")
	var dir string
	allowed := func() string {
		t.Helper()
		req := fx.exec.reqs[len(fx.exec.reqs)-1]
		dir = req.Dir
		for i, a := range req.ExtraArgs {
			if a == "--allowedTools" {
				return req.ExtraArgs[i+1]
			}
		}
		t.Fatalf("no --allowedTools in %q", req.ExtraArgs)
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
	args := Args(Template{Model: "sonnet"}, []string{"Read", "Bash(npm audit:*)"}, "")
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
	data := filepath.Join(home, ".local", "share", "dash-runner")
	root := filepath.Join(data, "scans")
	dir := filepath.Join(root, "worktrees", "p", "run")
	args := Args(Template{}, nil, dir, data, root)
	allow, _ := flagValue(args, "--allowedTools")
	for _, tool := range []string{"Read", "Grep", "Glob"} {
		if slices.Contains(strings.Split(allow, ","), tool) {
			t.Errorf("unscoped %s in %q", tool, allow)
		}
		if want := tool + "(/" + filepath.ToSlash(dir) + "/**)"; !strings.Contains(allow, want) {
			t.Errorf("no %s in %q", want, allow)
		}
	}
	if !strings.Contains(allow, "Task") || !strings.Contains(allow, "mcp__") {
		t.Errorf("other tools dropped: %q", allow)
	}
	deny, _ := flagValue(args, "--disallowedTools")
	for _, want := range []string{
		"Edit,Write,NotebookEdit",
		"Read(/" + filepath.ToSlash(filepath.Join(home, ".ssh")) + "/**)",
		"Grep(/" + filepath.ToSlash(filepath.Join(home, ".config", "gh")) + "/**)",
		"Glob(/" + filepath.ToSlash(filepath.Join(home, ".claude")) + "/**)",
		"Read(/" + filepath.ToSlash(filepath.Join(data, "*.json")) + ")",
		"Read(/" + filepath.ToSlash(filepath.Join(root, "logs")) + "/**)",
	} {
		if !strings.Contains(deny, want) {
			t.Errorf("no %s in %q", want, deny)
		}
	}
	// A deny rule beats an allow rule: nothing holding the checkout is denied whole.
	for _, p := range []string{data, root, filepath.Join(root, "worktrees")} {
		if strings.Contains(deny, "(/"+filepath.ToSlash(p)+"/**)") {
			t.Errorf("%s denied whole: %q", p, deny)
		}
	}
}

func TestPromptSaysToolsAreFixed(t *testing.T) {
	p := BuildPrompt(Template{Prompt: "Go."}, target{}, nil)
	if !strings.Contains(p, "Verify findings by reading the code") {
		t.Errorf("prompt does not say to verify by reading code:\n%s", p)
	}
}

// A run that takes longer than the limit is stopped and fails, keeping its log.
func TestRunTimeout(t *testing.T) {
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

// A runner refuses a job whose settings would turn into claude flags.
func TestJobCheck(t *testing.T) {
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
