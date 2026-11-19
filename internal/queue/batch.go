package queue

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// Batched merges (model.Project.MergeBatch).
//
// When a project batches merges and approved tasks are waiting in its
// queue, its worker takes up to MergeBatchSize of them, in queue order,
// that git merge-tree says merge cleanly with the default branch and with
// each other. In a checkout only Dash uses (_batch) it builds the default
// branch plus each task squashed onto it in turn: exactly the commits that
// would land, one per task, with the message a task merged alone gets. The
// test command runs once on that. When it passes, the default branch moves
// to those commits, but only while it is still where the batch started, so
// what lands is what was tested; when it moved, the batch is built and
// tested again on top of it.
//
// When the batch's tests fail, its tasks are tested one by one, in order,
// each on top of the default branch and the tasks before it that passed.
// Only a task that fails like that, on its own, is blamed and sent back to
// Claude; the others land. One by one rather than bisecting, because:
//   - batches are small (MaxMergeBatch), so bisecting saves at most a run
//     or two, and only when a single task broke the build;
//   - bisecting still has to test a suspect on its own, on top of what
//     passed, before it may blame it (a flaky run must blame nobody), which
//     is what one by one does for every task anyway;
//   - a failure two tasks cause only together needs no special case: the
//     later one fails on top of the earlier one, as it would merged alone.
//
// A batch of one is a task merged alone (process), with everything that
// does, such as not running the tests again on a commit they passed on.

// errBatchMoved is the default branch moving on while a batch was tested.
var errBatchMoved = errors.New("the default branch moved during the batch")

// maxBatchAttempts is how many times a batch is built and tested again when
// the default branch keeps moving; then its tasks merge one at a time.
const maxBatchAttempts = 3

// batchMember is one task of a batch.
type batchMember struct {
	job     model.MergeJob
	t       model.Todo
	backlog int    // its job's index in the lane's backlog (-1: the job that started the batch)
	head    string // its branch's commit
	commit  string // its squash commit in the batch ("": it changes nothing)
	// changed: it was last tested on top of more than its branch has (the
	// default branch moved, or tasks before it in the batch).
	changed bool
}

func (m *MergeQueue) batchDir(p model.Project) string {
	return filepath.Join(m.WorktreeRoot, p.ID, "_batch")
}

// batchable is job's todo and project when it may merge in a batch.
func (m *MergeQueue) batchable(job model.MergeJob) (model.Todo, model.Project, bool) {
	if job.Revert {
		return model.Todo{}, model.Project{}, false
	}
	t, err := m.Store.GetTodo(job.TodoID)
	if err != nil || t.Status != model.StatusMerging || t.Worktree == nil || t.Worktree.Branch == "" ||
		t.OnRunner() || (t.PR != nil && t.PR.State == "OPEN") {
		return t, model.Project{}, false
	}
	p, err := m.Store.GetProject(t.ProjectID)
	if err != nil {
		return t, p, false
	}
	p = p.For(t)
	return t, p, p.MergeBatchSize() > 1 && p.TestCommand != ""
}

