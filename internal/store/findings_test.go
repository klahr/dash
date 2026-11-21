package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/klahr/dash/internal/model"
)

func findingStore(t *testing.T) (*Store, model.Project, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dash.db")
	s := openTest(t, path)
	p, err := s.CreateProject(model.Project{Name: "p", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return s, p, path
}

func mustReport(t *testing.T, s *Store, in model.Issue, want model.UpsertResult) model.Todo {
	t.Helper()
	o, err := s.ReportFinding(in)
	if err != nil {
		t.Fatal(err)
	}
	if o.Result != want {
		t.Fatalf("report %q: %s, want %s", in.Title, o.Result, want)
	}
	return o.Todo
}

// findingDrafts are the project's tasks that have issues.
func findingDrafts(s *Store, pid string) []model.Todo {
	var out []model.Todo
	for _, t := range s.ListTodos(pid) {
		if t.Finding != nil {
			out = append(out, t)
		}
	}
	return out
}

func TestReportFindingAddsATaggedDraft(t *testing.T) {
	t.Parallel()
	s, p, path := findingStore(t)
	d := mustReport(t, s, model.Issue{ProjectID: p.ID, Title: "SQL injection", Type: model.IssueVuln, Severity: model.SevHigh,
		File: "./db/q.go", Line: 42, Details: "built with Sprintf", Fix: "use placeholders", Rule: "sqli", Source: "scan:security"}, model.UpsertNew)
	if d.Status != model.StatusDraft || d.Title != "Fix: SQL injection" {
		t.Fatalf("draft: %+v", d)
	}
	for _, want := range []string{"SQL injection", "db/q.go:42", "built with Sprintf", "use placeholders", "sqli", "high"} {
		if !strings.Contains(d.Prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, d.Prompt)
		}
	}
	if !slices.Equal(d.Tags, []string{"security", "high", "scan:security"}) {
		t.Errorf("tags %q", d.Tags)
	}
	is := d.Finding.Issues[0]
	if is.Fingerprint == "" || is.File != "db/q.go" || is.Line != 42 || is.ReportCount() != 1 || d.Finding.PromptSum != model.PromptSum(d.Prompt) {
		t.Errorf("finding: %+v", d.Finding)
	}
	// It is saved: a reopened store has it.
	s.Close()
	s2 := openTest(t, path)
	got, err := s2.GetTodo(d.ID)
	if err != nil || got.Finding == nil || got.Finding.Issues[0].Fingerprint != is.Fingerprint || !slices.Equal(got.Tags, d.Tags) {
		t.Fatalf("reloaded: %+v, %v", got, err)
	}
}

func TestSameFindingTwiceGivesOneDraft(t *testing.T) {
	t.Parallel()
	s, p, _ := findingStore(t)
	in := model.Issue{ProjectID: p.ID, Title: "Nil deref in handler", Type: model.IssueBug, File: "api/h.go", Line: 10, Source: "agent:t1"}
	a := mustReport(t, s, in, model.UpsertNew)
	// Reworded, moved, by another reporter: the same issue, the same draft.
	in.Title, in.Line, in.Source, in.Details = " nil DEREF in  handler", 14, "agent:t2", "panics on empty body"
	b := mustReport(t, s, in, model.UpsertUpdated)
	if b.ID != a.ID || b.Finding.Reports() != 2 || b.Finding.Issues[0].Line != 14 {
		t.Fatalf("bumped: %+v", b.Finding)
	}
	if !strings.Contains(b.Prompt, "panics on empty body") || !strings.Contains(b.Prompt, "Reported 2 times") {
		t.Errorf("the draft's prompt wasn't written again:\n%s", b.Prompt)
	}
	// The same reporter again doesn't count.
	if c := mustReport(t, s, in, model.UpsertKnown); c.Finding.Reports() != 2 {
		t.Fatalf("same reporter counted: %d", c.Finding.Reports())
	}
	if n := len(findingDrafts(s, p.ID)); n != 1 {
		t.Fatalf("%d drafts, want 1", n)
	}
	// A prompt the user edited stays theirs; the report still counts.
	if _, err := s.UpdateTodo(a.ID, func(t *model.Todo) error { t.Prompt = "my words"; return nil }); err != nil {
		t.Fatal(err)
	}
	in.Source, in.Details = "agent:t3", "new details"
	d := mustReport(t, s, in, model.UpsertUpdated)
	if d.Prompt != "my words" || d.Finding.Reports() != 3 || d.Finding.Issues[0].Details != "new details" {
		t.Fatalf("edited prompt: %q, %+v", d.Prompt, d.Finding)
	}
}

// advance moves task id through statuses, in order.
func advance(t *testing.T, s *Store, id string, statuses ...model.TodoStatus) {
	t.Helper()
	for _, st := range statuses {
		if _, err := s.SetStatus(id, st, ""); err != nil {
			t.Fatal(st, err)
		}
	}
}

func TestFindingBumpsTasksPastDraft(t *testing.T) {
	t.Parallel()
	s, p, _ := findingStore(t)
	for _, path := range [][]model.TodoStatus{
		{model.StatusQueued},
		{model.StatusQueued, model.StatusInProgress},
		{model.StatusQueued, model.StatusInProgress, model.StatusReview},
		{model.StatusQueued, model.StatusInProgress, model.StatusFailed},
	} {
		in := model.Issue{ProjectID: p.ID, Title: "Leak", Type: model.IssueBug, Source: "agent:a"}
		d := mustReport(t, s, in, model.UpsertNew)
		advance(t, s, d.ID, path...)
		st := path[len(path)-1]
		in.Source = "agent:b"
		b := mustReport(t, s, in, model.UpsertUpdated)
		if b.ID != d.ID || b.Status != st || b.Finding.Reports() != 2 || b.Prompt != d.Prompt {
			t.Fatalf("%s: bumped %+v", st, b)
		}
		if st == model.StatusInProgress {
			advance(t, s, d.ID, model.StatusFailed) // a running task can't be deleted
		}
		if _, err := s.DeleteTodo(d.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Undismiss(p.ID, b.Finding.Issues[0].Fingerprint); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScanBatchWithDuplicatesGivesOneDraftEach(t *testing.T) {
	t.Parallel()
	s, p, path := findingStore(t)
	batch := []model.Issue{
		{Title: "XSS in search", Type: model.IssueVuln, Severity: model.SevHigh, File: "web/s.js", Rule: "xss:search"},
		{Title: "Unused helper", Type: model.IssueOther, Severity: model.SevLow, File: "a.go", Rule: "dead:helper"},
		{Title: "XSS in search page", Type: model.IssueVuln, Severity: model.SevHigh, File: "./web/s.js", Line: 9, Rule: "xss:search"},
	}
	counts, outs, err := s.ReportFindings(p.ID, "scan:security", batch)
	if err != nil {
		t.Fatal(err)
	}
	if counts.New != 2 || counts.Known+counts.Updated != 1 || len(outs) != 3 || outs[0].Todo.ID != outs[2].Todo.ID {
		t.Fatalf("counts %+v, outcomes %+v", counts, outs)
	}
	ds := findingDrafts(s, p.ID)
	if len(ds) != 2 {
		t.Fatalf("%d drafts, want 2", len(ds))
	}
	// Run again: everything is there already.
	counts, _, err = s.ReportFindings(p.ID, "scan:security", batch)
	if err != nil || counts.New != 0 || counts.Known+counts.Updated != 3 || len(findingDrafts(s, p.ID)) != 2 {
		t.Fatalf("second run: %+v, %v", counts, err)
	}
	// A bad finding fails the batch, which adds nothing.
	if _, _, err := s.ReportFindings(p.ID, "scan:security", []model.Issue{{Title: "fine"}, {Title: ""}}); !errors.Is(err, ErrBadIssue) {
		t.Fatalf("bad batch: %v", err)
	}
	if n := len(findingDrafts(s, p.ID)); n != 2 {
		t.Fatalf("a failed batch left drafts: %d", n)
	}
	s.Close()
	if n := len(findingDrafts(openTest(t, path), p.ID)); n != 2 {
		t.Fatalf("reloaded: %d drafts", n)
	}
}

func TestDismissedFindingsStayOut(t *testing.T) {
	t.Parallel()
	s, p, path := findingStore(t)
	in := model.Issue{ProjectID: p.ID, Title: "Weak hash", Type: model.IssueVuln, File: "auth.go", Rule: "md5", Source: "scan:security"}
	d := mustReport(t, s, in, model.UpsertNew)
	if _, err := s.deleteTodo(d.ID, 7); err != nil { // as user 7 (OrgStore.DeleteTodo passes its user)
		t.Fatal(err)
	}
	ds := s.ListDismissed(p.ID)
	if len(ds) != 1 || ds[0].Fingerprint != d.Finding.Issues[0].Fingerprint || ds[0].Title != "Weak hash" || ds[0].TodoID != d.ID || ds[0].By != 7 {
		t.Fatalf("dismissed: %+v", ds)
	}
	o, err := s.ReportFinding(in)
	if err != nil || o.Result != model.UpsertDismissed || o.Dismissed == nil || o.Todo.ID != "" {
		t.Fatalf("reported again: %+v, %v", o, err)
	}
	counts, _, _ := s.ReportFindings(p.ID, "scan:security", []model.Issue{in})
	if counts.Dismissed != 1 || len(findingDrafts(s, p.ID)) != 0 {
		t.Fatalf("scan brought it back: %+v", counts)
	}
	// Dismissals are kept across restarts, and can be undone.
	s.Close()
	s = openTest(t, path)
	if len(s.ListDismissed(p.ID)) != 1 {
		t.Fatal("dismissal not saved")
	}
	if _, err := s.Undismiss(p.ID, d.Finding.Issues[0].Fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Undismiss(p.ID, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("undismiss unknown: %v", err)
	}
	mustReport(t, s, in, model.UpsertNew)
	s.Close()
	if len(openTest(t, path).ListDismissed(p.ID)) != 0 {
		t.Fatal("undo not saved")
	}
}

func TestDeletingATaskWithoutIssuesDismissesNothing(t *testing.T) {
	t.Parallel()
	s, p, _ := findingStore(t)
	td, _ := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: "plain"})
	if _, err := s.DeleteTodo(td.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.ListDismissed("")) != 0 {
		t.Fatal("a plain task's delete dismissed something")
	}
}

func TestFindingAfterMergeGivesNewDraft(t *testing.T) {
	t.Parallel()
	s, p, _ := findingStore(t)
	in := model.Issue{ProjectID: p.ID, Title: "Race in cache", Type: model.IssueBug, File: "cache.go", Source: "agent:a"}
	d := mustReport(t, s, in, model.UpsertNew)
	advance(t, s, d.ID, model.StatusQueued, model.StatusInProgress, model.StatusReview, model.StatusMerging, model.StatusMerged)
	b := mustReport(t, s, in, model.UpsertReopened)
	if b.ID == d.ID || b.Status != model.StatusDraft || b.Finding.Earlier != d.ID {
		t.Fatalf("came back: %+v", b)
	}
	if !strings.Contains(b.Prompt, d.ID) || !strings.Contains(b.Prompt, "came back") {
		t.Errorf("the new draft doesn't name the merged task:\n%s", b.Prompt)
	}
	// The merged task keeps its issues; deleting it dismisses nothing.
	if _, err := s.DeleteTodo(d.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.ListDismissed(p.ID)) != 0 {
		t.Fatal("deleting a merged task dismissed its issues")
	}
}

func TestConcurrentReportsGiveOneDraft(t *testing.T) {
	t.Parallel()
	s, p, path := findingStore(t)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			src := "agent:t" + string(rune('a'+i))
			if _, err := s.ReportFinding(model.Issue{ProjectID: p.ID, Title: "Flaky lock", Type: model.IssueBug, File: "l.go", Source: src}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	ds := findingDrafts(s, p.ID)
	if len(ds) != 1 || ds[0].Finding.Reports() != 20 {
		t.Fatalf("%d drafts (want 1), reports %d", len(ds), ds[0].Finding.Reports())
	}
	s.Close()
	if ds := findingDrafts(openTest(t, path), p.ID); len(ds) != 1 || ds[0].Finding.Reports() != 20 {
		t.Fatalf("reloaded: %+v", ds)
	}
}

func TestMergeTodos(t *testing.T) {
	t.Parallel()
	s, p, _ := findingStore(t)
	a := mustReport(t, s, model.Issue{ProjectID: p.ID, Title: "Unchecked error in a", Type: model.IssueBug, Severity: model.SevLow, File: "a.go", Source: "scan:lint"}, model.UpsertNew)
	a = mustReport(t, s, model.Issue{ProjectID: p.ID, Title: "Unchecked error in a", Type: model.IssueBug, Severity: model.SevLow, File: "a.go", Source: "agent:x"}, model.UpsertUpdated)
	b := mustReport(t, s, model.Issue{ProjectID: p.ID, Title: "Unchecked error in b", Type: model.IssueBug, Severity: model.SevHigh, File: "b.go", Source: "scan:lint"}, model.UpsertNew)
	dep, _ := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: "after b", DependsOn: []string{b.ID}})
	m, err := s.MergeTodos([]string{a.ID, b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != a.ID || len(m.Finding.Issues) != 2 || m.Finding.Reports() != 3 || m.Finding.Severity() != model.SevHigh {
		t.Fatalf("merged: %+v", m.Finding)
	}
	if !strings.HasPrefix(m.Title, "Fix 2 issues") || !strings.Contains(m.Prompt, "b.go") || !slices.Contains(m.Tags, "high") || slices.Contains(m.Tags, "low") {
		t.Fatalf("merged title %q, tags %q:\n%s", m.Title, m.Tags, m.Prompt)
	}
	if _, err := s.GetTodo(b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("the merged-away draft is still there")
	}
	if len(s.ListDismissed(p.ID)) != 0 {
		t.Fatal("merging dismissed the merged-away draft's issues")
	}
	if got, _ := s.GetTodo(dep.ID); !slices.Equal(got.DependsOn, []string{a.ID}) {
		t.Fatalf("dependency not moved to the merged draft: %q", got.DependsOn)
	}
	// Later reports of either issue land on the merged draft.
	r := mustReport(t, s, model.Issue{ProjectID: p.ID, Title: "Unchecked error in b", Type: model.IssueBug, Severity: model.SevHigh, File: "b.go", Source: "agent:y"}, model.UpsertUpdated)
	if r.ID != a.ID || r.Finding.Reports() != 4 {
		t.Fatalf("report after merge went to %s, reports %d", r.ID, r.Finding.Reports())
	}
	// Only drafts of one project, at least two.
	if _, err := s.MergeTodos([]string{a.ID}); !errors.Is(err, ErrBadMerge) {
		t.Fatalf("merge one: %v", err)
	}
	if _, err := s.SetStatus(dep.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MergeTodos([]string{a.ID, dep.ID}); !errors.Is(err, ErrBadMerge) {
		t.Fatalf("merge a queued task: %v", err)
	}
}

func TestMergeTodosWithEditedPrompts(t *testing.T) {
	t.Parallel()
	s, p, _ := findingStore(t)
	a, _ := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: "One", Prompt: "do one", Tags: []string{"ui"}})
	b := mustReport(t, s, model.Issue{ProjectID: p.ID, Title: "Two", Source: "agent:x"}, model.UpsertNew)
	m, err := s.MergeTodos([]string{a.ID, b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "One" || !strings.Contains(m.Prompt, "do one") || !strings.Contains(m.Prompt, "## Fix: Two") || m.Finding == nil || m.Finding.PromptSum != "" {
		t.Fatalf("merged: %q\n%s\n%+v", m.Title, m.Prompt, m.Finding)
	}
	if !slices.Contains(m.Tags, "ui") || !slices.Contains(m.Tags, "reported") {
		t.Fatalf("tags %q", m.Tags)
	}
}

// TestMigrateIssues turns an issues table, as Dash kept it before, into
// tasks and dismissed fingerprints.
func TestMigrateIssues(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "dash.db")
	s := openTest(t, path)
	p, _ := s.CreateProject(model.Project{Name: "p", Path: t.TempDir()})
	made, _ := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: "Fix the leak", Prompt: "fix it my way"})
	s.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	rows := []string{
		`{"id":"i-open","project_id":"` + p.ID + `","title":"Open one","type":"bug","severity":"high","file":"a.go","line":3,"source":"scan:sec","reports":4,"status":"open","created_at":"2026-01-02T00:00:00Z"}`,
		`{"id":"i-made","project_id":"` + p.ID + `","title":"Leak","type":"bug","severity":"medium","source":"agent:x","status":"task_created","todo_ids":["` + made.ID + `"]}`,
		`{"id":"i-dis","project_id":"` + p.ID + `","title":"Not a bug","type":"issue","severity":"low","source":"scan:sec","status":"dismissed","dismiss_reason":"by design"}`,
		`{"id":"i-gone","project_id":"` + p.ID + `","title":"Fixed long ago","type":"bug","severity":"low","status":"resolved","todo_ids":["nope"]}`,
	}
	if _, err := db.Exec(`CREATE TABLE issues (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, data TEXT NOT NULL, org_id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO issues (id, project_id, data) VALUES (json_extract(?1, '$.id'), json_extract(?1, '$.project_id'), ?1)`, r); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s = openTest(t, path)
	open, err := s.GetTodo("i-open")
	if err != nil {
		t.Fatal("the open issue's draft keeps its id:", err)
	}
	if open.Status != model.StatusDraft || open.Title != "Fix: Open one" || open.Finding.Reports() != 4 || !slices.Equal(open.Tags, []string{"bug", "high", "scan:sec"}) {
		t.Fatalf("open issue: %+v", open)
	}
	got, _ := s.GetTodo(made.ID)
	if got.Prompt != "fix it my way" || got.Finding == nil || got.Finding.Index((model.Issue{Type: model.IssueBug, Title: "Leak"}).ComputeFingerprint()) < 0 ||
		!slices.Contains(got.Finding.LegacyIDs, "i-made") || !slices.Contains(got.Tags, "reported") {
		t.Fatalf("task made for an issue: %+v", got)
	}
	ds := s.ListDismissed(p.ID)
	if len(ds) != 1 || ds[0].Title != "Not a bug" || ds[0].Reason != "by design" {
		t.Fatalf("dismissed: %+v", ds)
	}
	if n := len(s.ListTodos(p.ID)); n != 2 {
		t.Fatalf("%d tasks, want 2 (the resolved issue is dropped)", n)
	}
	var tables int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name = 'issues'").Scan(&tables)
	if tables != 0 {
		t.Fatal("the issues table is still there")
	}
	// Reports find the migrated tasks.
	mustReport(t, s, model.Issue{ProjectID: p.ID, Title: "Leak", Type: model.IssueBug, Source: "agent:y"}, model.UpsertUpdated)
	if o, _ := s.ReportFinding(model.Issue{ProjectID: p.ID, Title: "Not a bug", Type: model.IssueOther, Source: "scan:sec"}); o.Result != model.UpsertDismissed {
		t.Fatalf("migrated dismissal: %s", o.Result)
	}
}
