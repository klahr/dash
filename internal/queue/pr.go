package queue

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// openPR is step 3 in "pr" mode: push the todo's branch and open (or update)
// its pull request. The todo stays in Merging until PollPRs sees GitHub
// merge it (Dash merges it itself once its checks pass, see mergePR).
func (m *MergeQueue) openPR(ctx context.Context, t model.Todo, p model.Project) error {
	wt := *t.Worktree
	if err := m.Git.PushBranch(ctx, p.ID, wt.Path, wt.Branch); err != nil {
		return fmt.Errorf("push %s: %w", wt.Branch, err)
	}
	n, url, err := m.Git.OpenPR(ctx, p.ID, wt.Path, p.Base(), wt.Branch, subject(t), prBody(t))
	if err != nil {
		return fmt.Errorf("open pull request: %w", err)
	}
	if _, err := m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		if x.Status != model.StatusMerging {
			return nil // moved on while pushing (e.g. merged or closed on GitHub): keep what was seen
		}
		handled := ""
		if x.PR != nil && x.PR.Number == n {
			handled = x.PR.HandledSHA
		}
		x.PR = &model.PullRequest{Number: n, URL: url, State: "OPEN", HandledSHA: handled}
		x.Error = ""
		return nil
	}); err != nil {
		return err
	}
	m.Log.Line(t, fmt.Sprintf("⇡ pushed %s; pull request #%d is open: %s", wt.Branch, n, url))
	return nil
}

// PollPRs follows open pull requests: merged ones are marked merged,
// closed ones go back to review, and conflicts or failing checks are sent
// back to Claude like a local merge conflict. Tasks that failed or went to
// review with their pull request still open are followed too, so a merge or
// close done by hand on GitHub is noticed.
func (m *MergeQueue) PollPRs(ctx context.Context) {
	for _, t := range m.Store.ListTodos("", model.StatusMerging, model.StatusReview, model.StatusFailed) {
		if t.PR == nil || t.PR.State != "OPEN" || t.OnRunner() {
			continue // a runner follows the pull requests of its tasks and reports on them
		}
		p, err := m.Store.GetProject(t.ProjectID)
		if err != nil {
			continue
		}
		p = p.For(t)
		st, err := m.Git.PRStatus(ctx, p.ID, p.Path, t.PR.Number)
		if err != nil {
			slog.Warn("pull request status", "todo", t.ID, "pr", t.PR.Number, "err", err)
			continue
		}
		m.followPR(ctx, t, p, st)
	}
}

func (m *MergeQueue) followPR(ctx context.Context, t model.Todo, p model.Project, st gitops.PRStatus) {
	n := t.PR.Number
	switch st.State {
	case "MERGED":
		if p.SyncsRemote() {
			_ = m.syncRemote(ctx, p) // so new worktrees start from the merged code
		}
		sha := ""
		if st.MergeCommit != nil {
			sha = st.MergeCommit.Oid
		}
		m.landed(ctx, t, p, sha, fmt.Sprintf("✔ pull request #%d merged", n))
		if t.Worktree != nil {
			m.Git.DeleteRemoteBranch(ctx, p.ID, p.Path, t.Worktree.Branch)
		}
	case "CLOSED":
		m.Log.Line(t, fmt.Sprintf("✖ pull request #%d was closed without merging", n))
		_, _ = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
			x.Status, x.PR = model.StatusReview, nil
			x.Error = fmt.Sprintf("Pull request #%d was closed without merging. Approve again to open a new one.", n)
			return nil
		})
	case "OPEN":
		if t.Status != model.StatusMerging || st.HeadRefOid == "" {
			return
		}
		// The head Dash already acted on: a fix pass that made no new commit
		// pushed it again. Without this the task would wait in merging for a
		// head that never changes.
		handled := st.HeadRefOid == t.PR.HandledSHA
		autoMerge := p.MergesGreenPRs()
		if st.Mergeable == "CONFLICTING" {
			m.notePR(t, st, prStatusLine(n, p.Base(), st, nil, nil, autoMerge, handled))
			if handled {
				m.stuck(ctx, t, st, model.StatusFailed, fmt.Sprintf("Pull request #%d still conflicts with %s after Claude's fix", n, p.Base()))
				return
			}
			if p.SyncsRemote() && m.syncRemote(ctx, p) != nil {
				return
			}
			m.handled(t, st.HeadRefOid, fmt.Sprintf("⚠ pull request #%d conflicts with %s", n, p.Base()))
			t, _ = m.Store.GetTodo(t.ID)
			_ = m.sendBack(t, p, "", true, fmt.Sprintf("pull request #%d conflicts with %s", n, p.Base()))
			return
		}
		var failed []gitops.PRCheck
		for _, c := range st.Checks {
			if c.Pending() {
				// act once every check has finished
				m.notePR(t, st, prStatusLine(n, p.Base(), st, nil, nil, autoMerge, handled))
				return
			}
			if c.Failed() {
				failed = append(failed, c)
			}
		}
		code, other := m.sortFailed(ctx, p, n, failed)
		m.notePR(t, st, prStatusLine(n, p.Base(), st, code, other, autoMerge, handled))
		switch {
		case len(code) > 0 && handled:
			m.stuck(ctx, t, st, model.StatusFailed, fmt.Sprintf("Checks still fail on pull request #%d (%s) and Claude's fix made no new commit", n, labels(failed)))
		case len(code) > 0:
			m.handled(t, st.HeadRefOid, fmt.Sprintf("✖ checks fail on pull request #%d: %s", n, labels(failed)))
			t, _ = m.Store.GetTodo(t.ID)
			_ = m.sendBack(t, p, m.checksNote(ctx, p, n, code, other), false, fmt.Sprintf("checks fail on pull request #%d", n))
		case len(other) > 0:
			// Not something to fix in the code (e.g. an unsigned CLA): say so
			// and leave it to a person. PollPRs still follows the pull request.
			m.stuck(ctx, t, st, model.StatusReview, fmt.Sprintf("Pull request #%d can't merge: %s fails, which isn't about the code. Fix it on GitHub (e.g. sign the CLA), then merge it there or approve again", n, labels(other)))
		default:
			// Passing, or failing only checks that aren't required to merge.
			// A handled head here is one GitHub refused to merge: leave it.
			if autoMerge && !handled {
				m.mergePR(ctx, t, p, st)
			}
		}
	}
}