// batch merges the merge job in it together with the approved tasks queued
// behind it, when its project batches merges and some of them can go
// together, and reports whether it did; otherwise the caller handles it
// alone, as always.
func (m *MergeQueue) batch(ctx context.Context, l *lane, it mergeItem) bool {
	if it.fn != nil {
		return false
	}
	first, p, ok := m.batchable(it.job)
	if !ok {
		return false
	}
	for drained := false; !drained; { // what is queued now, to look through
		select {
		case x := <-l.jobs:
			l.backlog = append(l.backlog, x)
		default:
			drained = true
		}
	}
	cands := []*batchMember{{job: it.job, t: first, backlog: -1}}
	seen := map[string]bool{first.ID: true}
	for i, x := range l.backlog {
		if x.fn != nil || seen[x.job.TodoID] {
			continue
		}
		t, q, ok := m.batchable(x.job)
		if !ok || q.Base() != p.Base() {
			continue
		}
		seen[t.ID] = true
		cands = append(cands, &batchMember{job: x.job, t: t, backlog: i})
	}
	if len(cands) < 2 {
		return false
	}
	p = m.refreshRemote(ctx, p).For(first)
	if p.SyncsRemote() {
		if err := m.syncRemote(ctx, p); err != nil {
			return false // merged alone, which says why it can't
		}
	}
	members := m.pickBatch(ctx, p, cands, p.MergeBatchSize())
	if len(members) < 2 {
		return false
	}

	taken := map[int]bool{}
	for _, b := range members {
		taken[b.backlog] = true
	}
	rest := l.backlog[:0:0]
	for i, x := range l.backlog {
		if !taken[i] {
			rest = append(rest, x)
		}
	}
	l.backlog = rest
	for _, b := range members {
		m.publish(b.t.ID)
	}

	res, alone := m.mergeBatch(ctx, p, members)
	for _, b := range members {
		if err, ok := res[b.t.ID]; ok {
			m.finish(ctx, b.job, err)
		}
	}
	for _, b := range alone {
		m.handle(ctx, mergeItem{job: b.job})
	}
	return true
}

// pickBatch picks up to n of cands, in order, that merge cleanly with p's
// default branch and with each other (git merge-tree). The first must be
// one of them, or there is no batch: it then merges alone. The others left
// out stay queued: one that conflicts with the default branch merges alone
// (and is sent back), one that conflicts with a task in the batch merges
// after it.
func (m *MergeQueue) pickBatch(ctx context.Context, p model.Project, cands []*batchMember, n int) []*batchMember {
	base, err := m.Git.Head(ctx, p.ID, p.Path, p.Base())
	if err != nil {
		return nil
	}
	var out []*batchMember
	for i, c := range cands {
		if len(out) == n {
			break
		}
		head, err := m.Git.Head(ctx, p.ID, p.Path, "refs/heads/"+c.t.Worktree.Branch)
		if err == nil {
			var files []string
			if files, err = m.Git.MergeConflicts(ctx, p.ID, p.Path, base, head); err == nil && len(files) > 0 {
				err = &gitops.ErrConflict{Files: files}
			}
		}
		if err != nil {
			if i == 0 || errors.Is(err, gitops.ErrNoMergeTree) {
				return out // the first merges alone; an old git can't tell what goes together
			}
			continue
		}
		c.head = head
		clash := false
		for _, o := range out {
			files, err := m.Git.MergeConflicts(ctx, p.ID, p.Path, o.head, head)
			if err != nil || len(files) > 0 {
				if len(files) > 0 {
					m.Log.Event(c.t, model.RunEvent{Kind: "status", Tasks: taskRefs(o),
						Text: fmt.Sprintf("⏭ not merged in a batch with #%s: conflicts with it in %s; merges after it", o.t.ID, strings.Join(files, ", "))})
				}
				clash = true
				break
			}
		}
		if !clash {
			out = append(out, c)
		}
	}
	return out
}

