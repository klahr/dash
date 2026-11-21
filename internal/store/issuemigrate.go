package store

import (
	"database/sql"
	"log/slog"
	"slices"
	"sort"
	"time"

	"github.com/klahr/dash/internal/model"
)

// legacyIssue is an issue as Dash kept it before issues became draft tasks:
// in the issues table (or a state.json), with a status and the tasks made
// for it.
type legacyIssue struct {
	model.Issue
	ID            string    `json:"id"`
	Status        string    `json:"status"` // open, task_created, resolved or dismissed
	TodoIDs       []string  `json:"todo_ids,omitempty"`
	DismissReason string    `json:"dismiss_reason,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// migrateIssues turns the issues table into tasks (issuesToTasks) and
// drops it, in one transaction. A database without the table (a new one,
// or one migrated before) is left alone.
func migrateIssues(db *sql.DB) error {
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'issues'").Scan(&n); err != nil || n == 0 {
		return err
	}
	issues := map[string]*legacyIssue{}
	if err := loadRows(db, "SELECT data FROM issues", func(is *legacyIssue, _ int64) { issues[is.ID] = is }); err != nil {
		return err
	}
	todos := map[string]*model.Todo{}
	if err := loadRows(db, "SELECT data FROM todos", func(t *model.Todo, _ int64) { todos[t.ID] = t }); err != nil {
		return err
	}
	changed, dismissed := issuesToTasks(issues, todos)
	err := inTx(db, func(tx *sql.Tx) error {
		for _, t := range changed {
			if err := putTodo(tx, t); err != nil {
				return err
			}
		}
		for _, d := range dismissed {
			if err := putDismissed(tx, d); err != nil {
				return err
			}
		}
		_, err := tx.Exec("DROP TABLE issues")
		return err
	})
	if err != nil {
		return err
	}
	if len(issues) > 0 {
		slog.Info("turned issues into draft tasks", "issues", len(issues), "tasks", len(changed), "dismissed", len(dismissed))
	}
	return nil
}

// issuesToTasks turns issues kept the old way into tasks, adding to and
// changing todos (by id), and returns the tasks it added or changed and
// the dismissed issues to keep:
//
//   - an open issue becomes a draft with its title, prompt, tags and
//     Finding, keeping the issue's id (old links to it open the draft);
//   - an issue a task was created for (task_created, or resolved: that
//     task was merged) goes in those tasks' Findings, so reports find them;
//     one whose tasks are gone is open again, or dropped if it was resolved;
//   - a dismissed issue is kept as a dismissed fingerprint.
//
// Issues of one project with one fingerprint end up in one task.
func issuesToTasks(issues map[string]*legacyIssue, todos map[string]*model.Todo) ([]*model.Todo, []*model.DismissedFinding) {
	list := make([]*legacyIssue, 0, len(issues))
	for _, is := range issues {
		list = append(list, is)
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].CreatedAt.Before(list[j].CreatedAt)
		}
		return list[i].ID < list[j].ID
	})
	changed := map[string]*model.Todo{}
	var order []string
	touch := func(t *model.Todo) {
		if _, ok := changed[t.ID]; !ok {
			order = append(order, t.ID)
		}
		changed[t.ID] = t
	}
	drafts := map[string]*model.Todo{} // issueKey → the draft made for it
	doubled := map[*model.Todo]bool{}  // drafts that got one issue twice
	var dismissed []*model.DismissedFinding
	seenDismissed := map[string]bool{}
	reporter := reporterIn(todos)
	for _, li := range list {
		is := li.Issue
		if err := is.Normalize(); err != nil {
			slog.Warn("issue left out: it isn't valid", "id", li.ID, "err", err)
			continue
		}
		if is.Fingerprint == "" {
			is.Fingerprint = is.ComputeFingerprint()
		}
		is.Reports = is.ReportCount()
		is.Sources = nil
		is.AddReport("") // notes its first source
		if is.LastSource != "" && !slices.Contains(is.Sources, is.LastSource) {
			is.Sources = append(is.Sources, is.LastSource)
		}
		key := issueKey(is.ProjectID, is.Fingerprint)
		status := li.Status
		if status == "task_created" || status == "resolved" {
			var linked []*model.Todo
			for _, id := range li.TodoIDs {
				if t, ok := todos[id]; ok && t.ProjectID == is.ProjectID {
					linked = append(linked, t)
				}
			}
			for _, t := range linked {
				if t.Finding == nil {
					t.Finding = &model.Finding{} // its prompt was written by the user, or for several issues
				}
				if t.Finding.Index(is.Fingerprint) < 0 {
					t.Finding.Issues = append(t.Finding.Issues, is)
				}
				if !slices.Contains(t.Finding.LegacyIDs, li.ID) {
					t.Finding.LegacyIDs = append(t.Finding.LegacyIDs, li.ID)
				}
				t.Tags = model.WithFindingTags(t.Tags, t.Finding)
				touch(t)
			}
			if len(linked) > 0 {
				continue
			}
			if status == "resolved" {
				continue // fixed, by a task that is gone
			}
			status = "open"
		}
		switch status {
		case "dismissed":
			if seenDismissed[key] {
				continue
			}
			seenDismissed[key] = true
			d := model.DismissedFinding{
				ProjectID: is.ProjectID, Fingerprint: is.Fingerprint, Title: is.Title, Type: is.Type, Severity: is.Severity,
				File: is.File, Rule: is.Rule, Source: is.Source, ScanKind: is.ScanKind, Reason: li.DismissReason,
				At: firstTime(li.UpdatedAt, li.CreatedAt),
			}
			dismissed = append(dismissed, &d)
		case "open":
			if t, ok := drafts[key]; ok { // the same issue twice: one draft
				t.Finding.Issues[0].Reports += is.ReportCount()
				t.Finding.LegacyIDs = append(t.Finding.LegacyIDs, li.ID)
				doubled[t] = true
				continue
			}
			id := li.ID
			if _, taken := todos[id]; taken || id == "" {
				id = NewID()
			}
			t := &model.Todo{
				ID: id, ProjectID: is.ProjectID, Status: model.StatusDraft,
				Finding:   &model.Finding{Issues: []model.Issue{is}},
				CreatedAt: firstTime(li.CreatedAt, time.Now()), UpdatedAt: firstTime(li.UpdatedAt, li.CreatedAt, time.Now()),
			}
			if id != li.ID {
				t.Finding.LegacyIDs = []string{li.ID}
			}
			t.Title, t.Prompt = model.TaskFromIssues(t.Finding.Issues, reporter)
			t.Finding.PromptSum = model.PromptSum(t.Prompt)
			t.Tags = model.WithFindingTags(nil, t.Finding)
			todos[id] = t
			drafts[key] = t
			touch(t)
		}
	}
	// A draft that got the same issue twice has its prompt written again.
	for t := range doubled {
		t.Title, t.Prompt = model.TaskFromIssues(t.Finding.Issues, reporter)
		t.Finding.PromptSum = model.PromptSum(t.Prompt)
	}
	out := make([]*model.Todo, len(order))
	for n, id := range order {
		out[n] = changed[id]
	}
	return out, dismissed
}

// firstTime is the first of ts that isn't zero.
func firstTime(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}
