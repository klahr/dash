package store

import (
	"errors"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/klahr/dash/internal/model"
)

type recordedEvents struct{ evs []model.Event }

func (r *recordedEvents) Publish(ev model.Event) { r.evs = append(r.evs, ev) }

// Two orgs: each sees and changes only its own projects and what belongs to
// them; the other's records don't exist for it.
func TestForOrgIsolates(t *testing.T) {
	evs := &recordedEvents{}
	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), evs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	acc := s.Accounts()
	if err := acc.Bootstrap("u", "password"); err != nil {
		t.Fatal(err)
	}
	u, _ := acc.EnvUser()
	def, _ := acc.DefaultOrg()
	other, err := acc.CreateOrg(u.ID, "Other")
	if err != nil {
		t.Fatal(err)
	}
	a, b := s.ForOrg(def.ID), s.ForOrg(other.ID)

	pa, err := a.CreateProject(model.Project{Name: "a"})
	if err != nil || pa.OrgID != def.ID {
		t.Fatalf("create: %+v %v", pa, err)
	}
	if _, err := b.CreateProject(model.Project{Name: "b", OrgID: def.ID}); err != nil {
		t.Fatal(err) // the org given is ignored: the view's own is used
	}
	if ps := b.ListProjects(); len(ps) != 1 || ps[0].Name != "b" || ps[0].OrgID != other.ID {
		t.Fatalf("other org's projects: %+v", ps)
	}
	if _, err := s.ForOrg(0).CreateProject(model.Project{Name: "x"}); err == nil {
		t.Fatal("created a project in no org")
	}
	if ps := s.ForOrg(0).ListProjects(); len(ps) != 0 {
		t.Fatalf("no org sees %+v", ps)
	}

	td, _ := a.CreateTodo(model.Todo{ProjectID: pa.ID, Title: "a's", CreatedBy: u.ID})
	is, _, _ := a.UpsertIssue(model.Issue{ProjectID: pa.ID, Title: "a's issue"})
	run, _ := s.CreateScanRun(model.ScanRun{ProjectID: pa.ID, Scan: "sec"})
	if _, err := s.SaveScanTemplate(model.ScanTemplate{ProjectID: pa.ID, Name: "deps"}, true); err != nil {
		t.Fatal(err)
	}
	msgs := []model.ChatMessage{{Role: "user", Content: "hi", CreatedBy: u.ID}}
	_ = a.SetTodoChat(td.ID, msgs)
	_ = a.SetProjectChat(pa.ID, msgs)
	_ = a.SetChat(u.ID, msgs)

	notFound := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s from the other org: %v, want ErrNotFound", what, err)
		}
	}
	_, err = b.GetProject(pa.ID)
	notFound("GetProject", err)
	_, err = b.UpdateProject(pa.ID, func(p *model.Project) error { p.Name = "mine"; return nil })
	notFound("UpdateProject", err)
	_, err = b.CreateTodo(model.Todo{ProjectID: pa.ID})
	notFound("CreateTodo", err)
	_, err = b.GetTodo(td.ID)
	notFound("GetTodo", err)
	_, err = b.UpdateTodo(td.ID, func(t *model.Todo) error { t.Title = "mine"; return nil })
	notFound("UpdateTodo", err)
	_, err = b.SetStatus(td.ID, model.StatusQueued, "")
	notFound("SetStatus", err)
	_, err = b.DeleteTodo(td.ID)
	notFound("DeleteTodo", err)
	_, err = b.TodoChat(td.ID)
	notFound("TodoChat", err)
	notFound("SetTodoChat", b.SetTodoChat(td.ID, nil))
	_, err = b.ProjectChat(pa.ID)
	notFound("ProjectChat", err)
	notFound("SetProjectChat", b.SetProjectChat(pa.ID, nil))
	_, err = b.GetIssue(is.ID)
	notFound("GetIssue", err)
	_, _, err = b.UpsertIssue(model.Issue{ProjectID: pa.ID, Title: "x"})
	notFound("UpsertIssue", err)
	_, err = b.UpsertIssues(pa.ID, "", []model.Issue{{Title: "x"}})
	notFound("UpsertIssues", err)
	_, err = b.UpdateIssue(is.ID, func(*model.Issue) error { return nil })
	notFound("UpdateIssue", err)
	_, err = b.SetIssueStatus(is.ID, model.IssueDismissed, "")
	notFound("SetIssueStatus", err)
	_, err = b.LinkIssues([]string{is.ID}, td.ID)
	notFound("LinkIssues", err)
	_, err = b.DeleteIssue(is.ID)
	notFound("DeleteIssue", err)
	_, err = b.GetScanRun(run.ID)
	notFound("GetScanRun", err)
	_, err = b.SetScanSetting(pa.ID, "sec", func(*model.ScanSetting) {})
	notFound("SetScanSetting", err)
	_, err = b.ScanTemplate(pa.ID, "deps")
	notFound("ScanTemplate", err)
	_, err = b.SaveScanTemplate(model.ScanTemplate{ProjectID: pa.ID, Name: "x"}, true)
	notFound("SaveScanTemplate", err)
	notFound("DeleteScanTemplate", b.DeleteScanTemplate(pa.ID, "deps", false))

	if len(b.ListTodos("")) != 0 || len(b.ListTodos(pa.ID)) != 0 || len(b.ListIssues("")) != 0 || len(b.ListIssues(pa.ID)) != 0 ||
		len(b.ListScanRuns(pa.ID, "")) != 0 || len(b.ScanTemplates(pa.ID)) != 0 || len(b.Chat(u.ID)) != 0 {
		t.Error("the other org's lists show a's records")
	}
	if _, ok := b.LastScanRun(pa.ID, "sec", nil); ok {
		t.Error("LastScanRun across orgs")
	}
	if b.OwnsProject(pa.ID) || !a.OwnsProject(pa.ID) {
		t.Error("OwnsProject")
	}

	// a's records are as they were, and a sees them.
	if got, _ := a.GetTodo(td.ID); got.Title != "a's" || got.CreatedBy != u.ID {
		t.Errorf("a's todo: %+v", got)
	}
	if len(a.ListTodos("")) != 1 || len(a.ListIssues("")) != 1 || len(a.Chat(u.ID)) != 1 {
		t.Error("a lost its records")
	}

	// Events carry the org of their project, also when it is deleted.
	evs.evs = nil
	if _, err := s.DeleteProject(pa.ID); err != nil {
		t.Fatal(err)
	}
	if len(evs.evs) == 0 || evs.evs[len(evs.evs)-1].Type != model.EvProjectDeleted || evs.evs[len(evs.evs)-1].OrgID != def.ID {
		t.Errorf("project.deleted event: %+v", evs.evs)
	}
	if _, ok := s.ProjectOrg(pa.ID); ok {
		t.Error("deleted project still has an org")
	}
}