// mergeBatch merges members, a batch, into p's default branch. res holds
// each member it finished with what came of it (nil: merged or sent back);
// alone are the others, to merge one at a time.
func (m *MergeQueue) mergeBatch(ctx context.Context, p model.Project, members []*batchMember) (res map[string]error, alone []*batchMember) {
	res = map[string]error{}
	failAll := func(err error) {
		for _, b := range members {
			res[b.t.ID] = err
		}
	}
	for _, b := range members {
		if m.Git.AbortStaleMerge(ctx, p.ID, b.t.Worktree.Path) {
			m.Log.Line(b.t, "↺ undid a merge of "+p.Base()+" left unfinished")
		}
	}
	m.logBatch(members, func(b *batchMember, others string) string {
		return fmt.Sprintf("▶ merging into %s in a batch of %d, tested together with %s", p.Base(), len(members), others)
	})
	dir := m.batchDir(p)
	for attempt := 1; ; attempt++ {
		if attempt > maxBatchAttempts {
			for _, b := range members {
				m.Log.Line(b.t, "↻ "+p.Base()+" kept moving during the batch; merging the tasks one at a time")
			}
			return res, append(alone, members...)
		}
		base, err := m.Git.Head(ctx, p.ID, p.Path, p.Base())
		if err != nil {
			failAll(err)
			return res, alone
		}
		tip, built, conflicted, err := m.buildBatch(ctx, p, dir, base, members)
		if err != nil {
			failAll(err)
			return res, alone
		}
		alone = append(alone, conflicted...)
		members = built
		if len(members) < 2 {
			return res, append(alone, members...) // nothing left to batch
		}

		checks, err := m.testBatch(ctx, p, dir, members)
		if err != nil {
			failAll(err)
			return res, alone
		}
		passed, failed := members, []*batchMember(nil)
		if !checks.Passed {
			for _, b := range members {
				m.Log.Line(b.t, "🔍 the batch failed; testing its tasks one by one, each on top of "+p.Base()+" and the ones before it that pass")
			}
			var odd []*batchMember
			if passed, failed, odd, tip, err = m.culprits(ctx, p, dir, base, members); err != nil {
				failAll(err)
				return res, alone
			}
			alone = append(alone, odd...)
		}
		if m.batchTested != nil {
			m.batchTested()
		}

		moved, err := m.landBatch(ctx, p, base, tip, passed)
		for _, f := range failed {
			res[f.t.ID] = m.blame(ctx, p, f, len(members))
		}
		if err != nil {
			for _, b := range passed {
				res[b.t.ID] = err
			}
			return res, alone
		}
		if !moved {
			return res, alone
		}
		members = passed
		for _, b := range members {
			m.Log.Line(b.t, "↻ "+p.Base()+" moved during the batch; building and testing it again on top of it")
		}
	}
}

// buildBatch checks out base in dir and squashes each member onto it in
// turn, as landing does. tip is the last commit; a member that conflicts
// with the ones before it is left out (conflicted), to merge on its own.
func (m *MergeQueue) buildBatch(ctx context.Context, p model.Project, dir, base string, members []*batchMember) (tip string, built, conflicted []*batchMember, err error) {
	if err := m.Git.LandingCheckout(ctx, p.ID, p.Path, dir, base); err != nil {
		return "", nil, nil, err
	}
	tip = base
	for _, b := range members {
		sha, err := m.Git.SquashInto(ctx, p.ID, dir, b.t.Worktree.Branch, commitMessage(b.t))
		var c *gitops.ErrConflict
		if errors.As(err, &c) {
			m.Log.Line(b.t, "⏭ "+c.Error()+" on top of the tasks before it in the batch; merging it on its own after them")
			conflicted = append(conflicted, b)
			continue
		}
		if err != nil {
			return "", nil, nil, err
		}
		b.commit = sha
		if sha != "" {
			tip = sha
		}
		built = append(built, b)
	}
	return tip, built, conflicted, nil
}

// testBatch runs p's tests once in dir, on the batch, logged to every
// member, and saves the result on each.
func (m *MergeQueue) testBatch(ctx context.Context, p model.Project, dir string, members []*batchMember) (model.Checks, error) {
	if err := heldErr(p, model.CmdTest); err != nil {
		return model.Checks{}, fmt.Errorf("tests not run, so not merged: %w", err)
	}
	line := func(s string) {
		for _, b := range members {
			m.Log.Line(b.t, s)
		}
	}
	checks := runChecksLog(ctx, m.Git, line, m.Shared, members[0].t, p, dir, "merge", m.TestTimeout)
	if ctx.Err() != nil {
		return checks, ctx.Err() // tests cut off by a shutdown haven't failed
	}
	checks.Batch = batchIDs(members)
	verdict := "passed"
	if !checks.Passed {
		verdict = "failed"
	}
	m.logBatch(members, func(b *batchMember, others string) string {
		return fmt.Sprintf("🧪 batch of %d %s in %.0fs, tested together with %s", len(members), verdict, checks.Duration, others)
	})
	for _, b := range members {
		if err := m.saveChecks(b, checks); err != nil {
			return checks, err
		}
	}
	return checks, nil
}

