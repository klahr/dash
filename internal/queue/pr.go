package queue

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// openPR is step 3 in "pr" mode: push the todo's branch and open (or update)
// its pull request, as a draft until its checks pass. The todo then waits in
// Review, where the pull request is reviewed and merged by a person on
// GitHub (Dash never merges it), until PollPRs sees it merged or closed.
func (m *MergeQueue) openPR(ctx context.Context, t model.Todo, p model.Project) error {
	wt := *t.Worktree
	if t.WaitsOnGitHub() {
		// Closed on GitHub while Claude worked on it: the task is rejected,
		// rather than its work reopened in a new pull request.
		if st, err := m.Git.PRStatus(ctx, p.ID, p.Path, t.PR.Number); err == nil && st.State != "OPEN" {
			m.followPR(ctx, t, p, st)
			return nil
		}
	}
	if err := m.Git.PushBranch(ctx, p.ID, wt.Path, wt.Branch); err != nil {
		return &prError{fmt.Errorf("push %s: %w", wt.Branch, err)}
	}
	n, url, err := m.Git.OpenDraftPR(ctx, p.ID, wt.Path, p.Base(), wt.Branch, subject(t), prBody(t))
	if err != nil {
		return &prError{fmt.Errorf("open pull request: %w", err)}
	}
	if _, err := m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		if x.Status != model.StatusMerging {
			return nil // moved on while pushing (e.g. merged or closed on GitHub): keep what was seen
		}
		pr := model.PullRequest{Number: n, URL: url, State: "OPEN"}
		if x.PR != nil && x.PR.Number == n {
			pr = *x.PR // the same pull request: keep what was handled and seen of it
			pr.URL, pr.State = url, "OPEN"
		}
		x.PR = &pr
		x.Status, x.Error = model.StatusReview, "" // reviewed on GitHub now
		return nil
	}); err != nil {
		return err
	}
	m.Log.Line(t, fmt.Sprintf("⇡ pushed %s; pull request #%d is open for review on GitHub: %s", wt.Branch, n, url))
	return nil
}

// RejectPR closes task t's open pull request on GitHub and deletes its
// branch there: the task was rejected in Dash (deleted, or moved back to
// drafts). It does nothing for a task without one.
func (m *MergeQueue) RejectPR(ctx context.Context, t model.Todo) error {
	if !t.WaitsOnGitHub() {
		return nil
	}
	p, err := m.Store.GetProject(t.ProjectID)
	if err != nil {
		return err
	}
	p = p.For(t)
	n := t.PR.Number
	if err := m.Git.ClosePR(ctx, p.ID, p.Path, n, "Rejected in Dash."); err != nil {
		return fmt.Errorf("close pull request #%d: %w", n, err)
	}
	if t.Worktree != nil {
		m.Git.DeleteRemoteBranch(ctx, p.ID, p.Path, t.Worktree.Branch)
	}
	_, _ = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error { // gone already when it was deleted
		if x.PR != nil && x.PR.Number == n {
			x.PR.State = "CLOSED"
		}
		return nil
	})
	slog.Info("closed the pull request of a task rejected in Dash", "todo", t.ID, "pr", n)
	return nil
}

// prError is a pull request that couldn't be pushed or opened: the task
// waits in review, where approving it tries again.
type prError struct{ error }

func (e *prError) Unwrap() error { return e.error }

// PollPRs follows open pull requests: merged ones are marked merged, closed
// ones reject their task, and conflicts, failing checks and reviews asking
// for changes are sent back to Claude. Tasks that failed with their pull
// request still open are followed too, so a merge or close done by hand on
// GitHub is noticed.
func (m *MergeQueue) PollPRs(ctx context.Context) {
	m.pollPRs(ctx, "")
}

// schedulePRPolls queues, on the worker of every project with an open pull
// request, a poll of that project's pull requests (unless one is queued
// already), so a merged one lands after the project's merges in hand and a
// slow project never delays the others.
func (m *MergeQueue) schedulePRPolls() {
	seen := map[string]bool{}
	for _, t := range m.openPRs() {
		if pid := t.ProjectID; !seen[pid] {
			seen[pid] = true
			m.later(pid, func(l *lane) *atomic.Bool { return &l.polling }, func(c context.Context) { m.pollPRs(c, pid) })
		}
	}
}

