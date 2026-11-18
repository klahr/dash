package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// ConflictWatch finds merge conflicts before the merge queue does. For each
// project it looks at the tasks whose branches are here (in progress, in
// review or merging) and asks git merge-tree, which needs no checkout,
// whether each pair of them, and each of them with the default branch,
// would merge cleanly. Only pairs whose changed files overlap are merged.
// The conflicts go on the tasks (model.Todo.Conflicts), and a task that
// gains a conflict with a task ahead of it in the merge queue gets a note
// for Claude, once per such task.
//
// It looks again a moment after a task changes (a commit, a new status, a
// task landing on the default branch), and every Every for what moves
// without telling the store (a push to the default branch, a fetch). A
// pass whose branches haven't moved does no more than one for-each-ref.
type ConflictWatch struct {
	Store *store.Store
	Git   *gitops.Git
	Log   *TodoLog
	Delay time.Duration // wait this long after a change for more (default 3s)
	Every time.Duration // look at every project this often (default 1 minute)

	mu    sync.Mutex
	dirty map[string]bool // projects changed since the last pass
	wake  chan struct{}
	off   atomic.Bool // git can't (too old): nothing is checked

	// The worker's own, in Run's goroutine only.
	seen   map[string]string      // project id → its branches as last checked
	merges map[[2]string][]string // two commits → the files they conflict in (none: clean)
	forks  map[[2]string][]string // base and branch commit → the branch's files since it forked
}

// NewConflictWatch returns w ready to Observe and Run.
func NewConflictWatch(w *ConflictWatch) *ConflictWatch {
	w.wake = make(chan struct{}, 1)
	w.dirty = map[string]bool{}
	if w.Delay <= 0 {
		w.Delay = 3 * time.Second
	}
	if w.Every <= 0 {
		w.Every = time.Minute
	}
	return w
}

// Observe takes the store's events: a task changed, so its project is
// looked at again soon. It never blocks, and never calls the store (which
// holds its lock while it emits).
func (w *ConflictWatch) Observe(ev model.Event) {
	if ev.ProjectID == "" || (ev.Type != model.EvTodoUpdated && ev.Type != model.EvProjectUpdated) {
		return
	}
	if t, ok := ev.Data.(model.Todo); ok && t.OnRunner() {
		return // its branch is on the runner, which checks it
	}
	w.Changed(ev.ProjectID)
}

// Changed asks for project id to be looked at again soon.
func (w *ConflictWatch) Changed(id string) {
	w.mu.Lock()
	w.dirty[id] = true
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run looks at the projects that changed, a moment after they did, and at
// every project now and then, until ctx ends.
func (w *ConflictWatch) Run(ctx context.Context) {
	tick := time.NewTicker(w.Every)
	defer tick.Stop()
	for _, p := range w.Store.ListProjects() {
		w.Changed(p.ID) // a first look, at start
	}
	for {
		var ids []string
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
			select { // let a burst of changes settle
			case <-ctx.Done():
				return
			case <-time.After(w.Delay):
			}
			w.mu.Lock()
			for id := range w.dirty {
				ids = append(ids, id)
			}
			clear(w.dirty)
			w.mu.Unlock()
		case <-tick.C:
			for _, p := range w.Store.ListProjects() {
				ids = append(ids, p.ID)
			}
		}
		for _, id := range ids {
			if ctx.Err() != nil || w.off.Load() {
				return
			}
			p, err := w.Store.GetProject(id)
			if err != nil {
				continue
			}
			if err := w.Check(ctx, p); errors.Is(err, gitops.ErrNoMergeTree) {
				slog.Warn("early conflict check off", "err", err)
				w.off.Store(true)
			} else if err != nil && ctx.Err() == nil {
				slog.Warn("conflict check", "project", p.Name, "err", err)
			}
		}
	}
}

// watched reports whether t's branch is checked: it has one here, and is
// in progress, in review or merging.
func watched(t model.Todo) bool {
	if t.OnRunner() || t.Worktree == nil || t.Worktree.Branch == "" {
		return false
	}
	switch t.Status {
	case model.StatusInProgress, model.StatusReview, model.StatusMerging:
		return true
	}
	return false
}

// branchTask is a watched task's branch, as of one pass.
type branchTask struct {
	t         model.Todo
	base      string // its default branch
	head      string // its branch's commit
	baseHead  string // the default branch's
	files     []string
	baseFiles []string // the default branch's since the task forked from it
	conflicts []model.Conflict
}

