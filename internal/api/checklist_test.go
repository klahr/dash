package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/klahr/dash/internal/checklist"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/queue"
)

const repoTodo = "# TODO\r\n\r\n## Next\r\n\r\n- [ ] Ship it\r\n      before Friday\r\n- [ ] Docs\r\n"

// checklistServer is boardServer with project web in a git repository whose
// main has todo as TODO.md ("" for none), and a merge worker.
func checklistServer(t *testing.T, todo string) (*Server, model.Project, http.Handler) {
	t.Helper()
	s, _, p := boardServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.Git = gitops.New(nil)
	dir := filepath.Join(t.TempDir(), "web")
	if err := s.Git.InitRepo(ctx, p.ID, dir, "main"); err != nil {
		t.Fatal(err)
	}
	if todo != "" {
		if err := os.WriteFile(filepath.Join(dir, "TODO.md"), []byte(todo), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, dir, "add", "TODO.md")
		gitIn(t, dir, "commit", "-qm", "todo")
	}
	p, err := raw(s).UpdateProject(p.ID, func(x *model.Project) error {
		x.Path, x.DefaultBranch, x.MergeMode = dir, "main", model.MergeLocal
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Merges = queue.NewMergeQueue(raw(s), s.Git, s.Hub, &queue.TodoLog{Root: t.TempDir()}, 4)
	s.Merges.WorktreeRoot = t.TempDir()
	go s.Merges.Run(ctx)
	h := s.mux()
	owned := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, asOwner(t, s, r)) })
	return s, p, owned
}