// openPRs is the todos whose open pull requests this Dash follows.
func (m *MergeQueue) openPRs() []model.Todo {
	var out []model.Todo
	for _, t := range m.Store.ListTodos("", model.StatusMerging, model.StatusReview, model.StatusFailed) {
		if t.PR != nil && t.PR.State == "OPEN" && !t.OnRunner() {
			out = append(out, t) // a runner follows the pull requests of its tasks and reports on them
		}
	}
	return out
}

// pollPRs follows the open pull requests of project projectID ("" for all).
func (m *MergeQueue) pollPRs(ctx context.Context, projectID string) {
	for _, t := range m.openPRs() {
		if projectID != "" && t.ProjectID != projectID {
			continue
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
		if t.Status == model.StatusMerging || t.Status == model.StatusReview {
			m.pushed(p, p.Base(), sha) // merged on GitHub just now: origin's branch moved
		}
		m.landed(ctx, t, p, sha, fmt.Sprintf("✔ pull request #%d merged", n))
		if t.Worktree != nil {
			m.Git.DeleteRemoteBranch(ctx, p.ID, p.Path, t.Worktree.Branch)
		}
	case "CLOSED":
		// Closing the pull request on GitHub rejects the task.
		why := fmt.Sprintf("Pull request #%d was closed on GitHub without merging, so the task was rejected. "+
			"Rework it to have Claude try again in a new pull request, or delete it", n)
		m.Log.Line(t, fmt.Sprintf("✖ pull request #%d was closed on GitHub without merging; task rejected", n))
		noteFailure(m.Store, t.ID, why, "")
		_, _ = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
			if x.Status != model.StatusMerged {
				x.Status, x.Error = model.StatusFailed, why
			}
			if x.PR != nil && x.PR.Number == n {
				x.PR.State = "CLOSED"
			}
			return nil
		})
	case "OPEN":
		if st.HeadRefOid == "" || m.inQueue(t.ID) {
			return // GitHub still working it out, or the branch is about to be pushed (again)
		}
		// Reviews asking for changes go back to Claude first: its fix is
		// pushed to the same pull request, and checks run on that.
		reviews, err := m.Git.PRReviews(ctx, p.ID, p.Path, n)
		if err != nil {
			slog.Warn("pull request reviews", "todo", t.ID, "pr", n, "err", err)
		} else if t.PR.ReviewID == 0 && t.Status == model.StatusMerging {
			// Approved in Dash before reviews were followed: what was said
			// on it before isn't news.
			m.seenReviews(t, reviews)
		} else if m.reviewBack(ctx, t, p, reviews) {
			return
		}
		if t.Status != model.StatusMerging && t.Status != model.StatusReview {
			return // failed: only a merge, a close or a review changes that
		}
		if t.Status == model.StatusMerging {
			// Approved in Dash before reviews moved to GitHub: Dash doesn't
			// merge it, so it waits in review for a person to merge it there.
			if x, err := m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
				if x.Status == model.StatusMerging {
					x.Status = model.StatusReview
				}
				return nil
			}); err == nil {
				t = x
			}
		}
		review := reviewState(st.ReviewDecision, reviews)
		// The head Dash already acted on: a fix pass that made no new commit
		// pushed it again. Without this the task would wait for a head that
		// never changes.
		handled := st.HeadRefOid == t.PR.HandledSHA
		w := prWait{handled: handled, review: review, autoApprove: p.AutoApprove}
		note := func(code, other []gitops.PRCheck) {
			m.notePR(t, st, prStatusLine(p.Base(), st, code, other, w), prWaitsOn(st, code, other, w), review)
		}
		if st.Mergeable == "CONFLICTING" {
			note(nil, nil)
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
				note(nil, nil)
				return
			}
			if c.Failed() {
				failed = append(failed, c)
			}
		}
		code, other := m.sortFailed(ctx, p, n, failed)
		if len(code) == 0 && st.IsDraft && m.ready(ctx, t, p) {
			st.IsDraft = false // its checks pass: for a person to review now
		}
		note(code, other)
		switch {
		case len(code) > 0 && handled:
			m.stuck(ctx, t, st, model.StatusFailed, fmt.Sprintf("Checks still fail on pull request #%d (%s) and Claude's fix made no new commit", n, labels(failed)))
		case len(code) > 0:
			m.handled(t, st.HeadRefOid, fmt.Sprintf("✖ checks fail on pull request #%d: %s", n, labels(failed)))
			t, _ = m.Store.GetTodo(t.ID)
			_ = m.sendBack(t, p, m.checksNote(ctx, p, n, code, other), false, fmt.Sprintf("checks fail on pull request #%d", n))
		case len(other) > 0:
			// Not something to fix in the code (e.g. an unsigned CLA): say so
			// and leave it to a person. PollPRs still follows the pull
			// request, and carries on once the check passes.
			m.blocked(t, fmt.Sprintf("%s%s fails, which isn't about the code. Fix it on GitHub (e.g. sign the CLA); Dash carries on once it passes", blockedPrefix(n), labels(other)))
		default:
			// Passing, or failing only checks that aren't required to merge:
			// a person merges it on GitHub.
			m.blocked(t, "")
		}
	}
}