// culprits tests members one by one, in order, each on top of base and the
// members before it that passed. tip is the last of those commits.
// One that conflicts there (alone) merges on its own after them.
func (m *MergeQueue) culprits(ctx context.Context, p model.Project, dir, base string, members []*batchMember) (passed, failed, alone []*batchMember, tip string, err error) {
	tip = base
	for _, b := range members {
		if err := m.Git.LandingCheckout(ctx, p.ID, p.Path, dir, tip); err != nil {
			return nil, nil, nil, "", err
		}
		sha, err := m.Git.SquashInto(ctx, p.ID, dir, b.t.Worktree.Branch, commitMessage(b.t))
		var c *gitops.ErrConflict
		if errors.As(err, &c) {
			m.Log.Line(b.t, "⏭ "+c.Error()+" without the tasks before it that failed; merging it on its own")
			alone = append(alone, b)
			continue
		}
		if err != nil {
			return nil, nil, nil, "", err
		}
		if sha == "" {
			b.commit = ""
			passed = append(passed, b)
			continue
		}
		mb, _ := m.Git.MergeBase(ctx, p.ID, p.Path, tip, b.head)
		b.changed = mb != tip
		m.Log.Event(b.t, model.RunEvent{Kind: "status", Tasks: taskRefs(passed...),
			Text: "▶ testing it on its own, on top of " + p.Base() + onTopOf(passed)})
		checks := runChecks(ctx, m.Git, m.Log, m.Shared, b.t, p, dir, "merge", m.TestTimeout)
		if ctx.Err() != nil {
			return nil, nil, nil, "", ctx.Err()
		}
		checks.Batch = batchIDs(members)
		if err := m.saveChecks(b, checks); err != nil {
			return nil, nil, nil, "", err
		}
		if checks.Passed {
			b.commit, tip = sha, sha
			passed = append(passed, b)
		} else {
			failed = append(failed, b)
		}
	}
	return passed, failed, alone, tip, nil
}

// onTopOf names the tasks a member was tested on top of, after the default
// branch: "" or " and #3, #5".
func onTopOf(passed []*batchMember) string {
	if len(passed) == 0 {
		return ""
	}
	ids := make([]string, len(passed))
	for i, b := range passed {
		ids[i] = "#" + b.t.ID
	}
	return " and " + strings.Join(ids, ", ")
}

// landBatch moves p's default branch from base to tip, the commits of
// passed, and marks each of them merged. moved: the default branch isn't
// at base any more, so nothing landed (build and test it again).
func (m *MergeQueue) landBatch(ctx context.Context, p model.Project, base, tip string, passed []*batchMember) (moved bool, err error) {
	if len(passed) == 0 {
		return false, nil
	}
	if tip != base {
		_, err := m.land(ctx, p, func(dir string) error {
			old, err := m.Git.Head(ctx, p.ID, dir, "HEAD")
			if err != nil {
				return err
			}
			if old != base {
				return errBatchMoved
			}
			return m.Git.LandingCheckout(ctx, p.ID, p.Path, dir, tip)
		})
		if errors.Is(err, errBatchMoved) {
			return true, nil
		}
		if errors.As(err, new(*gitops.ErrDirtyCheckout)) {
			return false, err
		}
		if err != nil {
			return false, fmt.Errorf("land the batch on %s: %w", p.Base(), err)
		}
		if p.AutoPush && p.RemoteURL != "" {
			perr := m.pushSynced(ctx, p)
			for _, b := range passed {
				if perr != nil {
					m.Log.Line(b.t, "⚠ push failed (merged locally; Dash pushes again after the next merge): "+perr.Error())
				} else {
					m.Log.Line(b.t, "⇡ pushed "+p.Base())
				}
			}
			// A sync during the push may have rebased the commits.
			if head, herr := m.Git.Head(ctx, p.ID, p.Path, p.Base()); perr == nil && herr == nil && head != tip {
				for _, b := range passed {
					if b.commit == "" {
						continue
					}
					if sha, ferr := m.Git.FindTodoCommit(ctx, p.ID, p.Path, p.Base(), b.t.ID); ferr == nil {
						b.commit = sha
					}
				}
			}
		}
	}
	for _, b := range passed {
		if b.commit == "" {
			_, _ = m.closeNoChanges(ctx, b.t, p, nil) // nothing to merge
			continue
		}
		msg := "✔ merged into " + p.Base()
		if o := others(passed, b); len(o) > 0 {
			msg += ", tested in a batch with " + refList(o)
		}
		m.landed(ctx, b.t, p, b.commit, msg)
	}
	return false, nil
}

