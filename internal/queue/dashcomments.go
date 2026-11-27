package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/textutil"
)

// Line comments from Dash on GitHub: a comment a person writes on a task's
// diff in Dash (model.ReviewComment) is posted at once on the task's open
// pull request, for the reviewers there to see, as a line comment on the
// pull request's head (or, for lines not in its diff, in its conversation).
// Editing or deleting it in Dash does the same there. The ids of what Dash
// posted are kept (PullRequest.Posted), so its GitHub review watcher never
// sends them back to Claude as a reviewer's. When the comments are sent to
// Claude with a rework, Dash answers each posted one in its thread once the
// fix is pushed (replyToReview), as it answers a review from GitHub.
// Posting never fails the comment: a failure is kept on it
// (PostedComment.Error) for Dash to show, with a retry.

// commentTimeout bounds posting, editing or deleting one comment.
const commentTimeout = time.Minute

// PostsComments reports whether a comment on task t's diff goes to GitHub
// too: t has an open pull request, followed on this Dash.
func PostsComments(t model.Todo) bool { return t.WaitsOnGitHub() && !t.OnRunner() }

// commentLock serializes the posts, edits and deletes of task id's comments,
// so an edit or a delete made while its comment is being posted waits for it.
func (m *MergeQueue) commentLock(id string) func() {
	v, _ := m.commentMu.LoadOrStore(id, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// PostComment posts task todoID's comment cid on its open pull request,
// unless it is there already. What happened is kept on the comment: its id
// and link on GitHub, or why it couldn't be posted (also the error returned).
func (m *MergeQueue) PostComment(ctx context.Context, todoID, cid string) error {
	defer m.commentLock(todoID)()
	t, err := m.Store.GetTodo(todoID)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(t.Comments, func(c model.ReviewComment) bool { return c.ID == cid })
	if i < 0 || t.Comments[i].GitHub.Posted() || !PostsComments(t) {
		return nil
	}
	c, n := t.Comments[i], t.PR.Number
	p, err := m.Store.GetProject(t.ProjectID)
	if err != nil {
		return err
	}
	p = p.For(t)
	m.setPosted(todoID, cid, &model.PostedComment{PR: n, Since: time.Now().UTC()})
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commentTimeout)
	defer cancel()

	posted, err := m.postComment(ctx, p, n, c)
	if err != nil {
		slog.Warn("post a comment from Dash on GitHub", "todo", todoID, "pr", n, "err", err)
		m.setPosted(todoID, cid, &model.PostedComment{PR: n, Error: brief(err), Since: time.Now().UTC()})
		m.Log.Line(t, fmt.Sprintf("⚠ couldn't post a comment on %s on pull request #%d: %s", c.File, n, brief(err)))
		return err
	}
	m.postedNow.Store(cid, posted) // for a DeleteComment waiting on this
	// Recorded first, so the watcher never takes it for a reviewer's.
	var now *model.ReviewComment
	_, _ = m.Store.UpdateTodo(todoID, func(x *model.Todo) error {
		if x.PR != nil && x.PR.Number == n {
			x.PR.Posted = append(x.PR.Posted, model.PostedID{ID: posted.ID, ReviewID: posted.ReviewID, Issue: posted.Issue})
		}
		if j := slices.IndexFunc(x.Comments, func(c model.ReviewComment) bool { return c.ID == cid }); j >= 0 {
			x.Comments[j].GitHub = &posted
			c := x.Comments[j]
			now = &c
		}
		return nil
	})
	// Gone from Dash meanwhile: sent to Claude with a rework (it stays), or
	// deleted (DeleteComment, waiting on this, deletes it).
	if now != nil {
		m.postedNow.Delete(cid) // on the comment now
	}
	if now != nil && now.Text != c.Text {
		// Edited in Dash while it was being posted.
		_ = m.editPosted(ctx, t, p, *now)
	}
	where := "a line comment"
	if posted.Issue {
		where = "a comment (its lines aren't in the diff)"
	}
	m.Log.Line(t, fmt.Sprintf("💬 posted %s on %s on pull request #%d", where, c.File, n))
	return nil
}

// postComment posts c on pull request n of project p: as a line comment on
// its head, or, when GitHub can't place the lines in its diff, in its
// conversation.
func (m *MergeQueue) postComment(ctx context.Context, p model.Project, n int, c model.ReviewComment) (model.PostedComment, error) {
	out := model.PostedComment{PR: n, Since: time.Now().UTC()}
	st, err := m.Git.PRStatus(ctx, p.ID, p.Path, n)
	if err != nil {
		return out, err
	}
	if st.State != "OPEN" {
		return out, fmt.Errorf("pull request #%d isn't open", n)
	}
	if st.HeadRefOid != "" {
		lc, err := m.Git.PostLineComment(ctx, p.ID, p.Path, n, gitops.LineComment{
			Commit: st.HeadRefOid, Path: c.File, Old: c.Side == "old",
			StartLine: c.Line, Line: max(c.Line, c.EndLine), Body: dashCommentBody(c, false),
		})
		if err == nil {
			out.ID, out.ReviewID, out.URL = lc.ID, lc.ReviewID, lc.HTMLURL
			return out, nil
		}
		if !errors.Is(err, gitops.ErrNotInDiff) {
			return out, err
		}
	}
	ic, err := m.Git.PostIssueComment(ctx, p.ID, p.Path, n, dashCommentBody(c, true))
	if err != nil {
		return out, err
	}
	out.ID, out.Issue, out.URL = ic.ID, true, ic.HTMLURL
	return out, nil
}

// UpdateComment puts the text of task todoID's comment cid on GitHub, if
// it was posted there.
func (m *MergeQueue) UpdateComment(ctx context.Context, todoID, cid string) error {
	defer m.commentLock(todoID)()
	t, err := m.Store.GetTodo(todoID)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(t.Comments, func(c model.ReviewComment) bool { return c.ID == cid })
	if i < 0 || !t.Comments[i].GitHub.Posted() {
		return nil // not on GitHub; one being posted gets the new text
	}
	p, err := m.Store.GetProject(t.ProjectID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commentTimeout)
	defer cancel()
	return m.editPosted(ctx, t, p.For(t), t.Comments[i])
}

// editPosted puts posted comment c's text on GitHub, and keeps on it
// whether that failed.
func (m *MergeQueue) editPosted(ctx context.Context, t model.Todo, p model.Project, c model.ReviewComment) error {
	g := *c.GitHub
	err := m.Git.EditComment(ctx, p.ID, p.Path, g.ID, g.Issue, dashCommentBody(c, g.Issue))
	g.Error = ""
	if err != nil {
		slog.Warn("edit a comment from Dash on GitHub", "todo", t.ID, "pr", g.PR, "err", err)
		m.Log.Line(t, fmt.Sprintf("⚠ couldn't edit a comment on pull request #%d: %s", g.PR, brief(err)))
		g.Error = "edit: " + brief(err)
	}
	m.setPosted(t.ID, c.ID, &g)
	return err
}

// DeleteComment deletes comment c, just deleted from task t in Dash, from
// GitHub, if it was posted there (or once it is, when it was being posted).
// A failure is only logged: the comment is gone from Dash.
func (m *MergeQueue) DeleteComment(ctx context.Context, t model.Todo, c model.ReviewComment) {
	defer m.commentLock(t.ID)()
	if v, ok := m.postedNow.LoadAndDelete(c.ID); ok && !c.GitHub.Posted() {
		g := v.(model.PostedComment)
		c.GitHub = &g
	}
	if !c.GitHub.Posted() {
		return
	}
	p, err := m.Store.GetProject(t.ProjectID)
	if err != nil {
		return
	}
	p = p.For(t)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commentTimeout)
	defer cancel()
	if err := m.Git.DeleteComment(ctx, p.ID, p.Path, c.GitHub.ID, c.GitHub.Issue); err != nil {
		slog.Warn("delete a comment from Dash on GitHub", "todo", t.ID, "pr", c.GitHub.PR, "err", err)
		m.Log.Line(t, fmt.Sprintf("⚠ couldn't delete a comment on pull request #%d: %s", c.GitHub.PR, brief(err)))
	}
}

