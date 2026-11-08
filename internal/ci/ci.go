// Package ci follows GitHub Actions on each project's default branch: it
// keeps a build status for the dashboards and, when a build fails, queues a
// todo asking Claude to investigate and fix it.
package ci

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

type Publisher interface{ Publish(model.Event) }

// Build states.
const (
	Passing = "passing"
	Failing = "failing"
	Running = "running"
	None    = "none"    // the repository has no workflow runs yet
	Unknown = "unknown" // gh failed, see Status.Error
)

// Status is the build status of a project's default branch.
type Status struct {
	ProjectID string       `json:"project_id"`
	Branch    string       `json:"branch"`
	State     string       `json:"state"`
	SHA       string       `json:"sha,omitempty"` // commit the state describes
	Runs      []gitops.Run `json:"runs"`          // newest first
	Error     string       `json:"error,omitempty"`
	TodoID    string       `json:"todo_id,omitempty"` // open todo investigating a failure
	Checked   time.Time    `json:"checked"`
	// Release is the release tag Dash pushed last and how its build went.
	Release *Release `json:"release,omitempty"`
}

// Watcher polls GitHub Actions for every project with a GitHub remote.
type Watcher struct {
	Store *store.Store
	Git   *gitops.Git
	Pub   Publisher
	Kick  func()        // wake the todo engine after queueing a fix
	Every time.Duration // poll interval
	// AnyRemote also polls projects whose remote isn't on github.com (tests).
	AnyRemote bool

	mu       sync.Mutex
	status   map[string]Status
	busy     map[string]bool
	releases map[string]Release // project ID → the release being followed
}

func (w *Watcher) Run(ctx context.Context) {
	if w.Every <= 0 {
		w.Every = time.Minute
	}
	tick := time.NewTicker(w.Every)
	defer tick.Stop()
	for {
		for _, p := range w.Store.ListProjects() {
			if w.watched(p) {
				_, _ = w.Refresh(ctx, p.ID)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (w *Watcher) watched(p model.Project) bool {
	return p.RemoteURL != "" && (w.AnyRemote || strings.Contains(p.RemoteURL, "github.com"))
}

// Status returns the last known build status of a project.
func (w *Watcher) Status(projectID string) Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	st, ok := w.status[projectID]
	if !ok {
		st = Status{ProjectID: projectID, State: Unknown, Runs: []gitops.Run{}}
	}
	if rel, ok := w.releases[projectID]; ok {
		st.Release = &rel
	}
	return st
}

// Refresh checks GitHub now and investigates a new failure if the project wants that.
func (w *Watcher) Refresh(ctx context.Context, projectID string) (Status, error) {
	if !w.claim(projectID) {
		return w.Status(projectID), nil // a check is already running
	}
	defer w.release(projectID)
	p, err := w.Store.GetProject(projectID)
	if err != nil {
		return Status{}, errors.Join(errGone, err)
	}
	if !w.watched(p) {
		st := Status{ProjectID: p.ID, Branch: p.DefaultBranch, State: Unknown, Runs: []gitops.Run{},
			Error: "connect a GitHub repository to see builds", Checked: time.Now()}
		w.set(st)
		return st, nil
	}
	st := Status{ProjectID: p.ID, Branch: p.DefaultBranch, Checked: time.Now(), Release: w.refreshRelease(ctx, p)}
	runs, err := w.Git.Runs(ctx, p.ID, p.Path, p.DefaultBranch, 20)
	if err != nil {
		st.State, st.Runs, st.Error = Unknown, []gitops.Run{}, briefErr(err)
		w.set(st)
		return st, err
	}
	st.Runs = runs
	st.State, st.SHA = summarize(runs)
	if t := w.openFix(p.ID); t != nil {
		st.TodoID = t.ID
	}
	if p.InvestigatesCI() {
		if t, err := w.investigate(ctx, p, runs, false); err != nil {
			slog.Warn("investigate failed build", "project", p.Name, "err", err)
		} else if t != nil {
			st.TodoID = t.ID
		}
	} else {
		w.markHandled(p, runs)
	}
	w.set(st)
	return st, nil
}

// Investigate queues a fix for the current failure even if the project
// doesn't investigate automatically (the "Investigate" button).
func (w *Watcher) Investigate(ctx context.Context, projectID string) (model.Todo, error) {
	p, err := w.Store.GetProject(projectID)
	if err != nil {
		return model.Todo{}, err
	}
	if t := w.openFix(p.ID); t != nil {
		return *t, nil
	}
	runs, err := w.Git.Runs(ctx, p.ID, p.Path, p.DefaultBranch, 20)
	if err != nil {
		return model.Todo{}, err
	}
	if rel, ok := w.followed(p.ID); ok && rel.State == ReleaseFailed {
		// The release tag's runs are the newest: they decide for their workflows.
		runs = append(slices.Clone(rel.Runs), runs...)
	}
	t, err := w.investigate(ctx, p, runs, true)
	if err != nil {
		return model.Todo{}, err
	}
	if t == nil {
		return model.Todo{}, fmt.Errorf("the build on %s isn't failing", p.DefaultBranch)
	}
	go func() { _, _ = w.Refresh(context.Background(), p.ID) }()
	return *t, nil
}

// Retry re-runs the newest commit's build on the default branch (and the
// followed release's, when it failed): the failed jobs of failed workflows,
// or every finished workflow again when none failed. It returns the status
// checked right after.
func (w *Watcher) Retry(ctx context.Context, projectID string) (Status, error) {
	p, err := w.Store.GetProject(projectID)
	if err != nil {
		return Status{}, err
	}
	if !w.watched(p) {
		return Status{}, errors.New("connect a GitHub repository to see builds")
	}
	runs, err := w.Git.Runs(ctx, p.ID, p.Path, p.DefaultBranch, 20)
	if err != nil {
		return Status{}, err
	}
	var latest []gitops.Run
	if len(runs) > 0 {
		latest = latestPerWorkflow(runs, runs[0].SHA)
	}
	if rel, ok := w.followed(p.ID); ok && rel.State == ReleaseFailed {
		latest = append(latest, latestPerWorkflow(rel.Runs, rel.SHA)...)
	}
	targets, failedOnly := retryTargets(latest)
	if len(targets) == 0 {
		if len(latest) == 0 {
			return Status{}, fmt.Errorf("no builds ran on %s yet", p.DefaultBranch)
		}
		return Status{}, errors.New("the build is still running")
	}
	var errs []error
	for _, r := range targets {
		if err := w.Git.RerunRun(ctx, p.ID, p.Path, r.ID, failedOnly); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Workflow, err))
		}
	}
	if len(errs) == len(targets) {
		return Status{}, errors.Join(errs...)
	}
	if len(errs) > 0 {
		slog.Warn("retry build", "project", p.Name, "err", errors.Join(errs...))
	}
	st, _ := w.Refresh(ctx, p.ID)
	return st, nil
}

