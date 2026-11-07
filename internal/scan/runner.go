package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// ErrRunning is returned when a scan is started while it is already running.
var ErrRunning = errors.New("this scan is already running")

// ErrNoScan is returned for a scan name the project has no template for.
var ErrNoScan = errors.New("no such scan")

// errUserCanceled is the cancel cause of a run the user stopped (Cancel),
// as opposed to Dash shutting down (Runner.Context canceled).
var errUserCanceled = errors.New("canceled by the user")

// ResumeMaxAge is how old an interrupted run may be and still be restarted.
const ResumeMaxAge = 24 * time.Hour

// Runner runs scans: Claude Code in a throwaway, detached worktree of the
// project, never on a branch and never through review or the merge queue.
// It runs at most one copy of each scan of a project at a time.
type Runner struct {
	Store *store.Store
	Git   *gitops.Git
	Exec  agent.Executor
	// Root holds the throwaway worktrees (Root/worktrees) and run logs
	// (Root/logs). Keep it apart from the tasks' worktrees, whose cleanup
	// removes directories no task knows.
	Root string
	// Context is the parent of every run; cancel it to stop them all.
	Context context.Context
	Now     func() time.Time
	// RunTimeout bounds one run of Claude (default DefaultRunTimeout); a run
	// that takes longer is stopped and fails, keeping its log.
	RunTimeout time.Duration
	// GitTools, served at GitURL, give scans read-only git (DefaultTools);
	// without them a scan reads only files.
	GitTools *GitMCP
	GitURL   string

	mu      sync.Mutex
	running map[string]*active // project/scan → the run
	wg      sync.WaitGroup
}

type active struct {
	runID  string
	cancel context.CancelCauseFunc
}

// DefaultRunTimeout is how long a scan's Claude run may take by default.
const DefaultRunTimeout = 40 * time.Minute

func (r *Runner) runTimeout() time.Duration {
	if r.RunTimeout > 0 {
		return r.RunTimeout
	}
	return DefaultRunTimeout
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Running reports whether a scan of the project is running, and which run.
func (r *Runner) Running(projectID, scan string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.running[scanKey(projectID, scan)]
	if !ok {
		return "", false
	}
	return a.runID, true
}

// Wait blocks until every started run has finished.
func (r *Runner) Wait() { r.wg.Wait() }

// Cleanup removes throwaway worktrees left behind by a Dash that stopped
// mid-scan. Call it at startup, before any scan runs.
func (r *Runner) Cleanup(ctx context.Context) {
	root := filepath.Join(r.Root, "worktrees")
	dirs, _ := os.ReadDir(root)
	if len(dirs) == 0 {
		return
	}
	_ = os.RemoveAll(root)
	for _, p := range r.Store.ListProjects() {
		r.Git.PruneWorktrees(ctx, p.ID, p.Path)
	}
}

// LogPath is where a run's progress log is written.
func (r *Runner) LogPath(runID string) string {
	return filepath.Join(r.Root, "logs", runID+".log")
}

// Start begins a run of the project's scan and returns it while it runs.
func (r *Runner) Start(projectID, name, trigger string) (model.ScanRun, error) {
	return r.start(projectID, name, trigger, nil)
}

// start begins a run; from, if set, is the interrupted run it starts over,
// whose scope, path and base commit it keeps.
func (r *Runner) start(projectID, name, trigger string, from *model.ScanRun) (model.ScanRun, error) {
	p, err := r.Store.GetProject(projectID)
	if err != nil {
		return model.ScanRun{}, err
	}
	t, ok := Find(p.Path, name)
	if !ok {
		return model.ScanRun{}, fmt.Errorf("%w %q in %s", ErrNoScan, name, p.Name)
	}
	resumedFrom := ""
	if from != nil {
		// Cover what the original would have, even if the template's scope
		// or path changed since.
		t.Scope, t.Path, resumedFrom = from.Scope, from.Path, from.ID
	}
	key := scanKey(projectID, t.Name)
	r.mu.Lock()
	if _, busy := r.running[key]; busy {
		r.mu.Unlock()
		return model.ScanRun{}, ErrRunning
	}
	parent := r.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancelCause(parent)
	a := &active{cancel: cancel}
	if r.running == nil {
		r.running = map[string]*active{}
	}
	r.running[key] = a // claimed before anything slow, so no second copy starts
	r.mu.Unlock()

	run, err := r.Store.CreateScanRun(model.ScanRun{
		ProjectID: projectID, Scan: t.Name, Trigger: trigger, Scope: t.Scope, Path: t.Path,
		Status: model.ScanRunning, StartedAt: r.now(), Activity: "Starting", ResumedFrom: resumedFrom,
	})
	if err != nil {
		cancel(nil)
		r.release(key)
		return run, err
	}
	r.mu.Lock()
	a.runID = run.ID
	r.mu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer r.release(key)
		defer cancel(nil)
		r.execute(ctx, p, t, run, from)
		r.PruneLogs() // of runs the store just let go
	}()
	return run, nil
}