// blame sends f, a member that failed the tests on its own, back to Claude
// as a task merged alone would be: with the default branch, which now has
// the batch's tasks that passed, merged into its branch first.
func (m *MergeQueue) blame(ctx context.Context, p model.Project, f *batchMember, size int) error {
	t, err := m.Store.GetTodo(f.t.ID)
	if err != nil {
		return err
	}
	changed, err := m.Git.MergeIn(ctx, p.ID, t.Worktree.Path, p.Base())
	var conflict *gitops.ErrConflict
	if errors.As(err, &conflict) {
		return m.conflictBack(t, p, conflict)
	}
	if err != nil {
		return err
	}
	checks := model.Checks{}
	if t.Checks != nil {
		checks = *t.Checks
	}
	why := fmt.Sprintf(" (It was merged in a batch of %d approved tasks; the batch's tests failed, and so did yours tested on their own.)", size)
	if changed || f.changed {
		note := fmt.Sprintf("Your change was merged with the latest `%s`, and now the tests fail.%s %s", p.Base(), why, testNote(checks))
		return m.sendBack(t, p, note, false, "tests fail after merging "+p.Base())
	}
	note := "Your change was approved, but the tests fail, so it wasn't merged." + why + " " + testNote(checks)
	return m.sendBack(t, p, note, false, "tests fail; not merged")
}

// saveChecks saves checks as b's.
func (m *MergeQueue) saveChecks(b *batchMember, checks model.Checks) error {
	t, err := m.Store.UpdateTodo(b.t.ID, func(x *model.Todo) error { c := checks; x.Checks = &c; return nil })
	if err == nil {
		b.t = t
	}
	return err
}

// logBatch logs a line to each member, naming (and linking) the others.
func (m *MergeQueue) logBatch(members []*batchMember, text func(b *batchMember, others string) string) {
	for _, b := range members {
		o := others(members, b)
		m.Log.Event(b.t, model.RunEvent{Kind: "status", Tasks: o, Text: text(b, refList(o))})
	}
}

// others are the tasks of members but b.
func others(members []*batchMember, b *batchMember) []model.TaskRef {
	var o []*batchMember
	for _, x := range members {
		if x != b {
			o = append(o, x)
		}
	}
	return taskRefs(o...)
}

func taskRefs(bs ...*batchMember) []model.TaskRef {
	refs := make([]model.TaskRef, 0, len(bs))
	for _, b := range bs {
		refs = append(refs, model.TaskRef{ID: b.t.ID, HubID: b.t.HubTodoID, Name: taskName(b.t)})
	}
	return refs
}

// refList is "#3 Fix login, #5 Add logout".
func refList(refs []model.TaskRef) string {
	s := make([]string, len(refs))
	for i, r := range refs {
		s[i] = fmt.Sprintf("#%s %s", r.ID, r.Name)
	}
	return strings.Join(s, ", ")
}

func batchIDs(members []*batchMember) []string {
	ids := make([]string, len(members))
	for i, b := range members {
		ids[i] = b.t.ID
	}
	return ids
}
