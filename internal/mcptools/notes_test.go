package mcptools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klahr/dash/internal/notes"
)

// sendCounter is a notes.Transport that only counts what is sent; it can
// be made to hang, as an unreachable hub would.
type sendCounter struct {
	mu   sync.Mutex
	sent int
	hang chan struct{} // non-nil: sends wait for it
}

func (c *sendCounter) SendToPeers(ctx context.Context, _ string, _ []byte) (notes.Sent, error) {
	if c.hang != nil {
		select {
		case <-c.hang:
		case <-ctx.Done():
		}
	}
	c.mu.Lock()
	c.sent++
	c.mu.Unlock()
	return notes.Sent{}, nil
}

func (c *sendCounter) ReceiveFromPeers(notes.Receiver) {}

func (c *sendCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sent
}

// testNotes is project p1's notes on one runner, as task t1 uses them.
func testNotes(t *testing.T, tr notes.Transport) (*notes.Service, ServiceNotes) {
	t.Helper()
	dir := t.TempDir()
	id, err := notes.LoadIdentity(filepath.Join(dir, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := notes.NewService(filepath.Join(dir, "shared"), "r1", id, tr, func(p string) bool { return p == "p1" })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	return svc, ServiceNotes{S: svc, Project: "p1", Task: "t1"}
}

// call runs tool name of tools with args, returning its text and whether it failed.
func callNote(t *testing.T, tools []Tool, name string, args map[string]any) (string, bool) {
	t.Helper()
	s := &Server{Tools: tools}
	b, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	res, rerr := s.Handle(Request{Method: "tools/call", Params: b})
	if rerr != nil {
		t.Fatalf("%s: %v", name, rerr.Message)
	}
	m := res.(map[string]any)
	text := m["content"].([]any)[0].(map[string]string)["text"]
	return text, m["isError"] == true
}

func mustCall(t *testing.T, tools []Tool, name string, args map[string]any) string {
	t.Helper()
	text, failed := callNote(t, tools, name, args)
	if failed {
		t.Fatalf("%s %v: %s", name, args, text)
	}
	return text
}

func TestNotesToolsNames(t *testing.T) {
	var names []string
	for _, tool := range NotesTools(nil) {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != strings.Join(NotesToolNames, ",") {
		t.Fatalf("tools %v, names %v", names, NotesToolNames)
	}
}

func TestNotesToolsWriteListReadUpdate(t *testing.T) {
	tr := &sendCounter{}
	_, n := testNotes(t, tr)
	tools := NotesTools(n)

	if out := mustCall(t, tools, "shared_list", nil); out != "[]" {
		t.Fatalf("empty list: %s", out)
	}
	out := mustCall(t, tools, "shared_write", map[string]any{"title": "Running the e2e tests", "body": "Needs `make fixtures` first.", "tags": []any{"tests", "e2e"}})
	if !strings.Contains(out, "Saved a new shared note") {
		t.Fatalf("write: %s", out)
	}
	mustCall(t, tools, "shared_write", map[string]any{"title": "Deploy", "body": "Through the release workflow.", "tags": []any{"deploy"}, "permanent": true})

	var rows []struct {
		ID, Title, Author string
		Tags              []string
		Updated           time.Time
		Expires           *time.Time
		Permanent         bool
	}
	if err := json.Unmarshal([]byte(mustCall(t, tools, "shared_list", nil)), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows %+v", rows)
	}
	for _, r := range rows {
		if r.Author != "t1" || r.Updated.IsZero() || r.ID == "" {
			t.Errorf("row %+v", r)
		}
		switch r.Title {
		case "Deploy":
			if !r.Permanent || r.Expires != nil {
				t.Errorf("permanent note %+v", r)
			}
		case "Running the e2e tests":
			if r.Permanent || r.Expires == nil || time.Until(*r.Expires) < 29*24*time.Hour {
				t.Errorf("default expiry %+v", r)
			}
		}
	}

	// Filters.
	if out := mustCall(t, tools, "shared_list", map[string]any{"tag": "deploy"}); !strings.Contains(out, "Deploy") || strings.Contains(out, "e2e") {
		t.Fatalf("by tag: %s", out)
	}
	if out := mustCall(t, tools, "shared_list", map[string]any{"text": "FIXTURES"}); !strings.Contains(out, "e2e") || strings.Contains(out, "Deploy") {
		t.Fatalf("by text: %s", out)
	}
	// The list holds no bodies.
	if out := mustCall(t, tools, "shared_list", nil); strings.Contains(out, "make fixtures") {
		t.Fatalf("list shows bodies: %s", out)
	}

	// Read by title (any case) or id.
	var full struct{ Title, Body string }
	if err := json.Unmarshal([]byte(mustCall(t, tools, "shared_read", map[string]any{"title": "running THE e2e tests "})), &full); err != nil {
		t.Fatal(err)
	}
	if full.Body != "Needs `make fixtures` first." {
		t.Fatalf("read %+v", full)
	}
	byID := mustCall(t, tools, "shared_read", map[string]any{"id": rows[0].ID})
	if !strings.Contains(byID, rows[0].Title) {
		t.Fatalf("read by id: %s", byID)
	}

	// Writing the same title updates that note.
	out = mustCall(t, tools, "shared_write", map[string]any{"title": "running the E2E tests", "body": "Run `make e2e`; it builds the fixtures.", "expires_in_days": 3})
	if !strings.Contains(out, "Updated the shared note") {
		t.Fatalf("rewrite: %s", out)
	}
	es, _ := n.List(notes.Query{})
	if len(es) != 2 {
		t.Fatalf("rewrite added a note: %+v", es)
	}
	e, err := noteRef(n, Args{"title": "running the e2e tests"})
	if err != nil || !strings.Contains(e.Body, "make e2e") || time.Until(e.Expires) > 4*24*time.Hour {
		t.Fatalf("rewritten %+v %v", e, err)
	}

	// Every write left for the other runners, without waiting on the call.
	deadline := time.Now().Add(5 * time.Second)
	for tr.count() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if tr.count() < 3 {
		t.Fatalf("sent %d writes, want 3", tr.count())
	}
}

func TestNotesToolsConfirmAndDelete(t *testing.T) {
	svc, n := testNotes(t, &sendCounter{})
	var clock atomic.Int64 // read by the sends in the background too
	clock.Store(time.Now().UnixNano())
	svc.Now = func() time.Time { return time.Unix(0, clock.Load()) }
	tools := NotesTools(n)
	mustCall(t, tools, "shared_write", map[string]any{"title": "Flaky test", "body": "TestFoo fails under -race now and then.", "expires_in_days": 2})

	clock.Add(int64(24 * time.Hour))
	now := svc.Now()
	out := mustCall(t, tools, "shared_confirm", map[string]any{"title": "flaky test", "days": 10})
	if !strings.Contains(out, "Confirmed") {
		t.Fatalf("confirm: %s", out)
	}
	e, err := noteRef(n, Args{"title": "Flaky test"})
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(10 * 24 * time.Hour); !e.Expires.Equal(want.UTC()) {
		t.Fatalf("expires %v, want %v", e.Expires, want)
	}

	if _, failed := callNote(t, tools, "shared_confirm", map[string]any{"title": "Flaky test", "days": 0}); !failed {
		t.Fatal("confirmed for 0 days")
	}

	mustCall(t, tools, "shared_delete", map[string]any{"title": "Flaky test"})
	if out := mustCall(t, tools, "shared_list", nil); out != "[]" {
		t.Fatalf("after delete: %s", out)
	}
	for _, tool := range []string{"shared_read", "shared_delete", "shared_confirm"} {
		out, failed := callNote(t, tools, tool, map[string]any{"title": "Flaky test"})
		if !failed || !strings.Contains(out, `no shared note titled "Flaky test"`) {
			t.Errorf("%s of a deleted note: %v %s", tool, failed, out)
		}
	}
	if out, failed := callNote(t, tools, "shared_read", map[string]any{"id": "r1:999"}); !failed || !strings.Contains(out, "no shared note with id") {
		t.Errorf("unknown id: %s", out)
	}
	if out, failed := callNote(t, tools, "shared_read", nil); !failed || !strings.Contains(out, "title (or id) is required") {
		t.Errorf("no title: %s", out)
	}
}

func TestNotesToolsRefuse(t *testing.T) {
	_, n := testNotes(t, &sendCounter{})
	tools := NotesTools(n)
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"body": "x"}, "title is required"},
		{map[string]any{"title": "x"}, "body is required"},
		{map[string]any{"title": "x", "body": "y", "permanent": true, "expires_in_days": 3}, "not both"},
		{map[string]any{"title": "x", "body": "y", "expires_in_days": 0}, "at least 1"},
		{map[string]any{"title": "x", "body": strings.Repeat("a", notes.MaxEntrySize)}, "may hold at most 8192"},
		{map[string]any{"title": strings.Repeat("t", notes.MaxKeyLen+1), "body": "y"}, "the title is 201 bytes"},
		{map[string]any{"title": "x", "body": "y", "tags": []any{strings.Repeat("g", 65)}}, "a tag may have at most 64"},
		{map[string]any{"title": "x", "body": "y", "tags": []any{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15", "16", "17"}}, "17 tags"},
		// The secret scanner runs on the writing runner, before anything is saved.
		{map[string]any{"title": "CI token", "body": "use ghp_" + strings.Repeat("A1b2", 9)}, "looks like it holds a secret"},
	} {
		out, failed := callNote(t, tools, "shared_write", c.args)
		if !failed || !strings.Contains(out, c.want) {
			t.Errorf("write %v: %v %q, want an error with %q", c.args["title"], failed, out, c.want)
		}
	}
	if out := mustCall(t, tools, "shared_list", nil); out != "[]" {
		t.Fatalf("a refused write was saved: %s", out)
	}
}

// A project the runner keeps no notes for gets a clear error.
func TestNotesToolsNoProject(t *testing.T) {
	svc, _ := testNotes(t, &sendCounter{})
	tools := NotesTools(ServiceNotes{S: svc, Project: "other", Task: "t1"})
	out, failed := callNote(t, tools, "shared_list", nil)
	if !failed || !strings.Contains(out, "aren't available for this project") {
		t.Fatalf("list: %v %s", failed, out)
	}
}

// Writes are saved and answered at once, even while the relay hangs.
func TestNotesToolsDontWaitForRelay(t *testing.T) {
	tr := &sendCounter{hang: make(chan struct{})}
	defer close(tr.hang)
	_, n := testNotes(t, tr)
	tools := NotesTools(n)
	start := time.Now()
	mustCall(t, tools, "shared_write", map[string]any{"title": "a", "body": "b"})
	mustCall(t, tools, "shared_confirm", map[string]any{"title": "a"})
	mustCall(t, tools, "shared_delete", map[string]any{"title": "a"})
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("tools waited %v on the relay", d)
	}
}

func TestNotesHint(t *testing.T) {
	h := NotesHint(7)
	for _, want := range []string{"7 shared notes", "mcp__dash_notes__shared_list", "mcp__dash_notes__shared_write", "mcp__dash_notes__shared_confirm", "secrets", "permanent"} {
		if !strings.Contains(h, want) {
			t.Errorf("hint lacks %q: %s", want, h)
		}
	}
	if !strings.Contains(NotesHint(0), "no shared notes yet") || !strings.Contains(NotesHint(1), "1 shared note:") {
		t.Errorf("hint counts: %q / %q", NotesHint(0), NotesHint(1))
	}
}

func TestNotesHandler(t *testing.T) {
	_, n := testNotes(t, &sendCounter{})
	tokens := &Tokens{}
	h := &NotesHandler{Tokens: tokens, Notes: func(sc Scope) (Notes, error) {
		if sc.Todo != "t1" {
			return nil, notes.ErrNoProject
		}
		return n, nil
	}}
	post := func(token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/mcp/notes", strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	if w := post("", list); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", w.Code)
	}
	chat, release := tokens.Register(Caller{Org: 1, User: 1}, "proj")
	defer release()
	if w := post(chat, list); w.Code != http.StatusForbidden {
		t.Fatalf("chat token: %d", w.Code)
	}
	other, release2 := tokens.RegisterTask(Caller{Org: 1, User: 1}, "proj", "t2")
	defer release2()
	if w := post(other, list); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "aren't available") {
		t.Fatalf("task without notes: %d %s", w.Code, w.Body)
	}
	task, release3 := tokens.RegisterTask(Caller{Org: 1, User: 1}, "proj", "t1")
	w := post(task, list)
	if w.Code != http.StatusOK {
		t.Fatalf("task: %d %s", w.Code, w.Body)
	}
	for _, name := range NotesToolNames {
		if !strings.Contains(w.Body.String(), `"`+name+`"`) {
			t.Errorf("tools/list lacks %s: %s", name, w.Body)
		}
	}
	release3()
	if w := post(task, list); w.Code != http.StatusUnauthorized {
		t.Fatalf("released token: %d", w.Code)
	}
	if cfg := NotesServerConfig("http://127.0.0.1:9/", "tok"); cfg["url"] != "http://127.0.0.1:9/mcp/notes" {
		t.Fatalf("config %v", cfg)
	}
}