// ready marks a task's draft pull request ready for review, and reports
// whether it is.
func (m *MergeQueue) ready(ctx context.Context, t model.Todo, p model.Project) bool {
	if err := m.Git.ReadyPR(ctx, p.ID, p.Path, t.PR.Number); err != nil {
		slog.Warn("mark pull request ready for review", "todo", t.ID, "pr", t.PR.Number, "err", err)
		return false
	}
	m.Log.Line(t, fmt.Sprintf("✔ checks pass; pull request #%d is ready for review", t.PR.Number))
	return true
}

func blockedPrefix(n int) string { return fmt.Sprintf("Pull request #%d can't merge: ", n) }

// blocked says on a task waiting on GitHub why its pull request can't merge
// (why "" clears it once that is over), moving a merging one to review.
func (m *MergeQueue) blocked(t model.Todo, why string) {
	if why == "" && !strings.HasPrefix(t.Error, blockedPrefix(t.PR.Number)) {
		return
	}
	if t.Error == why && (why == "" || t.Status == model.StatusReview) {
		return // said already
	}
	if why != "" {
		m.Log.Line(t, "✖ "+why)
	}
	_, _ = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		if x.Status != model.StatusMerging && x.Status != model.StatusReview {
			return nil
		}
		x.Error = why
		if why != "" {
			x.Status = model.StatusReview
		}
		return nil
	})
}

// notePR keeps what GitHub last said about a task's pull request on the
// task, for its card: the checks, the review and what the merge waits for
// (waits: what a person has to do, see prWaitsOn).
func (m *MergeQueue) notePR(t model.Todo, st gitops.PRStatus, line, waits, review string) {
	checks := prChecks(st.Checks)
	if t.PR.Status == line && slices.Equal(t.PR.Checks, checks) && t.PR.Draft == st.IsDraft && t.PR.Review == review && t.PR.Waits == waits {
		return // unchanged: don't write (and broadcast) on every poll
	}
	now := time.Now()
	_, _ = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		if x.PR == nil || x.PR.Number != t.PR.Number {
			return nil
		}
		x.PR.Checks, x.PR.Status, x.PR.Draft, x.PR.Review = checks, line, st.IsDraft, review
		if x.PR.Waits != waits || (waits != "" && x.PR.WaitsSince == nil) {
			x.PR.Waits, x.PR.WaitsSince = waits, nil
			if waits != "" {
				x.PR.WaitsSince = &now
			}
		}
		return nil
	})
}

// reviewState is where a pull request's review stands: GitHub's decision
// when its base branch requires reviews, else the latest approval or
// request for changes of each reviewer (apps' don't count).
func reviewState(decision string, reviews []gitops.PRReview) string {
	switch decision {
	case "APPROVED":
		return model.PRApproved
	case "CHANGES_REQUESTED":
		return model.PRChangesRequested
	case "REVIEW_REQUIRED":
		return model.PRReviewPending
	}
	latest := map[string]string{}
	for _, r := range reviews {
		if !r.User.Bot() && (r.State == "APPROVED" || r.State == "CHANGES_REQUESTED") {
			latest[r.User.Login] = r.State
		}
	}
	state := model.PRReviewPending
	for _, s := range latest {
		if s == "CHANGES_REQUESTED" {
			return model.PRChangesRequested
		}
		state = model.PRApproved
	}
	return state
}

