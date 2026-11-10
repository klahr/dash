package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// A scan run has two parts: the bookkeeping (the run, its log, the issues
// it files), done where the board is, and the work on a checkout (working
// out the commits, a throwaway worktree, Claude), done where Claude runs.
// The hub, which runs no Claude, keeps the bookkeeping and sends the work,
// as a Job, to a runner with a clone of the project (Remote), which streams
// back what Hooks get. A runner's scans of its own projects do both.

// Job is a scan run's work on a checkout.
type Job struct {
	RunID    string   `json:"run_id,omitempty"` // the hub's run, for the worktree's name
	Template Template `json:"template"`
	// Tools are the tools Claude may use (see Tools: the template's once
	// approved, else read-only ones).
	Tools []string `json:"tools"`
	// Timeout bounds Claude's run (0: the Runner's RunTimeout).
	Timeout time.Duration `json:"timeout,omitempty"`
	// LastCommit is, for ScopeSinceLastScan, the commit the scan last
	// covered ("": none yet, scan everything).
	LastCommit string `json:"last_commit,omitempty"`
	// From is the interrupted run this one starts over, if any.
	From *Resume `json:"from,omitempty"`
	// Known are the issues this scan reported before; the prompt lists
	// those in the scanned scope.
	Known []model.Issue `json:"known,omitempty"`
	// Ref is the default branch's ref ("": refs/heads/<default>). A runner
	// scans origin's, which it fetched, rather than its clone's.
	Ref string `json:"-"`
}

// Resume is what a run that starts over an interrupted one keeps of it.
type Resume struct {
	Commit      string `json:"commit,omitempty"`
	Since       string `json:"since,omitempty"`
	Incremental bool   `json:"incremental,omitempty"`
	Note        string `json:"note,omitempty"`
}

// Outcome is what a Job found to scan, and Claude's answer.
type Outcome struct {
	Commit      string `json:"commit,omitempty"`
	Since       string `json:"since,omitempty"`
	Incremental bool   `json:"incremental,omitempty"`
	Note        string `json:"note,omitempty"`
	// Skip says why there was nothing to scan; Claude didn't run.
	Skip string `json:"skip,omitempty"`
	// Result is Claude's final message, with the findings.
	Result  string  `json:"result,omitempty"`
	CostUSD float64 `json:"cost_usd,omitempty"`
	Turns   int     `json:"turns,omitempty"`
	// Dir is the checkout Claude read, for findings with absolute paths.
	Dir string `json:"dir,omitempty"`
}

// Hooks hear how a Job goes. Event may be called from several goroutines.
type Hooks struct {
	Log    func(line string)      // a line for the run's log
	Target func(Outcome)          // the commits worked out, before Claude runs
	Event  func(e model.RunEvent) // Claude's progress
}

func (h Hooks) log(s string) {
	if h.Log != nil {
		h.Log(s)
	}
}

// Remote runs a Job on a runner with a clone of p. A *WaitError means no
// runner can take it now (the run waits and asks again); any other error
// fails the run.
type Remote interface {
	Scan(ctx context.Context, p model.Project, job Job, h Hooks) (Outcome, error)
}

// WaitError is why no runner can take a scan now.
type WaitError struct{ Reason string }

func (e *WaitError) Error() string { return e.Reason }

