package queue

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// openPR is step 3 in "pr" mode: push the todo's branch and open (or update)
// its pull request. The todo stays in Merging until PollPRs sees GitHub
// merge it.
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
// back to Claude like a local merge conflict.
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
		if t.Status != model.StatusMerging || st.HeadRefOid == "" || st.HeadRefOid == t.PR.HandledSHA {
			return
		}
		if st.Mergeable == "CONFLICTING" {
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
				return // act once every check has finished
			}
			if c.Failed() {
				failed = append(failed, c)
			}
		}
		if len(failed) == 0 {
			if p.HubProjectID != "" {
				m.mergePR(ctx, t, p, st)
			}
			return
		}
		m.handled(t, st.HeadRefOid, fmt.Sprintf("✖ checks fail on pull request #%d", n))
		t, _ = m.Store.GetTodo(t.ID)
		_ = m.sendBack(t, p, m.checksNote(ctx, p, n, failed), false, fmt.Sprintf("checks fail on pull request #%d", n))
	}
}

// mergePR merges the pull request of a hub's task on this runner once
// GitHub finds it mergeable and its checks pass: approving it on the hub was
// the review, and nobody here would merge it otherwise. When GitHub refuses
// (e.g. a required review), it stays open to be merged there.
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

func (m *MergeQueue) checksNote(ctx context.Context, p model.Project, n int, failed []gitops.PRCheck) string {
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