// seenReviews marks every review so far as handled.
func (m *MergeQueue) seenReviews(t model.Todo, reviews []gitops.PRReview) {
	var newest int64
	for _, r := range reviews {
		newest = max(newest, r.ID)
	}
	// 1 when there are none yet: seen, and every real review is newer.
	newest = max(newest, 1)
	_, _ = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		if x.PR != nil && x.PR.Number == t.PR.Number && x.PR.ReviewID < newest {
			x.PR.ReviewID = newest
		}
		return nil
	})
}

// reviewBack sends the reviews submitted on a task's pull request since the
// last ones sent (a request for changes, or comments) back to Claude, the way
// a rework from Dash is, and reports whether it did. Claude's fix is pushed
// to the same pull request. Approvals, and reviews by apps (CI, CLA or
// coverage bots), aren't sent.
func (m *MergeQueue) reviewBack(ctx context.Context, t model.Todo, p model.Project, reviews []gitops.PRReview) bool {
	n := t.PR.Number
	var fresh []gitops.PRReview
	var newest int64
	for _, r := range reviews {
		if r.ID <= t.PR.ReviewID || r.User.Bot() || (r.State != "CHANGES_REQUESTED" && r.State != "COMMENTED") {
			continue
		}
		fresh = append(fresh, r)
		newest = max(newest, r.ID)
	}
	if len(fresh) == 0 {
		return false
	}
	var comments []gitops.PRComment
	if cs, err := m.Git.PRComments(ctx, p.ID, p.Path, n); err != nil {
		slog.Warn("pull request review comments", "todo", t.ID, "pr", n, "err", err)
		return false // try again at the next poll rather than send half of it
	} else {
		comments = cs
	}
	note := reviewNote(n, fresh, comments)
	if note == "" {
		// Nothing written (e.g. an empty review): nothing for Claude.
		m.seenReviews(t, fresh)
		return false
	}
	who := reviewers(fresh)
	why := fmt.Sprintf("review by %s on pull request #%d", who, n)
	m.Log.Line(t, fmt.Sprintf("↻ %s asks for changes on pull request #%d; sending the review back to Claude", who, n))
	_, err := m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		switch x.Status {
		case model.StatusReview, model.StatusMerging, model.StatusFailed:
		default:
			return errSkipUpdate // moved on meanwhile; the next poll sees it again
		}
		if x.PR == nil || x.PR.Number != n {
			return errSkipUpdate
		}
		x.Feedback = append(x.Feedback, note)
		x.PR.ReviewID = max(x.PR.ReviewID, newest)
		x.Status, x.Error, x.SentBack = model.StatusQueued, "", why
		// A person asked, as with a rework from Dash: a fresh budget for
		// fixes, and the fix goes to the pull request, not to a merge.
		x.AutoFixes, x.AutoMerge = 0, false
		return nil
	})
	if err != nil {
		return false
	}
	if m.Kick != nil {
		m.Kick()
	}
	return true
}

var errSkipUpdate = errors.New("skip")

func reviewers(rs []gitops.PRReview) string {
	var names []string
	for _, r := range rs {
		if !slices.Contains(names, r.User.Login) {
			names = append(names, r.User.Login)
		}
	}
	return strings.Join(names, ", ")
}

// maxReviewText caps one review's or comment's text in Claude's note.
const maxReviewText = 4000

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxReviewText {
		s = s[:maxReviewText] + "…"
	}
	return s
}

