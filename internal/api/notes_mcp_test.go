package api

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/notes"
	"github.com/klahr/dash/internal/store"
)

// A runner's task of a hub's project reaches the project's shared notes on
// this runner's copy (/mcp/notes): writes are credited to the hub's task.
// Its own projects' tasks, and runs that aren't tasks, get no notes.
func TestTaskNotesTools(t *testing.T) {
	s, _, own := boardServer(t)
	h := s.Routes()
	dir := t.TempDir()
	ident, err := notes.LoadIdentity(notes.IdentityPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	offline := &notes.HubTransport{Post: func(context.Context, string, []byte) ([]byte, error) { return nil, context.DeadlineExceeded }}
	svc, err := notes.NewService(filepath.Join(dir, "shared"), "r1", ident, offline, func(p string) bool { return p == "hub-p" })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	s.Notes = svc

	p, err := raw(s).CreateProject(model.Project{ID: store.NewID(), Name: "linked", Path: t.TempDir(), DefaultBranch: "main", HubProjectID: "hub-p", HubCheckout: model.HubCloned})
	if err != nil {
		t.Fatal(err)
	}
	td, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "build it", HubTodoID: "hub-t"})
	if err != nil {
		t.Fatal(err)
	}
	token, release := s.Board.RegisterTask(testOwner(t, s), p.ID, td.ID)
	defer release()

	call := `{"name":"shared_write","arguments":{"title":"Build","body":"Run make first.","tags":["build"]}}`
	if text, isErr := callResult(t, rpcAt(h, "/mcp/notes", token, "tools/call", call)); isErr || !strings.Contains(text, "Saved a new shared note") {
		t.Fatalf("write: %s", text)
	}
	es, err := svc.List("hub-p", notes.Query{})
	if err != nil || len(es) != 1 || es[0].AuthorTask != "hub-t" || es[0].Body != "Run make first." {
		t.Fatalf("notes %+v %v", es, err)
	}
	if _, n, err := s.TaskNotes(td.ID); err != nil || n != 1 {
		t.Fatalf("TaskNotes: %d %v", n, err)
	}
	if text, isErr := callResult(t, rpcAt(h, "/mcp/notes", token, "tools/call", `{"name":"shared_list","arguments":{}}`)); isErr || !strings.Contains(text, `"Build"`) {
		t.Fatalf("list: %s", text)
	}

	// A task of this runner's own project: no notes.
	mine, err := raw(s).CreateTodo(model.Todo{ProjectID: own.ID, Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.TaskNotes(mine.ID); err == nil {
		t.Fatal("own project's task has notes")
	}
	tok2, release2 := s.Board.RegisterTask(testOwner(t, s), own.ID, mine.ID)
	defer release2()
	if w := rpcAt(h, "/mcp/notes", tok2, "tools/list", "{}"); w.Code != 403 {
		t.Fatalf("own project's task: %d %s", w.Code, w.Body)
	}
	// A chat: no notes.
	chat, release3 := s.Board.Register(testOwner(t, s), p.ID)
	defer release3()
	if w := rpcAt(h, "/mcp/notes", chat, "tools/list", "{}"); w.Code != 403 {
		t.Fatalf("chat: %d %s", w.Code, w.Body)
	}
	// No notes service (the hub, or a Dash without one).
	s.Notes = nil
	if w := rpcAt(h, "/mcp/notes", token, "tools/list", "{}"); w.Code != 403 {
		t.Fatalf("without notes: %d %s", w.Code, w.Body)
	}
}