func (r *Runner) release(key string) {
	r.mu.Lock()
	delete(r.running, key)
	r.mu.Unlock()
}

// Cancel stops a running scan run.
func (r *Runner) Cancel(runID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.running {
		if a.runID == runID {
			a.cancel(errUserCanceled)
			return true
		}
	}
	return false
}

// target is what one run scans.
type target struct {
	commit, since string
	incremental   bool
	files         []string // changed files, for incremental scopes
	skip          string   // why there is nothing to scan
	note          string
}

// resolve works out the commits a run scans for its template's scope. A
// resumed run (from set) scans the current commit but keeps the original
// run's base, so it covers at least what the original would have.
func (r *Runner) resolve(ctx context.Context, p model.Project, t Template, from *model.ScanRun) (target, error) {
	var tg target
	head := func(ref string) (string, error) { return r.Git.Commit(ctx, p.ID, p.Path, ref) }
	var err error
	if from != nil && from.Commit != "" {
		// The original had worked out its target before it was cut off.
		if t.Scope == model.ScopeSinceMain {
			tg.commit, err = head("HEAD")
		} else if tg.commit, err = head("refs/heads/" + p.DefaultBranch); err != nil {
			tg.commit, err = head("HEAD")
		}
		if err != nil {
			return tg, fmt.Errorf("the repository has no commit: %w", err)
		}
		tg.note = from.Note
		if from.Incremental && from.Since != "" {
			// The merge base, in case history was rewritten since.
			base, err := r.Git.MergeBase(ctx, p.ID, p.Path, from.Since, tg.commit)
			if err != nil || base == "" {
				tg.note = "The interrupted run's base commit is gone: scanned everything"
			} else {
				tg.since, tg.incremental, tg.note = base, true, ""
			}
		}
		return r.changedFiles(ctx, p, t, tg)
	}
	if t.Scope == model.ScopeSinceMain {
		// The checkout's current branch against the default branch.
		if tg.commit, err = head("HEAD"); err != nil {
			return tg, fmt.Errorf("the checkout has no commit: %w", err)
		}
		main, err := head("refs/heads/" + p.DefaultBranch)
		if err != nil {
			return tg, fmt.Errorf("no branch %s: %w", p.DefaultBranch, err)
		}
		if tg.since, err = r.Git.MergeBase(ctx, p.ID, p.Path, main, tg.commit); err != nil {
			return tg, err
		}
		tg.incremental = true
		if tg.since == tg.commit {
			tg.skip = fmt.Sprintf("The checkout has nothing that %s doesn't", p.DefaultBranch)
			return tg, nil
		}
	} else {
		if tg.commit, err = head("refs/heads/" + p.DefaultBranch); err != nil {
			if tg.commit, err = head("HEAD"); err != nil {
				return tg, fmt.Errorf("the repository has no commit: %w", err)
			}
		}
	}
	if t.Scope == model.ScopeSinceLastScan {
		last, ok := r.Store.LastScanRun(p.ID, t.Name, func(x model.ScanRun) bool {
			return (x.Status == model.ScanSucceeded || x.Status == model.ScanSkipped) && x.Commit != ""
		})
		switch {
		case !ok:
			tg.note = "First run: scanned everything"
		case last.Commit == tg.commit:
			tg.skip = "Nothing changed since the last scan"
			return tg, nil
		default:
			// The merge base, in case history was rewritten since.
			base, err := r.Git.MergeBase(ctx, p.ID, p.Path, last.Commit, tg.commit)
			if err != nil || base == "" {
				tg.note = "The last scanned commit is gone: scanned everything"
				break
			}
			tg.since, tg.incremental = base, true
		}
	}
	return r.changedFiles(ctx, p, t, tg)
}