// Check looks at project p's watched tasks once, and saves what changed.
func (w *ConflictWatch) Check(ctx context.Context, p model.Project) error {
	var tasks []*branchTask
	for _, t := range w.Store.ListTodos(p.ID) {
		switch {
		case watched(t):
			tasks = append(tasks, &branchTask{t: t, base: p.For(t).Base()})
		case !t.OnRunner() && (len(t.Conflicts) > 0):
			w.save(t, nil) // it left: what it conflicted with is old news
		}
	}
	if len(tasks) == 0 {
		delete(w.seen, p.ID)
		return nil
	}
	heads, err := w.Git.BranchHeads(ctx, p.ID, p.Path)
	if err != nil {
		return err
	}
	var live []*branchTask
	var print []string
	for _, b := range tasks {
		b.head, b.baseHead = heads["refs/heads/"+b.t.Worktree.Branch], heads["refs/heads/"+b.base]
		if b.head == "" || b.baseHead == "" {
			continue // no branch (yet): nothing to compare
		}
		live = append(live, b)
		print = append(print, strings.Join([]string{b.t.ID, string(b.t.Status), b.head, b.base, b.baseHead}, " "))
	}
	sort.Strings(print)
	fingerprint := strings.Join(print, "\n")
	if w.seen[p.ID] == fingerprint {
		return nil // nothing moved since the last pass
	}

	for _, b := range live {
		if b.files, err = w.forked(ctx, p, b.baseHead, b.head); err != nil {
			return err
		}
		if b.baseFiles, err = w.forked(ctx, p, b.head, b.baseHead); err != nil {
			return err
		}
	}
	// Each task with its default branch, when both changed a file.
	for _, b := range live {
		if !overlaps(b.files, b.baseFiles) {
			continue
		}
		conflicted, err := w.merge(ctx, p, b.head, b.baseHead)
		if err != nil {
			return err
		}
		if len(conflicted) > 0 {
			b.conflicts = append(b.conflicts, model.Conflict{Base: true, Name: b.base, Files: conflicted})
		}
	}
	// Each pair of tasks that changed a file both.
	for _, pair := range conflictPairs(live) {
		a, b := live[pair[0]], live[pair[1]]
		conflicted, err := w.merge(ctx, p, a.head, b.head)
		if err != nil {
			return err
		}
		if len(conflicted) > 0 {
			a.conflicts = append(a.conflicts, conflictWith(b.t, conflicted))
			b.conflicts = append(b.conflicts, conflictWith(a.t, conflicted))
		}
	}

	byID := map[string]model.Todo{}
	for _, b := range tasks {
		byID[b.t.ID] = b.t
	}
	for _, b := range tasks {
		w.save(b.t, b.conflicts)
		for _, o := range newlyAhead(b.t, b.conflicts, byID) {
			w.note(b.t.ID, o, b.base)
		}
	}
	if w.seen == nil {
		w.seen = map[string]string{}
	}
	w.seen[p.ID] = fingerprint
	w.prune(live)
	return nil
}

// conflictWith is a conflict with task o in files.
func conflictWith(o model.Todo, files []string) model.Conflict {
	return model.Conflict{TodoID: o.ID, HubTodoID: o.HubTodoID, Name: taskName(o), Files: files}
}

// conflictPairs are the pairs of tasks (indexes into tasks) that branch
// from the same default branch and changed at least one file both: only
// those can conflict.
func conflictPairs(tasks []*branchTask) [][2]int {
	var pairs [][2]int
	for i := range tasks {
		for j := i + 1; j < len(tasks); j++ {
			if tasks[i].base == tasks[j].base && overlaps(tasks[i].files, tasks[j].files) {
				pairs = append(pairs, [2]int{i, j})
			}
		}
	}
	return pairs
}

// overlaps reports whether a and b have a file in common.
func overlaps(a, b []string) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(a) == 0 {
		return false
	}
	in := make(map[string]bool, len(a))
	for _, f := range a {
		in[f] = true
	}
	for _, f := range b {
		if in[f] {
			return true
		}
	}
	return false
}

// newlyAhead are the tasks t conflicts with that are ahead of it in the
// merge queue (merging, while t isn't yet) and that Claude hasn't had a
// note about.
func newlyAhead(t model.Todo, conflicts []model.Conflict, byID map[string]model.Todo) []model.Conflict {
	if t.Status == model.StatusMerging {
		return nil
	}
	var out []model.Conflict
	for _, c := range conflicts {
		if c.Base || slices.Contains(t.ConflictNoted, c.TodoID) {
			continue
		}
		if o, ok := byID[c.TodoID]; ok && o.Status == model.StatusMerging {
			out = append(out, c)
		}
	}
	return out
}

