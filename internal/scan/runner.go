package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/disk"
	"github.com/klahr/dash/internal/fsutil"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/textutil"
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
// It runs at most one copy of each scan of a project at a time. On the
// hub, which doesn't run Claude, Claude's part runs on a runner (see Remote).
type Runner struct {
	Store *store.Store
	Git   *gitops.Git
	// Exec runs every scan, whatever agent the project's tasks use: it must
	// follow per-tool rules (agent.CanRuleTools), which keep a scan's reads
	// in its checkout (RunRequest). Claude Code does; Codex reads anywhere.
	Exec agent.Executor
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
	// Remote, on the hub, which doesn't run Claude itself, runs each scan's
	// Claude on a runner linked to the project; the hub keeps the runs,
	// their logs and the issues they file. Nil on a runner, which runs its
	// scans here.
	Remote Remote
	// WaitEvery is how often a run waiting for a runner asks again
	// (default 5s).
	WaitEvery time.Duration
	// LogMaxBytes caps a run's log (default DefaultLogMaxBytes; below 0, no
	// cap). A log that grows past it is cut to its newest half, behind a
	// note saying earlier output was trimmed.
	LogMaxBytes int64
	// Sandboxed says this computer runs Claude in a sandbox
	// (-claude-sandbox, docs/sandbox.md): a built-in scan that runs any
	// command (Template.ShellApproval) runs here without approval.
	Sandboxed bool

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

// DefaultLogMaxBytes is the cap on a run's log by default.
const DefaultLogMaxBytes = 32 << 20

func (r *Runner) logMaxBytes() int64 {
	switch {
	case r.LogMaxBytes > 0:
		return r.LogMaxBytes
	case r.LogMaxBytes < 0:
		return 0
	}
	return DefaultLogMaxBytes
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
	_ = removeTree(root)
	if !localrun.Allowed() {
		return // the hub's scans run on runners: it has no clones to prune
	}
	for _, p := range r.Store.ListProjects() {
		r.Git.PruneWorktrees(ctx, p.ID, p.Path)
	}
}

// LogPath is where a run's progress log is written.
func (r *Runner) LogPath(runID string) string {
	return filepath.Join(r.Root, "logs", runID+".log")
}

// Start begins a run of the project's scan and returns it while it runs.
// A scheduled run is for whoever last turned its schedule on or changed it
// (model.ScanSetting.ScheduledBy).
func (r *Runner) Start(projectID, name, trigger string) (model.ScanRun, error) {
	return r.start(projectID, name, trigger, nil, 0, "", false)
}

// StartFor is Start for the hub's user by, who started it: on a hub, it
// runs on one of their runners, or a shared one (model.ScanRun.StartedBy).
func (r *Runner) StartFor(projectID, name, trigger string, by int64) (model.ScanRun, error) {
	return r.start(projectID, name, trigger, nil, by, "", false)
}

// StartFullFor is StartFor for a full pass: a since_last_scan scan looks
// at everything, not only what changed since its last run.
func (r *Runner) StartFullFor(projectID, name, trigger string, by int64) (model.ScanRun, error) {
	return r.start(projectID, name, trigger, nil, by, "", true)
}

// StartOn is Start on a hub, on a runner logged in to the Claude account
// account ("": any of them), as an early run picks one with spare usage.
func (r *Runner) StartOn(projectID, name, trigger, account string) (model.ScanRun, error) {
	return r.start(projectID, name, trigger, nil, 0, account, false)
}

// start begins a run for by; from, if set, is the interrupted run it starts
// over, whose scope, path, base commit and user it keeps. account, on a
// hub, is the Claude account whose runners may take it ("": any). full
// asks a since_last_scan scan for a full pass.
func (r *Runner) start(projectID, name, trigger string, from *model.ScanRun, by int64, account string, full bool) (model.ScanRun, error) {
	p, err := r.Store.GetProject(projectID)
	if err != nil {
		return model.ScanRun{}, err
	}
	t, ok := FindIn(r.Store, p, name)
	if !ok {
		return model.ScanRun{}, fmt.Errorf("%w %q in %s", ErrNoScan, name, p.Name)
	}
	resumedFrom := ""
	if (trigger == model.TriggerSchedule || trigger == model.TriggerEarly) && by == 0 {
		if set, ok := r.Store.ScanSetting(projectID, t.Name); ok {
			by = set.ScheduledBy
		}
	}
	if from != nil {
		by = from.StartedBy
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
		Status: model.ScanRunning, StartedAt: r.now(), Activity: "Starting", ResumedFrom: resumedFrom, StartedBy: by,
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
		r.execute(ctx, p, t, run, from, account, full)
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
// resumed run (job.From set) scans the current commit but keeps the
// original run's base, so it covers at least what the original would have.
func (r *Runner) resolve(ctx context.Context, p model.Project, job Job) (target, error) {
	t, from := job.Template, job.From
	var tg target
	head := func(ref string) (string, error) { return r.Git.Commit(ctx, p.ID, p.Path, ref) }
	mainRef := job.Ref
	if mainRef == "" {
		mainRef = "refs/heads/" + p.DefaultBranch
	}
	var err error
	if from != nil && from.Commit != "" {
		// The original had worked out its target before it was cut off.
		if t.Scope == model.ScopeSinceMain {
			tg.commit, err = head("HEAD")
		} else if tg.commit, err = head(mainRef); err != nil {
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
		main, err := head(mainRef)
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
		if tg.commit, err = head(mainRef); err != nil {
			if tg.commit, err = head("HEAD"); err != nil {
				return tg, fmt.Errorf("the repository has no commit: %w", err)
			}
		}
	}
	if t.Scope == model.ScopeSinceLastScan {
		switch {
		case job.Full != "":
			tg.note = job.Full
		case job.LastCommit == "":
			tg.note = "First run: scanned everything"
		case job.LastCommit == tg.commit:
			tg.skip = "Nothing changed since the last scan"
			return tg, nil
		default:
			// The merge base, in case history was rewritten since.
			base, err := r.Git.MergeBase(ctx, p.ID, p.Path, job.LastCommit, tg.commit)
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

// remote reports whether runs go to a runner (see Runner.Remote).
func (r *Runner) remote() bool { return r.Remote != nil }

func (r *Runner) execute(ctx context.Context, p model.Project, t Template, run model.ScanRun, from *model.ScanRun, account string, full bool) {
	logf, _ := r.openLog(run.ID)
	defer func() {
		if logf != nil {
			logf.Close()
		}
	}()
	var logMu sync.Mutex // Claude's stdout and stderr report from their own goroutines
	var logSize int64
	logLine := func(s string) {
		logMu.Lock()
		defer logMu.Unlock()
		if logf == nil || s == "" {
			return
		}
		at := time.Now().Format("15:04:05")
		n, _ := fmt.Fprintf(logf, "%s %s\n", at, s)
		logSize += int64(n)
		if max := r.logMaxBytes(); max > 0 && logSize > max {
			// Cut it to its newest half, as todo logs are (queue.TodoLog).
			path := logf.Name()
			logf.Close()
			logSize = disk.CapFile(path, max, func([]byte) string {
				return at + " " + disk.TrimmedNote(max) + "\n"
			})
			logf, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
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

	set, has := r.Store.ScanSetting(p.ID, t.Name)
	tools, approved := Tools(t, set, has)
	sandboxTools := SandboxTools(t, approved)
	switch {
	case approved:
	case sandboxTools != nil:
		logLine(fmt.Sprintf("⚠ %s needs approval for this project (%s), or a runner that runs Claude in a sandbox (-claude-sandbox): "+
			"without either it doesn't run", t.Name, strings.Join(t.Tools, ", ")))
	default:
		logLine(fmt.Sprintf("⚠ the template asks for tools that are not approved in Dash (%s): running with read-only tools", strings.Join(t.Tools, ", ")))
	}
	job := Job{RunID: run.ID, Template: t, Tools: tools, SandboxTools: sandboxTools, Timeout: r.runTimeout(), Known: r.scanIssues(p.ID, t), For: run.StartedBy, Account: account}
	if from != nil {
		job.From = &Resume{Commit: from.Commit, Since: from.Since, Incremental: from.Incremental, Note: from.Note}
	}
	if t.Scope == model.ScopeSinceLastScan {
		if last, ok := r.Store.LastScanRun(p.ID, t.Name, func(x model.ScanRun) bool {
			return (x.Status == model.ScanSucceeded || x.Status == model.ScanSkipped) && x.Commit != ""
		}); ok {
			job.LastCommit = last.Commit
		}
		job.Full = r.fullPass(p.ID, t, run, full, job.LastCommit != "")
		if job.Full != "" {
			// A runner that doesn't know Full still scans everything.
			job.LastCommit = ""
		}
	}
	var mu sync.Mutex
	var lastActivity time.Time
	hooks := Hooks{
		Log: logLine,
		Target: func(o Outcome) {
			run, _ = r.Store.UpdateScanRun(run.ID, true, func(x *model.ScanRun) {
				x.Commit, x.Since, x.Incremental, x.Note = o.Commit, o.Since, o.Incremental, o.Note
			})
		},
		Event: func(e model.RunEvent) {
			line := agent.Summary(e)
			logLine(line)
			mu.Lock()
			defer mu.Unlock()
			if e.Kind != "tool_use" && e.Kind != "text" || time.Since(lastActivity) < time.Second {
				return
			}
			lastActivity = time.Now()
			act := line
			if e.Kind == "tool_use" {
				act = "Using " + e.Tool
			}
			_, _ = r.Store.UpdateScanRun(run.ID, false, func(x *model.ScanRun) { x.Activity = textutil.Clip(oneLine(act), 120) })
		},
	}
	var out Outcome
	var err error
	if r.remote() {
		out, err = r.runRemote(ctx, p, job, hooks)
	} else {
		out, err = r.Work(ctx, p, job, hooks)
	}
	if err == nil && out.Skip != "" {
		logLine("⏭ " + out.Skip)
		finish(func(x *model.ScanRun) { x.Status, x.Note = model.ScanSkipped, out.Skip })
		return
	}
	if out.CostUSD != 0 || out.Turns != 0 {
		run, _ = r.Store.UpdateScanRun(run.ID, false, func(x *model.ScanRun) { x.CostUSD, x.Turns = out.CostUSD, out.Turns })
	}
	if err != nil {
		fail(err)
		return
	}
	found, err := ParseFindings(out.Result)
	if err != nil {
		run, _ = r.Store.UpdateScanRun(run.ID, false, func(x *model.ScanRun) { x.Answer = textutil.Clip(out.Result, maxAnswer) })
		fail(err)
		return
	}
	issues, invalid := Issues(t, out.Dir, found)
	for _, e := range invalid {
		logLine("dropped finding: " + e.Error())
	}
	run.Invalid = len(invalid)
	rec, err := Record(r.Store, run, issues)
	if err != nil {
		fail(err)
		return
	}
	finish(func(x *model.ScanRun) {
		*x = rec
		x.Status = model.ScanSucceeded
	})
	logLine(fmt.Sprintf("✔ %d findings: %d new, %d updated, %d known, %d dismissed, %d reopened",
		run.Findings, run.Counts.New, run.Counts.Updated, run.Counts.Known, run.Counts.Dismissed, run.Counts.Reopened))
}

// fullPass is why a since_last_scan run scans everything ("": it looks
// only at what changed): asked is a full pass asked for by hand; otherwise
// the template's FullScanDays have passed since its last full pass (a
// succeeded run that wasn't incremental), or no kept run was one. hasLast
// says an earlier run covered a commit (else the run is a first run anyway).
func (r *Runner) fullPass(projectID string, t Template, run model.ScanRun, asked, hasLast bool) string {
	if asked {
		return "Full scan, asked for: scanned everything"
	}
	if t.FullScanDays <= 0 || !hasLast {
		return ""
	}
	last, ok := r.Store.LastScanRun(projectID, t.Name, func(x model.ScanRun) bool {
		return x.ID != run.ID && x.Status == model.ScanSucceeded && x.Commit != "" && !x.Incremental
	})
	if !ok {
		return fmt.Sprintf("Periodic full scan (every %d days), none in the kept runs: scanned everything", t.FullScanDays)
	}
	if r.now().Sub(last.StartedAt) < time.Duration(t.FullScanDays)*24*time.Hour {
		return ""
	}
	return fmt.Sprintf("Periodic full scan (every %d days): scanned everything", t.FullScanDays)
}

// Record files a run's findings as draft tasks on the project's Board, by
// fingerprint: one a task has already bumps that task, a dismissed one
// adds nothing, one a merged task had fixed comes back as a new draft
// (store.Store.ReportFindings). It returns the run with its counts.
func Record(st *store.Store, run model.ScanRun, issues []model.Issue) (model.ScanRun, error) {
	counts, outcomes, err := st.ReportFindings(run.ProjectID, "scan:"+run.Scan, issues)
	if err != nil {
		return run, err
	}
	run.Findings, run.Counts = len(issues), counts
	run.NewTasks, run.ReopenedTasks = nil, nil
	run.Critical, run.High, run.Urgent = 0, 0, 0
	for _, o := range outcomes {
		switch o.Result {
		case model.UpsertNew:
			run.NewTasks = append(run.NewTasks, o.Todo.ID)
		case model.UpsertReopened:
			run.ReopenedTasks = append(run.ReopenedTasks, o.Todo.ID)
		default:
			continue
		}
		switch o.Issue.Severity {
		case model.SevCritical:
			run.Critical++
		case model.SevHigh:
			run.High++
		}
	}
	run.Urgent = run.Critical + run.High
	return run, nil
}

// maxKnown is how many issues a scan's prompt lists as reported before.
const maxKnown = 100

// Known is an issue a scan reported before, for its next prompt: on a
// task that isn't merged ("open"), or dismissed.
type Known struct {
	Rule   string `json:"rule"`
	File   string `json:"file,omitempty"`
	Title  string `json:"title"`
	Status string `json:"status"` // open or dismissed
}

// scanIssues are the issues this scan reported before (at most a few
// hundred), for knownIssues: those on tasks that aren't merged, most
// severe first, then the dismissed ones.
func (r *Runner) scanIssues(projectID string, t Template) []Known {
	var out []Known
	full := func() bool { return len(out) >= 10*maxKnown }
	var open []model.Issue
	for _, td := range r.Store.ListTodos(projectID) {
		if td.Finding == nil || td.Status == model.StatusMerged {
			continue
		}
		for _, is := range td.Finding.Issues {
			if is.Source == t.Source() && is.Rule != "" {
				open = append(open, is)
			}
		}
	}
	slices.SortStableFunc(open, func(a, b model.Issue) int { return a.Severity.Rank() - b.Severity.Rank() })
	for _, is := range open {
		if full() {
			return out
		}
		out = append(out, Known{Rule: is.Rule, File: is.File, Title: is.Title, Status: "open"})
	}
	for _, d := range r.Store.ListDismissed(projectID) {
		if full() {
			break
		}
		if d.Source == t.Source() && d.Rule != "" {
			out = append(out, Known{Rule: d.Rule, File: d.File, Title: d.Title, Status: "dismissed"})
		}
	}
	return out
}

// knownIssues are the issues of all this scan reported before in its scope,
// so the agent can reuse their fingerprints and leave dismissed ones out.
func knownIssues(all []Known, t Template, tg target) []Known {
	var in map[string]bool
	if tg.incremental {
		in = map[string]bool{}
		for _, f := range tg.files {
			in[f] = true
		}
	}
	var out []Known
	for _, is := range all {
		if in != nil && !in[is.File] {
			continue
		}
		if in == nil && t.Path != "" && len(underPath([]string{is.File}, t.Path)) == 0 {
			continue
		}
		out = append(out, is)
		if len(out) == maxKnown {
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
// template asks for. A built-in's and a Dash scan's are trusted. A repository template's
// tools are code anyone who can merge may change (Bash is a shell on this
// computer), so they apply only while the user's approval covers exactly
// that list; otherwise the scan runs with the read-only DefaultTools. A
// built-in that runs any command (Template.ShellApproval) needs the same
// approval, or a runner with a sandbox (SandboxTools).
func Tools(t Template, set model.ScanSetting, hasSetting bool) (tools []string, approved bool) {
	if len(t.Tools) == 0 {
		return DefaultTools, true
	}
	if t.Trusted() && !t.ShellApproval() || hasSetting && slices.Equal(set.ApprovedTools, t.Tools) {
		return t.Tools, true
	}
	return DefaultTools, false
}

// SandboxTools are the tools a scan that Tools left read-only may use all
// the same on a runner that runs Claude in a sandbox (Runner.Sandboxed):
// a ShellApproval built-in's; nil for any other.
func SandboxTools(t Template, approved bool) []string {
	if approved || !t.ShellApproval() {
		return nil
	}
	return slices.Clone(t.Tools)
}

// RunRequest is the agent run of a scan, without its directory and prompt:
// its model and effort, and the tools it may use (see Tools). Edits are
// refused (the checkout is thrown away anyway).
//
// Scans run unattended (agent.ExecRequest.Unattended), so nothing may wait
// for a permission answer: any tool call outside the allowed tools is
// denied at once, and the user's and project's settings are not loaded, so
// their hooks (e.g. a PermissionRequest hook that asks a phone) don't run.
//
// With a checkout dir, the file tools are confined to it (see Confine): a
// prompt injection in the scanned code must not be able to read the
// runner's token or the user's credentials and quote them in a finding.
func RunRequest(t Template, tools []string, dir string, secrets ...string) agent.ExecRequest {
	if len(tools) == 0 {
		tools = DefaultTools
	}
	deny := []string{"Edit", "Write", "NotebookEdit"}
	req := agent.ExecRequest{Unattended: true, Model: t.Model, Effort: t.Effort}
	if dir != "" {
		tools = Confine(tools, dir)
		deny = append(deny, DenyReads(dir, secrets)...)
		if usesShell(tools) {
			req.Settings = SandboxSettings(dir, secrets)
			req.Env = CacheEnv(CacheDir(dir))
		}
	}
	req.AllowedTools, req.DeniedTools = tools, deny
	return req
}

// SandboxSettings are the Claude Code settings (claude --settings) of a
// scan that may run commands: Bash runs in Claude Code's sandbox, which
// can't read the credentials DenyReads keeps from the file tools, may write
// only the checkout, the temp directory, the run's own throwaway caches
// (CacheDir, see CacheEnv) and the audit tools' advisory caches, and
// reaches only the package registries the audit tools ask. Nothing
// runs outside it (allowUnsandboxedCommands). Where the sandbox can't start
// (Linux without bubblewrap, Windows), Claude Code warns and runs commands
// without it: see docs/scans.md.
func SandboxSettings(dir string, secrets []string) string {
	var denyRead []string
	for _, s := range secretPaths(dir, secrets) {
		if !strings.Contains(s.path, "*") {
			denyRead = append(denyRead, s.path)
			continue
		}
		// The sandbox takes paths; spell a pattern out as the files there now.
		matches, _ := filepath.Glob(s.path)
		denyRead = append(denyRead, matches...)
	}
	set := map[string]any{"sandbox": map[string]any{
		"enabled":                  true,
		"allowUnsandboxedCommands": false,
		// Go's tools (govulncheck, go test) need macOS's trust daemon for TLS.
		"enableWeakerNetworkIsolation": true,
		"network":                      map[string]any{"allowedDomains": registryDomains},
		"filesystem":                   map[string]any{"denyRead": denyRead, "allowWrite": writableDirs(dir)},
	}}
	b, _ := json.Marshal(set)
	return string(b)
}

// registryDomains are the package registries and advisory databases a
// scan's commands may reach: what govulncheck, npm audit, pip-audit,
// cargo audit and the toolchains' module downloads use.
var registryDomains = []string{
	"proxy.golang.org", "sum.golang.org", "vuln.go.dev", "storage.googleapis.com",
	"registry.npmjs.org", "registry.yarnpkg.com",
	"pypi.org", "files.pythonhosted.org", "api.osv.dev",
	"github.com", "index.crates.io", "static.crates.io",
}

// CacheDir is the throwaway cache directory of a scan run checked out in
// dir: beside the checkout, and removed with it (RemoveCache).
func CacheDir(dir string) string { return dir + ".cache" }

// CacheEnv points the package managers' caches of a scan's commands at
// cache, the run's own (CacheDir). Code a scan runs (a repository's tests,
// a build tool's config) must not write the shared caches (GOMODCACHE,
// GOCACHE, ~/.npm, …): the user's tasks build and test from them later,
// outside any sandbox.
func CacheEnv(cache string) []string {
	return []string{
		"GOCACHE=" + filepath.Join(cache, "go-build"),
		"GOMODCACHE=" + filepath.Join(cache, "go-mod"),
		"npm_config_cache=" + filepath.Join(cache, "npm"),
		"PIP_CACHE_DIR=" + filepath.Join(cache, "pip"),
		"XDG_CACHE_HOME=" + filepath.Join(cache, "xdg"),
	}
}

// RemoveCache removes a run's CacheDir. Go makes its module cache
// read-only, so the directories are made writable first.
func RemoveCache(dir string) error { return removeTree(CacheDir(dir)) }

// removeTree is os.RemoveAll that also removes read-only directories
// (a Go module cache's).
func removeTree(p string) error {
	_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(path, 0o755)
		}
		return nil
	})
	return os.RemoveAll(p)
}

// writableDirs are where a scan's commands in the checkout dir may write
// besides it and the temp directory: the run's CacheDir, and the advisory
// databases of the audit tools (cargo audit's, pip-audit's), which no
// build reads.
func writableDirs(dir string) []string {
	out := []string{CacheDir(dir)}
	if c, err := os.UserCacheDir(); err == nil {
		out = append(out, filepath.Join(c, "pip-audit"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out, filepath.Join(home, ".cargo", "advisory-db"))
	}
	return out
}

// fileTools read files; unscoped, they read anything the runner's user can.
var fileTools = []string{"Read", "Grep", "Glob"}

// pathRule is a permission rule for tool on the absolute path p and, with
// tree, everything below it ("//" marks an absolute path).
func pathRule(tool, p string, tree bool) string {
	r := tool + "(/" + rulePath(p)
	if tree {
		r += "/**"
	}
	return r + ")"
}

// rulePath is the absolute path p in the POSIX form Claude Code matches
// permission rules against: forward slashes, and on Windows the drive as a
// lower-case first segment (C:\Users\a becomes /c/Users/a).
func rulePath(p string) string {
	s := filepath.ToSlash(p)
	if v := filepath.VolumeName(p); len(v) == 2 && v[1] == ':' {
		s = "/" + strings.ToLower(v[:1]) + s[2:]
	}
	return "/" + strings.TrimPrefix(s, "/")
}

// Confine replaces the unscoped file tools (Read, Grep, Glob) in tools with
// ones limited to the checkout dir. Other tools, and file tools a trusted
// or approved template already scoped itself, are left as they are.
func Confine(tools []string, dir string) []string {
	dirs := []string{dir}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != dir {
		dirs = append(dirs, real)
	}
	out := make([]string, 0, len(tools)+len(dirs)*len(fileTools))
	for _, tool := range tools {
		if !slices.Contains(fileTools, strings.TrimSpace(tool)) {
			out = append(out, tool)
			continue
		}
		for _, d := range dirs {
			out = append(out, pathRule(strings.TrimSpace(tool), d, true))
		}
	}
	return out
}

// homeSecrets are credentials in the user's home a scan never reads.
var homeSecrets = []string{
	".ssh", ".gnupg", ".aws", ".config/gh", ".config/gcloud", ".claude", ".claude.json",
	".netrc", ".git-credentials", ".docker", ".kube",
	".npmrc", ".pypirc", ".cargo/credentials", ".cargo/credentials.toml", ".gem/credentials",
	".local/share/dash", ".local/share/dash-runner", ".local/share/dash-hub",
}

// dataSecrets are the files in a Dash data directory a scan never reads:
// the runner's registration (its hub token), the database, other runs'
// logs and the tasks' worktrees.
var dataSecrets = []string{"*.json", "dash.db*", "logs", "worktrees", "uploads"}

// DenyReads are deny rules for the file tools on credentials: the ones in
// the user's home, and the Dash data directories (secrets, e.g. the parent
// of the runner's scan root). A deny rule wins over an allow rule, so a
// directory that holds the checkout dir itself is not denied whole, only
// its known secret files.
func DenyReads(dir string, secrets []string) []string {
	var deny []string
	add := func(p string, tree bool) {
		for _, tool := range fileTools {
			r := pathRule(tool, p, tree)
			if !slices.Contains(deny, r) {
				deny = append(deny, r)
			}
		}
	}
	for _, s := range secretPaths(dir, secrets) {
		add(s.path, false)
		if s.tree {
			add(s.path, true)
		}
	}
	return deny
}

// secret is a credential path a scan never reads, and whether everything
// below it is one too; path may hold a * pattern.
type secret struct {
	path string
	tree bool
}

// secretPaths are the credentials of DenyReads, as paths: the ones in the
// user's home, and in each Dash data directory in secrets its known secret
// files, or all of it when it doesn't hold the checkout dir.
func secretPaths(dir string, secrets []string) []secret {
	var paths []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, s := range homeSecrets {
			paths = append(paths, filepath.Join(home, s))
		}
	}
	paths = append(paths, secrets...)
	inside := func(parent string) bool { return fsutil.Within(parent, dir) }
	var out []secret
	add := func(s secret) {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	for _, p := range paths {
		if p == "" || !filepath.IsAbs(p) {
			continue
		}
		p = filepath.Clean(p)
		if !inside(p) {
			add(secret{p, true})
			continue
		}
		for _, s := range dataSecrets {
			sub := filepath.Join(p, s)
			if inside(sub) {
				continue
			}
			add(secret{sub, !strings.Contains(s, "*")})
		}
	}
	return out
}

// BuildPrompt is what the scan agent is asked: the template's prompt, what
// to look at, the issues it reported before, what it may do with the tools
// it runs with (see Tools), and the answer format.
func BuildPrompt(t Template, tools []string, tg target, known []Known) string {
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
			where, gitops.ShortSHA(tg.since), tg.since)
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
	b.WriteString("\n## Answer\n\n")
	b.WriteString(toolRules(tools))
	b.WriteString(answerFormat)
	return b.String()
}

// gitToolsNote points a scan at the git MCP tools for history.
const gitToolsNote = "For git history use the git_diff, git_log, git_show, git_blame and git_grep tools, if you have them.\n"

// toolRules says what a scan may do with its tools. Without Bash it is
// read-only. With Bash (or Bash(...) rules) it may run the commands its
// prompt asks for and write to a temp directory, but still must not change
// the repository's files.
func toolRules(tools []string) string {
	all, rules := bashTools(tools)
	if !all && len(rules) == 0 {
		return "This is a read-only scan of a throwaway checkout: do not change, create or delete files, and do not commit.\n" +
			"You can only use the tools you were given; anything else (other commands, builds, tests, network) is refused, " +
			"and nobody is there to approve it. Verify findings by reading the code instead. " + gitToolsNote
	}
	var b strings.Builder
	b.WriteString("This scan runs in a throwaway checkout. Do not change, create or delete the repository's files, and do not commit. " +
		"You may write only to a temporary directory: make one with `mktemp -d` and put output files there.\n")
	if all {
		b.WriteString("You may run shell commands with Bash: the ones these instructions call for (builds, tests, audit tools, " +
			"and the network access they need) and commands that read the code. ")
	} else {
		cmds := make([]string, len(rules))
		for i, r := range rules {
			if p, ok := strings.CutSuffix(r, ":*"); ok {
				cmds[i] = "commands starting with `" + p + "`"
			} else {
				cmds[i] = "`" + r + "`"
			}
		}
		b.WriteString("You may run only these shell commands with Bash: " + strings.Join(cmds, ", ") + ". ")
	}
	b.WriteString("Do not install anything. Any other tool or command is refused, and nobody is there to approve it. " + gitToolsNote)
	return b.String()
}

// bashTools reports whether tools allow any Bash command (all), and else
// the commands of their Bash(...) rules.
func bashTools(tools []string) (all bool, rules []string) {
	for _, tool := range tools {
		tool = strings.TrimSpace(tool)
		if tool == "Bash" {
			return true, nil
		}
		if r, ok := strings.CutPrefix(tool, "Bash("); ok {
			if r, ok = strings.CutSuffix(r, ")"); ok && strings.TrimSpace(r) != "" {
				rules = append(rules, strings.TrimSpace(r))
			}
		}
	}
	return false, rules
}

const answerFormat = `
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

### Severity

One scale for every scan. Only critical and high findings show on the team's Board; the rest wait in a list, so use those two only when they hold.

- "critical": fix today. An exploitable vulnerability with severe impact that anyone who can reach the app can use (remote code execution, authentication bypass, reading or changing other users' data, a leaked live secret), or data loss or corruption in normal use.
- "high": fix soon. A vulnerability with a confirmed attack path but limited impact or needing an account or a victim's action; a bug that breaks a main feature or gives wrong results in ordinary use; a crash or hang an ordinary input triggers; a broken build or tests failing on every run on the default branch.
- "medium": a real problem with limited reach: an edge case or a secondary feature, a weakness that needs an unusual setup or another flaw first, a cost that grows with use, a finding you could not confirm.
- "low": worth doing, but nothing breaks: hardening, cleanup, small savings, missing tests or docs for ordinary code.
- "info": a note with nothing to fix.

When torn between two, pick the lower and say why in the details. A scan's own rules above say how this scale applies to what it looks for.
`

func scanKey(projectID, scan string) string { return projectID + "/" + scan }