// Message is one message of a scan's stream from a runner to its hub:
// exactly one field is set, and Done or Error ends it.
type Message struct {
	Log    string          `json:"log,omitempty"`
	Target *Outcome        `json:"target,omitempty"`
	Event  *model.RunEvent `json:"event,omitempty"`
	Done   *Outcome        `json:"done,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Request is what the hub sends a runner to start a scan's Job: Project is
// the hub's id of a project linked to the runner.
type Request struct {
	Project string `json:"project"`
	Job     Job    `json:"job"`
}

// Check refuses a Job a runner shouldn't run as sent: claude's flags are
// built from its fields, so none may look like a flag.
func (j Job) Check() error {
	t := j.Template
	if !nameRE.MatchString(t.Name) {
		return fmt.Errorf("bad scan name %q", t.Name)
	}
	switch t.Scope {
	case model.ScopeRepo, model.ScopePath, model.ScopeSinceLastScan, model.ScopeSinceMain:
	default:
		return fmt.Errorf("bad scan scope %q", t.Scope)
	}
	for _, v := range append([]string{t.Model, t.Effort, j.LastCommit}, j.Tools...) {
		if strings.HasPrefix(strings.TrimSpace(v), "-") {
			return fmt.Errorf("bad scan setting %q", v)
		}
	}
	for _, v := range j.Tools {
		if strings.TrimSpace(v) == "" {
			return errors.New("empty tool name")
		}
	}
	return nil
}

// Work does job on this computer's checkout of p: works out the commits,
// checks the one to scan out in a throwaway worktree, and runs Claude
// there. It doesn't touch the store's scan runs (the caller keeps those).
func (r *Runner) Work(ctx context.Context, p model.Project, job Job, h Hooks) (Outcome, error) {
	if err := localrun.Check("scan " + job.Template.Name); err != nil {
		return Outcome{}, fmt.Errorf("%w: %w", agent.ErrNoLocalClaude, err)
	}
	t := job.Template
	tg, err := r.resolve(ctx, p, job)
	if err != nil {
		return Outcome{}, err
	}
	out := Outcome{Commit: tg.commit, Since: tg.since, Incremental: tg.incremental, Note: tg.note, Skip: tg.skip}
	if h.Target != nil {
		h.Target(out)
	}
	if tg.skip != "" {
		return out, nil
	}
	name := job.RunID
	if name == "" || filepath.Base(name) != name || strings.HasPrefix(name, ".") {
		name = store.NewID()
	}
	dir := filepath.Join(r.Root, "worktrees", p.ID, name)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return out, err
	}
	if err := r.Git.AddDetachedWorktree(ctx, p.ID, p.Path, dir, tg.commit); err != nil {
		return out, fmt.Errorf("check out %s: %w", short(tg.commit), err)
	}
	defer r.Git.RemoveDetachedWorktree(context.WithoutCancel(ctx), p.ID, p.Path, dir)

	// A scan for some kinds of file only (requires_files) doesn't run in a
	// repository with none of them.
	if ok, err := requiresMet(t, dir); err != nil {
		return out, err
	} else if !ok {
		out.Skip = "No files matching " + strings.Join(t.RequiresFiles, ", ")
		if t.Path != "" {
			out.Skip += " under " + t.Path
		}
		return out, nil
	}

	prompt := BuildPrompt(t, tg, knownIssues(job.Known, t, tg))
	h.log("prompt:\n" + prompt)
	timeout := job.Timeout
	if timeout <= 0 {
		timeout = r.runTimeout()
	}
	runCtx, cancelRun := context.WithTimeout(ctx, timeout)
	defer cancelRun()
	// The tools may read the checkout only: not the runner's data directory
	// (its hub token) or the user's credentials.
	extra := Args(t, job.Tools, dir, filepath.Dir(r.Root), r.Root)
	if r.GitTools != nil && r.GitURL != "" {
		token, release := r.GitTools.Register(dir)
		defer release()
		cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{GitServerName: GitServerConfig(r.GitURL, token)}})
		extra = append(extra, "--mcp-config", string(cfg))
	}
	emit := h.Event
	if emit == nil {
		emit = func(model.RunEvent) {}
	}
	res, err := r.Exec.Run(runCtx, agent.ExecRequest{Dir: dir, Prompt: prompt, ExtraArgs: extra}, emit)
	out.Result, out.CostUSD, out.Turns, out.Dir = res.Result, res.CostUSD, res.Turns, dir
	if ctx.Err() == nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return out, fmt.Errorf("stopped: the scan ran longer than its %s limit", timeout)
	}
	return out, err
}

func (r *Runner) waitEvery() time.Duration {
	if r.WaitEvery > 0 {
		return r.WaitEvery
	}
	return 5 * time.Second
}

// runRemote sends job to a runner, waiting for one while none can take it.
// The run shows why it waits.
func (r *Runner) runRemote(ctx context.Context, p model.Project, job Job, h Hooks) (Outcome, error) {
	var waiting string
	for {
		out, err := r.Remote.Scan(ctx, p, job, h)
		var w *WaitError
		if !errors.As(err, &w) {
			return out, err
		}
		if w.Reason != waiting {
			waiting = w.Reason
			h.log("⏸ " + w.Reason)
			_, _ = r.Store.UpdateScanRun(job.RunID, true, func(x *model.ScanRun) { x.Activity = clip(oneLine(w.Reason), 160) })
		}
		select {
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		case <-time.After(r.waitEvery()):
		}
	}
}
