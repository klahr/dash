package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/klahr/dash/internal/model"
)

func issueStore(t *testing.T) (*Store, model.Project, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProject(model.Project{Name: "p", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return s, p, path
}

func mustUpsert(t *testing.T, s *Store, in model.Issue, want model.UpsertResult) model.Issue {
	t.Helper()
	is, r, err := s.UpsertIssue(in)
	if err != nil {
		t.Fatal(err)
	}
	if r != want {
		t.Fatalf("upsert %q: %s, want %s", in.Title, r, want)
	}
	return is
}

func TestUpsertIssueByFingerprint(t *testing.T) {
	s, p, path := issueStore(t)
	a := mustUpsert(t, s, model.Issue{ProjectID: p.ID, Title: "Nil deref in handler", Type: model.IssueBug, File: "./api/h.go", Line: 10}, model.UpsertNew)
	if a.Status != model.IssueOpen || a.Severity != model.SevMedium || a.Source != "manual" || a.File != "api/h.go" {
		t.Fatalf("defaults: %+v", a)
	}
	// Same type, file and title (other case, spacing and line): the same issue.
	b := mustUpsert(t, s, model.Issue{ProjectID: p.ID, Title: "nil  deref in Handler", Type: model.IssueBug, File: "api/h.go", Line: 14, Severity: model.SevHigh}, model.UpsertUpdated)
	if b.ID != a.ID || b.Severity != model.SevHigh || b.Line != 14 {
		t.Fatalf("not updated in place: %+v", b)
	}
	mustUpsert(t, s, model.Issue{ProjectID: p.ID, Title: "nil  deref in Handler", Type: model.IssueBug, File: "api/h.go", Line: 14, Severity: model.SevHigh}, model.UpsertKnown)
	// Another type or file is another issue.
	mustUpsert(t, s, model.Issue{ProjectID: p.ID, Title: "Nil deref in handler", Type: model.IssueVuln, File: "api/h.go"}, model.UpsertNew)
	if n := len(s.ListIssues(p.ID)); n != 2 {
		t.Fatalf("%d issues, want 2", n)
	}
	// A rule identifies the issue in place of its title.
	r1 := mustUpsert(t, s, model.Issue{ProjectID: p.ID, Title: "SQL built from input", Rule: "sqli", Type: model.IssueVuln, File: "db.go"}, model.UpsertNew)
	r2 := mustUpsert(t, s, model.Issue{ProjectID: p.ID, Title: "SQL injection", Rule: "sqli", Type: model.IssueVuln, File: "db.go"}, model.UpsertUpdated)
	if r1.ID != r2.ID || r2.Title != "SQL injection" {
		t.Fatalf("rule fingerprint: %+v", r2)
	}

	// Details the user wrote survive a report without details.
	if _, err := s.UpdateIssue(a.ID, func(is *model.Issue) error { is.Details = "seen in prod"; return nil }); err != nil {
		t.Fatal(err)
	}
	c := mustUpsert(t, s, model.Issue{ProjectID: p.ID, Title: "nil  deref in Handler", Type: model.IssueBug, File: "api/h.go", Line: 14, Severity: model.SevHigh}, model.UpsertKnown)
	if c.Details != "seen in prod" {
		t.Fatalf("details lost: %q", c.Details)
	}

	if s, _ = Open(path, nil); len(s.ListIssues(p.ID)) != 3 {
		t.Fatalf("issues after reopen: %d", len(s.ListIssues(p.ID)))
	}
}

func TestUpsertIssueKeepsScanKind(t *testing.T) {
	s, p, path := issueStore(t)
	// Filed before issues carried their scan's kind: a later report adds it,
	// and a report without one keeps it.
	a := mustUpsert(t, s, model.Issue{ProjectID: p.ID, Title: "Slow query", File: "db.go", Source: "scan:perf"}, model.UpsertNew)
	b := mustUpsert(t, s, model.Issue{ProjectID: p.ID, Title: "Slow query", File: "db.go", Source: "scan:perf", ScanKind: model.ScanKindPerformance}, model.UpsertUpdated)
	if b.ID != a.ID || b.ScanKind != model.ScanKindPerformance {
		t.Fatalf("kind not added: %+v", b)
	}
	if c := mustUpsert(t, s, model.Issue{ProjectID: p.ID, Title: "Slow query", File: "db.go", Source: "scan:perf"}, model.UpsertKnown); c.ScanKind != model.ScanKindPerformance {
		t.Fatalf("kind lost: %+v", c)
	}
	if s, _ = Open(path, nil); s.ListIssues(p.ID)[0].ScanKind != model.ScanKindPerformance {
		t.Fatalf("kind after reopen: %+v", s.ListIssues(p.ID))
	}
}

func TestUpsertIssueValidates(t *testing.T) {
	s, p, _ := issueStore(t)
	for _, in := range []model.Issue{
		{ProjectID: p.ID, Title: "  "},
		{ProjectID: p.ID, Title: "x", Type: "feature"},
		{ProjectID: p.ID, Title: "x", Severity: "urgent"},
	} {
		if _, _, err := s.UpsertIssue(in); err == nil {
			t.Errorf("%+v: no error", in)
		}
	}
	if _, _, err := s.UpsertIssue(model.Issue{ProjectID: "nope", Title: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project: %v", err)
	}
}

func TestDismissedIssueStaysDismissed(t *testing.T) {
	s, p, _ := issueStore(t)
	in := model.Issue{ProjectID: p.ID, Title: "Weak hash", Type: model.IssueVuln, File: "auth.go", Source: "scan:gosec"}
	a := mustUpsert(t, s, in, model.UpsertNew)
	if _, err := s.SetIssueStatus(a.ID, model.IssueDismissed, "test fixture only"); err != nil {
		t.Fatal(err)
	}
	in.Severity = model.SevCritical
	got := mustUpsert(t, s, in, model.UpsertDismissed)
	if got.Status != model.IssueDismissed || got.DismissReason != "test fixture only" || got.Severity != model.SevMedium {
		t.Fatalf("dismissed issue changed: %+v", got)
	}
	counts, err := s.UpsertIssues(p.ID, "scan:gosec", []model.Issue{in, {Title: "Hardcoded key", Type: model.IssueVuln, File: "cfg.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if counts != (model.UpsertCounts{New: 1, Dismissed: 1}) {
		t.Fatalf("counts %+v", counts)
	}
	for _, is := range s.ListIssues(p.ID) {
		if is.Title == "Hardcoded key" && is.Source != "scan:gosec" {
			t.Fatalf("batch source: %q", is.Source)
		}
	}
}

func TestResolvedIssueReopensWhenReportedAgain(t *testing.T) {
	s, p, _ := issueStore(t)
	in := model.Issue{ProjectID: p.ID, Title: "Race in cache", Type: model.IssueBug}
	a := mustUpsert(t, s, in, model.UpsertNew)
	if _, err := s.SetIssueStatus(a.ID, model.IssueResolved, ""); err != nil {
		t.Fatal(err)
	}
	b := mustUpsert(t, s, in, model.UpsertReopened)
	if b.ID != a.ID || b.Status != model.IssueOpen {
		t.Fatalf("not reopened: %+v", b)
	}
	counts, err := s.UpsertIssues(p.ID, "scan:x", []model.Issue{in})
	if err != nil {
		t.Fatal(err)
	}
	if counts != (model.UpsertCounts{Known: 1}) {
		t.Fatalf("counts %+v", counts)
	}
}

func TestIssueStatusTransitions(t *testing.T) {
	s, p, _ := issueStore(t)
	a := mustUpsert(t, s, model.Issue{ProjectID: p.ID, Title: "a"}, model.UpsertNew)
	step := func(to model.IssueStatus, ok bool) {
		t.Helper()
		_, err := s.SetIssueStatus(a.ID, to, "why")
		var inv model.ErrInvalidIssueTransition
		if ok && err != nil {
			t.Fatalf("→ %s: %v", to, err)
		}
		if !ok && !errors.As(err, &inv) {
			t.Fatalf("→ %s allowed (err %v)", to, err)
		}
	}
	step(model.IssueDismissed, true)
	if is, _ := s.GetIssue(a.ID); is.DismissReason != "why" {
		t.Fatalf("reason %q", is.DismissReason)
	}
	step(model.IssueResolved, false) // dismissed → resolved
	step(model.IssueTaskCreated, false)
	step(model.IssueOpen, true) // reopen
	if is, _ := s.GetIssue(a.ID); is.DismissReason != "" {
		t.Fatalf("reopened issue kept its dismiss reason %q", is.DismissReason)
	}
	step(model.IssueResolved, true)
	step(model.IssueDismissed, false) // resolved → dismissed
	step(model.IssueOpen, true)

	td, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "fix"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkIssues([]string{a.ID}, td.ID); err != nil {
		t.Fatal(err)
	}
	if is, _ := s.GetIssue(a.ID); is.Status != model.IssueTaskCreated || len(is.TodoIDs) != 1 || is.TodoIDs[0] != td.ID {
		t.Fatalf("after link: %+v", is)
	}
	// A dismissed issue can't get a task without being reopened first.
	b := mustUpsert(t, s, model.Issue{ProjectID: p.ID, Title: "b"}, model.UpsertNew)
	if _, err := s.SetIssueStatus(b.ID, model.IssueDismissed, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkIssues([]string{b.ID}, td.ID); err == nil {
		t.Fatal("linked a dismissed issue")
	}
}

func TestMergedTodoResolvesItsIssues(t *testing.T) {
	s, p, _ := issueStore(t)
	a := mustUpsert(t, s, model.Issue{ProjectID: p.ID, Title: "a"}, model.UpsertNew)
	b := mustUpsert(t, s, model.Issue{ProjectID: p.ID, Title: "b"}, model.UpsertNew)
	other := mustUpsert(t, s, model.Issue{ProjectID: p.ID, Title: "other"}, model.UpsertNew)
	td, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "fix"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkIssues([]string{a.ID, b.ID}, td.ID); err != nil {
		t.Fatal(err)
	}
	for _, st := range []model.TodoStatus{model.StatusQueued, model.StatusInProgress, model.StatusReview} {
		if _, err := s.SetStatus(td.ID, st, ""); err != nil {
			t.Fatal(err)
		}
	}
	if is, _ := s.GetIssue(a.ID); is.Status != model.IssueTaskCreated {
		t.Fatalf("resolved before merge: %s", is.Status)
	}
	if _, err := s.SetStatus(td.ID, model.StatusMerged, ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID} {
		if is, _ := s.GetIssue(id); is.Status != model.IssueResolved {
			t.Fatalf("issue %s: %s after merge", is.Title, is.Status)
		}
	}
	if is, _ := s.GetIssue(other.ID); is.Status != model.IssueOpen {
		t.Fatalf("unlinked issue: %s", is.Status)
	}
}

func TestDeletedTodoReopensItsIssues(t *testing.T) {
	s, p, _ := issueStore(t)
	a := mustUpsert(t, s, model.Issue{ProjectID: p.ID, Title: "a"}, model.UpsertNew)
	t1, _ := s.CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "1"})
	t2, _ := s.CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "2"})
	for _, td := range []model.Todo{t1, t2} {
		if _, err := s.LinkIssues([]string{a.ID}, td.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DeleteTodo(t1.ID); err != nil {
		t.Fatal(err)
	}
	if is, _ := s.GetIssue(a.ID); is.Status != model.IssueTaskCreated || len(is.TodoIDs) != 1 {
		t.Fatalf("one task left: %+v", is)
	}
	if _, err := s.DeleteTodo(t2.ID); err != nil {
		t.Fatal(err)
	}
	if is, _ := s.GetIssue(a.ID); is.Status != model.IssueOpen || len(is.TodoIDs) != 0 {
		t.Fatalf("no task left: %+v", is)
	}
}
