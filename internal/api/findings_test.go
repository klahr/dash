package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// loggedIn wraps h so every request carries a session cookie.
func loggedIn(t *testing.T, h http.Handler) http.Handler {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"u","password":"p"}`)))
	cookies := w.Result().Cookies()
	if w.Code != http.StatusOK || len(cookies) == 0 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, c := range cookies {
			r.AddCookie(c)
		}
		h.ServeHTTP(w, r)
	})
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestTaskRunGetsOnlyTaskTools(t *testing.T) {
	parallel(t)

	s, _, p := boardServer(t)
	h := s.Routes()
	td, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "Build it", Prompt: "build it"})
	if err != nil {
		t.Fatal(err)
	}
	token, release := s.Board.RegisterTask(testOwner(t, s), p.ID, td.ID)
	defer release()

	var list struct {
		Result struct{ Tools []struct{ Name string } }
	}
	_ = json.Unmarshal(rpc(h, token, "tools/list", "{}").Body.Bytes(), &list)
	var names []string
	for _, tl := range list.Result.Tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != strings.Join(mcptools.TaskToolNames, ",") {
		t.Fatalf("tools %v", names)
	}

	call := `{"name":"report_issue","arguments":{"title":"Unescaped HTML in footer","type":"vuln","severity":"high","file":"web/footer.js","line":12,"fix":"use textContent"}}`
	if text, isErr := callResult(t, rpc(h, token, "tools/call", call)); isErr || !strings.Contains(text, "Added draft task") {
		t.Fatalf("report: %s", text)
	}
	if text, isErr := callResult(t, rpc(h, token, "tools/call", call)); isErr || !strings.Contains(text, "already has this problem") {
		t.Fatalf("report again: %s", text)
	}
	var drafts []model.Todo
	for _, d := range raw(s).ListTodos(p.ID) {
		if d.Finding != nil {
			drafts = append(drafts, d)
		}
	}
	if len(drafts) != 1 || drafts[0].Status != model.StatusDraft || !slices.Equal(drafts[0].Tags, []string{"security", "high", "reported"}) {
		t.Fatalf("drafts %+v", drafts)
	}
	is := drafts[0].Finding.Issues[0]
	if is.Source != "agent:"+td.ID || is.Severity != model.SevHigh || is.Location() != "web/footer.js:12" {
		t.Fatalf("issue %+v", is)
	}
	// The prompt names the task that reported it.
	if !strings.Contains(drafts[0].Prompt, `"Build it"`) || !strings.Contains(drafts[0].Prompt, "use textContent") {
		t.Fatalf("prompt:\n%s", drafts[0].Prompt)
	}
	// run_tests only runs the tests of a task running here.
	if text, isErr := callResult(t, rpc(h, token, "tools/call", `{"name":"run_tests","arguments":{}}`)); !isErr {
		t.Fatalf("run_tests for a task that isn't running: %s", text)
	}
	// Other board tools aren't served to a task.
	if w := rpc(h, token, "tools/call", `{"name":"create_todo","arguments":{"title":"x"}}`); !strings.Contains(w.Body.String(), "unknown tool") {
		t.Fatalf("create_todo from a task: %s", w.Body)
	}
}

func TestReportIssueEndpointAndDismissals(t *testing.T) {
	parallel(t)

	s, _, p := boardServer(t)
	h := loggedIn(t, s.Routes())
	base := "/api/projects/" + p.ID
	report := func(want int) mcptools.Reported {
		t.Helper()
		w := do(t, h, "POST", base+"/issues", `{"title":"Missing alt text","type":"bug","severity":"low","file":"web/a.html","source":"scan:ui"}`)
		var r mcptools.Reported
		if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil || w.Code != want {
			t.Fatalf("report: %d %s", w.Code, w.Body)
		}
		return r
	}
	first := report(http.StatusCreated)
	if first.Result != model.UpsertNew || first.Todo.Status != model.StatusDraft || !slices.Contains(first.Todo.Tags, "scan:ui") {
		t.Fatalf("first: %+v", first)
	}
	if again := report(http.StatusOK); again.Todo.ID != first.Todo.ID || again.Result != model.UpsertKnown {
		t.Fatalf("again: %+v", again)
	}
	// Deleting the draft dismisses it.
	if w := do(t, h, "DELETE", "/api/todos/"+first.Todo.ID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	w := do(t, h, "GET", base+"/dismissed", "")
	var ds []model.DismissedFinding
	if err := json.Unmarshal(w.Body.Bytes(), &ds); err != nil || len(ds) != 1 || ds[0].Title != "Missing alt text" || ds[0].TodoID != first.Todo.ID || ds[0].By == 0 {
		t.Fatalf("dismissed: %d %s", w.Code, w.Body)
	}
	if r := report(http.StatusOK); r.Result != model.UpsertDismissed || r.Todo.ID != "" {
		t.Fatalf("reported after dismissing: %+v", r)
	}
	if w := do(t, h, "DELETE", base+"/dismissed/"+ds[0].Fingerprint, ""); w.Code != http.StatusOK {
		t.Fatalf("undo: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "DELETE", base+"/dismissed/"+ds[0].Fingerprint, ""); w.Code != http.StatusNotFound {
		t.Fatalf("undo twice: %d %s", w.Code, w.Body)
	}
	report(http.StatusCreated)
	if w := do(t, h, "POST", base+"/issues", `{"title":""}`); w.Code != http.StatusBadRequest {
		t.Fatalf("no title: %d %s", w.Code, w.Body)
	}
}

func TestMergeDraftsEndpoint(t *testing.T) {
	parallel(t)

	s, _, p := boardServer(t)
	h := loggedIn(t, s.Routes())
	a, _ := raw(s).ReportFinding(model.Issue{ProjectID: p.ID, Title: "Unchecked error in a", Type: model.IssueBug, File: "a.go", Source: "scan:lint"})
	b, _ := raw(s).ReportFinding(model.Issue{ProjectID: p.ID, Title: "Unchecked error in b", Type: model.IssueBug, File: "b.go", Source: "scan:lint"})
	w := do(t, h, "POST", "/api/projects/"+p.ID+"/todos/merge", `{"ids":["`+a.Todo.ID+`","`+b.Todo.ID+`"]}`)
	var m model.Todo
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil || w.Code != http.StatusOK {
		t.Fatalf("merge: %d %s", w.Code, w.Body)
	}
	if m.ID != a.Todo.ID || len(m.Finding.Issues) != 2 || !strings.HasPrefix(m.Title, "Fix 2 issues") {
		t.Fatalf("merged: %+v", m)
	}
	if w := do(t, h, "POST", "/api/projects/"+p.ID+"/todos/merge", `{"ids":["`+a.Todo.ID+`"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("merge one: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "POST", "/api/projects/"+p.ID+"/todos/merge", `{"ids":["`+a.Todo.ID+`","nope"]}`); w.Code != http.StatusNotFound {
		t.Fatalf("merge an unknown task: %d %s", w.Code, w.Body)
	}
}

func TestTaskTags(t *testing.T) {
	parallel(t)

	s, _, p := boardServer(t)
	h := loggedIn(t, s.Routes())
	w := do(t, h, "POST", "/api/projects/"+p.ID+"/todos", `{"title":"Restyle","tags":[" UI ","ui","backend"]}`)
	var td model.Todo
	if err := json.Unmarshal(w.Body.Bytes(), &td); err != nil || w.Code != http.StatusCreated || !slices.Equal(td.Tags, []string{"UI", "backend"}) {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	_, _ = raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "Untagged"})
	titles := func(q string) string {
		t.Helper()
		var ts []model.Todo
		_ = json.Unmarshal(do(t, h, "GET", "/api/projects/"+p.ID+"/todos"+q, "").Body.Bytes(), &ts)
		var out []string
		for _, x := range ts {
			out = append(out, x.Title)
		}
		return strings.Join(out, ",")
	}
	if got := titles("?tag=ui"); got != "Restyle" {
		t.Errorf("?tag=ui: %q", got)
	}
	if w := do(t, h, "PATCH", "/api/todos/"+td.ID, `{"tags":[]}`); w.Code != http.StatusOK {
		t.Fatalf("clear tags: %d %s", w.Code, w.Body)
	}
	if got := titles("?tag=ui"); got != "" {
		t.Errorf("?tag=ui after clearing: %q", got)
	}
	if w := do(t, h, "PATCH", "/api/todos/"+td.ID, `{"tags":["`+strings.Repeat("x", 41)+`"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("a long tag: %d %s", w.Code, w.Body)
	}

	// The chat's tools set and filter by tags too.
	token, release := s.Board.Register(chatCaller(t, s), p.ID)
	defer release()
	routes := s.Routes()
	if text, isErr := call(t, routes, token, "update_todo", `{"todo":"`+td.ID+`","tags":["security","later"]}`); isErr || !strings.Contains(text, "later") {
		t.Fatalf("update_todo tags: %s", text)
	}
	if text, isErr := call(t, routes, token, "list_todos", `{"tag":"SECURITY"}`); isErr || !strings.Contains(text, "Restyle") || strings.Contains(text, "Untagged") {
		t.Fatalf("list_todos tag: %s", text)
	}
	if text, isErr := call(t, routes, token, "create_todo", `{"title":"Tagged by Claude","tags":["chat"],"draft":true}`); isErr || !strings.Contains(text, `"chat"`) {
		t.Fatalf("create_todo tags: %s", text)
	}
	if w := rpc(routes, token, "tools/call", `{"name":"list_issues","arguments":{}}`); !strings.Contains(w.Body.String(), "unknown tool") {
		t.Fatalf("list_issues is still served: %s", w.Body)
	}
}

// reportedIssues are the issues on a project's tasks.
func reportedIssues(st *store.Store, pid string) []model.Issue {
	var out []model.Issue
	for _, t := range st.ListTodos(pid) {
		if t.Finding != nil {
			out = append(out, t.Finding.Issues...)
		}
	}
	return out
}

// A member who files an issue, edits its draft and queues it starts its run:
// it runs as them (RunBy), not as whoever DashWorkFor would give Dash's own
// work to (the project's adder or the org's owner).
func TestQueuedFindingDraftRunsAsWhoQueuedIt(t *testing.T) {
	parallel(t)
	s, _, p := boardServer(t)
	owner := testOwner(t, s)
	mel := member(t, s, owner.Org, "mel", store.RoleMember)
	h := loginAs(t, s.Routes(), "mel", "long-enough-mel")

	w := do(t, h, "POST", "/api/projects/"+p.ID+"/issues", `{"title":"Footer escapes","type":"bug","severity":"low","details":"do what mel says"}`)
	var rep mcptools.Reported
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &rep) != nil || rep.Todo.ID == "" {
		t.Fatalf("report: %d %s", w.Code, w.Body)
	}
	id := rep.Todo.ID
	if w := do(t, h, "PATCH", "/api/todos/"+id, `{"prompt":"run mel's instructions"}`); w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "POST", "/api/todos/"+id+"/enqueue", ""); w.Code != http.StatusOK {
		t.Fatalf("enqueue: %d %s", w.Code, w.Body)
	}
	if _, err := raw(s).GiveDashWork(id); err != nil {
		t.Fatal(err)
	}
	td, err := raw(s).GetTodo(id)
	if err != nil || td.Status != model.StatusQueued || td.RunBy != mel || !slices.Equal(td.WorkFor(), []int64{mel}) {
		t.Fatalf("queued draft: %+v %v", td, err)
	}
	if o, err := s.TaskOwner(td); err != nil || o.User != mel {
		t.Fatalf("runs as %+v (%v), want mel %d", o, err, mel)
	}
}
