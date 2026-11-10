package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
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

func TestTaskRunGetsOnlyIssueTools(t *testing.T) {
	s, _, p := boardServer(t)
	h := s.Routes()
	td, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "build it"})
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
	if strings.Join(names, ",") != strings.Join(mcptools.IssueToolNames, ",") {
		t.Fatalf("tools %v", names)
	}

	call := `{"name":"report_issue","arguments":{"title":"Unescaped HTML in footer","type":"vuln","severity":"high","file":"web/footer.js","line":12,"fix":"use textContent"}}`
	if text, isErr := callResult(t, rpc(h, token, "tools/call", call)); isErr || !strings.Contains(text, "new issue") {
		t.Fatalf("report: %s", text)
	}
	if text, isErr := callResult(t, rpc(h, token, "tools/call", call)); isErr || !strings.Contains(text, "already known") {
		t.Fatalf("report again: %s", text)
	}
	is := raw(s).ListIssues(p.ID)
	if len(is) != 1 || is[0].Source != "agent:"+td.ID || is[0].Severity != model.SevHigh || is[0].Location() != "web/footer.js:12" {
		t.Fatalf("issues %+v", is)
	}
	text, isErr := callResult(t, rpc(h, token, "tools/call", `{"name":"list_issues","arguments":{}}`))
	if isErr || !strings.Contains(text, "Unescaped HTML in footer") {
		t.Fatalf("list: %s", text)
	}
	// Other board tools aren't served to a task.
	if w := rpc(h, token, "tools/call", `{"name":"create_todo","arguments":{"title":"x"}}`); !strings.Contains(w.Body.String(), "unknown tool") {
		t.Fatalf("create_todo from a task: %s", w.Body)
	}
}

func TestIssuesFilteredByScanKind(t *testing.T) {
	s, _, p := boardServer(t)
	h := loggedIn(t, s.Routes())
	for _, is := range []model.Issue{
		{ProjectID: p.ID, Title: "Missing alt text", Type: model.IssueBug, Source: "scan:ui", ScanKind: model.ScanKindAccessibility},
		{ProjectID: p.ID, Title: "N+1 query", Type: model.IssueOther, Source: "scan:speed", ScanKind: model.ScanKindPerformance},
		{ProjectID: p.ID, Title: "Typo", Source: "manual"},
	} {
		if _, _, err := raw(s).UpsertIssue(is); err != nil {
			t.Fatal(err)
		}
	}
	base := "/api/projects/" + p.ID + "/issues"
	titles := func(query string) string {
		t.Helper()
		w := do(t, h, "GET", base+query, "")
		var is []model.Issue
		if err := json.Unmarshal(w.Body.Bytes(), &is); err != nil {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body)
		}
		var out []string
		for _, i := range is {
			out = append(out, i.Title)
		}
		return strings.Join(out, ",")
	}
	if got := titles("?kind=a11y"); got != "Missing alt text" {
		t.Errorf("kind=a11y: %q", got)
	}
	if got := titles("?kind=performance&type=issue"); got != "N+1 query" {
		t.Errorf("kind=performance: %q", got)
	}
	if got := titles("?kind=tests"); got != "" {
		t.Errorf("kind=tests: %q", got)
	}
	if w := do(t, h, "GET", base+"?kind=vibes", ""); w.Code != http.StatusBadRequest {
		t.Errorf("unknown kind: %d %s", w.Code, w.Body)
	}

	td, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	token, release := s.Board.RegisterTask(testOwner(t, s), p.ID, td.ID)
	defer release()
	text, isErr := callResult(t, rpc(s.Routes(), token, "tools/call", `{"name":"list_issues","arguments":{"kind":"perf"}}`))
	if isErr || !strings.Contains(text, "N+1 query") || strings.Contains(text, "alt text") || strings.Contains(text, "Typo") {
		t.Fatalf("list_issues kind=perf: %s", text)
	}
	if _, isErr := callResult(t, rpc(s.Routes(), token, "tools/call", `{"name":"list_issues","arguments":{"kind":"vibes"}}`)); !isErr {
		t.Fatal("list_issues accepted an unknown kind")
	}
}