// reviewNote is what Claude is told of reviews rs on pull request n, with
// their comments on lines of the diff (comments holds the pull request's
// line comments; those of other reviews are left out). It is "" when they
// say nothing.
func reviewNote(n int, rs []gitops.PRReview, comments []gitops.PRComment) string {
	var b strings.Builder
	for _, r := range rs {
		var lines []gitops.PRComment
		for _, c := range comments {
			if c.ReviewID == r.ID && !c.User.Bot() && strings.TrimSpace(c.Body) != "" {
				lines = append(lines, c)
			}
		}
		body := clip(r.Body)
		if body == "" && len(lines) == 0 {
			continue
		}
		verb := "commented"
		if r.State == "CHANGES_REQUESTED" {
			verb = "requested changes"
		}
		fmt.Fprintf(&b, "\n**%s** %s:\n", r.User.Login, verb)
		if body != "" {
			fmt.Fprintf(&b, "\n%s\n", quote(body))
		}
		for _, c := range lines {
			where := c.Path
			if l := cmp.Or(c.Line, c.OriginalLine); l > 0 {
				where = fmt.Sprintf("%s:%d", c.Path, l)
			}
			reply := ""
			if c.InReplyTo != 0 {
				reply = " (in reply to an earlier comment)"
			}
			fmt.Fprintf(&b, "\nOn `%s`%s:\n%s\n", where, reply, quote(clip(c.Body)))
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return fmt.Sprintf("A reviewer left a review on pull request #%d:\n%s\n"+
		"Address it: change the code where the reviewer is right. Where a comment needs no change, or you disagree, "+
		"say why in your final message. Your commits are pushed to the same pull request.", n, b.String())
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

// prWait is what, besides its checks, a pull request's merge on GitHub
// waits for.
type prWait struct {
	handled     bool   // Dash already acted on its head
	review      string // where its review on GitHub stands (model.PRApproved…)
	autoApprove bool   // the project approves automatically: no review needed
}

// prStatusLine is what a card says about a pull request: what its merge
// waits for. code and other are its failing checks, sorted as sortFailed
// does.
func prStatusLine(base string, st gitops.PRStatus, code, other []gitops.PRCheck, w prWait) string {
	if st.Mergeable == "CONFLICTING" {
		if w.handled {
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
	case len(code) > 0 && w.handled:
		return fmt.Sprintf("%s still failing after Claude's fix: %s", plural(code), labels(code))
	case len(code) > 0:
		return fmt.Sprintf("%s failed: %s, sent back to Claude", plural(code), labels(code))
	case len(other) > 0:
		why := make([]string, len(other))
		for i, c := range other {
			why[i] = blockedBy(c)
		}
		return "Blocked: " + strings.Join(why, ", ")
	case w.review == model.PRChangesRequested:
		return "Changes requested on GitHub; waiting for the reviewer to look again"
	case w.review == model.PRApproved:
		return "Approved; waiting for you to merge on GitHub"
	case w.autoApprove:
		return "Waiting for you to merge on GitHub"
	}
	return "Waiting for a review on GitHub"
}

// prWaitsOn is what a person has to do for pull request st to go on
// (model.PullRequest.Waits), "" while it waits on Dash, Claude or its
// checks. It follows prStatusLine.
func prWaitsOn(st gitops.PRStatus, code, other []gitops.PRCheck, w prWait) string {
	if st.Mergeable == "CONFLICTING" || st.IsDraft || len(code) > 0 {
		return "" // Claude fixes it, or its checks still run before it is ready
	}
	pending := slices.ContainsFunc(st.Checks, gitops.PRCheck.Pending)
	approved := w.review == model.PRApproved || (w.autoApprove && w.review != model.PRChangesRequested)
	switch {
	case pending && (approved || w.review == model.PRChangesRequested):
		return "" // the checks first
	case len(other) > 0:
		return model.PRWaitBlocked
	case w.review == model.PRChangesRequested:
		return "" // the review went back to Claude
	case !approved:
		return model.PRWaitReview // a person can review while checks run
	}
	return model.PRWaitMerge
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

// stuck moves a task waiting on GitHub whose pull request can't go further without a
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
		if x.Status != model.StatusMerging && x.Status != model.StatusReview {
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
	if t.Summary != "" {
		body += "\n\n**Claude:** " + t.Summary
	}
	return body + "\n\n---\nOpened by Dash when Claude finished the task. Review it here: a review asking for changes " +
		"(or comments) goes back to Claude, whose fixes are pushed to this pull request.\n\nDash-Todo: " + t.ID
}