func readList(t *testing.T, h http.Handler, p model.Project) mcptools.Checklist {
	t.Helper()
	w := do(t, h, "GET", "/api/projects/"+p.ID+"/checklist", "")
	if w.Code != http.StatusOK {
		t.Fatalf("read: %d %s", w.Code, w.Body)
	}
	var c mcptools.Checklist
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func items(c mcptools.Checklist) []*checklist.Item {
	return checklist.Doc{Sections: c.Sections}.Items()
}

func change(t *testing.T, h http.Handler, p model.Project, version string, ops ...checklist.Op) (int, mcptools.ChecklistSaved, string) {
	t.Helper()
	b, _ := json.Marshal(mcptools.ChecklistChange{Version: &version, Ops: ops})
	w := do(t, h, "POST", "/api/projects/"+p.ID+"/checklist", string(b))
	var out mcptools.ChecklistSaved
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

// A change to a committed TODO.md is one commit on main, made without a
// task, and the checkout follows.
func TestChecklistCommitsToDefaultBranch(t *testing.T) {
	s, p, h := checklistServer(t, repoTodo)
	c := readList(t, h, p)
	if !c.Exists || c.Via != "commit" || c.Local || len(items(c)) != 2 || items(c)[0].More != "before Friday" {
		t.Fatalf("read: %+v", c)
	}
	before := gitIn(t, p.Path, "rev-parse", "main")
	ship := items(c)[0]
	code, out, body := change(t, h, p, c.Version,
		checklist.Op{Op: "check", Ref: ship.Ref, Checked: true},
		checklist.Op{Op: "add", Text: "Tests", Section: "Next"})
	if code != http.StatusOK {
		t.Fatalf("change: %d %s", code, body)
	}
	want := strings.Replace(repoTodo, "- [ ] Ship it", "- [x] Ship it", 1) + "- [ ] Tests\r\n"
	if got := gitIn(t, p.Path, "show", "main:TODO.md"); got != strings.TrimSpace(want) {
		t.Fatalf("on main:\n%q\nwant\n%q", got, want)
	}
	if b, _ := os.ReadFile(filepath.Join(p.Path, "TODO.md")); string(b) != want {
		t.Errorf("checkout: %q", b)
	}
	if parent := gitIn(t, p.Path, "rev-parse", "main^"); parent != before {
		t.Errorf("parent %s, want %s", parent, before)
	}
	if msg := gitIn(t, p.Path, "log", "-1", "--format=%s", "main"); msg != "TODO.md: check off Ship it (and 1 more)" {
		t.Errorf("message %q", msg)
	}
	if out.Commit != gitIn(t, p.Path, "rev-parse", "main") || out.Checklist.Version == c.Version || out.Refs[ship.Ref] == "" {
		t.Errorf("answer: %+v", out)
	}
	if ts := raw(s).ListTodos(p.ID); len(ts) != 0 {
		t.Errorf("tasks: %+v", ts)
	}
	if st := gitIn(t, p.Path, "status", "--porcelain"); st != "" {
		t.Errorf("checkout dirty: %s", st)
	}

	// The old version, or a stale ref, is a conflict and changes nothing.
	head := gitIn(t, p.Path, "rev-parse", "main")
	if code, _, body := change(t, h, p, c.Version, checklist.Op{Op: "check", Ref: items(c)[1].Ref, Checked: true}); code != http.StatusConflict {
		t.Errorf("old version: %d %s", code, body)
	}
	b, _ := json.Marshal(mcptools.ChecklistChange{Ops: []checklist.Op{{Op: "delete", Ref: ship.Ref}}})
	if w := do(t, h, "POST", "/api/projects/"+p.ID+"/checklist", string(b)); w.Code != http.StatusConflict {
		t.Errorf("stale ref: %d %s", w.Code, w.Body)
	}
	if now := gitIn(t, p.Path, "rev-parse", "main"); now != head {
		t.Error("main moved on a conflict")
	}
}

// Someone committed to TODO.md after it was read: the commit is refused.
func TestChecklistCommitConflict(t *testing.T) {
	_, p, h := checklistServer(t, repoTodo)
	c := readList(t, h, p)
	if err := os.WriteFile(filepath.Join(p.Path, "TODO.md"), []byte(repoTodo+"- [ ] Theirs\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, p.Path, "commit", "-qam", "theirs")
	head := gitIn(t, p.Path, "rev-parse", "main")
	code, _, body := change(t, h, p, c.Version, checklist.Op{Op: "check", Ref: items(c)[0].Ref, Checked: true})
	if code != http.StatusConflict || !strings.Contains(body, "changed") {
		t.Fatalf("conflict: %d %s", code, body)
	}
	if gitIn(t, p.Path, "rev-parse", "main") != head {
		t.Error("main moved")
	}
}

// An untracked TODO.md is saved in the checkout, with no commit.
func TestChecklistLocalFile(t *testing.T) {
	s, p, h := checklistServer(t, "")
	var told atomic.Int32 // checklist.updated events: open checklists read it again
	s.Merges.Remote.Pub = pubFunc(func(ev model.Event) {
		if ev.Type == model.EvChecklist && ev.ProjectID == p.ID {
			told.Add(1)
		}
	})
	if c := readList(t, h, p); c.Exists || c.Version != "" {
		t.Fatalf("no file: %+v", c)
	}
	if err := os.WriteFile(filepath.Join(p.Path, "TODO.md"), []byte("- [ ] Local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head := gitIn(t, p.Path, "rev-parse", "main")
	c := readList(t, h, p)
	if !c.Local || c.Via != "local" || len(items(c)) != 1 {
		t.Fatalf("local: %+v", c)
	}
	if code, _, body := change(t, h, p, c.Version, checklist.Op{Op: "check", Ref: items(c)[0].Ref, Checked: true}); code != http.StatusOK {
		t.Fatalf("change: %d %s", code, body)
	}
	if b, _ := os.ReadFile(filepath.Join(p.Path, "TODO.md")); string(b) != "- [x] Local\n" {
		t.Errorf("file: %q", b)
	}
	if n := told.Load(); n != 1 {
		t.Errorf("%d checklist.updated events for the saved local file, want 1", n)
	}
	if gitIn(t, p.Path, "rev-parse", "main") != head {
		t.Error("committed a local file")
	}
	// Changed on disk since: a conflict.
	if code, _, _ := change(t, h, p, c.Version, checklist.Op{Op: "add", Text: "x"}); code != http.StatusConflict {
		t.Errorf("stale local version: %d", code)
	}
}

// Without a TODO.md, adding an item commits a new one.
func TestChecklistCreatesFile(t *testing.T) {
	_, p, h := checklistServer(t, "")
	code, out, body := change(t, h, p, "", checklist.Op{Op: "add", Text: "First"})
	if code != http.StatusOK || !out.Checklist.Exists {
		t.Fatalf("add: %d %s", code, body)
	}
	if got := gitIn(t, p.Path, "show", "main:TODO.md"); got != "# TODO\n\n- [ ] First" {
		t.Errorf("TODO.md: %q", got)
	}
}

// A project in pull request mode gets a task that commits the change.
func TestChecklistPRModeQueuesTask(t *testing.T) {
	s, p, h := checklistServer(t, repoTodo)
	if _, err := raw(s).UpdateProject(p.ID, func(x *model.Project) error { x.MergeMode = model.MergePR; return nil }); err != nil {
		t.Fatal(err)
	}
	head := gitIn(t, p.Path, "rev-parse", "main")
	c := readList(t, h, p)
	if c.Via != "task" {
		t.Fatalf("via %q", c.Via)
	}
	code, out, body := change(t, h, p, c.Version, checklist.Op{Op: "check", Ref: items(c)[1].Ref, Checked: true})
	if code != http.StatusOK || out.Task == nil || len(out.Task.Edits) != 1 || !strings.Contains(out.Task.Edits[0].Content, "- [x] Docs") || out.Note == "" {
		t.Fatalf("change: %d %s", code, body)
	}
	if gitIn(t, p.Path, "rev-parse", "main") != head {
		t.Error("committed straight to main in PR mode")
	}
}

// Moving an item makes a draft or a queued task and removes the item with
// its continuation lines, in one commit.
func TestChecklistMoveToTask(t *testing.T) {
	s, p, h := checklistServer(t, repoTodo)
	move := func(ref string, queue bool) mcptools.ChecklistMoved {
		t.Helper()
		b, _ := json.Marshal(mcptools.ChecklistMove{Ref: ref, Queue: queue})
		w := do(t, h, "POST", "/api/projects/"+p.ID+"/checklist/move", string(b))
		if w.Code != http.StatusCreated {
			t.Fatalf("move: %d %s", w.Code, w.Body)
		}
		var out mcptools.ChecklistMoved
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	c := readList(t, h, p)
	m := move(items(c)[0].Ref, false)
	if !m.Removed || m.Task.Status != model.StatusDraft || m.Task.Title != "Ship it" ||
		!strings.Contains(m.Task.Prompt, "Ship it\nbefore Friday") || !strings.Contains(m.Task.Prompt, `"Next" section`) {
		t.Fatalf("draft: %+v", m)
	}
	if got := gitIn(t, p.Path, "show", "main:TODO.md"); got != "# TODO\r\n\r\n## Next\r\n\r\n- [ ] Docs" {
		t.Errorf("TODO.md: %q", got)
	}
	if msg := gitIn(t, p.Path, "log", "-1", "--format=%s", "main"); msg != "TODO.md: move Ship it to task "+m.Task.ID {
		t.Errorf("message %q", msg)
	}
	c = readList(t, h, p)
	m = move(items(c)[0].Ref, true)
	if !m.Removed || m.Task.Status != model.StatusQueued || m.Task.Title != "Docs" {
		t.Fatalf("queued: %+v", m)
	}
	if n := len(raw(s).ListTodos(p.ID)); n != 2 {
		t.Errorf("%d tasks", n)
	}
}

// TODO.md changes between the task and the removal: the task stays and
// the answer says the item is still there.
func TestChecklistMoveConflictKeepsTask(t *testing.T) {
	s, p, h := checklistServer(t, repoTodo)
	afterMoveTask = func() {
		if err := os.WriteFile(filepath.Join(p.Path, "TODO.md"), []byte(repoTodo+"- [ ] Theirs\r\n"), 0o644); err != nil {
			t.Error(err)
		}
		gitIn(t, p.Path, "commit", "-qam", "theirs")
	}
	t.Cleanup(func() { afterMoveTask = nil })
	c := readList(t, h, p)
	b, _ := json.Marshal(mcptools.ChecklistMove{Ref: items(c)[0].Ref})
	w := do(t, h, "POST", "/api/projects/"+p.ID+"/checklist/move", string(b))
	var m mcptools.ChecklistMoved
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	if w.Code != http.StatusCreated || m.Removed || m.Error == "" || m.Task.ID == "" {
		t.Fatalf("move: %d %s", w.Code, w.Body)
	}
	if ts := raw(s).ListTodos(p.ID); len(ts) != 1 || ts[0].ID != m.Task.ID {
		t.Errorf("tasks: %+v", ts)
	}
	if got := gitIn(t, p.Path, "show", "main:TODO.md"); !strings.Contains(got, "Ship it") {
		t.Errorf("item gone: %q", got)
	}
}

// The board's MCP tools reach the same checklist.
func TestChecklistBoardTools(t *testing.T) {
	s, p, _ := checklistServer(t, repoTodo)
	b := board{s.as(ownerCaller(t, s))}
	c, err := b.ReadChecklist(p.ID)
	if err != nil || len(items(c)) != 2 {
		t.Fatalf("read: %v %+v", err, c)
	}
	if _, err := b.ChangeChecklist(p.ID, mcptools.ChecklistChange{Ops: []checklist.Op{{Op: "check", Ref: items(c)[1].Ref, Checked: true}}}); err != nil {
		t.Fatal(err)
	}
	if msg := gitIn(t, p.Path, "log", "-1", "--format=%s", "main"); msg != "TODO.md: check off Docs" {
		t.Errorf("message %q", msg)
	}
	if _, err := b.ChangeChecklist(p.ID, mcptools.ChecklistChange{Ops: []checklist.Op{{Op: "check", Ref: items(c)[1].Ref}}}); err == nil || !strings.Contains(err.Error(), "read it again") {
		t.Errorf("stale ref: %v", err)
	}
}