func TestIssueTriageIntoTasks(t *testing.T) {
	s, _, p := boardServer(t)
	h := loggedIn(t, s.Routes())
	base := "/api/projects/" + p.ID + "/issues"

	if w := do(t, h, "POST", base, `{"title":""}`); w.Code != http.StatusBadRequest {
		t.Fatalf("no title: %d %s", w.Code, w.Body)
	}
	w := do(t, h, "POST", base+"/batch", `{"source":"scan:lint","issues":[
		{"title":"Unchecked error","type":"bug","severity":"low","file":"a.go","line":3,"details":"err ignored","fix":"handle it"},
		{"title":"Unchecked error","type":"bug","severity":"low","file":"b.go","line":8},
		{"title":"Debug endpoint exposed","type":"vuln","severity":"critical","file":"srv.go"}]}`)
	var counts model.UpsertCounts
	if err := json.Unmarshal(w.Body.Bytes(), &counts); err != nil || counts != (model.UpsertCounts{New: 3}) {
		t.Fatalf("batch: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "POST", base, `{"title":"Typo in README"}`); w.Code != http.StatusCreated {
		t.Fatalf("quick add: %d %s", w.Code, w.Body)
	}
	all := raw(s).ListIssues(p.ID)
	if all[0].Severity != model.SevCritical {
		t.Fatalf("not sorted by severity: %+v", all[0])
	}
	byTitle := func(title, file string) model.Issue {
		for _, is := range raw(s).ListIssues(p.ID) {
			if is.Title == title && is.File == file {
				return is
			}
		}
		t.Fatalf("no issue %q in %q", title, file)
		return model.Issue{}
	}
	a, b := byTitle("Unchecked error", "a.go"), byTitle("Unchecked error", "b.go")

	// One task for both instances of the pattern.
	w = do(t, h, "POST", base+"/tasks", `{"ids":["`+a.ID+`","`+b.ID+`"],"combine":true}`)
	var todos []model.Todo
	if err := json.Unmarshal(w.Body.Bytes(), &todos); err != nil || len(todos) != 1 {
		t.Fatalf("combine: %d %s", w.Code, w.Body)
	}
	td := todos[0]
	if td.Status != model.StatusDraft {
		t.Fatalf("status %s", td.Status)
	}
	for _, want := range []string{"Unchecked error", "a.go:3", "b.go:8", "err ignored", "handle it", "low", "bug"} {
		if !strings.Contains(td.Prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, td.Prompt)
		}
	}
	for _, id := range []string{a.ID, b.ID} {
		if is, _ := raw(s).GetIssue(id); is.Status != model.IssueTaskCreated || len(is.TodoIDs) != 1 || is.TodoIDs[0] != td.ID {
			t.Fatalf("issue after task: %+v", is)
		}
	}

	// Dismissing asks for a reason and keeps it; a dismissed issue gets no task.
	c := byTitle("Debug endpoint exposed", "srv.go")
	if w := do(t, h, "POST", base+"/dismiss", `{"ids":["`+c.ID+`"],"reason":"dev only"}`); w.Code != http.StatusOK {
		t.Fatalf("dismiss: %d %s", w.Code, w.Body)
	}
	if is, _ := raw(s).GetIssue(c.ID); is.Status != model.IssueDismissed || is.DismissReason != "dev only" {
		t.Fatalf("dismissed: %+v", is)
	}
	if w := do(t, h, "POST", base+"/tasks", `{"ids":["`+c.ID+`"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("task for a dismissed issue: %d %s", w.Code, w.Body)
	}
	// Reopen through PATCH; resolved → dismissed is refused.
	if w := do(t, h, "PATCH", "/api/issues/"+c.ID, `{"status":"open"}`); w.Code != http.StatusOK {
		t.Fatalf("reopen: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "PATCH", "/api/issues/"+c.ID, `{"status":"resolved"}`); w.Code != http.StatusOK {
		t.Fatalf("resolve: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "PATCH", "/api/issues/"+c.ID, `{"status":"dismissed"}`); w.Code != http.StatusConflict {
		t.Fatalf("resolved → dismissed: %d %s", w.Code, w.Body)
	}

	// One task each, queued.
	d := byTitle("Typo in README", "")
	w = do(t, h, "POST", base+"/tasks", `{"ids":["`+d.ID+`"],"enqueue":true}`)
	todos = nil
	if err := json.Unmarshal(w.Body.Bytes(), &todos); err != nil || len(todos) != 1 || todos[0].Status != model.StatusQueued || todos[0].Title != "Fix: Typo in README" {
		t.Fatalf("create & queue: %d %s", w.Code, w.Body)
	}
}
