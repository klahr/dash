package queue

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// Closing the loop on GitHub after a review went back to Claude: once its
// fix is pushed, Dash posts Claude's summary on the pull request, answers
// each line comment's thread (resolving those Claude addressed, leaving
// open those it disagreed with) and asks the reviewers who requested
// changes to look again. When the fix fails instead, it says why. None of
// it fails the task: GitHub errors are logged and Dash carries on.

// reviewBlock is the fenced block Claude ends its final message with, one
// line per numbered item of the review (see reviewNote).
const reviewBlock = "dash-review"

var (
	blankLinesRe  = regexp.MustCompile(`\n\s*\n\s*\n`)
	reviewBlockRe = regexp.MustCompile("(?s)```" + reviewBlock + "[^\\n]*\\n(.*?)(?:```|$)")
	// [3] addressed: what changed   /   - [3] disagreed — why not
	reviewLineRe = regexp.MustCompile(`(?i)^\s*(?:[-*]\s*)?\[?#?(\d+)\]?[.):]?\s*` +
		`(addressed|fixed|done|disagreed|disagree|declined|not addressed|not changed|no change)\b\s*[:\-–—]?\s*(.*)$`)
)

// reviewAnswer is Claude's answer to one numbered item of a review.
type reviewAnswer struct {
	Addressed bool
	Text      string
}

// reviewReply is Claude's final message on a review fix: the summary for
// the reviewer, and its answer to each numbered item (none when it gave no
// dash-review block).
type reviewReply struct {
	Summary string
	Answers map[int]reviewAnswer
}

// parseReviewReply reads Claude's final message on a review fix. The last
// dash-review block holds the answers; the rest is the summary.
func parseReviewReply(report string) reviewReply {
	out := reviewReply{Answers: map[int]reviewAnswer{}}
	locs := reviewBlockRe.FindAllStringSubmatchIndex(report, -1)
	if len(locs) == 0 {
		out.Summary = strings.TrimSpace(report)
		return out
	}
	loc := locs[len(locs)-1]
	out.Summary = blankLinesRe.ReplaceAllString(strings.TrimSpace(report[:loc[0]]+"\n\n"+report[loc[1]:]), "\n\n")
	last := -1
	for _, line := range strings.Split(report[loc[2]:loc[3]], "\n") {
		if m := reviewLineRe.FindStringSubmatch(line); m != nil {
			ref, _ := strconv.Atoi(m[1])
			verdict := strings.ToLower(m[2])
			a := reviewAnswer{Addressed: verdict == "addressed" || verdict == "fixed" || verdict == "done", Text: strings.TrimSpace(m[3])}
			out.Answers[ref], last = a, ref
			continue
		}
		// A line carrying on the previous answer.
		if s := strings.TrimSpace(line); s != "" && last >= 0 {
			a := out.Answers[last]
			a.Text = strings.TrimSpace(a.Text + " " + s)
			out.Answers[last] = a
		}
	}
	return out
}

// keepReviewReport keeps Claude's final message on the fix of a review owed
// a reply. A later run in the same pass (fixing tests, say) doesn't answer
// the review again, so its message only replaces one without answers.
func keepReviewReport(x *model.Todo, report string) {
	if x.PR == nil || x.PR.Reply == nil || strings.TrimSpace(report) == "" {
		return
	}
	if x.PR.Reply.Report == "" || reviewBlockRe.MatchString(report) || !reviewBlockRe.MatchString(x.PR.Reply.Report) {
		x.PR.Reply.Report = report
	}
}

// takeReply claims the reply owed on task todoID's pull request n, marking
// it posted first: a reply is posted at most once, even if Dash stops
// half way through.
func (m *MergeQueue) takeReply(todoID string, n int) *model.PRReply {
	var r *model.PRReply
	_, err := m.Store.UpdateTodo(todoID, func(x *model.Todo) error {
		if x.PR == nil || x.PR.Number != n || x.PR.Reply == nil {
			return errSkipUpdate
		}
		if x.PR.Reply.ReviewID > x.PR.RepliedID {
			c := *x.PR.Reply
			r = &c
			x.PR.RepliedID = c.ReviewID
		}
		x.PR.Reply = nil
		return nil
	})
	if err != nil {
		return nil
	}
	return r
}

// ghTimeout bounds the replies on GitHub after a fix, which no task waits
// on for long.
const ghTimeout = 2 * time.Minute