// notePR keeps what GitHub last said about a merging task's pull request
// on the task, for its card: the checks and what the merge waits for.
func (m *MergeQueue) notePR(t model.Todo, st gitops.PRStatus, line string) {
	checks := prChecks(st.Checks)
	if t.PR.Status == line && slices.Equal(t.PR.Checks, checks) {
		return // unchanged: don't write (and broadcast) on every poll
	}
	_, _ = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		if x.PR == nil || x.PR.Number != t.PR.Number {
			return nil
		}
		x.PR.Checks, x.PR.Status = checks, line
		return nil
	})
}

func prChecks(cs []gitops.PRCheck) []model.PRCheck {
	if len(cs) == 0 {
		return nil
	}
	out := make([]model.PRCheck, len(cs))
	for i, c := range cs {
		state := "passed"
		switch {
		case c.Pending():
			state = "pending"
		case c.Failed():
			state = "failed"
		}
		out[i] = model.PRCheck{Name: c.Label(), State: state, URL: c.URL()}
	}
	return out
}

// prStatusLine is what a merging card says about pull request n: what its
// merge waits for. code and other are its failing checks, sorted as
// sortFailed does; autoMerge is set when Dash merges it once it can, and
// handled when Dash already acted on its head.
func prStatusLine(n int, base string, st gitops.PRStatus, code, other []gitops.PRCheck, autoMerge, handled bool) string {
	if st.Mergeable == "CONFLICTING" {
		if handled {
			return "Still conflicts with " + base + " after Claude's fix"
		}
		return "Conflicts with " + base + ", sent back to Claude"
	}
	var pending []gitops.PRCheck
	passed := 0
	for _, c := range st.Checks {
		switch {
		case c.Pending():
			pending = append(pending, c)
		case !c.Failed():
			passed++
		}
	}
	if len(pending) > 0 {
		return fmt.Sprintf("Waiting for checks: %s (%d/%d passed)", labels(pending), passed, len(st.Checks))
	}
	plural := func(cs []gitops.PRCheck) string {
		if len(cs) == 1 {
			return "Check"
		}
		return "Checks"
	}
	switch {
	case len(code) > 0 && handled:
		return fmt.Sprintf("%s still failing after Claude's fix: %s", plural(code), labels(code))
	case len(code) > 0:
		return fmt.Sprintf("%s failed: %s, sent back to Claude", plural(code), labels(code))
	case len(other) > 0:
		why := make([]string, len(other))
		for i, c := range other {
			why[i] = blockedBy(c)
		}
		return "Blocked: " + strings.Join(why, ", ")
	case !autoMerge || handled:
		return "Waiting for you to merge on GitHub"
	}
	return fmt.Sprintf("Merging PR #%d…", n)
}

var (
	claRe = regexp.MustCompile(`(?i)(^|[^a-z])cla([^a-z]|$)|contributor license`)
	dcoRe = regexp.MustCompile(`(?i)(^|[^a-z])dco([^a-z]|$)|signed[- ]off`)
)

// blockedBy says why a failing check no code change can fix holds a merge.
func blockedBy(c gitops.PRCheck) string {
	switch l := c.Label(); {
	case claRe.MatchString(l):
		return "CLA not signed"
	case dcoRe.MatchString(l):
		return "commits not signed off (DCO)"
	default:
		return l + " failed (it runs from the base branch)"
	}
}