// aheadNote is the note for Claude about task c, ahead in the merge queue.
func aheadNote(c model.Conflict, base string) string {
	return fmt.Sprintf("Heads-up: another task, #%s %q, is ahead of yours in the merge queue, "+
		"and your changes conflict with it in:\n- %s\n\n"+
		"It lands on `%s` first. Keep your changes to those files small and to the point, so the "+
		"conflict stays easy to resolve when `%s` is merged into your work. Don't run git commands.",
		c.TodoID, c.Name, strings.Join(c.Files, "\n- "), base, base)
}

// note gives task id a note about c, once.
func (w *ConflictWatch) note(id string, c model.Conflict, base string) {
	t, err := w.Store.UpdateTodo(id, func(x *model.Todo) error {
		if slices.Contains(x.ConflictNoted, c.TodoID) {
			return errNoChange
		}
		x.ConflictNoted = append(x.ConflictNoted, c.TodoID)
		x.Feedback = append(x.Feedback, aheadNote(c, base))
		return nil
	})
	if err != nil {
		return
	}
	if w.Log != nil {
		w.Log.Line(t, fmt.Sprintf("⚠ conflicts with #%s, ahead in the merge queue, in %s; noted for Claude's next run",
			c.TodoID, strings.Join(c.Files, ", ")))
	}
}

var errNoChange = errors.New("no change")

// save sets t's conflicts, when they changed.
func (w *ConflictWatch) save(t model.Todo, conflicts []model.Conflict) {
	sortConflicts(conflicts)
	if sameConflicts(t.Conflicts, conflicts) {
		return
	}
	if _, err := w.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		if sameConflicts(x.Conflicts, conflicts) {
			return errNoChange
		}
		x.Conflicts = conflicts
		return nil
	}); err != nil && !errors.Is(err, errNoChange) && !errors.Is(err, store.ErrNotFound) {
		slog.Warn("save conflicts", "todo", t.ID, "err", err)
	}
}

// sortConflicts puts the default branch first, then the tasks by id.
func sortConflicts(cs []model.Conflict) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Base != cs[j].Base {
			return cs[i].Base
		}
		return cs[i].TodoID < cs[j].TodoID
	})
}

func sameConflicts(a, b []model.Conflict) bool {
	return slices.EqualFunc(a, b, func(x, y model.Conflict) bool {
		return x.TodoID == y.TodoID && x.HubTodoID == y.HubTodoID && x.Base == y.Base && x.Name == y.Name && slices.Equal(x.Files, y.Files)
	})
}

// forked lists the files changed on commit to since it forked from from,
// remembered by commit.
func (w *ConflictWatch) forked(ctx context.Context, p model.Project, from, to string) ([]string, error) {
	key := [2]string{from, to}
	if f, ok := w.forks[key]; ok {
		return f, nil
	}
	f, err := w.Git.FilesSinceFork(ctx, p.ID, p.Path, from, to)
	if err != nil {
		return nil, err
	}
	if w.forks == nil {
		w.forks = map[[2]string][]string{}
	}
	w.forks[key] = f
	return f, nil
}

// merge lists the files commits a and b conflict in, remembered by commit.
func (w *ConflictWatch) merge(ctx context.Context, p model.Project, a, b string) ([]string, error) {
	if b < a {
		a, b = b, a
	}
	key := [2]string{a, b}
	if f, ok := w.merges[key]; ok {
		return f, nil
	}
	f, err := w.Git.MergeConflicts(ctx, p.ID, p.Path, a, b)
	if err != nil {
		return nil, err
	}
	if w.merges == nil {
		w.merges = map[[2]string][]string{}
	}
	w.merges[key] = f
	return f, nil
}

// prune forgets what was remembered about commits no task is at now, once
// there is a fair amount of it, so the caches stay small.
func (w *ConflictWatch) prune(live []*branchTask) {
	if len(w.merges)+len(w.forks) < 512 {
		return
	}
	now := map[string]bool{}
	for _, b := range live {
		now[b.head], now[b.baseHead] = true, true
	}
	for k := range w.merges {
		if !now[k[0]] || !now[k[1]] {
			delete(w.merges, k)
		}
	}
	for k := range w.forks {
		if !now[k[0]] || !now[k[1]] {
			delete(w.forks, k)
		}
	}
}
