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
	"github.com/klahr/dash/internal/textutil"
)

// openPR is step 3 in "pr" mode: push the todo's branch and open (or update)
// its pull request, as a draft until its checks pass. The todo then waits in
// Review, where the pull request is reviewed on GitHub and merged by a
// person (on GitHub, or with MergePR from Dash; Dash never merges it on its
// own), until pollPRs sees it merged or closed.
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
	n, url, err := m.Git.OpenDraftPR(ctx, p.ID, wt.Path, p.Base(), wt.Branch, textutil.NoMentions(taskName(t)), prBody(t))
	if err != nil {
		return &prError{fmt.Errorf("open pull request: %w", err)}
	}
	pushed := false
	if _, err := m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		if x.Status != model.StatusMerging {
			return nil // moved on while pushing (e.g. merged or closed on GitHub): keep what was seen
		}
		pushed = true
		// CommentID 1: its conversation is followed from the start.
		pr := model.PullRequest{Number: n, URL: url, State: "OPEN", CommentID: 1}
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
	if x, err := m.Store.GetTodo(t.ID); pushed && err == nil && x.PR != nil && x.PR.Number == n {
		// A review's fix: answer the reviewer there (never fails the task).
		m.replyToReview(ctx, x, p)
	}
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
	if err := m.Git.ClosePR(ctx, p.ID, p.Path, n, "Rejected in Dash.\n\n"+dashMark); err != nil {
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

// MergeRefused is GitHub refusing to merge a task's pull request (a
// conflict, a failing required check, a missing review…): Reason is
// GitHub's message. The task is left as it is.
type MergeRefused struct{ Reason string }

func (e *MergeRefused) Error() string { return e.Reason }

// Merged is what MergePR did: merged the pull request, or added it to the
// base branch's merge queue (Queued). As is the GitHub login it was done
// as ("" when gh can't say).
type Merged struct {
	Number int    `json:"number"`
	Method string `json:"method"`
	Queued bool   `json:"queued,omitempty"`
	As     string `json:"as,omitempty"`
}

// MergePR merges task t's open pull request on GitHub, as a person asked in
// Dash, with method (gitops.MergeSquash…; "" is the project's, else the
// repository's default) and the gh account signed in on this computer.
// GitHub decides whether it may: branch protection is never bypassed. Once
// merged the task is followed at once on the project's merge worker, so it
// becomes merged (notifications, dependants, the checklist) without
// waiting for the next poll; a pull request added to a merge queue is
// followed as before until GitHub merges it.
func (m *MergeQueue) MergePR(ctx context.Context, t model.Todo, method string) (Merged, error) {
	if !t.WaitsOnGitHub() {
		return Merged{}, &MergeRefused{Reason: "this task has no open pull request to merge"}
	}
	p, err := m.Store.GetProject(t.ProjectID)
	if err != nil {
		return Merged{}, err
	}
	p = p.For(t)
	n := t.PR.Number
	method = cmp.Or(method, p.MergeMethod)
	if method == "" {
		if method, err = m.Git.DefaultMergeMethod(ctx, p.ID, p.Path); err != nil {
			return Merged{}, &MergeRefused{Reason: "can't tell how the repository merges pull requests: " + ghMessage(err)}
		}
	}
	queued, err := m.Git.MergePR(ctx, p.ID, p.Path, n, method)
	if err != nil {
		var ce *gitops.CmdError
		if errors.As(err, &ce) {
			m.Log.Line(t, fmt.Sprintf("✖ GitHub didn't merge pull request #%d: %s", n, ghMessage(err)))
			return Merged{}, &MergeRefused{Reason: fmt.Sprintf("GitHub didn't merge pull request #%d: %s", n, ghMessage(err))}
		}
		return Merged{}, err
	}
	as, _ := m.Git.GitHubLogin(ctx)
	who := ""
	if as != "" {
		who = " as " + as
	}
	res := Merged{Number: n, Method: method, Queued: queued, As: as}
	if queued {
		m.Log.Line(t, fmt.Sprintf("⇢ pull request #%d queued to merge on GitHub from Dash%s", n, who))
		return res, nil
	}
	m.Log.Line(t, fmt.Sprintf("⇣ pull request #%d merged on GitHub from Dash%s (%s)", n, who, method))
	// Follow it now rather than at the next poll, on the project's worker
	// like a poll (a merge lands after the merges in hand). Not waited for:
	// it syncs the remote, which can take a while.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = m.Do(ctx, p.ID, func(c context.Context) { m.followNow(c, t.ID) })
	}()
	return res, nil
}