// changedFiles lists an incremental target's changed files in the scope,
// and skips it if there are none.
func (r *Runner) changedFiles(ctx context.Context, p model.Project, t Template, tg target) (target, error) {
	if tg.incremental {
		files, err := r.Git.ChangedFiles(ctx, p.ID, p.Path, tg.since, tg.commit)
		if err != nil {
			return tg, err
		}
		if t.Path != "" {
			files = underPath(files, t.Path)
		}
		if len(files) == 0 {
			tg.skip = "No files changed in the scanned scope"
			if t.Path != "" {
				tg.skip = "No files changed under " + t.Path
			}
		}
		tg.files = files
	}
	return tg, nil
}

func underPath(files []string, dir string) []string {
	var out []string
	for _, f := range files {
		if f == dir || strings.HasPrefix(f, strings.TrimSuffix(dir, "/")+"/") {
			out = append(out, f)
		}
	}
	return out
}

func (r *Runner) execute(ctx context.Context, p model.Project, t Template, run model.ScanRun, from *model.ScanRun) {
	logf, _ := r.openLog(run.ID)
	if logf != nil {
		defer logf.Close()
	}
	logLine := func(s string) {
		if logf != nil && s != "" {
			fmt.Fprintf(logf, "%s %s\n", time.Now().Format("15:04:05"), s)
		}
	}
	finish := func(fn func(*model.ScanRun)) {
		now := r.now()
		run, _ = r.Store.UpdateScanRun(run.ID, true, func(x *model.ScanRun) {
			fn(x)
			x.FinishedAt, x.Activity = &now, ""
		})
		if run.Error != "" {
			logLine("✖ " + run.Error)
		}
	}
	fail := func(err error) {
		finish(func(x *model.ScanRun) {
			x.Status, x.Error = model.ScanFailed, err.Error()
			if ctx.Err() != nil {
				x.Status, x.Error = model.ScanInterrupted, ""
				x.Note = "Dash shut down during the scan"
				if errors.Is(context.Cause(ctx), errUserCanceled) {
					x.Status, x.Note = model.ScanCanceled, ""
				}
			}
		})
	}
	logLine(fmt.Sprintf("▶ scan %s (%s) of %s", t.Name, t.Scope, p.Name))

	tg, err := r.resolve(ctx, p, t, from)
	if err != nil {
		fail(err)
		return
	}
	run, _ = r.Store.UpdateScanRun(run.ID, true, func(x *model.ScanRun) {
		x.Commit, x.Since, x.Incremental, x.Note = tg.commit, tg.since, tg.incremental, tg.note
	})
	if tg.skip != "" {
		logLine("⏭ " + tg.skip)
		finish(func(x *model.ScanRun) { x.Status, x.Note = model.ScanSkipped, tg.skip })
		return
	}

	dir := filepath.Join(r.Root, "worktrees", p.ID, run.ID)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		fail(err)
		return
	}
	if err := r.Git.AddDetachedWorktree(ctx, p.ID, p.Path, dir, tg.commit); err != nil {
		fail(fmt.Errorf("check out %s: %w", short(tg.commit), err))
		return
	}
	defer r.Git.RemoveDetachedWorktree(context.WithoutCancel(ctx), p.ID, p.Path, dir)

	set, has := r.Store.ScanSetting(p.ID, t.Name)
	tools, approved := Tools(t, set, has)
	if !approved {
		logLine(fmt.Sprintf("⚠ the template asks for tools that are not approved in Dash (%s): running with read-only tools", strings.Join(t.Tools, ", ")))
	}
	prompt := BuildPrompt(t, tg, r.knownIssues(p.ID, t, tg))
	logLine("prompt:\n" + prompt)
	var mu sync.Mutex // stdout and stderr report from their own goroutines
	var lastActivity time.Time
	timeout := r.runTimeout()
	runCtx, cancelRun := context.WithTimeout(ctx, timeout)
	defer cancelRun()
	extra := Args(t, tools)
	if r.GitTools != nil && r.GitURL != "" {
		token, release := r.GitTools.Register(dir)
		defer release()
		cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{GitServerName: GitServerConfig(r.GitURL, token)}})
		extra = append(extra, "--mcp-config", string(cfg))
	}
	res, err := r.Exec.Run(runCtx, agent.ExecRequest{Dir: dir, Prompt: prompt, ExtraArgs: extra}, func(e model.RunEvent) {
		mu.Lock()
		defer mu.Unlock()
		line := agent.Summary(e)
		logLine(line)
		if e.Kind != "tool_use" && e.Kind != "text" || time.Since(lastActivity) < time.Second {
			return
		}
		lastActivity = time.Now()
		act := line
		if e.Kind == "tool_use" {
			act = "Using " + e.Tool
		}
		_, _ = r.Store.UpdateScanRun(run.ID, false, func(x *model.ScanRun) { x.Activity = clip(oneLine(act), 120) })
	})
	run, _ = r.Store.UpdateScanRun(run.ID, false, func(x *model.ScanRun) { x.CostUSD, x.Turns = res.CostUSD, res.Turns })
	if ctx.Err() == nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		fail(fmt.Errorf("stopped: the scan ran longer than its %s limit", timeout))
		return
	}
	if err != nil {
		fail(err)
		return
	}
	found, err := ParseFindings(res.Result)
	if err != nil {
		run, _ = r.Store.UpdateScanRun(run.ID, false, func(x *model.ScanRun) { x.Answer = clip(res.Result, maxAnswer) })
		fail(err)
		return
	}
	issues, invalid := Issues(t, dir, found)
	for _, e := range invalid {
		logLine("dropped finding: " + e.Error())
	}
	run.Invalid = len(invalid)
	out, err := Record(r.Store, run, issues)
	if err != nil {
		fail(err)
		return
	}
	finish(func(x *model.ScanRun) {
		*x = out
		x.Status = model.ScanSucceeded
	})
	logLine(fmt.Sprintf("✔ %d findings: %d new, %d updated, %d known, %d dismissed, %d reopened",
		run.Findings, run.Counts.New, run.Counts.Updated, run.Counts.Known, run.Counts.Dismissed, run.Counts.Reopened))
}

