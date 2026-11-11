package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/tunnel"
)

const publicURL = "https://dash.example.ts.net"

// remoteServer is a hub with OAuth on and one runner, "work", connected
// through its tunnel, which has the same two projects ("other" and "web")
// as the hub.
func remoteServer(t *testing.T) (h http.Handler, s *Server, home, work model.Project) {
	t.Helper()
	s, _, home = boardServer(t)
	rh := &tunnel.Hub{Accounts: s.Auth.Accounts()}
	s.Peers = &peer.Hub{Self: "home", Runners: rh}
	rh.Taken = s.Peers.Taken
	var err error
	if s.OAuth, err = auth.NewOAuth(s.Auth, publicURL); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Routes())
	t.Cleanup(srv.Close)

	ps, _, work := boardServer(t)
	ps.Peers = &peer.Hub{Self: "work"}
	ps.Git = gitops.New(nil) // for update_project
	code, _, err := rh.NewCode(testOwner(t, s))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	creds, err := tunnel.Register(ctx, srv.URL, code, "work", "")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	client := &tunnel.Client{Creds: creds, Handler: ps.RunnerRoutes(), MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	stopped := make(chan struct{})
	go func() { _ = client.Run(ctx); close(stopped) }()
	t.Cleanup(func() { cancel(); <-stopped })
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })
	return s.Routes(), s, home, work
}