// retryTargets picks the runs to re-run: the failed ones (their failed jobs
// only), else every finished one.
func retryTargets(runs []gitops.Run) (out []gitops.Run, failedOnly bool) {
	for _, r := range runs {
		if r.Failed() {
			out = append(out, r)
		}
	}
	if len(out) > 0 {
		return out, true
	}
	for _, r := range runs {
		if r.Done() {
			out = append(out, r)
		}
	}
	return out, false
}

// summarize derives the state from the runs of the newest commit.
func summarize(runs []gitops.Run) (state, sha string) {
	if len(runs) == 0 {
		return None, ""
	}
	sha = runs[0].SHA
	state = Passing
	for _, r := range latestPerWorkflow(runs, sha) {
		switch {
		case r.Failed():
			return Failing, sha // a known failure outweighs workflows still running
		case !r.Done():
			state = Running
		}
	}
	return state, sha
}

// latestPerWorkflow keeps the newest run of each workflow, optionally only for one commit.
func latestPerWorkflow(runs []gitops.Run, sha string) []gitops.Run {
	seen := map[string]bool{}
	var out []gitops.Run
	for _, r := range runs {
		if (sha != "" && r.SHA != sha) || seen[r.Workflow] {
			continue
		}
		seen[r.Workflow] = true
		out = append(out, r)
	}
	return out
}