// Record files a run's findings in the project's Issues (by fingerprint: a
// known issue is updated, a dismissed one stays dismissed, a resolved one
// is reopened) and returns the run with its counts.
func Record(st *store.Store, run model.ScanRun, issues []model.Issue) (model.ScanRun, error) {
	counts, outcomes, err := st.UpsertIssueBatch(run.ProjectID, "scan:"+run.Scan, issues)
	if err != nil {
		return run, err
	}
	run.Findings, run.Counts = len(issues), counts
	run.NewIssues, run.ReopenedIssues, run.Urgent = nil, nil, 0
	for _, o := range outcomes {
		switch o.Result {
		case model.UpsertNew:
			run.NewIssues = append(run.NewIssues, o.Issue.ID)
		case model.UpsertReopened:
			run.ReopenedIssues = append(run.ReopenedIssues, o.Issue.ID)
		default:
			continue
		}
		if o.Issue.Severity == model.SevCritical || o.Issue.Severity == model.SevHigh {
			run.Urgent++
		}
	}
	return run, nil
}

// knownIssues are the issues this scan reported before, in its scope, so
// the agent can reuse their fingerprints and leave dismissed ones out.
func (r *Runner) knownIssues(projectID string, t Template, tg target) []model.Issue {
	var in map[string]bool
	if tg.incremental {
		in = map[string]bool{}
		for _, f := range tg.files {
			in[f] = true
		}
	}
	var out []model.Issue
	for _, is := range r.Store.ListIssues(projectID) {
		if is.Source != t.Source() || is.Rule == "" {
			continue
		}
		if in != nil && !in[is.File] {
			continue
		}
		if in == nil && t.Path != "" && len(underPath([]string{is.File}, t.Path)) == 0 {
			continue
		}
		out = append(out, is)
		if len(out) == 100 {
			break
		}
	}
	return out
}

func (r *Runner) openLog(runID string) (*os.File, error) {
	path := r.LogPath(runID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Warn("scan log", "err", err)
		return nil, err
	}
	return os.Create(path)
}

// PruneLogs removes the logs of runs the store no longer keeps.
func (r *Runner) PruneLogs() {
	entries, _ := os.ReadDir(filepath.Join(r.Root, "logs"))
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".log")
		if _, err := r.Store.GetScanRun(id); errors.Is(err, store.ErrNotFound) {
			_ = os.Remove(filepath.Join(r.Root, "logs", e.Name()))
		}
	}
}