// stuck moves a merging task whose pull request can't go further without a
// person to status (failed or review), with why as its error. The pull
// request stays open and followed, so merging or closing it on GitHub is
// still noticed.
func (m *MergeQueue) stuck(ctx context.Context, t model.Todo, st gitops.PRStatus, status model.TodoStatus, why string) {
	if m.inQueue(t.ID) {
		return // its branch is about to be pushed (again): judge what GitHub gets
	}
	if t.Worktree != nil {
		// Until the push lands, GitHub still shows the old head; wait for it
		// rather than judge the commit Claude just made by the last one.
		if local, err := m.Git.Head(ctx, t.ProjectID, t.Worktree.Path, "HEAD"); err != nil || local != st.HeadRefOid {
			return
		}
	}
	if status == model.StatusFailed {
		why += " — fix it on GitHub, or send it back for rework to let Claude try again"
		noteFailure(m.Store, t.ID, why, "")
	}
	m.Log.Line(t, "✖ "+why)
	_, _ = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		if x.Status != model.StatusMerging {
			return nil
		}
		x.Status, x.Error = status, why
		if x.PR != nil {
			x.PR.HandledSHA = st.HeadRefOid
		}
		return nil
	})
}

// sortFailed splits failing checks into those Claude should fix in the code
// and those no change to the branch can fix: a CLA or DCO signature, or a
// pull_request_target workflow, which runs from the base branch. Checks the
// base branch doesn't require to merge are dropped: they neither go to
// Claude nor hold the task. When the branch requires no checks (or gh can't
// tell), every check counts as required.
func (m *MergeQueue) sortFailed(ctx context.Context, p model.Project, n int, failed []gitops.PRCheck) (code, other []gitops.PRCheck) {
	if len(failed) == 0 {
		return
	}
	return classifyFailed(failed, m.Git.RequiredChecks(ctx, p.ID, p.Path, n), m.Git.CheckEvents(ctx, p.ID, p.Path, n))
}

func classifyFailed(failed []gitops.PRCheck, required map[string]bool, events map[string]string) (code, other []gitops.PRCheck) {
	for _, c := range failed {
		switch {
		case len(required) > 0 && !required[c.Label()]:
			// not required to merge: neither fixed nor waited for
		case gitops.NotAboutCode(c), gitops.RunsFromBase(events[c.Label()]):
			other = append(other, c)
		default:
			code = append(code, c)
		}
	}
	return
}

func labels(cs []gitops.PRCheck) string {
	names := make([]string, len(cs))
	for i, c := range cs {
		names[i] = c.Label()
	}
	return strings.Join(names, ", ")
}

// mergePR squash-merges a task's pull request once GitHub finds it
// mergeable and its checks pass (model.Project.MergesGreenPRs): approving
// the task was the review, and the pull request is there so GitHub's checks
// run before the work reaches the default branch. When GitHub refuses (e.g.
// a required review), it stays open to be merged there.
func (m *MergeQueue) mergePR(ctx context.Context, t model.Todo, p model.Project, st gitops.PRStatus) {
	if st.Mergeable != "MERGEABLE" {
		return // UNKNOWN while GitHub works it out; the next poll tries again
	}
	n := t.PR.Number
	if err := m.Git.MergePR(ctx, p.ID, p.Path, n, st.HeadRefOid); err != nil {
		m.handled(t, st.HeadRefOid, fmt.Sprintf("⚠ couldn't merge pull request #%d: %v; merge it on GitHub", n, err))
		return
	}
	m.Log.Line(t, fmt.Sprintf("⇢ merging pull request #%d", n))
	if st, err := m.Git.PRStatus(ctx, p.ID, p.Path, n); err == nil && st.State == "MERGED" {
		m.followPR(ctx, t, p, st)
	}
}

// handled remembers that the PR's current head was dealt with.
func (m *MergeQueue) handled(t model.Todo, sha, line string) {
	m.Log.Line(t, line)
	_, _ = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		if x.PR != nil {
			x.PR.HandledSHA = sha
		}
		return nil
	})
}

func (m *MergeQueue) checksNote(ctx context.Context, p model.Project, n int, failed, other []gitops.PRCheck) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Checks fail on pull request #%d:\n", n)
	for _, c := range failed {
		fmt.Fprintf(&b, "- %s %s\n", c.Label(), c.URL())
	}
	shown := 0
	for _, c := range failed {
		if shown == 2 {
			break
		}
		if log := m.Git.FailedLog(ctx, p.ID, p.Path, c, 80); log != "" {
			fmt.Fprintf(&b, "\nLast lines of the failed log for %s:\n```\n%s\n```\n", c.Label(), log)
			shown++
		}
	}
	if len(other) > 0 {
		b.WriteString("\nThese also fail, but no change to this branch can fix them; leave them alone:\n")
		for _, c := range other {
			fmt.Fprintf(&b, "- %s %s\n", c.Label(), c.URL())
		}
	}
	b.WriteString("\nFix the code so these checks pass. Don't weaken or delete tests unless they are wrong.")
	return b.String()
}

func subject(t model.Todo) string {
	if t.Title != "" {
		return t.Title
	}
	return firstLine(t.Prompt)
}

func prBody(t model.Todo) string {
	body := strings.TrimSpace(t.Prompt)
	if len(body) > 4000 {
		body = body[:4000] + "…"
	}
	if t.Checks != nil && t.Checks.Passed {
		body += fmt.Sprintf("\n\nTests pass locally (`%s`).", t.Checks.Command)
	}
	return body + "\n\n---\nOpened by Dash after review.\n\nDash-Todo: " + t.ID
}
