package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

// sameJSON compares values by their JSON, the form they are stored in (a
// time read back has no monotonic reading, so DeepEqual would differ).
func sameJSON(t *testing.T, what string, got, want any) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Errorf("%s:\n got %s\nwant %s", what, g, w)
	}
}

func reopen(t *testing.T, s *Store, path string) *Store {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

const legacyState = `{
  "projects": {
    "p1": {"id": "p1", "name": "app", "path": "/src/app", "default_branch": "main", "auto_push": true,
           "allowed_tools": ["Bash"], "created_at": "2026-01-02T03:04:05Z"},
    "p2": {"id": "p2", "name": "lib", "path": "/src/lib", "default_branch": "trunk", "created_at": "2026-01-03T00:00:00Z"}
  },
  "todos": {
    "t1": {"id": "t1", "project_id": "p1", "title": "Fix login", "prompt": "fix it", "priority": 1,
           "depends_on": [], "status": "review", "feedback": ["tests fail"],
           "worktree": {"name": "todo-t1", "path": "/wt/todo-t1", "branch": "dash/todo-t1", "base_commit": "abc"},
           "created_at": "2026-01-04T00:00:00Z", "updated_at": "2026-01-04T01:00:00Z"},
    "t2": {"id": "t2", "project_id": "p1", "title": "Refactor", "prompt": "r", "depends_on": ["t1"],
           "status": "in_progress", "reviewing": true,
           "created_at": "2026-01-05T00:00:00Z", "updated_at": "2026-01-05T00:00:00Z"}
  },
  "chat": [{"role": "user", "content": "hi"}, {"role": "assistant", "content": "hello"}],
  "todo_chats": {"t1": [{"role": "user", "content": "why?"}]},
  "project_chats": {"p2": [{"role": "user", "content": "plan"}, {"role": "assistant", "content": "ok"}]},
  "issues": {
    "i1": {"id": "i1", "project_id": "p1", "title": "SQLi", "type": "vuln", "severity": "high",
           "file": "db.go", "line": 3, "source": "scan:sec", "status": "task_created", "fingerprint": "fp1",
           "todo_ids": ["t1"], "created_at": "2026-01-04T00:00:00Z", "updated_at": "2026-01-04T00:00:00Z"}
  },
  "scan_runs": {
    "r1": {"id": "r1", "project_id": "p1", "scan": "sec", "trigger": "schedule", "scope": "repo", "commit": "abc",
           "status": "succeeded", "started_at": "2026-01-04T00:00:00Z", "finished_at": "2026-01-04T00:10:00Z", "findings": 1},
    "r2": {"id": "r2", "project_id": "p1", "scan": "sec", "trigger": "manual", "scope": "repo", "commit": "def",
           "status": "running", "started_at": "2026-01-06T00:00:00Z", "findings": 0}
  },
  "scan_settings": {
    "p1/sec": {"project_id": "p1", "scan": "sec", "enabled": false, "schedule": "0 3 * * *",
               "approved_tools": ["Read"], "updated_at": "2026-01-04T00:00:00Z"}
  },
  "scan_templates": {
    "p1/deps": {"project_id": "p1", "name": "deps", "kind": "review", "scope": "repo", "tools": ["Read"],
                "prompt": "Check deps.", "created_at": "2026-01-04T00:00:00Z", "updated_at": "2026-01-04T00:00:00Z"}
  }
}`

func TestImportStateJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	legacy := filepath.Join(dir, LegacyFile)
	if err := os.WriteFile(legacy, []byte(legacyState), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state.json still there: %v", err)
	}
	if _, err := os.Stat(legacy + ".imported"); err != nil {
		t.Fatalf("state.json.imported: %v", err)
	}

	var want snapshot
	if err := json.Unmarshal([]byte(legacyState), &want); err != nil {
		t.Fatal(err)
	}
	// What Open does to a loaded state: an in-progress todo is queued again,
	// a review chat is over, a running scan was interrupted.
	want.Todos["t2"].Status, want.Todos["t2"].Reviewing = model.StatusQueued, false

	check := func(s *Store) {
		t.Helper()
		if got := s.ListProjects(); len(got) != 2 {
			t.Fatalf("projects: %+v", got)
		}
		for id, p := range want.Projects {
			got, err := s.GetProject(id)
			if err != nil {
				t.Fatal(err)
			}
			sameJSON(t, "project "+id, got, p)
		}
		for id, td := range want.Todos {
			got, err := s.GetTodo(id)
			if err != nil {
				t.Fatal(err)
			}
			sameJSON(t, "todo "+id, got, td)
		}
		got, err := s.GetIssue("i1")
		if err != nil {
			t.Fatal(err)
		}
		sameJSON(t, "issue", got, want.Issues["i1"])
		r1, _ := s.GetScanRun("r1")
		sameJSON(t, "scan run r1", r1, want.ScanRuns["r1"])
		if r2, _ := s.GetScanRun("r2"); r2.Status != model.ScanInterrupted || r2.FinishedAt == nil {
			t.Errorf("running scan not interrupted: %+v", r2)
		}
		st, ok := s.ScanSetting("p1", "sec")
		if !ok {
			t.Fatal("scan setting missing")
		}
		sameJSON(t, "scan setting", st, want.ScanSettings["p1/sec"])
		tpl, err := s.ScanTemplate("p1", "deps")
		if err != nil {
			t.Fatal(err)
		}
		sameJSON(t, "scan template", tpl, want.ScanTemplates["p1/deps"])
		sameJSON(t, "chat", s.data.MainChats[""], want.Chat)
		sameJSON(t, "todo chat", s.TodoChat("t1"), want.TodoChats["t1"])
		sameJSON(t, "project chat", s.ProjectChat("p2"), want.ProjectChats["p2"])
	}
	check(s)

	// Imported once: a state.json that turns up later is ignored.
	if err := os.WriteFile(legacy, []byte(`{"projects":{"x":{"id":"x","name":"x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s = reopen(t, s, path)
	check(s)
	if _, err := s.GetProject("x"); err != ErrNotFound {
		t.Fatalf("state.json imported into a database with data: %v", err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("a state.json that wasn't imported was moved: %v", err)
	}
}

func TestImportBadStateJSONFailsOpen(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	legacy := filepath.Join(dir, LegacyFile)
	if err := os.WriteFile(legacy, []byte(`{"projects": [`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(dir, "dash.db"), nil); err == nil {
		t.Fatal("opened with a broken state.json")
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("broken state.json was moved: %v", err)
	}
}

func TestRoundTripEveryRecord(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	p, err := s.CreateProject(model.Project{Name: "p", Path: t.TempDir(), DefaultBranch: "main", CopyFiles: []string{".env"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AllowTool(p.ID, "Bash"); err != nil {
		t.Fatal(err)
	}
	p, _ = s.GetProject(p.ID)
	gone, _ := s.CreateProject(model.Project{Name: "gone"})

	a, _ := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: "a", Prompt: "do a"})
	b, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: "b", DependsOn: []string{a.ID}})
	if err != nil {
		t.Fatal(err)
	}
	b, err = s.UpdateTodo(b.ID, func(t *model.Todo) error {
		t.Feedback = []string{"more tests"}
		t.Worktree = &model.Worktree{Name: "w", Path: "/w", Branch: "dash/w", BaseCommit: "abc"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	chat := []model.ChatMessage{{Role: "user", Content: "hi", At: &at}, {Role: "assistant", Content: "hello"}}
	if err := s.SetChat(1, 1, chat); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTodoChat(a.ID, chat[:1]); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendTodoChat(a.ID, chat[1]); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectChat(p.ID, chat); err != nil {
		t.Fatal(err)
	}
	is, _, err := s.UpsertIssue(model.Issue{ProjectID: p.ID, Title: "Leak", Type: model.IssueBug, File: "a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkIssues([]string{is.ID}, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertIssueBatch(p.ID, "scan:x", []model.Issue{{Title: "Other", Type: model.IssueBug}}); err != nil {
		t.Fatal(err)
	}
	dropped, _, _ := s.UpsertIssue(model.Issue{ProjectID: p.ID, Title: "Drop me", Type: model.IssueBug})
	if _, err := s.DeleteIssue(dropped.ID); err != nil {
		t.Fatal(err)
	}
	run, _ := s.CreateScanRun(model.ScanRun{ProjectID: p.ID, Scan: "x", Status: model.ScanRunning})
	run, err = s.UpdateScanRun(run.ID, true, func(r *model.ScanRun) {
		now := time.Now()
		r.Status, r.FinishedAt, r.Findings, r.NewIssues = model.ScanSucceeded, &now, 1, []string{is.ID}
	})
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	setting, err := s.SetScanSetting(p.ID, "x", func(st *model.ScanSetting) { st.Enabled, st.Schedule = &enabled, "@daily" })
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := s.SaveScanTemplate(model.ScanTemplate{ProjectID: p.ID, Name: "x", Scope: "repo", Tools: []string{"Read"}, Prompt: "look"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveScanTemplate(model.ScanTemplate{ProjectID: p.ID, Name: "tmp", Prompt: "p"}, true); err != nil {
		t.Fatal(err)
	}
	_, _ = s.SetScanSetting(p.ID, "tmp", func(st *model.ScanSetting) {})
	if err := s.DeleteScanTemplate(p.ID, "tmp", false); err != nil {
		t.Fatal(err)
	}
	// Things in the deleted project go with it.
	gt, _ := s.CreateTodo(model.Todo{ProjectID: gone.ID, Title: "g"})
	_ = s.SetTodoChat(gt.ID, chat)
	_ = s.SetProjectChat(gone.ID, chat)
	_, _, _ = s.UpsertIssue(model.Issue{ProjectID: gone.ID, Title: "g", Type: model.IssueBug})
	_, _ = s.CreateScanRun(model.ScanRun{ProjectID: gone.ID, Scan: "x", Status: model.ScanSucceeded})
	_, _ = s.SetScanSetting(gone.ID, "x", func(st *model.ScanSetting) {})
	_, _ = s.SaveScanTemplate(model.ScanTemplate{ProjectID: gone.ID, Name: "x", Prompt: "p"}, true)
	if _, err := s.DeleteProject(gone.ID); err != nil {
		t.Fatal(err)
	}

	before := struct {
		Projects                                  []model.Project
		Todos                                     []model.Todo
		Issues                                    []model.Issue
		Runs                                      []model.ScanRun
		Chat, TodoChat, ProjectChat, GoneTodoChat []model.ChatMessage
	}{s.ListProjects(), s.ListTodos(""), s.ListIssues(""), s.ListScanRuns(p.ID, ""), s.Chat(1, 1), s.TodoChat(a.ID), s.ProjectChat(p.ID), s.TodoChat(gt.ID)}
	if len(before.Projects) != 1 || len(before.Todos) != 2 || len(before.Issues) != 2 || len(before.Runs) != 1 {
		t.Fatalf("unexpected state before reopen: %+v", before)
	}

	s = reopen(t, s, path)
	sameJSON(t, "projects", s.ListProjects(), before.Projects)
	sameJSON(t, "todos", s.ListTodos(""), before.Todos)
	sameJSON(t, "issues", s.ListIssues(""), before.Issues)
	sameJSON(t, "scan runs", s.ListScanRuns(p.ID, ""), before.Runs)
	sameJSON(t, "chat", s.Chat(1, 1), chat)
	sameJSON(t, "todo chat", s.TodoChat(a.ID), chat)
	sameJSON(t, "project chat", s.ProjectChat(p.ID), chat)
	got, ok := s.ScanSetting(p.ID, "x")
	if !ok {
		t.Fatal("scan setting lost")
	}
	sameJSON(t, "scan setting", got, setting)
	sameJSON(t, "scan templates", s.ScanTemplates(p.ID), []model.ScanTemplate{tpl})
	if _, ok := s.ScanSetting(p.ID, "tmp"); ok {
		t.Error("settings of a deleted scan template came back")
	}
	gotRun, _ := s.GetScanRun(run.ID)
	sameJSON(t, "scan run", gotRun, run)

	// Nothing of the deleted project is left in the database.
	for _, table := range []string{"todos", "issues", "scan_runs", "scan_settings", "scan_templates"} {
		var n int
		if err := s.db.QueryRow("SELECT count(*) FROM "+table+" WHERE project_id = ?", gone.ID).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d rows of the deleted project (%v)", table, n, err)
		}
	}
	var n int
	_ = s.db.QueryRow("SELECT count(*) FROM chat_messages WHERE owner_id IN (?, ?)", gone.ID, gt.ID).Scan(&n)
	if n != 0 {
		t.Errorf("%d chat messages of the deleted project left", n)
	}

	// Deleting a todo removes its row and chat and updates its dependents.
	if _, err := s.DeleteTodo(a.ID); err != nil {
		t.Fatal(err)
	}
	s = reopen(t, s, path)
	if _, err := s.GetTodo(a.ID); err != ErrNotFound {
		t.Fatalf("deleted todo came back: %v", err)
	}
	if got, _ := s.GetTodo(b.ID); len(got.DependsOn) != 0 {
		t.Fatalf("dependency on deleted todo came back: %v", got.DependsOn)
	}
	if got, _ := s.GetIssue(is.ID); got.Status != model.IssueOpen || len(got.TodoIDs) != 0 {
		t.Fatalf("issue still linked to the deleted todo: %+v", got)
	}
	if len(s.TodoChat(a.ID)) != 0 {
		t.Fatal("deleted todo's chat came back")
	}
}

func TestScanRunPruneIsPersisted(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p, _ := s.CreateProject(model.Project{Name: "p"})
	start := time.Now().Add(-time.Hour)
	for i := range KeepScanRuns + 3 {
		if _, err := s.CreateScanRun(model.ScanRun{ProjectID: p.ID, Scan: "x", Status: model.ScanSucceeded, StartedAt: start.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	s = reopen(t, s, path)
	if n := len(s.ListScanRuns(p.ID, "x")); n != KeepScanRuns {
		t.Fatalf("%d runs after reopen, want %d", n, KeepScanRuns)
	}
}

func TestMigrateFromEmpty(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sub", "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	checkDB := func(db *sql.DB) {
		t.Helper()
		var v int
		if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != len(ms) {
			t.Fatalf("user_version %d (%v), want %d", v, err, len(ms))
		}
		var mode string
		if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
			t.Fatalf("journal_mode %q (%v), want wal", mode, err)
		}
		var timeout int
		if err := db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil || timeout <= 0 {
			t.Fatalf("busy_timeout %d (%v)", timeout, err)
		}
		for _, table := range dataTables {
			var n int
			if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
				t.Fatalf("%s: %d rows (%v)", table, n, err)
			}
		}
	}
	checkDB(s.db)
	if len(s.ListProjects()) != 0 || len(s.data.MainChats) != 0 {
		t.Fatal("a new database isn't empty")
	}
	// Migrating again is a no-op.
	s = reopen(t, s, path)
	checkDB(s.db)
	if err := migrate(s.db, nil); err != nil {
		t.Fatal(err)
	}
	// A database from a newer Dash is refused rather than misread.
	if _, err := s.db.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(s.db, nil); err == nil {
		t.Fatal("migrated a database newer than this Dash")
	}
}

func TestFailedWriteLeavesMemoryAsStored(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p, _ := s.CreateProject(model.Project{Name: "p"})
	// Make every insert of a todo fail.
	if _, err := s.db.Exec(`CREATE TRIGGER no_todos BEFORE INSERT ON todos BEGIN SELECT RAISE(ABORT, 'no'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: "x"}); err == nil {
		t.Fatal("todo created though the write failed")
	}
	if got := s.ListTodos(""); len(got) != 0 {
		t.Fatalf("memory kept a todo the database refused: %+v", got)
	}
	if _, err := s.UpdateProject(p.ID, func(p *model.Project) error { p.Name = "kept"; return nil }); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetProject(p.ID); got.Name != "kept" {
		t.Fatalf("project: %+v", got)
	}
}
