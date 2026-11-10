package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/preview"
	"github.com/klahr/dash/internal/runner"
	"github.com/klahr/dash/internal/store"
)

// twoOrgs is a Dash with two orgs. The first ("Default") belongs to u, the
// platform admin, and holds project "web" with a task in review (with a
// live preview), an issue, a scan run, chats, an upload, a running process
// and a waiting permission prompt. The second ("Beta") belongs to b, who
// must not see or touch any of it. c is a plain member of Default.
type twoOrgs struct {
	s       *Server
	h       http.Handler // the full router
	a, b, c http.Handler // h signed in as u, b and c
	web     model.Project
	todo    model.Todo
	issue   model.Issue
	run     model.ScanRun
	proc    model.Process
	perm    string // the waiting permission prompt's id
	upload  model.Attachment
	hook    model.Webhook
	beta    store.Org
	bUser   store.User
	cUser   store.User
}

func newTwoOrgs(t *testing.T) *twoOrgs {
	t.Helper()
	s, _, web := boardServer(t)
	s.UploadRoot = t.TempDir()
	s.ProjectRoot = t.TempDir()
	s.Procs = runner.NewManager(s.Hub)
	t.Cleanup(s.Procs.StopAll)
	s.Previews = preview.New(s.Procs, s.Hub, raw(s), "")
	s.Peers = &peer.Hub{Self: "here"}
	st := raw(s)
	acc := st.Accounts()

	owner := ownerSession(t, s)
	beta, err := acc.CreateOrg(owner.User.ID, "Beta")
	if err != nil {
		t.Fatal(err)
	}
	invite := func(org int64, name string) store.User {
		tok, _, err := acc.Invite(owner.User.ID, org, name+"@example.com", store.RoleMember)
		if err != nil {
			t.Fatal(err)
		}
		u, _, err := acc.RegisterWithInvitation(tok, name, name+"-password")
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	bUser := invite(beta.ID, "b")
	// u is in Beta only to create it and invite b; b's org is b's alone.
	if err := acc.RemoveMember(owner.User.ID, beta.ID, owner.User.ID); err == nil {
		t.Log("owner left Beta")
	}
	cUser := invite(owner.Org.ID, "c")

	web, err = st.UpdateProject(web.ID, func(p *model.Project) error {
		p.Path, p.RunCommand = t.TempDir(), "sleep 60"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	td, err := st.CreateTodo(model.Todo{ProjectID: web.ID, Title: "secret task", Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	td, err = st.UpdateTodo(td.ID, func(x *model.Todo) error {
		x.Worktree = &model.Worktree{Name: "w", Path: t.TempDir(), Branch: "dash/w"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []model.TodoStatus{model.StatusQueued, model.StatusInProgress, model.StatusReview} {
		if td, err = st.SetStatus(td.ID, to, ""); err != nil {
			t.Fatal(err)
		}
	}
	is, _, err := st.UpsertIssue(model.Issue{ProjectID: web.ID, Title: "secret issue"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.CreateScanRun(model.ScanRun{ProjectID: web.ID, Scan: "security", Status: model.ScanSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	secret := []model.ChatMessage{{Role: "user", Content: "secret question"}, {Role: "assistant", Content: "secret answer"}}
	if err := st.SetTodoChat(td.ID, secret); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectChat(web.ID, secret); err != nil {
		t.Fatal(err)
	}
	if err := st.SetChat(owner.Org.ID, owner.User.ID, secret); err != nil {
		t.Fatal(err)
	}
	hook, err := st.CreateWebhook(model.Webhook{ProjectID: web.ID, Name: "secret hook", URL: "https://hooks.example.com/x", Format: "json", Enabled: true, Secret: "hook-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Previews.Start(td, web); err != nil {
		t.Fatal(err)
	}
	proc, err := s.Procs.StartTask(web.ID, web.Path, "sleep 60")
	if err != nil {
		t.Fatal(err)
	}

	o := &twoOrgs{s: s, h: s.Routes(), web: web, todo: td, issue: is, run: run, proc: proc, hook: hook, beta: beta, bUser: bUser, cUser: cUser}
	o.a = loggedIn(t, o.h)
	o.b = loginAs(t, o.h, "b", "b-password")
	o.c = loginAs(t, o.h, "c", "c-password")

	// An upload in Default.
	w := do(t, o.a, "POST", "/api/uploads?name=shot.png", "png")
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &o.upload) != nil {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}

	// A permission prompt of the task, waiting for an answer.
	runOwner, err := s.TaskOwner(td)
	if err != nil {
		t.Fatal(err)
	}
	token, release := s.Perms.Register(runOwner, td.ID, web.ID)
	t.Cleanup(release)
	go func() {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"permission_prompt","arguments":{"tool_name":"Bash","input":{"command":"ls"}}}}`
		r := httptest.NewRequest("POST", "/mcp/permissions", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		s.Perms.ServeMCP(httptest.NewRecorder(), r)
	}()
	waitFor(t, "the permission prompt", func() bool { return len(s.Perms.Pending("")) == 1 })
	o.perm = s.Perms.Pending("")[0].ID
	return o
}

// loginAs wraps h so every request carries name's session cookie.
func loginAs(t *testing.T, h http.Handler, name, password string) http.Handler {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/login", strings.NewReader(fmt.Sprintf(`{"username":%q,"password":%q}`, name, password))))
	cookies := w.Result().Cookies()
	if w.Code != http.StatusOK || len(cookies) == 0 {
		t.Fatalf("login %s: %d %s", name, w.Code, w.Body)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, c := range cookies {
			r.AddCookie(c)
		}
		h.ServeHTTP(w, r)
	})
}

// untouched fails the test if anything of Default's changed.
func (o *twoOrgs) untouched(t *testing.T, after string) {
	t.Helper()
	st := raw(o.s)
	if p, err := st.GetProject(o.web.ID); err != nil || p.Name != o.web.Name || p.RunCommand != o.web.RunCommand || p.Color != "" {
		t.Fatalf("after %s: project changed: %+v %v", after, p, err)
	}
	if td, err := st.GetTodo(o.todo.ID); err != nil || td.Title != o.todo.Title || td.Status != model.StatusReview || len(td.Feedback) != 0 {
		t.Fatalf("after %s: task changed: %+v %v", after, td, err)
	}
	if n := len(st.ListTodos(o.web.ID)); n != 1 {
		t.Fatalf("after %s: %d tasks in the project", after, n)
	}
	if is, err := st.GetIssue(o.issue.ID); err != nil || is.Status != model.IssueOpen || is.Title != o.issue.Title {
		t.Fatalf("after %s: issue changed: %+v %v", after, is, err)
	}
	if n := len(st.ListIssues(o.web.ID)); n != 1 {
		t.Fatalf("after %s: %d issues in the project", after, n)
	}
	if len(st.TodoChat(o.todo.ID)) != 2 || len(st.ProjectChat(o.web.ID)) != 2 {
		t.Fatalf("after %s: chats changed", after)
	}
	if len(st.ScanTemplates(o.web.ID)) != 0 {
		t.Fatalf("after %s: scan added", after)
	}
	if _, has := st.ScanSetting(o.web.ID, "security"); has {
		t.Fatalf("after %s: scan settings changed", after)
	}
	if pv, ok := o.s.Previews.Get(o.todo.ID); !ok || pv.State == model.PreviewStopped {
		t.Fatalf("after %s: preview stopped", after)
	}
	if p, err := o.s.Procs.Get(o.proc.ID); err != nil || !p.Running {
		t.Fatalf("after %s: process stopped", after)
	}
	if len(o.s.Perms.Pending("")) != 1 {
		t.Fatalf("after %s: permission prompt answered", after)
	}
	if h, err := st.GetWebhook(o.hook.ID); err != nil || h.Name != o.hook.Name || h.URL != o.hook.URL || h.Secret != o.hook.Secret || !h.Enabled {
		t.Fatalf("after %s: webhook changed: %+v %v", after, h, err)
	}
	if hooks, _ := st.ListWebhooks(o.web.ID); len(hooks) != 1 {
		t.Fatalf("after %s: %d webhooks in the project", after, len(hooks))
	}
}

// endpoint is one API call.
type endpoint struct{ method, path, body string }

// recordEndpoints are the calls that read or change one of Default's
// records by naming it: none may reach it for anyone outside Default.
func (o *twoOrgs) recordEndpoints() []endpoint {
	pid, tid, iid, rid, hid := o.web.ID, o.todo.ID, o.issue.ID, o.run.ID, o.hook.ID
	return []endpoint{
		{"GET", "/api/projects/" + pid, ""},
		{"PATCH", "/api/projects/" + pid, `{"name":"mine","color":"#000000"}`},
		{"DELETE", "/api/projects/" + pid, `{"password":"b-password","delete_files":true}`},
		{"POST", "/api/projects/" + pid + "/push", `{}`},
		{"GET", "/api/projects/" + pid + "/unpushed", ""},
		{"GET", "/api/projects/" + pid + "/remote", ""},
		{"POST", "/api/projects/" + pid + "/remote/fetch", ""},
		{"GET", "/api/projects/" + pid + "/graph", ""},
		{"GET", "/api/projects/" + pid + "/files", ""},
		{"GET", "/api/projects/" + pid + "/file?path=README.md", ""},
		{"PUT", "/api/projects/" + pid + "/file", `{"path":"x.txt","content":"x"}`},
		{"POST", "/api/projects/" + pid + "/edits", `{"title":"x","edits":[{"path":"x.txt","content":"x"}]}`},
		{"GET", "/api/projects/" + pid + "/todos", ""},
		{"POST", "/api/projects/" + pid + "/todos", `{"title":"intruder"}`},
		{"GET", "/api/projects/" + pid + "/queues", ""},
		{"GET", "/api/projects/" + pid + "/issues", ""},
		{"POST", "/api/projects/" + pid + "/issues", `{"title":"intruder"}`},
		{"POST", "/api/projects/" + pid + "/issues/batch", `{"issues":[{"title":"intruder"}]}`},
		{"POST", "/api/projects/" + pid + "/issues/tasks", `{"ids":["` + iid + `"]}`},
		{"POST", "/api/projects/" + pid + "/issues/dismiss", `{"ids":["` + iid + `"]}`},
		{"GET", "/api/projects/" + pid + "/scans", ""},
		{"POST", "/api/projects/" + pid + "/scans/security/run", ""},
		{"PATCH", "/api/projects/" + pid + "/scans/security", `{"enabled":false}`},
		{"POST", "/api/projects/" + pid + "/scans", `{"name":"mine","prompt":"look"}`},
		{"PUT", "/api/projects/" + pid + "/scans/mine", `{"prompt":"look"}`},
		{"DELETE", "/api/projects/" + pid + "/scans/mine", ""},
		{"GET", "/api/projects/" + pid + "/scan-runs", ""},
		{"GET", "/api/projects/" + pid + "/chat", ""},
		{"DELETE", "/api/projects/" + pid + "/chat", ""},
		{"POST", "/api/projects/" + pid + "/cleanup", ""},
		{"GET", "/api/projects/" + pid + "/ci", ""},
		{"POST", "/api/projects/" + pid + "/ci/refresh", ""},
		{"POST", "/api/projects/" + pid + "/ci/investigate", ""},
		{"POST", "/api/projects/" + pid + "/ci/retry", ""},
		{"GET", "/api/projects/" + pid + "/processes", ""},
		{"POST", "/api/projects/" + pid + "/processes", `{"kind":"task","command":"true"}`},

		{"POST", "/api/projects/" + pid + "/remote/pull", `{}`},
		{"GET", "/api/projects/" + pid + "/checkouts", ""},
		{"GET", "/api/projects/" + pid + "/branches", ""},
		{"PUT", "/api/projects/" + pid + "/branch", `{"branch":"main"}`},
		{"POST", "/api/projects/" + pid + "/branch/pr", `{}`},
		{"GET", "/api/projects/" + pid + "/release", ""},
		{"POST", "/api/projects/" + pid + "/tags", `{"tag":"v9.9.9"}`},
		{"GET", "/api/projects/" + pid + "/tag-format", ""},
		{"GET", "/api/projects/" + pid + "/repo-info", ""},
		{"GET", "/api/projects/" + pid + "/claude-files", ""},
		{"GET", "/api/projects/" + pid + "/webhooks", ""},
		{"POST", "/api/projects/" + pid + "/webhooks", `{"name":"intruder","url":"https://evil.example.com","format":"json"}`},
		{"PATCH", "/api/webhooks/" + hid, `{"name":"mine","url":"https://evil.example.com"}`},
		{"DELETE", "/api/webhooks/" + hid, ""},
		{"POST", "/api/webhooks/" + hid + "/rotate", ""},
		{"POST", "/api/webhooks/" + hid + "/test", ""},

		{"GET", "/api/todos/" + tid, ""},
		{"PATCH", "/api/todos/" + tid, `{"title":"mine"}`},
		{"DELETE", "/api/todos/" + tid, ""},
		{"POST", "/api/todos/" + tid + "/enqueue", ""},
		{"POST", "/api/todos/" + tid + "/cancel", ""},
		{"POST", "/api/todos/" + tid + "/pause", ""},
		{"POST", "/api/todos/" + tid + "/resume", ""},
		{"GET", "/api/todos/" + tid + "/diff", ""},
		{"GET", "/api/todos/" + tid + "/log", ""},
		{"GET", "/api/todos/" + tid + "/events", ""},
		{"POST", "/api/todos/" + tid + "/approve", ""},
		{"POST", "/api/todos/" + tid + "/rework", `{"feedback":"mine"}`},
		{"POST", "/api/todos/" + tid + "/test", ""},
		{"POST", "/api/todos/" + tid + "/duplicate", ""},
		{"POST", "/api/todos/" + tid + "/open", ""},
		{"POST", "/api/todos/" + tid + "/revert", ""},
		{"GET", "/api/todos/" + tid + "/chat", ""},
		{"DELETE", "/api/todos/" + tid + "/chat", ""},
		{"POST", "/api/todos/" + tid + "/preview", ""},
		{"DELETE", "/api/todos/" + tid + "/preview", ""},
		{"GET", "/api/todos/" + tid + "/preview/log", ""},

		{"PATCH", "/api/issues/" + iid, `{"status":"dismissed"}`},
		{"DELETE", "/api/issues/" + iid, ""},
		{"GET", "/api/scan-runs/" + rid, ""},
		{"GET", "/api/scan-runs/" + rid + "/log", ""},
		{"POST", "/api/scan-runs/" + rid + "/cancel", ""},

		{"DELETE", "/api/processes/" + o.proc.ID, ""},
		{"GET", "/ws/proc/" + o.proc.ID, ""},
		{"POST", "/api/permissions/" + o.perm, `{"decision":"allow"}`},
		{"GET", "/api/uploads/" + o.upload.ID + "/" + o.upload.Name, ""},
	}
}

// Every endpoint that names a record of another org answers as if it didn't
// exist, and changes nothing.
func TestOrgCannotReachAnotherOrgsRecords(t *testing.T) {
	o := newTwoOrgs(t)
	pid, tid := o.web.ID, o.todo.ID
	for _, tc := range o.recordEndpoints() {
		w := do(t, o.b, tc.method, tc.path, tc.body)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s as another org: want 404, got %d %s", tc.method, tc.path, w.Code, strings.TrimSpace(w.Body.String()))
		}
		for _, secret := range []string{"secret", o.web.Name} {
			if strings.Contains(w.Body.String(), secret) {
				t.Errorf("%s %s leaked %q: %s", tc.method, tc.path, secret, w.Body)
			}
		}
		o.untouched(t, tc.method+" "+tc.path)
	}

	// The same calls as Default's own member work (a sample), so the 404s
	// above are about the org and not the request.
	for _, path := range []string{"/api/projects/" + pid, "/api/todos/" + tid, "/api/todos/" + tid + "/chat", "/api/projects/" + pid + "/issues"} {
		if w := do(t, o.c, "GET", path, ""); w.Code != http.StatusOK {
			t.Errorf("GET %s as a member of the org: %d %s", path, w.Code, w.Body)
		}
	}
}

// Lists show only the caller's org's records.
func TestOrgListsAreScoped(t *testing.T) {
	o := newTwoOrgs(t)
	lists := []string{"/api/projects", "/api/previews", "/api/permissions", "/api/chat", "/api/machines"}
	for _, path := range lists {
		w := do(t, o.b, "GET", path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
		}
		for _, secret := range []string{o.web.ID, o.todo.ID, "secret", `"other"`} {
			if strings.Contains(w.Body.String(), secret) {
				t.Errorf("GET %s as another org shows %s: %s", path, secret, w.Body)
			}
		}
	}
	for _, path := range []string{"/api/projects", "/api/previews", "/api/permissions"} {
		if w := do(t, o.a, "GET", path, ""); !strings.Contains(w.Body.String(), o.web.ID) {
			t.Errorf("GET %s as the org itself lacks its project: %s", path, w.Body)
		}
	}
	// Stopping "all" previews stops only the org's own.
	if w := do(t, o.b, "DELETE", "/api/previews", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"stopped":0`) {
		t.Errorf("DELETE /api/previews as another org: %d %s", w.Code, w.Body)
	}
	o.untouched(t, "listing")
}

// Each user has a general chat of their own, also within one org.
func TestGeneralChatIsPerUser(t *testing.T) {
	o := newTwoOrgs(t)
	for name, h := range map[string]http.Handler{"another org": o.b, "the same org": o.c} {
		if w := do(t, h, "GET", "/api/chat", ""); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
			t.Errorf("chat of a user in %s: %d %s", name, w.Code, w.Body)
		}
		if w := do(t, h, "DELETE", "/api/chat", ""); w.Code != http.StatusNoContent {
			t.Errorf("clear chat in %s: %d", name, w.Code)
		}
	}
	if w := do(t, o.a, "GET", "/api/chat", ""); !strings.Contains(w.Body.String(), "secret question") {
		t.Errorf("owner's chat gone after others cleared theirs: %s", w.Body)
	}
}

// The chat socket only reaches the caller's org's projects and tasks, and
// saves who asked.
func TestChatSocketIsScoped(t *testing.T) {
	o := newTwoOrgs(t)
	o.s.Chat = stubChat{}
	srv := httptest.NewServer(o.b)
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	q := []agent.ChatMessage{{Role: "user", Content: "what's in there?"}}
	for _, target := range []map[string]any{{"project": o.web.ID}, {"todo": o.todo.ID}} {
		msg := map[string]any{"type": "send", "messages": q}
		for k, v := range target {
			msg[k] = v
		}
		if err := conn.WriteJSON(msg); err != nil {
			t.Fatal(err)
		}
		var d agent.ChatDelta
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if err := conn.ReadJSON(&d); err != nil || d.Kind != "error" {
			t.Errorf("chat about another org's %v: %+v %v", target, d, err)
		}
	}
	o.untouched(t, "chatting")

	// b's own general chat saves b as the author of the question.
	if err := conn.WriteJSON(map[string]any{"type": "send", "messages": q}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the question to be saved", func() bool { return len(raw(o.s).Chat(o.beta.ID, o.bUser.ID)) == 1 })
	if got := raw(o.s).Chat(o.beta.ID, o.bUser.ID); got[0].CreatedBy != o.bUser.ID {
		t.Errorf("question saved without its author: %+v", got)
	}
}

// Dashboards receive only their org's events, and those about Dash as a whole.
func TestEventsAreScopedToTheOrg(t *testing.T) {
	o := newTwoOrgs(t)
	srv := httptest.NewServer(o.h)
	t.Cleanup(srv.Close)
	dial := func(name, password string) *websocket.Conn {
		w := httptest.NewRecorder()
		o.h.ServeHTTP(w, httptest.NewRequest("POST", "/api/login", strings.NewReader(fmt.Sprintf(`{"username":%q,"password":%q}`, name, password))))
		hdr := http.Header{}
		for _, c := range w.Result().Cookies() {
			hdr.Add("Cookie", c.Name+"="+c.Value)
		}
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/events", hdr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	a, b := dial("u", "p"), dial("b", "b-password")
	waitFor(t, "both dashboards", func() bool { return o.s.Hub.Clients() == 2 })

	beta, err := o.s.Store.ForOrg(o.beta.ID).CreateProject(model.Project{Name: "beta-only", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// Default's events: by org, by project (a publisher that only knows
	// the project), and of a deleted project of Default's.
	o.s.Hub.Publish(model.Event{Type: model.EvTodoUpdated, ProjectID: o.web.ID, TodoID: o.todo.ID, Data: "secret 1", OrgID: ownerSession(t, o.s).Org.ID})
	o.s.Hub.Publish(model.Event{Type: model.EvPreview, ProjectID: o.web.ID, TodoID: o.todo.ID, Data: "secret 2"})
	o.s.Hub.Publish(model.Event{Type: model.EvProjectDeleted, ProjectID: "gone", Data: "secret 3"})
	o.s.Hub.Publish(model.Event{Type: model.EvUsage, Data: "for everyone"})
	o.s.Hub.Publish(model.Event{Type: model.EvProjectUpdated, ProjectID: beta.ID, Data: "beta's"})
	o.s.Hub.Publish(model.Event{Type: model.EvUsage, Data: "the end"})

	read := func(conn *websocket.Conn) model.Event {
		t.Helper()
		var ev struct {
			model.Event
			Data any `json:"data"`
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if err := conn.ReadJSON(&ev); err != nil {
			t.Fatal(err)
		}
		ev.Event.Data = ev.Data
		return ev.Event
	}
	if ev := read(b); ev.Type != model.EvUsage {
		t.Fatalf("another org's dashboard got %s %v first", ev.Type, ev.Data)
	}
	if ev := read(b); ev.ProjectID != beta.ID {
		t.Fatalf("another org's dashboard got %s %v", ev.Type, ev.Data)
	}
	for _, want := range []string{"secret 1", "secret 2", "for everyone"} {
		if ev := read(a); ev.Data != want {
			t.Fatalf("the org's dashboard got %v, want %q", ev.Data, want)
		}
	}
	if ev := read(a); ev.Data != "the end" {
		t.Fatalf("the org's dashboard got the other org's event: %s %v", ev.Type, ev.Data)
	}
}

// The chat's board tools act in the chat user's org.
func TestBoardMCPIsScoped(t *testing.T) {
	o := newTwoOrgs(t)
	token, release := o.s.Board.Register(mcptools.Caller{Org: o.beta.ID, User: o.bUser.ID}, "")
	defer release()
	text, _ := call(t, o.h, token, "list_projects", `{}`)
	if strings.Contains(text, o.web.ID) || strings.Contains(text, "other") {
		t.Errorf("list_projects shows another org's projects: %s", text)
	}
	for _, c := range []struct{ tool, args string }{
		{"list_todos", `{"project":"` + o.web.ID + `"}`},
		{"list_todos", `{"project":"web"}`},
		{"get_todo", `{"todo":"` + o.todo.ID + `"}`},
		{"create_todo", `{"project":"` + o.web.ID + `","title":"intruder"}`},
		{"update_todo", `{"todo":"` + o.todo.ID + `","title":"mine"}`},
		{"queue_todo", `{"todo":"` + o.todo.ID + `"}`},
		{"delete_todo", `{"todo":"` + o.todo.ID + `"}`},
		{"list_issues", `{"project":"` + o.web.ID + `"}`},
		{"report_issue", `{"project":"` + o.web.ID + `","title":"intruder"}`},
	} {
		text, isErr := call(t, o.h, token, c.tool, c.args)
		if !isErr || strings.Contains(text, "secret") {
			t.Errorf("%s %s from another org: %v %s", c.tool, c.args, isErr, text)
		}
	}
	o.untouched(t, "board tools")

	// A token without an org gets nothing.
	bad, release2 := o.s.Board.Register(mcptools.Caller{}, "")
	defer release2()
	if w := rpc(o.h, bad, "tools/list", "{}"); w.Code != http.StatusForbidden {
		t.Errorf("token without an org: %d %s", w.Code, w.Body)
	}
}

// /mcp (claude.ai connectors) calls the service as the token's user, not
// the REST API past the login.
func TestRemoteMCPIsScoped(t *testing.T) {
	o := newTwoOrgs(t)
	h := o.s.remoteMCP()
	callAs := func(sess store.Session, tool, args string) (string, bool) {
		t.Helper()
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, args)
		r := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
		r = r.WithContext(auth.WithSession(r.Context(), sess))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return callResult(t, w)
	}
	ms, _ := o.s.Store.Accounts().Memberships(o.bUser.ID)
	b := store.Session{User: o.bUser, Org: &ms[0]}
	if text, _ := callAs(b, "list_projects", `{}`); strings.Contains(text, o.web.ID) {
		t.Errorf("list_projects shows another org's projects: %s", text)
	}
	for _, c := range []struct{ tool, args string }{
		{"get_todo", `{"todo":"` + o.todo.ID + `"}`},
		{"get_diff", `{"todo":"` + o.todo.ID + `"}`},
		{"approve_todo", `{"todo":"` + o.todo.ID + `"}`},
		{"rework_todo", `{"todo":"` + o.todo.ID + `","feedback":"x"}`},
		{"delete_todo", `{"todo":"` + o.todo.ID + `"}`},
		{"update_project", `{"project":"` + o.web.ID + `","name":"mine"}`},
		{"get_project", `{"project":"web"}`},
	} {
		if text, isErr := callAs(b, c.tool, c.args); !isErr || strings.Contains(text, "secret") {
			t.Errorf("%s from another org: %v %s", c.tool, isErr, text)
		}
	}
	o.untouched(t, "remote tools")
	if text, _ := callAs(ownerSession(t, o.s), "get_todo", `{"todo":"`+o.todo.ID+`"}`); !strings.Contains(text, "secret task") {
		t.Errorf("the org's own get_todo: %s", text)
	}
}

// A member's new projects go in their org's folder; platform admins may
// put them anywhere.
func TestNewProjectsStayInTheOrgsFolder(t *testing.T) {
	o := newTwoOrgs(t)
	o.s.Git = nil // no git needed to be refused
	outside, root := t.TempDir(), o.s.ProjectRoot
	refuse := func(h http.Handler, who string, paths ...string) {
		t.Helper()
		for _, path := range paths {
			body, _ := json.Marshal(map[string]string{"name": "x", "path": path})
			if w := do(t, h, "POST", "/api/projects", string(body)); w.Code != http.StatusBadRequest {
				t.Errorf("%s adopting %s: %d %s", who, path, w.Code, w.Body)
			}
		}
	}
	refuse(o.c, "a member", outside, root, filepath.Join(root, "beta", "x"), filepath.Join(root, "default", "..", "beta", "x"),
		filepath.Join(root, "default"), o.web.Path)
	refuse(o.b, "another org", outside, root, filepath.Join(root, "default", "x"), filepath.Join(root, "beta", "..", "default", "x"),
		filepath.Join(root, "beta"), o.web.Path)
	c := Caller{User: o.cUser.ID, Org: ownerSession(t, o.s).Org.ID, OrgFolder: "default"}
	if p, err := o.s.as(c).projectPath("My App", ""); err != nil || p != filepath.Join(o.s.ProjectRoot, "default", "my-app") {
		t.Errorf("default path: %s %v", p, err)
	}
	if p, err := o.s.as(c).projectPath("x", filepath.Join(o.s.ProjectRoot, "default", "x")); err != nil {
		t.Errorf("inside the org's folder: %s %v", p, err)
	}
	admin := ownerCaller(t, o.s)
	if p, err := o.s.as(admin).projectPath("x", outside); err != nil || p != outside {
		t.Errorf("platform admin adopting %s: %s %v", outside, p, err)
	}
}

// Other computers, runners and the queue pause belong to the whole Dash.
func TestInstanceThingsAreForPlatformAdmins(t *testing.T) {
	o := newTwoOrgs(t)
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/queue/pause"},
		{"POST", "/api/queue/resume"},
		{"GET", "/peer/work/api/projects"},
	} {
		if w := do(t, o.c, c.method, c.path, ""); w.Code != http.StatusForbidden {
			t.Errorf("%s %s as a member: %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
	var ms []map[string]any
	if w := do(t, o.c, "GET", "/api/machines", ""); json.Unmarshal(w.Body.Bytes(), &ms) != nil || len(ms) != 1 {
		t.Errorf("machines for a member: %s", w.Body)
	}
}

// Uploads belong to the org they were uploaded in.
func TestUploadsAreScoped(t *testing.T) {
	o := newTwoOrgs(t)
	if w := do(t, o.c, "GET", "/api/uploads/"+o.upload.ID+"/"+o.upload.Name, ""); w.Code != http.StatusOK {
		t.Errorf("same org: %d", w.Code)
	}
	p, err := o.s.Store.ForOrg(o.beta.ID).CreateProject(model.Project{Name: "b", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if w := do(t, o.b, "POST", "/api/projects/"+p.ID+"/todos", `{"title":"x","attachments":["`+o.upload.ID+`"]}`); w.Code != http.StatusBadRequest {
		t.Errorf("attaching another org's upload: %d %s", w.Code, w.Body)
	}
	// Uploads from before orgs belong to the default org.
	old := filepath.Join(o.s.UploadRoot, "0123456789ab")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "old.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if w := do(t, o.c, "GET", "/api/uploads/0123456789ab/old.png", ""); w.Code != http.StatusOK {
		t.Errorf("old upload in the default org: %d", w.Code)
	}
	if w := do(t, o.b, "GET", "/api/uploads/0123456789ab/old.png", ""); w.Code != http.StatusNotFound {
		t.Errorf("old upload from another org: %d", w.Code)
	}
}