// Tools are the tools a scan runs with, and whether they are the ones its
// template asks for. A built-in's are trusted. A repository template's
// tools are code anyone who can merge may change (Bash is a shell on this
// computer), so they apply only while the user's approval covers exactly
// that list; otherwise the scan runs with the read-only DefaultTools.
func Tools(t Template, set model.ScanSetting, hasSetting bool) (tools []string, approved bool) {
	if len(t.Tools) == 0 {
		return DefaultTools, true
	}
	if t.Builtin || hasSetting && slices.Equal(set.ApprovedTools, t.Tools) {
		return t.Tools, true
	}
	return DefaultTools, false
}

// Args are the claude flags of a scan: its model and effort, and the tools
// it may use (see Tools). Edits are refused (the checkout is thrown away
// anyway).
//
// Scans run unattended, so nothing may wait for a permission answer: any
// tool call outside the allowed tools is denied at once (dontAsk, and no
// prompt handler), and the user's and project's settings are not loaded, so
// their hooks (e.g. a PermissionRequest hook that asks a phone) don't run.
func Args(t Template, tools []string) []string {
	if len(tools) == 0 {
		tools = DefaultTools
	}
	args := []string{
		"--setting-sources", "",
		"--permission-mode", "dontAsk",
		"--permission-prompts", "none",
		"--allowedTools", strings.Join(tools, ","),
		"--disallowedTools", "Edit,Write,NotebookEdit",
	}
	if t.Model != "" {
		args = append(args, "--model", t.Model)
	}
	if t.Effort != "" {
		args = append(args, "--effort", t.Effort)
	}
	return args
}

// BuildPrompt is what the scan agent is asked: the template's prompt, what
// to look at, the issues it reported before, and the answer format.
func BuildPrompt(t Template, tg target, known []model.Issue) string {
	var b strings.Builder
	b.WriteString(t.Render(t.Path, tg.since))
	b.WriteString("\n\n## Scope\n\n")
	where := "the whole repository (the current directory)"
	if t.Path != "" {
		where = "`" + t.Path + "`"
	}
	if tg.incremental {
		fmt.Fprintf(&b, "Only look at what changed in %s between commit %s and HEAD (the git_diff tool with from %s). "+
			"Report problems in the changed code, or that the change causes elsewhere; read other code only for context.\n",
			where, short(tg.since), tg.since)
		if len(tg.files) > 0 {
			b.WriteString("\nChanged files:\n")
			for i, f := range tg.files {
				if i == 300 {
					fmt.Fprintf(&b, "- … and %d more\n", len(tg.files)-300)
					break
				}
				fmt.Fprintf(&b, "- %s\n", f)
			}
		}
	} else {
		fmt.Fprintf(&b, "Look at %s.\n", where)
	}
	if len(known) > 0 {
		b.WriteString("\n## Already reported\n\nThis scan reported these before. If you find one of them again, report it with the same " +
			"fingerprint. Leave dismissed ones out: the team decided they are not a problem.\n\n")
		for _, is := range known {
			loc := is.File
			if loc == "" {
				loc = "-"
			}
			fmt.Fprintf(&b, "- fingerprint `%s` · %s · %s · %s\n", is.Rule, is.Status, loc, is.Title)
		}
	}
	b.WriteString(answerFormat)
	return b.String()
}

const answerFormat = `
## Answer

This is a read-only scan of a throwaway checkout: do not change, create or delete files, and do not commit.
You can only use the tools you were given; anything else (other commands, builds, tests, network) is refused, and nobody is there to approve it. Verify findings by reading the code instead. For git history use the git_diff, git_log, git_show and git_blame tools, if you have them.

End your final message with your findings as one JSON code block, and nothing after it:

` + "```json" + `
{"findings": [
  {
    "title": "One line saying what is wrong",
    "type": "bug | vuln | issue",
    "severity": "critical | high | medium | low | info",
    "file": "path/relative/to/the/repository/root",
    "line": 42,
    "details": "Markdown: what is wrong, why it matters, how it can happen",
    "fix": "Markdown: how to fix it",
    "fingerprint": "short-stable-id"
  }
]}
` + "```" + `

- "file" and "line" may be left out when a finding has no single place.
- "fingerprint" names the problem so that it stays the same when the code moves or you word the title differently, e.g. "sql-injection:UserStore.Find" or "missing-auth:/api/admin". Two findings of the same problem in one file share it.
- Report each problem once. If you find nothing, answer {"findings": []}.
`

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

func scanKey(projectID, scan string) string { return projectID + "/" + scan }
