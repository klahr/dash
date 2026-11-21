package store

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
)

// ---- Findings: reported issues, as draft tasks ----
//
// An issue someone reports (a scan, Claude's report_issue, by hand) becomes
// a draft task with the issue in its Finding, unless a task has it already.
// The check and the insert happen under the store's lock and are saved in
// one transaction, so two reports of one issue at once add one draft.

// ErrBadIssue is returned for an issue without a title or with an unknown
// type or severity.
var ErrBadIssue = errors.New("invalid issue")

// ErrBadMerge is returned when tasks can't be merged: fewer than two, not
// all drafts, or not all in one project.
var ErrBadMerge = errors.New("can't merge these tasks")

// FindingOutcome is what reporting one issue did: the task that has it now
// (zero for a dismissed one), and Dismissed, the record that kept it out.
type FindingOutcome struct {
	Todo      model.Todo
	Result    model.UpsertResult
	Issue     model.Issue // the issue as reported, normalized, with its fingerprint
	Dismissed *model.DismissedFinding
}

// ReportFinding adds the issue as a draft to its project's Board, unless
// it is known: a task that isn't merged has its fingerprint (that task is
// bumped: see Issue.AddReport), or it was dismissed (nothing changes). If a
// merged task had it, it came back: the new draft names that task.
func (s *Store) ReportFinding(in model.Issue) (FindingOutcome, error) {
	_, out, err := s.ReportFindings(in.ProjectID, "", []model.Issue{in})
	if err != nil {
		return FindingOutcome{}, err
	}
	return out[0], nil
}