// setPosted sets what Dash knows of task todoID's comment cid on GitHub.
func (m *MergeQueue) setPosted(todoID, cid string, g *model.PostedComment) {
	_, _ = m.Store.UpdateTodo(todoID, func(x *model.Todo) error {
		i := slices.IndexFunc(x.Comments, func(c model.ReviewComment) bool { return c.ID == cid })
		if i < 0 {
			return errSkipUpdate
		}
		x.Comments[i].GitHub = g
		return nil
	})
}

// dashCommentBody is comment c as Dash posts it on GitHub: who wrote it in
// Dash (Dash posts with the gh account signed in here, which may be someone
// else's), and, in the conversation (issue), the file and lines it is on
// with the code it quotes.
func dashCommentBody(c model.ReviewComment, issue bool) string {
	who := "someone"
	if c.Author != "" {
		who = "**" + c.Author + "**"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<sub>From %s in Dash</sub>\n\n", who)
	if issue {
		fmt.Fprintf(&b, "On `%s`, %s (not in this pull request's diff):\n\n", c.File, agent.CommentLines(c))
		if code := strings.TrimRight(c.Code, "\n"); code != "" {
			fence := agent.CodeFence(code)
			fmt.Fprintf(&b, "%sdiff\n%s\n%s\n\n", fence, code, fence)
		}
	}
	b.WriteString(strings.TrimSpace(c.Text) + "\n\n" + dashMark)
	return textutil.NoMentions(b.String())
}

// OweDashReply records on task x, whose comments are being sent to Claude
// with a rework (numbered from 1 as agent.ReviewNote numbers them), that
// those posted on its pull request are answered there once Claude's fix is
// pushed. It returns what to add to Claude's note for that: the ask to
// answer each one; "" when none was posted.
func OweDashReply(x *model.Todo, comments []model.ReviewComment) string {
	if !x.WaitsOnGitHub() {
		return ""
	}
	var items []model.PRReplyItem
	n := 0
	for _, c := range comments {
		if strings.TrimSpace(c.Text) == "" {
			continue
		}
		n++
		if !c.GitHub.Posted() || c.GitHub.PR != x.PR.Number {
			continue
		}
		it := model.PRReplyItem{Ref: n, Author: c.Author, Where: fmt.Sprintf("%s:%d", c.File, c.Line)}
		if !c.GitHub.Issue {
			it.Thread = c.GitHub.ID
		}
		items = append(items, it)
	}
	if len(items) == 0 {
		return ""
	}
	reply := model.PRReply{}
	if x.PR.Reply != nil {
		reply = *x.PR.Reply
	}
	// Claude numbers its answers as this note does: items of an earlier
	// reply still owed would be confused with these.
	reply.Items, reply.Report, reply.Dash = items, "", true
	x.PR.Reply = &reply
	return fmt.Sprintf("These comments are also on pull request #%d on GitHub, where Dash answers each of them once your fix is pushed. "+
		"End your final message with a short summary of what you changed, for the reviewers, followed by a `%s` block "+
		"answering every numbered comment above, one line each: `addressed` and what you changed, or `disagreed` and why you didn't. "+
		"For example:\n\n```%s\n[1] addressed: renamed the file to notes.md\n[2] disagreed: the constant is used once; inlining it reads better\n```",
		x.PR.Number, reviewBlock, reviewBlock)
}
