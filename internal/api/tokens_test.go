package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// member invites name into org (as its first user) and returns their id.
func member(t *testing.T, s *Server, org int64, name string, role store.Role) int64 {
	t.Helper()
	acc := s.Auth.Accounts()
	env, _ := acc.EnvUser()
	tok, _, err := acc.Invite(env.ID, org, name+"@example.com", role)
	if err != nil {
		t.Fatal(err)
	}
	u, _, err := acc.RegisterWithInvitation(tok, name, "long-enough-"+name)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// project finds the project named name.
func project(t *testing.T, s *Server, name string) model.Project {
	t.Helper()
	for _, p := range raw(s).ListProjects() {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no project %s", name)
	return model.Project{}
}

// A run's token reaches only its owner's org: projects and tasks of other
// orgs don't exist for its tools.
func TestRunTokenOnlyReachesItsOrg(t *testing.T) {
	s, _, web := boardServer(t)
	h := s.Routes()
	owner := testOwner(t, s)
	two, err := s.Auth.Accounts().CreateOrg(owner.User, "Two")
	if err != nil {
		t.Fatal(err)
	}
	other, err := raw(s).CreateProject(model.Project{Name: "theirs", OrgID: two.ID})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := raw(s).CreateTodo(model.Todo{ProjectID: other.ID, Prompt: "theirs"})
	if err != nil {
		t.Fatal(err)
	}

	token, release := s.Board.Register(owner, web.ID)
	defer release()
	call := func(tool, args string) (string, bool) {
		t.Helper()
		return callResult(t, rpc(h, token, "tools/call", fmt.Sprintf(`{"name":%q,"arguments":%s}`, tool, args)))
	}
	if text, _ := call("list_projects", "{}"); !strings.Contains(text, web.ID) || strings.Contains(text, other.ID) {
		t.Errorf("list_projects: %s", text)
	}
	for _, c := range []struct{ tool, args string }{
		{"get_todo", `{"todo":"` + theirs.ID + `"}`},
		{"list_todos", `{"project":"` + other.ID + `"}`},
		{"list_todos", `{"project":"theirs"}`},
		{"create_todo", `{"project":"` + other.ID + `","prompt":"x"}`},
		{"create_todo", `{"prompt":"x","depends_on":["` + theirs.ID + `"]}`},
		{"queue_todo", `{"todo":"` + theirs.ID + `"}`},
		{"update_todo", `{"todo":"` + theirs.ID + `","title":"mine now"}`},
		{"delete_todo", `{"todo":"` + theirs.ID + `"}`},
		{"list_issues", `{"project":"` + other.ID + `"}`},
		{"report_issue", `{"project":"` + other.ID + `","title":"x","type":"bug","severity":"low","details":"y"}`},
	} {
		if text, isErr := call(c.tool, c.args); !isErr {
			t.Errorf("%s %s reached another org: %s", c.tool, c.args, text)
		}
	}
	if td, err := raw(s).GetTodo(theirs.ID); err != nil || td.Title == "mine now" {
		t.Errorf("their task changed: %+v %v", td, err)
	}

	// Its own org works, and what it makes is its user's.
	text, isErr := call("create_todo", `{"prompt":"mine","draft":true}`)
	if isErr {
		t.Fatal(text)
	}
	var mine model.Todo
	_ = json.Unmarshal([]byte(text), &mine)
	if got, _ := raw(s).GetTodo(mine.ID); got.CreatedBy != owner.User || got.ProjectID != web.ID {
		t.Errorf("created %+v", got)
	}

	// The same user's run in the other org sees only that one.
	token2, release2 := s.Board.Register(model.Owner{User: owner.User, Org: two.ID}, "")
	defer release2()
	text, _ = callResult(t, rpc(h, token2, "tools/call", `{"name":"list_projects","arguments":{}}`))
	if !strings.Contains(text, other.ID) || strings.Contains(text, web.ID) {
		t.Errorf("list_projects in Two: %s", text)
	}
}

// A run's token needs a user and an org, and stops working when the user
// leaves the org.
func TestRunTokenNeedsOwnerInOrg(t *testing.T) {
	s, _, web := boardServer(t)
	h := s.Routes()
	owner := testOwner(t, s)

	for _, o := range []model.Owner{{}, {User: owner.User}, {Org: owner.Org}} {
		token, release := s.Board.Register(o, web.ID)
		if w := rpc(h, token, "ping", "{}"); w.Code != http.StatusForbidden {
			t.Errorf("token of %+v: %d", o, w.Code)
		}
		release()
	}

	bob := member(t, s, owner.Org, "bob", store.RoleMember)
	token, release := s.Board.Register(model.Owner{User: bob, Org: owner.Org}, web.ID)
	defer release()
	if w := rpc(h, token, "ping", "{}"); w.Code != http.StatusOK {
		t.Fatalf("bob's run: %d %s", w.Code, w.Body)
	}
	if err := s.Auth.Accounts().RemoveMember(owner.User, owner.Org, bob); err != nil {
		t.Fatal(err)
	}
	if w := rpc(h, token, "ping", "{}"); w.Code != http.StatusForbidden {
		t.Errorf("run of a removed member: %d", w.Code)
	}
	if s.Member(model.Owner{User: bob, Org: owner.Org}) {
		t.Error("removed member still a member")
	}
}

// A task's runs act as its creator in its org; tasks without a creator act
// as the env account.
func TestTaskOwner(t *testing.T) {
	s, _, web := boardServer(t)
	owner := testOwner(t, s)
	bob := member(t, s, owner.Org, "bob", store.RoleMember)

	legacy, _ := raw(s).CreateTodo(model.Todo{ProjectID: web.ID, Prompt: "old"})
	if o, err := s.TaskOwner(legacy); err != nil || o != owner {
		t.Errorf("task without a creator: %+v %v", o, err)
	}
	bobs, _ := raw(s).CreateTodo(model.Todo{ProjectID: web.ID, Prompt: "bob's", CreatedBy: bob})
	if o, err := s.TaskOwner(bobs); err != nil || o != (model.Owner{User: bob, Org: owner.Org}) {
		t.Errorf("bob's task: %+v %v", o, err)
	}
	// In an org bob isn't in, his task has no one to act as.
	two, _ := s.Auth.Accounts().CreateOrg(owner.User, "Two")
	theirs, err := raw(s).CreateProject(model.Project{Name: "theirs", OrgID: two.ID})
	if err != nil {
		t.Fatal(err)
	}
	stray, _ := raw(s).CreateTodo(model.Todo{ProjectID: theirs.ID, Prompt: "bob's elsewhere", CreatedBy: bob})
	if o, err := s.TaskOwner(stray); err == nil {
		t.Errorf("bob's task in an org he isn't in: %+v", o)
	}
	if err := s.Auth.Accounts().RemoveMember(owner.User, owner.Org, bob); err != nil {
		t.Fatal(err)
	}
	if o, err := s.TaskOwner(bobs); err == nil {
		t.Errorf("task of a removed member: %+v", o)
	}
}

// Tasks made through the API record who made them.
func TestCreateTodoRecordsCreator(t *testing.T) {
	s, _, web := boardServer(t)
	owner := testOwner(t, s)
	org := owner.Org
	secret, _, err := s.Auth.Accounts().CreateToken(store.NewToken{Kind: store.TokenAPI, Name: "ci", UserID: owner.User, OrgID: org, Scopes: store.UserScopes}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/projects/"+web.ID+"/todos", strings.NewReader(`{"title":"x"}`))
	r.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, r)
	var td model.Todo
	if err := json.Unmarshal(w.Body.Bytes(), &td); err != nil || w.Code != http.StatusCreated || td.CreatedBy != owner.User {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
}

// A read-only token's /mcp tools may look but not change anything.
func TestRemoteMCPReadOnlyToken(t *testing.T) {
	h, s, home, _ := remoteServer(t)
	owner := testOwner(t, s)
	ro, _, err := s.Auth.Accounts().CreateToken(store.NewToken{Kind: store.TokenAPI, Name: "ro", UserID: owner.User, OrgID: owner.Org, Scopes: []string{"read"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call := func(tool, args string) (string, bool) {
		t.Helper()
		return callResult(t, rpcAt(h, "/mcp", ro, "tools/call", fmt.Sprintf(`{"name":%q,"arguments":%s}`, tool, args)))
	}
	if text, isErr := call("list_projects", "{}"); isErr || !strings.Contains(text, home.ID) {
		t.Errorf("list_projects: %s", text)
	}
	before := len(raw(s).ListTodos(home.ID))
	if text, isErr := call("create_todo", `{"project":"`+home.ID+`","prompt":"x"}`); !isErr || !strings.Contains(text, "read-only") {
		t.Errorf("create_todo with a read-only token: %s", text)
	}
	if n := len(raw(s).ListTodos(home.ID)); n != before {
		t.Errorf("a task was created: %d → %d", before, n)
	}
}

// Only a signed-in browser makes runner codes, and the runner registered
// with one is its maker's, in their active org.
func TestRunnerCodesNeedSession(t *testing.T) {
	s, _, _ := boardServer(t)
	acc := s.Auth.Accounts()
	s.Peers = &peer.Hub{Self: "home", Runners: &tunnel.Hub{Accounts: acc}}
	h := s.Routes()
	owner := testOwner(t, s)
	secret, _, err := acc.CreateToken(store.NewToken{Kind: store.TokenAPI, Name: "ci", UserID: owner.User, OrgID: owner.Org, Scopes: store.UserScopes}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/runners/codes", nil)
	r.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("runner code with a token: %d %s", w.Code, w.Body)
	}

	login := httptest.NewRecorder()
	h.ServeHTTP(login, httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"u","password":"p"}`)))
	cookies := login.Result().Cookies()
	if login.Code != http.StatusOK || len(cookies) == 0 {
		t.Fatalf("login: %d %s", login.Code, login.Body)
	}
	r = httptest.NewRequest("POST", "/api/runners/codes", nil)
	r.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var c struct{ Code string }
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil || w.Code != http.StatusOK || c.Code == "" {
		t.Fatalf("runner code: %d %s", w.Code, w.Body)
	}
	_, runnerToken, err := s.Peers.Runners.Register(c.Code, "work")
	if err != nil {
		t.Fatal(err)
	}
	tok, sess, err := acc.Authenticate(runnerToken, time.Now(), store.TokenRunner)
	if err != nil || tok.UserID != owner.User || tok.Org.ID != owner.Org || sess.Org.ID != owner.Org {
		t.Fatalf("runner token %+v %v", tok, err)
	}
	if rs := s.Peers.Runners.List(); len(rs) != 1 || rs[0].Owner == nil || rs[0].Owner.Username != "u" {
		t.Errorf("runners %+v", rs)
	}
}