// ReportFindings reports a batch of issues of one project (e.g. a scan's
// findings) with ReportFinding's rules, saving once, and counts the
// outcomes and reports each issue's outcome, in order. Issues without a
// Source get source. Two issues of the batch with one fingerprint give one
// draft. The batch is all or nothing: if one issue is invalid, none is
// added.
func (s *Store) ReportFindings(projectID, source string, in []model.Issue) (model.UpsertCounts, []FindingOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var counts model.UpsertCounts
	if _, ok := s.data.Projects[projectID]; !ok {
		return counts, nil, ErrNotFound
	}
	issues := make([]model.Issue, len(in))
	for n, is := range in {
		is.ProjectID = projectID
		if strings.TrimSpace(is.Source) == "" {
			is.Source = source
		}
		if err := is.Normalize(); err != nil {
			return counts, nil, fmt.Errorf("%w: %w", ErrBadIssue, err)
		}
		is.Fingerprint = strings.TrimSpace(is.Fingerprint)
		if is.Fingerprint == "" {
			is.Fingerprint = is.ComputeFingerprint()
		}
		issues[n] = is
	}
	idx := s.findingIndexLocked(projectID)
	changed := map[string]*model.Todo{}
	var order []string // changed ids, in the order they first changed
	out := make([]FindingOutcome, 0, len(issues))
	now := time.Now()
	for _, is := range issues {
		o := s.reportLocked(is, idx, changed, now)
		if o.Result == model.UpsertNew || o.Result == model.UpsertReopened || o.Result == model.UpsertUpdated {
			if !slices.Contains(order, o.Todo.ID) {
				order = append(order, o.Todo.ID)
			}
		}
		counts.Add(o.Result)
		out = append(out, o)
	}
	if len(order) == 0 {
		return counts, out, nil
	}
	for _, id := range order {
		s.data.Todos[id] = changed[id]
	}
	err := s.writeLocked(func(tx *sql.Tx) error {
		for _, id := range order {
			if err := putTodo(tx, changed[id]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return counts, nil, err
	}
	// Each outcome shows its task as the batch left it.
	for n := range out {
		if t, ok := changed[out[n].Todo.ID]; ok {
			out[n].Todo = clone(*t)
		}
	}
	for _, id := range order {
		t := changed[id]
		s.emit(model.EvTodoUpdated, t.ProjectID, t.ID, *t)
	}
	return counts, out, nil
}

// findingIndex finds a project's tasks by the fingerprints they have.
type findingIndex struct {
	open   map[string]string // fingerprint → the task that has it, not merged
	merged map[string]string // fingerprint → the latest merged task that had it
}

func (s *Store) findingIndexLocked(projectID string) findingIndex {
	idx := findingIndex{open: map[string]string{}, merged: map[string]string{}}
	mergedAt := map[string]time.Time{}
	openAt := map[string]time.Time{}
	for id, t := range s.data.Todos {
		if t.ProjectID != projectID || t.Finding == nil {
			continue
		}
		for _, fp := range t.Finding.Fingerprints() {
			if t.Status == model.StatusMerged {
				if at, ok := mergedAt[fp]; !ok || t.UpdatedAt.After(at) {
					idx.merged[fp], mergedAt[fp] = id, t.UpdatedAt
				}
				continue
			}
			// Should two open tasks have it, the older one gets the reports.
			if at, ok := openAt[fp]; !ok || t.CreatedAt.Before(at) {
				idx.open[fp], openAt[fp] = id, t.CreatedAt
			}
		}
	}
	return idx
}

// reportLocked is ReportFindings for one issue, normalized and with its
// fingerprint: it changes the tasks in changed (copies, by id; taken from
// the store the first time), and idx with them.
func (s *Store) reportLocked(is model.Issue, idx findingIndex, changed map[string]*model.Todo, now time.Time) FindingOutcome {
	get := func(id string) *model.Todo {
		if t, ok := changed[id]; ok {
			return t
		}
		cp := clone(*s.data.Todos[id])
		return &cp
	}
	if id, ok := idx.open[is.Fingerprint]; ok {
		t := get(id)
		before := clone(*t)
		n := t.Finding.Index(is.Fingerprint)
		cur := &t.Finding.Issues[n]
		// A report refreshes what it knows; blank fields keep what was there.
		cur.Title, cur.Type, cur.Severity = is.Title, is.Type, is.Severity
		if is.File != "" {
			cur.File, cur.Line = is.File, is.Line
		}
		if is.Details != "" {
			cur.Details = is.Details
		}
		if is.Fix != "" {
			cur.Fix = is.Fix
		}
		if is.Rule != "" {
			cur.Rule = is.Rule
		}
		if is.ScanKind != "" {
			cur.ScanKind = is.ScanKind
		}
		cur.AddReport(is.Source)
		t.Tags = model.WithFindingTags(t.Tags, t.Finding)
		if t.Status == model.StatusDraft {
			s.rewriteLocked(t, before.Finding)
		}
		if todoEqual(*t, before) {
			return FindingOutcome{Todo: before, Result: model.UpsertKnown, Issue: is}
		}
		t.UpdatedAt = now
		changed[id] = t
		return FindingOutcome{Todo: clone(*t), Result: model.UpsertUpdated, Issue: is}
	}
	if d, ok := s.data.Dismissed[issueKey(is.ProjectID, is.Fingerprint)]; ok {
		cp := *d
		return FindingOutcome{Result: model.UpsertDismissed, Issue: is, Dismissed: &cp}
	}
	first := is
	first.Reports, first.LastSource, first.Sources = 1, "", nil
	first.AddReport(first.Source)
	t := &model.Todo{
		ID: NewID(), ProjectID: is.ProjectID, Status: model.StatusDraft,
		Finding:   &model.Finding{Issues: []model.Issue{first}, Earlier: idx.merged[is.Fingerprint]},
		CreatedAt: now, UpdatedAt: now,
	}
	t.Tags = model.WithFindingTags(nil, t.Finding)
	s.rewriteLocked(t, nil)
	changed[t.ID] = t
	idx.open[is.Fingerprint] = t.ID
	res := model.UpsertNew
	if t.Finding.Earlier != "" {
		res = model.UpsertReopened
	}
	return FindingOutcome{Todo: clone(*t), Result: res, Issue: is}
}

// rewriteLocked writes t's title and prompt from its Finding again, where
// they are still what Dash wrote from the issues it had before (prev; nil
// for a new task, whose are written).
func (s *Store) rewriteLocked(t *model.Todo, prev *model.Finding) {
	title, prompt := s.findingTaskLocked(t.Finding)
	if prev == nil || t.Finding.PromptSum != "" && t.Finding.PromptSum == model.PromptSum(t.Prompt) {
		t.Prompt = prompt
		t.Finding.PromptSum = model.PromptSum(prompt)
	}
	if prev == nil || t.Title == s.findingTitleLocked(prev) {
		t.Title = title
	}
}

func (s *Store) findingTitleLocked(f *model.Finding) string {
	title, _ := model.TaskFromIssues(f.Issues, nil)
	return title
}

// findingTaskLocked is the title and prompt of a task that fixes f's
// issues: model.TaskFromIssues, naming the tasks that reported them, and
// the merged task that had fixed them before, if they came back.
func (s *Store) findingTaskLocked(f *model.Finding) (title, prompt string) {
	title, prompt = model.TaskFromIssues(f.Issues, reporterIn(s.data.Todos))
	if f.Earlier != "" {
		what := "task " + f.Earlier
		if t, ok := s.data.Todos[f.Earlier]; ok && t.Title != "" {
			what = fmt.Sprintf("task %s, %q", f.Earlier, t.Title)
		}
		prompt += "\n\nThis came back: " + what + " fixed it before and was merged. Look at what that task changed, and why it no longer holds."
	}
	return title, prompt
}

// reporterIn names an issue's source for a task prompt: a task that
// reported it by its id and title, rather than "agent:<id>".
func reporterIn(todos map[string]*model.Todo) func(string) string {
	return func(source string) string {
		switch {
		case source == "agent:chat":
			return "the chat"
		case strings.HasPrefix(source, "agent:"):
			id := strings.TrimPrefix(source, "agent:")
			if t, ok := todos[id]; ok && t.Title != "" {
				return fmt.Sprintf("task %s, %q", id, t.Title)
			}
			return "task " + id
		}
		return ""
	}
}

// todoEqual reports whether a report changed nothing a reader sees in t.
func todoEqual(a, b model.Todo) bool {
	if a.Title != b.Title || a.Prompt != b.Prompt || !slices.Equal(a.Tags, b.Tags) {
		return false
	}
	fa, fb := a.Finding, b.Finding
	if (fa == nil) != (fb == nil) {
		return false
	}
	if fa == nil {
		return true
	}
	return fa.Earlier == fb.Earlier && fa.PromptSum == fb.PromptSum && slices.EqualFunc(fa.Issues, fb.Issues, func(x, y model.Issue) bool {
		return x.Title == y.Title && x.Type == y.Type && x.Severity == y.Severity && x.File == y.File && x.Line == y.Line &&
			x.Details == y.Details && x.Fix == y.Fix && x.Rule == y.Rule && x.Source == y.Source && x.Reports == y.Reports &&
			x.LastSource == y.LastSource && slices.Equal(x.Sources, y.Sources) && x.ScanKind == y.ScanKind && x.Fingerprint == y.Fingerprint
	})
}

// issueKey is the key of a project's fingerprint in snapshot.Dismissed.
func issueKey(projectID, fingerprint string) string {
	return projectID + "\x00" + fingerprint
}

// dismissLocked records the issues of t, which is being deleted, as
// dismissed, unless it was merged: its issues were fixed then, not
// dismissed. It returns the records to save.
func (s *Store) dismissLocked(t *model.Todo, by int64) []*model.DismissedFinding {
	if t.Finding == nil || t.Status == model.StatusMerged {
		return nil
	}
	if s.data.Dismissed == nil {
		s.data.Dismissed = map[string]*model.DismissedFinding{}
	}
	var out []*model.DismissedFinding
	now := time.Now()
	for _, is := range t.Finding.Issues {
		if is.Fingerprint == "" {
			continue
		}
		d := model.Dismissal(*t, is, "", by, now)
		s.data.Dismissed[issueKey(t.ProjectID, is.Fingerprint)] = &d
		out = append(out, &d)
	}
	return out
}

// ListDismissed is a project's dismissed issues ("" for every project's),
// latest first.
func (s *Store) ListDismissed(projectID string) []model.DismissedFinding {
	return s.listDismissed(func(p string) bool { return projectID == "" || p == projectID })
}

func (s *Store) listDismissed(keep func(projectID string) bool) []model.DismissedFinding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []model.DismissedFinding{}
	for _, d := range s.data.Dismissed {
		if keep(d.ProjectID) {
			out = append(out, *d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].Fingerprint < out[j].Fingerprint
	})
	return out
}

// Undismiss forgets that a project's issue was dismissed: the next report
// of it adds a draft again.
func (s *Store) Undismiss(projectID, fingerprint string) (model.DismissedFinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := issueKey(projectID, fingerprint)
	d, ok := s.data.Dismissed[k]
	if !ok {
		return model.DismissedFinding{}, ErrNotFound
	}
	delete(s.data.Dismissed, k)
	err := s.writeLocked(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM dismissed_findings WHERE project_id = ? AND fingerprint = ?", projectID, fingerprint)
		return err
	})
	if err != nil {
		return *d, err
	}
	s.emit(model.EvDismissedUpdated, projectID, "", map[string]string{"fingerprint": fingerprint})
	return *d, nil
}

// MergeTodos combines drafts of one project into the first of them: it
// gets all of their issues (so later reports of any of them land on it),
// tags, attachments and dependencies, the most urgent priority, and their
// report counts added up. Where every draft's prompt is still the one Dash
// wrote from its issues, the merged prompt is written from all of them;
// otherwise the prompts are put one after another. The other drafts are
// deleted; their issues aren't dismissed.
func (s *Store) MergeTodos(ids []string) (model.Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids = cleanIDs(ids)
	if len(ids) < 2 {
		return model.Todo{}, fmt.Errorf("%w: choose at least two drafts", ErrBadMerge)
	}
	var parts []*model.Todo
	for _, id := range ids {
		t, ok := s.data.Todos[id]
		if !ok {
			return model.Todo{}, ErrNotFound
		}
		if t.Status != model.StatusDraft {
			return model.Todo{}, fmt.Errorf("%w: %q is %s; only drafts can be merged", ErrBadMerge, t.Title, t.Status)
		}
		if len(parts) > 0 && t.ProjectID != parts[0].ProjectID {
			return model.Todo{}, fmt.Errorf("%w: they are in different projects", ErrBadMerge)
		}
		if t.Reviewing {
			return model.Todo{}, ErrReviewing
		}
		parts = append(parts, t)
	}
	into := clone(*parts[0])
	gone := map[string]bool{}
	generated := true
	var prompts []string
	for _, t := range parts {
		generated = generated && t.Finding != nil && t.Finding.PromptSum != "" && t.Finding.PromptSum == model.PromptSum(t.Prompt)
		prompts = append(prompts, t.Prompt)
	}
	for _, t := range parts[1:] {
		gone[t.ID] = true
		into.Tags = append(into.Tags, t.Tags...)
		into.Attachments = append(into.Attachments, t.Attachments...)
		into.DependsOn = append(into.DependsOn, t.DependsOn...)
		if t.Priority != nil && (into.Priority == nil || *t.Priority < *into.Priority) {
			p := *t.Priority
			into.Priority = &p
		}
		if t.Finding != nil {
			if into.Finding == nil {
				into.Finding = &model.Finding{}
			}
			for _, is := range t.Finding.Issues {
				if n := into.Finding.Index(is.Fingerprint); n >= 0 {
					into.Finding.Issues[n].Reports = into.Finding.Issues[n].ReportCount() + is.ReportCount()
					continue
				}
				into.Finding.Issues = append(into.Finding.Issues, is)
			}
			if into.Finding.Earlier == "" {
				into.Finding.Earlier = t.Finding.Earlier
			}
			into.Finding.LegacyIDs = append(into.Finding.LegacyIDs, t.Finding.LegacyIDs...)
		}
	}
	into.DependsOn = slices.DeleteFunc(cleanIDs(into.DependsOn), func(id string) bool { return gone[id] || id == into.ID })
	if into.BuildsOn != "" && gone[into.BuildsOn] {
		into.BuildsOn = ""
	}
	if tags, err := model.CleanTags(into.Tags); err == nil {
		into.Tags = tags
	} else {
		into.Tags, _ = model.CleanTags(into.Tags[:model.MaxCardTags])
	}
	if into.Finding != nil {
		into.Tags = model.WithFindingTags(into.Tags, into.Finding)
	}
	if generated {
		into.Title, into.Prompt = s.findingTaskLocked(into.Finding)
		into.Finding.PromptSum = model.PromptSum(into.Prompt)
	} else {
		var b strings.Builder
		for n, t := range parts {
			if n > 0 {
				b.WriteString("\n\n---\n\n")
			}
			if t.Title != "" {
				fmt.Fprintf(&b, "## %s\n\n", t.Title)
			}
			b.WriteString(strings.TrimSpace(prompts[n]))
		}
		into.Prompt = b.String()
		if into.Finding != nil {
			into.Finding.PromptSum = ""
		}
	}
	into.UpdatedAt = time.Now()
	// Tasks waiting for a merged-away draft wait for the merged one.
	var others []*model.Todo
	for oid, o := range s.data.Todos {
		if gone[oid] || oid == into.ID {
			continue
		}
		touched := false
		cp := clone(*o)
		for n, d := range cp.DependsOn {
			if gone[d] {
				cp.DependsOn[n], touched = into.ID, true
			}
		}
		if gone[cp.BuildsOn] {
			cp.BuildsOn, touched = into.ID, true
		}
		if touched {
			cp.DependsOn = cleanIDs(cp.DependsOn)
			cp.UpdatedAt = into.UpdatedAt
			others = append(others, &cp)
		}
	}
	for id := range gone {
		delete(s.data.Todos, id)
		delete(s.data.TodoChats, id)
	}
	s.data.Todos[into.ID] = &into
	for _, o := range others {
		s.data.Todos[o.ID] = o
	}
	err := s.writeLocked(func(tx *sql.Tx) error {
		for id := range gone {
			if err := deleteRow(tx, "todos", id); err != nil {
				return err
			}
			if err := setChat(tx, chatTodo, id, 0, nil); err != nil {
				return err
			}
		}
		for _, o := range others {
			if err := putTodo(tx, o); err != nil {
				return err
			}
		}
		return putTodo(tx, &into)
	})
	if err != nil {
		return into, err
	}
	for _, t := range parts[1:] {
		s.emit(model.EvTodoDeleted, t.ProjectID, t.ID, nil)
	}
	for _, o := range others {
		s.emit(model.EvTodoUpdated, o.ProjectID, o.ID, *o)
	}
	s.emit(model.EvTodoUpdated, into.ProjectID, into.ID, into)
	return into, nil
}

func putDismissed(tx *sql.Tx, d *model.DismissedFinding) error {
	return put(tx, "INSERT INTO dismissed_findings (project_id, fingerprint, org_id, data) VALUES (?, ?, "+projectOrg+", ?)"+
		" ON CONFLICT (project_id, fingerprint) DO UPDATE SET org_id = excluded.org_id, data = excluded.data",
		d, d.ProjectID, d.Fingerprint, d.ProjectID)
}