// followNow follows task id's pull request once, if it is still open in
// Dash.
func (m *MergeQueue) followNow(ctx context.Context, id string) {
	t, err := m.Store.GetTodo(id)
	if err != nil || !t.WaitsOnGitHub() {
		return
	}
	p, err := m.Store.GetProject(t.ProjectID)
	if err != nil {
		return
	}
	p = p.For(t)
	st, err := m.Git.PRStatus(ctx, p.ID, p.Path, t.PR.Number)
	if err != nil {
		slog.Warn("pull request status after merging it", "todo", t.ID, "pr", t.PR.Number, "err", err)
		return
	}
	m.followPR(ctx, t, p, st)
}

// ghMessage is what gh said about a failure: its output (GitHub's message)
// rather than the command line.
func ghMessage(err error) string {
	var ce *gitops.CmdError
	if errors.As(err, &ce) {
		if msg := strings.TrimSpace(ce.Output); msg != "" {
			return textutil.Clip(msg, 1000)
		}
	}
	return err.Error()
}

// prError is a pull request that couldn't be pushed or opened: the task
// waits in review, where approving it tries again.
type prError struct{ error }

func (e *prError) Unwrap() error { return e.error }

// schedulePRPolls queues, on the worker of every project with an open pull
// request, a poll of that project's pull requests (unless one is queued
// already), so a merged one lands after the project's merges in hand and a
// slow project never delays the others.
func (m *MergeQueue) schedulePRPolls() {
	seen := map[string]bool{}
	for _, t := range m.openPRs() {
		if pid := t.ProjectID; !seen[pid] {
			seen[pid] = true
			m.later(pid, func(l *lane) *atomic.Bool { return &l.polling }, func(c context.Context) { m.pollOpenPRs(c, pid, true) })
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

// pollPRs follows the open pull requests of project projectID ("" for
// all): merged ones are marked merged, closed ones reject their task, and
// conflicts, failing checks and reviews asking for changes are sent back to
// Claude. Tasks that failed with their pull request still open are followed
// too, so a merge or close done by hand on GitHub is noticed.
// schedulePRPolls runs it per project.
func (m *MergeQueue) pollPRs(ctx context.Context, projectID string) {
	m.pollOpenPRs(ctx, projectID, false)
}

// failedPollEvery is how many PR polls apart the pull requests of failed
// tasks are followed: only a merge, a close or a review on GitHub changes
// those, so they needn't cost GitHub calls every minute.
const failedPollEvery = 10

// pollOpenPRs is pollPRs. It asks GitHub for the state of a project's pull
// requests in one query (see gitops.PRStatuses), and reads their reviews
// and comments only when GitHub says they changed (see followPR). With
// spaceFailed (the timed polls), failed tasks' pull requests are followed
// only every failedPollEvery polls.
func (m *MergeQueue) pollOpenPRs(ctx context.Context, projectID string, spaceFailed bool) {
	type group struct {
		project model.Project
		todos   []model.Todo
	}
	var order []string
	groups := map[string]*group{}
	now := time.Now()
	for _, t := range m.openPRs() {
		if projectID != "" && t.ProjectID != projectID {
			continue
		}
		if spaceFailed && !m.failedDue(t, now) {
			continue
		}
		g := groups[t.ProjectID]
		if g == nil {
			p, err := m.Store.GetProject(t.ProjectID)
			if err != nil {
				continue
			}
			g = &group{project: p}
			groups[t.ProjectID] = g
			order = append(order, t.ProjectID)
		}
		g.todos = append(g.todos, t)
	}
	for _, pid := range order {
		g := groups[pid]
		numbers := make([]int, len(g.todos))
		for i, t := range g.todos {
			numbers[i] = t.PR.Number
		}
		sts := m.Git.PRStatuses(ctx, g.project.ID, g.project.Path, numbers)
		for _, t := range g.todos {
			p := g.project.For(t)
			st, ok := sts[t.PR.Number]
			if !ok {
				var err error
				if st, err = m.Git.PRStatus(ctx, p.ID, p.Path, t.PR.Number); err != nil {
					slog.Warn("pull request status", "todo", t.ID, "pr", t.PR.Number, "err", err)
					continue
				}
			}
			m.followPR(ctx, t, p, st)
		}
	}
}

// failedDue reports whether task t's pull request is to be followed by
// this timed poll: always, unless the task failed and was followed less
// than failedPollEvery polls ago.
func (m *MergeQueue) failedDue(t model.Todo, now time.Time) bool {
	if t.Status != model.StatusFailed {
		m.failedPolled.Delete(t.ID)
		return true
	}
	if last, ok := m.failedPolled.Load(t.ID); ok && now.Sub(last.(time.Time)) < failedPollEvery*m.PRPoll-m.PRPoll/2 {
		return false
	}
	m.failedPolled.Store(t.ID, now)
	return true
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
		// So are comments in its conversation tab. Both are read only when
		// GitHub says the pull request changed since they were last read
		// whole (a review or a comment changes its updatedAt).
		var reviews []gitops.PRReview
		read := st.UpdatedAt == "" || st.UpdatedAt != t.PR.UpdatedAt
		if read {
			var err error
			reviews, err = m.Git.PRReviews(ctx, p.ID, p.Path, n)
			fresh := reviews
			if err != nil {
				slog.Warn("pull request reviews", "todo", t.ID, "pr", n, "err", err)
			} else if t.PR.ReviewID == 0 && t.Status == model.StatusMerging {
				// Approved in Dash before reviews were followed: what was said
				// on it before isn't news.
				m.seenReviews(t, reviews)
				fresh = nil
			}
			talk, talkOK := m.conversation(ctx, t, p)
			sent, ok := m.reviewBack(ctx, t, p, fresh, talk)
			if sent {
				return
			}
			if ok && talkOK && err == nil && st.UpdatedAt != "" {
				m.seenUpdate(t, st.UpdatedAt)
			}
		}
		if t.Status != model.StatusMerging && t.Status != model.StatusReview {
			return // failed: only a merge, a close or a review changes that
		}
		if t.Status == model.StatusMerging {
			// Approved in Dash before reviews moved to GitHub: Dash doesn't
			// merge it on its own, so it waits in review for a person to.
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
		if !read && st.ReviewDecision == "" {
			review = cmp.Or(t.PR.Review, review) // its reviews haven't changed since
		}
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
			// and leave it to a person. pollPRs still follows the pull
			// request, and carries on once the check passes.
			m.blocked(t, fmt.Sprintf("%s%s fails, which isn't about the code. Fix it on GitHub (e.g. sign the CLA); Dash carries on once it passes", blockedPrefix(n), labels(other)))
		default:
			// Passing, or failing only checks that aren't required to merge:
			// a person merges it (on GitHub, or from Dash).
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
		if !r.User.Bot() && gitops.Trusted(r.Association) && (r.State == "APPROVED" || r.State == "CHANGES_REQUESTED") {
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

// seenUpdate remembers that task t's pull request's reviews and comments
// were read whole at GitHub's updatedAt.
func (m *MergeQueue) seenUpdate(t model.Todo, updatedAt string) {
	if t.PR.UpdatedAt == updatedAt {
		return
	}
	_, _ = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		if x.PR == nil || x.PR.Number != t.PR.Number {
			return errSkipUpdate
		}
		x.PR.UpdatedAt = updatedAt
		return nil
	})
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

// dashMark ends the comments Dash itself leaves on a pull request, which
// are never sent back to Claude (Dash comments with the gh account of the
// person it works for).
const dashMark = "<!-- dash -->"

// command matches a comment that asks Claude for something: the command
// "/dash" (or "/claude"), as a whole word, in any case, and not part of a
// URL or a path ("https://example.com/dash", "a/dash", "/dash.md"). Match it
// against the comment without its code (textutil.WithoutCode). A bare
// "@dash" or "@claude" asks for nothing: those are real people's GitHub
// handles, whom no one should tag.
var command = regexp.MustCompile(`(?i)(?:^|[^\w@/\\.:~-])/(?:dash|claude)(?:$|[^\w/\\.-]|\.(?:[^\w/\\]|$))`)

// conversation is the comments in a task's pull request's conversation tab,
// nil when they can't be fetched (ok false), or on a pull request opened
// before Dash followed them, whose comments so far are then marked seen.
func (m *MergeQueue) conversation(ctx context.Context, t model.Todo, p model.Project) (_ []gitops.IssueComment, ok bool) {
	cs, err := m.Git.PRIssueComments(ctx, p.ID, p.Path, t.PR.Number)
	if err != nil {
		slog.Warn("pull request conversation", "todo", t.ID, "pr", t.PR.Number, "err", err)
		return nil, false
	}
	if t.PR.CommentID == 0 {
		var newest int64 = 1 // seen, and every real comment is newer
		for _, c := range cs {
			newest = max(newest, c.ID)
		}
		_, _ = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
			if x.PR != nil && x.PR.Number == t.PR.Number && x.PR.CommentID < newest {
				x.PR.CommentID = newest
			}
			return nil
		})
		return nil, true
	}
	return cs, true
}

// asksClaude reports a conversation comment that is for Claude: written by
// a person (not an app, nor Dash itself), saying something, and with the
// command /dash (or /claude) outside its code. Without it, it is taken as
// talk between people.
// Who wrote it is checked apart, with gitops.Trusted.
func asksClaude(c gitops.IssueComment) bool {
	return !c.User.Bot() && !strings.Contains(c.Body, dashMark) && strings.TrimSpace(c.Body) != "" && command.MatchString(textutil.WithoutCode(c.Body))
}

// reviewBack sends the reviews submitted on a task's pull request since the
// last ones sent (a request for changes, or comments), and the comments in
// its conversation tab asking for Claude since the last ones sent, back to
// Claude in one note, the way a rework from Dash is, and reports whether it
// did (sent). ok is false when it has to look at them again at the next
// poll (a fetch failed, or the task moved on meanwhile). Claude's fix is pushed to the same pull request. Approvals, and
// reviews and comments by apps (CI, CLA or coverage bots), aren't sent.
// Nor is anything by someone who can't push to the repository (on a public
// one, anyone on GitHub can comment): Claude runs with the shell allowed, so
// their words would be commands run on this computer. Those are noted on the
// task once and skipped.
func (m *MergeQueue) reviewBack(ctx context.Context, t model.Todo, p model.Project, reviews []gitops.PRReview, talk []gitops.IssueComment) (sent, ok bool) {
	n := t.PR.Number
	var fresh []gitops.PRReview
	var newest, skippedReview int64
	var outsiders []string
	outsider := func(login string) {
		if !slices.Contains(outsiders, login) {
			outsiders = append(outsiders, login)
		}
	}
	for _, r := range reviews {
		if r.ID <= t.PR.ReviewID || r.User.Bot() || (r.State != "CHANGES_REQUESTED" && r.State != "COMMENTED") {
			continue
		}
		if !gitops.Trusted(r.Association) {
			outsider(r.User.Login)
			skippedReview = max(skippedReview, r.ID)
			continue
		}
		fresh = append(fresh, r)
		newest = max(newest, r.ID)
	}
	var said []gitops.IssueComment
	var newestComment, skippedComment int64
	for _, c := range talk {
		if c.ID <= t.PR.CommentID || !asksClaude(c) {
			continue
		}
		if !gitops.Trusted(c.Association) {
			outsider(c.User.Login)
			skippedComment = max(skippedComment, c.ID)
			continue
		}
		said = append(said, c)
		newestComment = max(newestComment, c.ID)
	}
	if len(outsiders) > 0 {
		m.Log.Line(t, fmt.Sprintf("⊘ not sent to Claude: review or comment on pull request #%d by %s, who can't push to the repository", n, strings.Join(outsiders, ", ")))
	}
	if len(fresh) == 0 && len(said) == 0 {
		if len(outsiders) > 0 {
			m.seenFeedback(t, skippedReview, skippedComment)
		}
		return false, true
	}
	var comments []gitops.PRComment
	if len(fresh) > 0 {
		cs, err := m.Git.PRComments(ctx, p.ID, p.Path, n)
		if err != nil {
			slog.Warn("pull request review comments", "todo", t.ID, "pr", n, "err", err)
			return false, false // try again at the next poll rather than send half of it
		}
		comments = cs
	}
	// Items of a reply still owed (its fix not pushed yet) keep their numbers.
	first := 1
	if t.PR.Reply != nil {
		for _, it := range t.PR.Reply.Items {
			first = max(first, it.Ref+1)
		}
	}
	note, items := reviewNote(n, fresh, comments, said, first)
	if note == "" {
		// Nothing written (e.g. an empty review): nothing for Claude.
		m.seenReviews(t, fresh)
		return false, true
	}
	who := reviewers(fresh, said)
	what := "review"
	if len(fresh) == 0 {
		what = "comment"
	}
	why := fmt.Sprintf("%s by %s on pull request #%d", what, who, n)
	m.Log.Line(t, fmt.Sprintf("↻ %s asks for changes on pull request #%d; sending the %s back to Claude", who, n, what))
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
		x.PR.ReviewID = max(x.PR.ReviewID, newest, skippedReview)
		x.PR.CommentID = max(x.PR.CommentID, newestComment, skippedComment)
		// Answered on GitHub once the fix is pushed (or fails).
		reply := model.PRReply{}
		if x.PR.Reply != nil {
			reply = *x.PR.Reply
		}
		reply.ReviewID, reply.Report = max(reply.ReviewID, newest), ""
		reply.CommentID = max(reply.CommentID, newestComment)
		reply.Items = append(slices.Clip(reply.Items), items...)
		for _, r := range fresh {
			if r.State == "CHANGES_REQUESTED" && !slices.Contains(reply.Reviewers, r.User.Login) {
				reply.Reviewers = append(reply.Reviewers, r.User.Login)
			}
		}
		x.PR.Reply = &reply
		x.Status, x.Error, x.SentBack = model.StatusQueued, "", why
		// A person asked, as with a rework from Dash: a fresh budget for
		// fixes, and the fix goes to the pull request, not to a merge.
		x.AutoFixes, x.AutoMerge = 0, false
		return nil
	})
	if err != nil {
		return false, false
	}
	if m.Kick != nil {
		m.Kick()
	}
	return true, true
}

var errSkipUpdate = errors.New("skip")

// seenFeedback marks the reviews up to reviewID and the conversation
// comments up to commentID on a task's pull request as handled.
func (m *MergeQueue) seenFeedback(t model.Todo, reviewID, commentID int64) {
	_, _ = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		if x.PR == nil || x.PR.Number != t.PR.Number {
			return errSkipUpdate
		}
		x.PR.ReviewID = max(x.PR.ReviewID, reviewID)
		x.PR.CommentID = max(x.PR.CommentID, commentID)
		return nil
	})
}

