package scan

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
}

func (f *fakeExec) Run(ctx context.Context, req agent.ExecRequest, emit func(model.RunEvent)) (agent.ExecResult, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	answer, release, started := f.answer, f.release, f.started
	f.mu.Unlock()
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
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProject(model.Project{Name: "p", Path: repo, DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	fx := &fixture{st: st, p: p, repo: repo, exec: &fakeExec{answer: `{"findings": []}`}}
	fx.runner = &Runner{Store: st, Git: gitops.New(nil), Exec: fx.exec, Root: t.TempDir()}
	t.Cleanup(fx.runner.Wait)
	return fx
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
	if r2 := fx.run(t, "daily"); r2.Status != model.ScanFailed || !strings.Contains(r2.Error, "findings JSON") {
		t.Fatalf("bad answer: %+v", r2)
	}
	fx.exec.setAnswer(`{"findings": []}`)
	if r3 := fx.run(t, "daily"); r3.Since != r.Commit {
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
	if _, err := fx.runner.Start(fx.p.ID, "security", "manual"); !errors.Is(err, ErrNoScan) {
		t.Fatalf("built-in in a repo with templates: %v", err)
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

func TestStoreMarksInterruptedRunsFailed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, _ := store.Open(path, nil)
	p, _ := st.CreateProject(model.Project{Name: "p", Path: t.TempDir()})
	r, _ := st.CreateScanRun(model.ScanRun{ProjectID: p.ID, Scan: "x", Status: model.ScanRunning, StartedAt: time.Now()})
	st, _ = store.Open(path, nil)
	if got, _ := st.GetScanRun(r.ID); got.Status != model.ScanFailed || got.FinishedAt == nil {
		t.Fatalf("after restart: %+v", got)
	}
}