// replyToReview answers on GitHub the review whose fix was just pushed to
// task t's pull request, if one is owed.
func (m *MergeQueue) replyToReview(ctx context.Context, t model.Todo, p model.Project) {
	n := t.PR.Number
	r := m.takeReply(t.ID, n)
	if r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ghTimeout)
	defer cancel()
	warn := func(what string, err error) {
		slog.Warn("reply to review on GitHub: "+what, "todo", t.ID, "pr", n, "err", err)
		m.Log.Line(t, fmt.Sprintf("⚠ couldn't %s on pull request #%d: %s", what, n, brief(err)))
	}
	reply := parseReviewReply(r.Report)
	if err := m.Git.CommentPR(ctx, p.ID, p.Path, n, replyComment(r, reply)); err != nil {
		warn("post Claude's summary", err)
	}

	// Each line comment's thread: one reply with Claude's answers to its
	// comments, and resolved when it addressed them all.
	var roots []int64
	byThread := map[int64][]model.PRReplyItem{}
	for _, it := range r.Items {
		if it.Thread == 0 {
			continue
		}
		if _, seen := byThread[it.Thread]; !seen {
			roots = append(roots, it.Thread)
		}
		byThread[it.Thread] = append(byThread[it.Thread], it)
	}
	var threads map[int64]gitops.ReviewThread
	var threadsErr error
	replied, resolved, open := 0, 0, 0
	for _, root := range roots {
		body, all := threadReply(byThread[root], reply.Answers)
		if body == "" {
			continue // Claude didn't answer it
		}
		if err := m.Git.ReplyToReviewComment(ctx, p.ID, p.Path, n, root, body); err != nil {
			warn("reply to a comment", err)
			continue
		}
		replied++
		if !all {
			open++
			continue // pushed back on: left open for the reviewer
		}
		if threads == nil && threadsErr == nil {
			if threads, threadsErr = m.Git.ReviewThreads(ctx, p.ID, p.Path, n); threadsErr != nil {
				warn("list the review threads", threadsErr)
			}
		}
		th, ok := threads[root]
		if !ok || th.Resolved {
			continue
		}
		if err := m.Git.ResolveReviewThread(ctx, p.ID, p.Path, th.ID); err != nil {
			warn("resolve a review thread", err)
			continue
		}
		resolved++
	}

	if err := m.Git.RequestReview(ctx, p.ID, p.Path, n, r.Reviewers); err != nil {
		warn("ask "+strings.Join(r.Reviewers, ", ")+" to review again", err)
	}
	line := fmt.Sprintf("💬 replied to the review on pull request #%d", n)
	if replied > 0 {
		line += fmt.Sprintf(": answered %d comment thread(s), resolved %d, left %d open", replied, resolved, open)
	}
	if len(r.Reviewers) > 0 {
		line += "; asked " + strings.Join(r.Reviewers, ", ") + " to review again"
	}
	m.Log.Line(t, line)
}

// reviewFailed tells the reviewers on GitHub that the fix of their review
// failed, with why, if task todoID owes a reply: otherwise they would wait
// for a fix that isn't coming.
func (m *MergeQueue) reviewFailed(ctx context.Context, todoID, why string) {
	t, err := m.Store.GetTodo(todoID)
	if err != nil || !t.WaitsOnGitHub() || t.PR.Reply == nil {
		return
	}
	p, err := m.Store.GetProject(t.ProjectID)
	if err != nil {
		return
	}
	p = p.For(t)
	n := t.PR.Number
	r := m.takeReply(t.ID, n)
	if r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ghTimeout)
	defer cancel()
	body := fmt.Sprintf("Claude couldn't finish the changes asked for in the review%s, so nothing new was pushed:\n\n%s\n\n"+
		"Rework the task in Dash, or leave another review here, to have Claude try again.", by(r), quote(clip(why)))
	if err := m.Git.CommentPR(ctx, p.ID, p.Path, n, body); err != nil {
		slog.Warn("say on GitHub that a review fix failed", "todo", t.ID, "pr", n, "err", err)
		m.Log.Line(t, fmt.Sprintf("⚠ couldn't say on pull request #%d that the fix failed: %s", n, brief(err)))
		return
	}
	m.Log.Line(t, fmt.Sprintf("💬 said on pull request #%d that Claude's fix of the review failed", n))
}

func by(r *model.PRReply) string {
	var names []string
	for _, it := range r.Items {
		if it.Author != "" && !slices.Contains(names, it.Author) {
			names = append(names, it.Author)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return " by " + strings.Join(names, ", ")
}

// replyComment is the pull request comment with Claude's summary of its
// fix, and why it changed nothing for the items it disagreed with.
func replyComment(r *model.PRReply, reply reviewReply) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Claude's changes for the review%s**\n\n", by(r))
	if s := clip(reply.Summary); s != "" {
		b.WriteString(s + "\n")
	} else {
		b.WriteString("Claude finished its fix for the review.\n")
	}
	var not []string
	for _, it := range r.Items {
		a, ok := reply.Answers[it.Ref]
		if !ok || a.Addressed {
			continue
		}
		what := "The review"
		if it.Where != "" {
			what = "`" + it.Where + "`"
		}
		if it.Author != "" {
			what += " (" + it.Author + ")"
		}
		why := clip(a.Text)
		if why == "" {
			why = "no reason given"
		}
		not = append(not, "- "+what+": "+why)
	}
	if len(not) > 0 {
		b.WriteString("\n**Not changed**, where Claude disagreed:\n\n" + strings.Join(not, "\n") + "\n")
	}
	b.WriteString("\n<sub>Posted by Dash after pushing Claude's fix.</sub>")
	return b.String()
}

// threadReply is the reply to one thread with Claude's answers to its
// comments ("" when it answered none), and whether it addressed them all.
func threadReply(items []model.PRReplyItem, answers map[int]reviewAnswer) (body string, all bool) {
	var lines []string
	all = true
	for _, it := range items {
		a, ok := answers[it.Ref]
		if !ok {
			all = false
			continue
		}
		text := clip(a.Text)
		switch {
		case a.Addressed && text == "":
			lines = append(lines, "Addressed.")
		case a.Addressed:
			lines = append(lines, "Addressed: "+text)
		default:
			all = false
			if text == "" {
				text = "no reason given"
			}
			lines = append(lines, "Not changed: "+text)
		}
	}
	if len(lines) == 0 {
		return "", false
	}
	return "**Claude:** " + strings.Join(lines, "\n\n"), all
}