func reviewers(rs []gitops.PRReview, cs []gitops.IssueComment) string {
	var names []string
	add := func(login string) {
		if !slices.Contains(names, login) {
			names = append(names, login)
		}
	}
	for _, r := range rs {
		add(r.User.Login)
	}
	for _, c := range cs {
		add(c.User.Login)
	}
	return strings.Join(names, ", ")
}

// maxReviewText caps one review's or comment's text in Claude's note.
const maxReviewText = 4000

func clip(s string) string { return textutil.Clip(strings.TrimSpace(s), maxReviewText) }

// reviewNote is what Claude is told of reviews rs on pull request n, with
// their comments on lines of the diff (comments holds the pull request's
// line comments; those of other reviews are left out), and of comments talk
// in its conversation tab, and the items it numbers for Claude to answer one
// by one, from first on. The note is "" when they say nothing.
func reviewNote(n int, rs []gitops.PRReview, comments []gitops.PRComment, talk []gitops.IssueComment, first int) (string, []model.PRReplyItem) {
	var b strings.Builder
	reviewed, commented := false, false
	var items []model.PRReplyItem
	ref := max(first, 1)
	for _, r := range rs {
		var lines []gitops.PRComment
		for _, c := range comments {
			if c.ReviewID == r.ID && !c.User.Bot() && gitops.Trusted(c.Association) && strings.TrimSpace(c.Body) != "" {
				lines = append(lines, c)
			}
		}
		body := clip(r.Body)
		if body == "" && len(lines) == 0 {
			continue
		}
		reviewed = true
		verb := "commented"
		if r.State == "CHANGES_REQUESTED" {
			verb = "requested changes"
		}
		fmt.Fprintf(&b, "\n**%s** %s:\n", r.User.Login, verb)
		if body != "" {
			fmt.Fprintf(&b, "\n[%d]\n%s\n", ref, quote(body))
			items = append(items, model.PRReplyItem{Ref: ref, Author: r.User.Login})
			ref++
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
			fmt.Fprintf(&b, "\n[%d] On `%s`%s:\n%s\n", ref, where, reply, quote(clip(c.Body)))
			// Replies go to the thread's first comment: GitHub takes no
			// replies to replies.
			items = append(items, model.PRReplyItem{Ref: ref, Author: c.User.Login, Thread: cmp.Or(c.InReplyTo, c.ID), Where: where})
			ref++
		}
	}
	for _, c := range talk {
		if body := clip(c.Body); body != "" {
			commented = true
			fmt.Fprintf(&b, "\n**%s** commented on the pull request:\n\n[%d]\n%s\n", c.User.Login, ref, quote(body))
			items = append(items, model.PRReplyItem{Ref: ref, Author: c.User.Login})
			ref++
		}
	}
	if b.Len() == 0 {
		return "", nil
	}
	left := "a review"
	switch {
	case commented && reviewed:
		left = "a review and comments"
	case commented:
		left = "comments"
	}
	return fmt.Sprintf("A reviewer left %s on pull request #%d:\n%s\n"+
		"Address it: change the code where the reviewer is right. Where a comment needs no change, or you disagree, "+
		"don't change it; say why instead. Your commits are pushed to the same pull request, and Dash posts your "+
		"final message there for the reviewer.\n\n"+
		"End your final message with a short summary of what you changed, for the reviewer, followed by a "+
		"`%s` block answering every numbered item above, one line each: `addressed` and what you changed, or "+
		"`disagreed` and why you didn't. For example:\n\n"+
		"```%s\n[%d] addressed: renamed the file to notes.md\n[%d] disagreed: the constant is used once; inlining it reads better\n```",
		left, n, b.String(), reviewBlock, reviewBlock, items[0].Ref, items[0].Ref+1), items
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

// prBody is the description of task t's pull request: its prompt, whether
// its tests passed, Claude's summary and how to review it. It mentions no
// one: GitHub would notify the owner of each bare @handle in it.
func prBody(t model.Todo) string {
	body := textutil.NoMentions(textutil.Clip(strings.TrimSpace(t.Prompt), 4000))
	if t.Checks != nil && t.Checks.Passed {
		body += fmt.Sprintf("\n\nTests pass locally (`%s`).", t.Checks.Command)
	}
	if t.Summary != "" {
		body += "\n\n**Claude:** " + textutil.NoMentions(t.Summary)
	}
	return body + "\n\n---\nOpened by Dash when Claude finished the task. Review it here: a review asking for changes " +
		"(or comments), or a comment with the command `/dash` (e.g. \"`/dash` also update the docs\"), goes back to Claude, whose fixes are pushed to this pull request.\n\nDash-Todo: " + t.ID
}