// Each record's row carries its project's org, so the database agrees.
func TestRowsCarryTheirProjectsOrg(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	acc := s.Accounts()
	if err := acc.Bootstrap("u", "password"); err != nil {
		t.Fatal(err)
	}
	u, _ := acc.EnvUser()
	other, _ := acc.CreateOrg(u.ID, "Other")
	o := s.ForOrg(other.ID)
	p, _ := o.CreateProject(model.Project{Name: "p"})
	td, _ := o.CreateTodo(model.Todo{ProjectID: p.ID, Title: "t"})
	_, _, _ = o.UpsertIssue(model.Issue{ProjectID: p.ID, Title: "i"})
	_, _ = s.CreateScanRun(model.ScanRun{ProjectID: p.ID, Scan: "s"})
	_, _ = o.SetScanSetting(p.ID, "s", func(*model.ScanSetting) {})
	_, _ = o.SaveScanTemplate(model.ScanTemplate{ProjectID: p.ID, Name: "n"}, true)
	_ = o.SetTodoChat(td.ID, []model.ChatMessage{{Role: "user", Content: "x"}})
	_ = o.SetChat(u.ID, []model.ChatMessage{{Role: "user", Content: "x"}})
	for table, byOrg := range orgsOf(t, s) {
		if len(byOrg) != 1 || byOrg[itoa(other.ID)] == 0 {
			t.Errorf("%s: rows by org %v, want all in %d", table, byOrg, other.ID)
		}
	}
	s = reopen(t, s, path)
	if got, _ := s.GetProject(p.ID); got.OrgID != other.ID {
		t.Errorf("org after reopening: %d", got.OrgID)
	}
	if got := s.ForOrg(other.ID).Chat(u.ID); len(got) != 1 {
		t.Errorf("general chat after reopening: %+v", got)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