// investigate queues one todo for the workflows whose newest finished run
// failed and hasn't been looked at yet (or regardless, when forced).
func (w *Watcher) investigate(ctx context.Context, p model.Project, runs []gitops.Run, force bool) (*model.Todo, error) {
	var failed []gitops.Run
	seen := map[string]bool{}
	for _, r := range runs {
		if !r.Done() || seen[r.Workflow] {
			continue
		}
		seen[r.Workflow] = true // newest finished run per workflow decides
		if r.Failed() && (force || r.ID > p.CIHandledRun) {
			failed = append(failed, r)
		}
	}
	defer w.markHandled(p, runs)
	if len(failed) == 0 || (!force && w.openFix(p.ID) != nil) {
		return nil, nil
	}
	t, err := w.Store.CreateTodo(model.Todo{
		ProjectID: p.ID, Title: fixTitle(failed), Prompt: w.fixPrompt(ctx, p, failed), CIRun: failed[0].ID,
	})
	if err != nil {
		return nil, err
	}
	if t, err = w.Store.SetStatus(t.ID, model.StatusQueued, ""); err != nil {
		return nil, err
	}
	slog.Info("queued a todo for a failed build", "project", p.Name, "run", failed[0].URL)
	if w.Kick != nil {
		w.Kick()
	}
	return &t, nil
}

// markHandled remembers the newest finished run, so old failures are never
// investigated again.
func (w *Watcher) markHandled(p model.Project, runs []gitops.Run) {
	var newest int64
	for _, r := range runs {
		if r.Done() && r.ID > newest {
			newest = r.ID
		}
	}
	if newest > p.CIHandledRun {
		_, _ = w.Store.UpdateProject(p.ID, func(x *model.Project) error {
			x.CIHandledRun = max(x.CIHandledRun, newest)
			return nil
		})
	}
}

// openFix is a build-fix todo that hasn't finished yet.
func (w *Watcher) openFix(projectID string) *model.Todo {
	for _, t := range w.Store.ListTodos(projectID) {
		if t.CIRun != 0 && t.Status != model.StatusMerged && t.Status != model.StatusFailed {
			return &t
		}
	}
	return nil
}

func fixTitle(failed []gitops.Run) string {
	if len(failed) == 1 {
		return "Fix failing build: " + failed[0].Workflow
	}
	return fmt.Sprintf("Fix %d failing builds", len(failed))
}

func (w *Watcher) fixPrompt(ctx context.Context, p model.Project, failed []gitops.Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The GitHub Actions build on `%s` is failing:\n\n", p.DefaultBranch)
	for _, r := range failed {
		ref := ""
		if r.Branch != "" && r.Branch != p.DefaultBranch {
			ref = fmt.Sprintf(" for `%s` (a release tag)", r.Branch)
		}
		fmt.Fprintf(&b, "- %s (%s) on commit %s%s \"%s\": %s\n", r.Workflow, r.Conclusion, short(r.SHA), ref, r.Title, r.URL)
	}
	for i, r := range failed {
		if i == 2 {
			break
		}
		if log := w.Git.RunFailedLog(ctx, p.ID, p.Path, r.ID, 120); log != "" {
			fmt.Fprintf(&b, "\nLast lines of the failed log for %s:\n```\n%s\n```\n", r.Workflow, log)
		}
	}
	b.WriteString("\nFind out why it fails and fix it so the build passes. Reproduce it locally if you can. " +
		"Don't weaken or delete tests unless they are wrong. If the failure isn't caused by the code (a flaky " +
		"test, an expired secret, a GitHub outage), change nothing and explain what you found.")
	return b.String()
}

func (w *Watcher) claim(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.busy == nil {
		w.busy = map[string]bool{}
	}
	if w.busy[id] {
		return false
	}
	w.busy[id] = true
	return true
}

func (w *Watcher) release(id string) {
	w.mu.Lock()
	delete(w.busy, id)
	w.mu.Unlock()
}

// set stores a status and publishes it when it changed.
func (w *Watcher) set(st Status) {
	w.mu.Lock()
	if w.status == nil {
		w.status = map[string]Status{}
	}
	prev, had := w.status[st.ProjectID]
	w.status[st.ProjectID] = st
	w.mu.Unlock()
	if had && prev.State == st.State && prev.SHA == st.SHA && prev.TodoID == st.TodoID && prev.Error == st.Error &&
		len(prev.Runs) == len(st.Runs) && (len(st.Runs) == 0 || prev.Runs[0] == st.Runs[0]) && sameRelease(prev.Release, st.Release) {
		return
	}
	if w.Pub != nil {
		w.Pub.Publish(model.Event{Type: model.EvCI, ProjectID: st.ProjectID, Data: st, Time: time.Now()})
	}
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func briefErr(err error) string {
	for _, l := range strings.Split(err.Error(), "\n") {
		if strings.Contains(l, "gh auth login") {
			return "gh isn't logged in; run `gh auth login` on the Dash machine"
		}
	}
	s := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