func post(h http.Handler, path, contentType, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// oauthToken goes through registration, consent and the code exchange.
func oauthToken(t *testing.T, h http.Handler) string {
	t.Helper()
	const callback = "https://claude.ai/api/mcp/auth_callback"
	w := post(h, "/register", "application/json", `{"client_name":"Claude","redirect_uris":["`+callback+`"]}`)
	var c struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil || c.ClientID == "" {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	verifier := strings.Repeat("v", 64)
	sum := sha256.Sum256([]byte(verifier))
	w = post(h, "/authorize", "application/x-www-form-urlencoded", url.Values{
		"response_type": {"code"}, "client_id": {c.ClientID}, "redirect_uri": {callback},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"username": {"u"}, "password": {"p"}, "action": {"allow"},
	}.Encode())
	loc, err := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusFound || err != nil || loc.Query().Get("code") == "" {
		t.Fatalf("authorize: %d %s", w.Code, w.Header().Get("Location"))
	}
	w = post(h, "/token", "application/x-www-form-urlencoded", url.Values{
		"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "client_id": {c.ClientID},
		"redirect_uri": {callback}, "code_verifier": {verifier},
	}.Encode())
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tok); err != nil || tok.AccessToken == "" {
		t.Fatalf("token: %d %s", w.Code, w.Body)
	}
	return tok.AccessToken
}

func rpcAt(h http.Handler, path, token, method, params string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s}`, method, params)
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestRemoteMCPOff(t *testing.T) {
	parallel(t)

	s, _, _ := boardServer(t)
	h := s.Routes()
	for _, path := range []string{"/mcp", "/authorize", "/.well-known/oauth-authorization-server"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusSeeOther { // behind the login like any other page
			t.Errorf("%s without -public-url: %d", path, w.Code)
		}
	}
}

func TestRemoteMCPTokensAreSeparate(t *testing.T) {
	parallel(t)

	h, s, home, _ := remoteServer(t)

	w := rpcAt(h, "/mcp", "", "ping", "{}")
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Header().Get("WWW-Authenticate"),
		`resource_metadata="`+publicURL+`/.well-known/oauth-protected-resource"`) {
		t.Errorf("no token: %d %q", w.Code, w.Header().Get("WWW-Authenticate"))
	}
	chat, release := s.Board.Register(chatCaller(t, s), home.ID)
	defer release()
	if w := rpcAt(h, "/mcp", chat, "ping", "{}"); w.Code != http.StatusUnauthorized {
		t.Errorf("/mcp took a chat-run token: %d", w.Code)
	}

	access := oauthToken(t, h)
	if w := rpcAt(h, "/mcp", access, "ping", "{}"); w.Code != http.StatusOK {
		t.Errorf("/mcp with OAuth token: %d %s", w.Code, w.Body)
	}
	if w := rpcAt(h, "/mcp/dash", access, "ping", "{}"); w.Code != http.StatusUnauthorized {
		t.Errorf("/mcp/dash took an OAuth token: %d", w.Code)
	}
	if w := rpcAt(h, "/mcp/dash", chat, "ping", "{}"); w.Code != http.StatusOK {
		t.Errorf("/mcp/dash with chat token: %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/api/projects", nil)
	r.Header.Set("Authorization", "Bearer "+access)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("OAuth token opened the REST API: %d", w.Code)
	}

	// Same tools as cmd/dash-mcp's shared part.
	var list struct {
		Result struct{ Tools []struct{ Name string } }
	}
	_ = json.Unmarshal(rpcAt(h, "/mcp", access, "tools/list", "{}").Body.Bytes(), &list)
	var names, want []string
	for _, tl := range list.Result.Tools {
		names = append(names, tl.Name)
	}
	for _, tl := range mcptools.FleetTools(mcptools.Fleet{}) {
		want = append(want, tl.Name)
	}
	if strings.Join(names, ",") != strings.Join(want, ",") || !strings.Contains(strings.Join(names, ","), "approve_todo") {
		t.Errorf("tools %v, want %v", names, want)
	}
}

func TestRemoteMCPToolsAcrossComputers(t *testing.T) {
	parallel(t)

	h, s, home, work := remoteServer(t)
	access := oauthToken(t, h)
	call := func(tool, args string) (string, bool) {
		t.Helper()
		return callResult(t, rpcAt(h, "/mcp", access, "tools/call", fmt.Sprintf(`{"name":%q,"arguments":%s}`, tool, args)))
	}
	todo := func(text string) model.Todo {
		t.Helper()
		var td model.Todo
		if err := json.Unmarshal([]byte(text), &td); err != nil {
			t.Fatalf("%v: %s", err, text)
		}
		return td
	}

	text, _ := call("list_projects", "{}")
	for _, want := range []string{`"machine": "home"`, `"machine": "work"`, home.ID, work.ID} {
		if !strings.Contains(text, want) {
			t.Errorf("list_projects lacks %s: %s", want, text)
		}
	}

	// Settings on the runner, by project id.
	text, isErr := call("update_project", `{"project":"`+work.ID+`","test_command":"go test ./...","auto_approve":true,"max_turns":7,"allowed_tools":["Bash"," "]}`)
	if isErr {
		t.Fatal(text)
	}
	var proj model.Project
	_ = json.Unmarshal([]byte(text), &proj)
	if proj.ID != work.ID || proj.Machine != "work" || proj.TestCommand != "go test ./..." || !proj.AutoApprove || proj.MaxTurns != 7 || strings.Join(proj.AllowedTools, ",") != "Bash" {
		t.Errorf("update_project: %s", text)
	}
	if text, _ := call("get_project", `{"project":"`+work.ID+`"}`); !strings.Contains(text, `"test_command": "go test ./..."`) {
		t.Errorf("get_project: %s", text)
	}
	if text, isErr := call("update_project", `{"project":"`+work.ID+`","color":"blue"}`); !isErr || !strings.Contains(text, "color") {
		t.Errorf("bad color accepted: %s", text)
	}
	if text, isErr := call("update_project", `{"project":"`+work.ID+`"}`); !isErr {
		t.Errorf("update without settings: %s", text)
	}

	// On the runner, by project id.
	text, isErr = call("create_todo", `{"project":"`+work.ID+`","title":"Remote task","draft":true}`)
	if isErr {
		t.Fatal(text)
	}
	created := todo(text)
	if _, err := raw(s).GetTodo(created.ID); err == nil {
		t.Fatalf("created on the hub, not the runner")
	}
	if text, _ := call("list_todos", `{"project":"`+work.ID+`"}`); !strings.Contains(text, "Remote task") {
		t.Errorf("list_todos on runner: %s", text)
	}
	if text, _ := call("get_todo", `{"todo":"`+created.ID+`"}`); todo(text).Title != "Remote task" {
		t.Errorf("get_todo on runner: %s", text)
	}
	if text, isErr := call("approve_todo", `{"todo":"`+created.ID+`"}`); !isErr {
		t.Errorf("approved a draft: %s", text)
	}
	if text, isErr := call("rework_todo", `{"todo":"`+created.ID+`"}`); !isErr || !strings.Contains(text, "feedback") {
		t.Errorf("rework without feedback: %s", text)
	}
	if text, isErr := call("get_diff", `{"todo":"`+created.ID+`"}`); !isErr || !strings.Contains(text, "no worktree") {
		t.Errorf("get_diff without worktree: %s", text)
	}
	if text, _ := call("enqueue_todo", `{"todo":"`+created.ID+`"}`); todo(text).Status != model.StatusQueued {
		t.Errorf("enqueue_todo: %s", text)
	}
	if text, _ := call("cancel_todo", `{"todo":"`+created.ID+`"}`); todo(text).Status != model.StatusDraft {
		t.Errorf("cancel_todo: %s", text)
	}
	if text, isErr := call("delete_todo", `{"todo":"`+created.ID+`"}`); isErr || text != "deleted" {
		t.Errorf("delete_todo: %s", text)
	}
	if text, isErr := call("get_todo", `{"todo":"`+created.ID+`"}`); !isErr {
		t.Errorf("deleted task still there: %s", text)
	}

	// On the hub, by name (the hub's projects come first).
	text, _ = call("create_todo", `{"project":"web","title":"Local task","draft":false}`)
	local := todo(text)
	if local.ProjectID != home.ID || local.Status != model.StatusQueued {
		t.Errorf("local create_todo: %+v", local)
	}
	if text, isErr := call("rework_todo", `{"todo":"`+local.ID+`","feedback":"more"}`); isErr {
		t.Errorf("rework_todo: %s", text)
	} else if got := todo(text); len(got.Feedback) != 1 || got.Feedback[0] != "more" {
		t.Errorf("rework_todo: %+v", got)
	}
	if text, isErr := call("get_todo", `{"todo":"../projects"}`); !isErr {
		t.Errorf("path in task id: %s", text)
	}
}
