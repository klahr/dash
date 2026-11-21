package perm

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

type nopPub struct{}

func (nopPub) Publish(model.Event) {}

func rpc(t *testing.T, srv *httptest.Server, token, method string, params any) (*http.Response, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest("POST", srv.URL+"/mcp/permissions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res, out
}

// answer extracts the {"behavior":…} JSON Claude Code receives from a tools/call result.
func answer(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	content := out["result"].(map[string]any)["content"].([]any)
	var a map[string]any
	if err := json.Unmarshal([]byte(content[0].(map[string]any)["text"].(string)), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func call(tool string) map[string]any {
	return map[string]any{"name": ToolName, "arguments": map[string]any{"tool_name": tool, "input": map[string]any{"command": "npm install"}}}
}

func waitPending(t *testing.T, b *Broker) Request {
	t.Helper()
	for i := 0; i < 200; i++ {
		if p := b.Pending(""); len(p) > 0 {
			return p[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no pending request")
	return Request{}
}

func TestMCPHandshakeAndAuth(t *testing.T) {
	b := NewBroker(nopPub{})
	srv := httptest.NewServer(http.HandlerFunc(b.ServeMCP))
	defer srv.Close()

	if res, _ := rpc(t, srv, "bogus", "initialize", nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown token: want 401, got %d", res.StatusCode)
	}
	token, release := b.Register(owner, "todo1", "proj1")
	_, out := rpc(t, srv, token, "initialize", map[string]any{"protocolVersion": "2025-06-18"})
	if out["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize: %v", out)
	}
	_, out = rpc(t, srv, token, "tools/list", nil)
	tools := out["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != ToolName {
		t.Fatalf("tools/list: %v", out)
	}
	release()
	if res, _ := rpc(t, srv, token, "tools/list", nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatal("token should stop working after release")
	}
}

func TestAllowDenyAlways(t *testing.T) {
	b := NewBroker(nopPub{})
	srv := httptest.NewServer(http.HandlerFunc(b.ServeMCP))
	defer srv.Close()
	token, release := b.Register(owner, "todo1", "proj1")
	defer release()

	decide := func(decision, msg string) map[string]any {
		done := make(chan map[string]any)
		go func() { _, out := rpc(t, srv, token, "tools/call", call("Bash")); done <- out }()
		req := waitPending(t, b)
		if req.Tool != "Bash" || req.TodoID != "todo1" || req.ProjectID != "proj1" {
			t.Fatalf("unexpected request %+v", req)
		}
		if err := b.Decide(req.ID, decision, msg); err != nil {
			t.Fatal(err)
		}
		return answer(t, <-done)
	}

	if a := decide("deny", "not now"); a["behavior"] != "deny" || a["message"] != "not now" {
		t.Fatalf("deny: %v", a)
	}
	if a := decide("allow", ""); a["behavior"] != "allow" || a["updatedInput"].(map[string]any)["command"] != "npm install" {
		t.Fatalf("allow: %v", a)
	}
	if a := decide("always", ""); a["behavior"] != "allow" {
		t.Fatalf("always: %v", a)
	}
	// Now Bash is allowed for this todo without asking.
	_, out := rpc(t, srv, token, "tools/call", call("Bash"))
	if a := answer(t, out); a["behavior"] != "allow" || len(b.Pending("")) != 0 {
		t.Fatalf("always-allowed tool should not prompt: %v", a)
	}
}

type memAllow map[string]bool

func (m memAllow) ToolAllowed(projectID, tool string) bool { return m[projectID+"/"+tool] }
func (m memAllow) AllowTool(projectID, tool string) error {
	m[projectID+"/"+tool] = true
	return nil
}

func TestAlwaysSavedForProject(t *testing.T) {
	b := NewBroker(nopPub{})
	saved := memAllow{}
	b.Saved = saved
	srv := httptest.NewServer(http.HandlerFunc(b.ServeMCP))
	defer srv.Close()

	token1, release1 := b.Register(owner, "todo1", "proj1")
	done := make(chan map[string]any)
	go func() { _, out := rpc(t, srv, token1, "tools/call", call("Bash")); done <- out }()
	if err := b.Decide(waitPending(t, b).ID, "always", ""); err != nil {
		t.Fatal(err)
	}
	<-done
	release1()
	b.Forget("todo1")
	if !saved["proj1/Bash"] {
		t.Fatalf("always grant not saved: %v", saved)
	}

	// Another todo in the same project doesn't ask.
	token2, release2 := b.Register(owner, "todo2", "proj1")
	defer release2()
	_, out := rpc(t, srv, token2, "tools/call", call("Bash"))
	if a := answer(t, out); a["behavior"] != "allow" || len(b.Pending("")) != 0 {
		t.Fatalf("saved tool should not prompt: %v", a)
	}
}

func TestTimeoutAndReleaseDeny(t *testing.T) {
	b := NewBroker(nopPub{})
	b.Timeout = 50 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(b.ServeMCP))
	defer srv.Close()
	token, release := b.Register(owner, "todo1", "proj1")

	_, out := rpc(t, srv, token, "tools/call", call("Bash"))
	if a := answer(t, out); a["behavior"] != "deny" {
		t.Fatalf("timeout should deny: %v", a)
	}

	b.Timeout = time.Minute
	done := make(chan map[string]any)
	go func() { _, out := rpc(t, srv, token, "tools/call", call("WebFetch")); done <- out }()
	waitPending(t, b)
	release() // run ended
	if a := answer(t, <-done); a["behavior"] != "deny" {
		t.Fatalf("release should deny pending: %v", a)
	}
}

func TestRegisterFuncAnswersItself(t *testing.T) {
	b := NewBroker(nopPub{})
	srv := httptest.NewServer(http.HandlerFunc(b.ServeMCP))
	defer srv.Close()
	var gotTool, gotCmd string
	allow := false
	token, release := b.RegisterFunc(owner, func(_ context.Context, tool string, input json.RawMessage) (bool, string) {
		var in struct{ Command string }
		_ = json.Unmarshal(input, &in)
		gotTool, gotCmd = tool, in.Command
		if allow {
			return true, ""
		}
		return false, "no thanks"
	})

	_, out := rpc(t, srv, token, "tools/call", call("Bash"))
	if a := answer(t, out); a["behavior"] != "deny" || a["message"] != "no thanks" {
		t.Fatalf("deny: %v", a)
	}
	if gotTool != "Bash" || gotCmd != "npm install" {
		t.Fatalf("ask got %q %q", gotTool, gotCmd)
	}
	allow = true
	_, out = rpc(t, srv, token, "tools/call", call("Bash"))
	if a := answer(t, out); a["behavior"] != "allow" || a["updatedInput"].(map[string]any)["command"] != "npm install" {
		t.Fatalf("allow: %v", a)
	}
	if len(b.Pending("")) != 0 {
		t.Fatal("func-answered requests should not be pending on the board")
	}
	release()
	if res, _ := rpc(t, srv, token, "tools/list", nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatal("token should stop working after release")
	}
}

var owner = model.Owner{User: 1, Org: 1}

// A run's token acts as its owner, only while they are in its org.
func TestTokenNeedsOwnerInOrg(t *testing.T) {
	b := NewBroker(nil)
	member := true
	b.Member = func(o model.Owner) bool { return o == owner && member }
	call := func(token string) int {
		r := httptest.NewRequest("POST", "/mcp/permissions", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		b.ServeMCP(w, r)
		return w.Code
	}
	token, release := b.Register(owner, "todo1", "proj1")
	defer release()
	if c := call(token); c != http.StatusOK {
		t.Fatalf("owner's run: %d", c)
	}
	member = false
	if c := call(token); c != http.StatusUnauthorized {
		t.Errorf("run of a removed member: %d", c)
	}
	unowned, release2 := b.Register(model.Owner{}, "todo2", "proj1")
	defer release2()
	member = true
	if c := call(unowned); c != http.StatusUnauthorized {
		t.Errorf("run with no org: %d", c)
	}
}

// askCall is Claude Code calling AskUserQuestion with two questions.
func askCall() map[string]any {
	return map[string]any{"name": ToolName, "arguments": map[string]any{"tool_name": AskTool, "input": map[string]any{
		"questions": []any{
			map[string]any{"question": "Which database?", "header": "Database", "multiSelect": false,
				"options": []any{map[string]any{"label": "Postgres", "description": "SQL"}, map[string]any{"label": "SQLite"}}},
			map[string]any{"question": "Which tests?", "header": "Tests", "multiSelect": true,
				"options": []any{map[string]any{"label": "Unit"}, map[string]any{"label": "Browser"}}},
		},
	}}}
}

func TestQuestionAnswersMergedIntoInput(t *testing.T) {
	b := NewBroker(nopPub{})
	b.Saved = memAllow{"proj1/" + AskTool: true} // a question still waits for answers
	srv := httptest.NewServer(http.HandlerFunc(b.ServeMCP))
	defer srv.Close()
	token, release := b.Register(owner, "todo1", "proj1")
	defer release()

	done := make(chan map[string]any)
	go func() { _, out := rpc(t, srv, token, "tools/call", askCall()); done <- out }()
	req := waitPending(t, b)
	if req.Tool != AskTool {
		t.Fatalf("unexpected request %+v", req)
	}
	if qs := Questions(req.Input); len(qs) != 2 || qs[1].Header != "Tests" || !qs[1].MultiSelect || qs[0].Options[0].Label != "Postgres" {
		t.Fatalf("questions: %+v", qs)
	}
	// A plain allow, always, or answers that leave a question out are refused.
	for _, bad := range []struct {
		decision string
		answers  map[string]string
	}{
		{"allow", nil},
		{"always", map[string]string{"Which database?": "SQLite", "Which tests?": "Unit"}},
		{"allow", map[string]string{"Which database?": "SQLite"}},
		{"allow", map[string]string{"Which database?": "SQLite", "Which tests?": "   "}},
	} {
		if err := b.DecideAnswers(req.ID, bad.decision, "", bad.answers); err == nil {
			t.Errorf("%s %v: want an error", bad.decision, bad.answers)
		}
	}
	if _, ok := b.Get(req.ID); !ok {
		t.Fatal("a refused answer should leave the question pending")
	}
	answers := map[string]string{"Which database?": "SQLite", "Which tests?": "Unit, my own words", "Not asked": "x"}
	if err := b.DecideAnswers(req.ID, "allow", "", answers); err != nil {
		t.Fatal(err)
	}
	a := answer(t, <-done)
	in, _ := a["updatedInput"].(map[string]any)
	if a["behavior"] != "allow" || in == nil {
		t.Fatalf("allow: %v", a)
	}
	if qs, _ := in["questions"].([]any); len(qs) != 2 {
		t.Fatalf("the original questions should be kept: %v", in)
	}
	want := map[string]any{"Which database?": "SQLite", "Which tests?": "Unit, my own words"}
	if got, _ := in["answers"].(map[string]any); len(got) != len(want) || got["Which database?"] != want["Which database?"] || got["Which tests?"] != want["Which tests?"] {
		t.Fatalf("answers: %v", in["answers"])
	}
}

func TestQuestionDenyAndTimeoutUnchanged(t *testing.T) {
	b := NewBroker(nopPub{})
	srv := httptest.NewServer(http.HandlerFunc(b.ServeMCP))
	defer srv.Close()
	token, release := b.Register(owner, "todo1", "proj1")
	defer release()

	done := make(chan map[string]any)
	go func() { _, out := rpc(t, srv, token, "tools/call", askCall()); done <- out }()
	req := waitPending(t, b)
	if err := b.DecideAnswers(req.ID, "deny", "Pick yourself", map[string]string{"Which database?": "SQLite"}); err != nil {
		t.Fatal(err)
	}
	if a := answer(t, <-done); a["behavior"] != "deny" || a["message"] != "Pick yourself" || a["updatedInput"] != nil {
		t.Fatalf("deny: %v", a)
	}

	b.Timeout = 50 * time.Millisecond
	_, out := rpc(t, srv, token, "tools/call", askCall())
	if a := answer(t, out); a["behavior"] != "deny" || a["message"] != "Nobody answered the permission request in time." {
		t.Fatalf("timeout should deny: %v", a)
	}
}

// Answers sent with another tool's decision change nothing.
func TestAnswersIgnoredForOtherTools(t *testing.T) {
	b := NewBroker(nopPub{})
	srv := httptest.NewServer(http.HandlerFunc(b.ServeMCP))
	defer srv.Close()
	token, release := b.Register(owner, "todo1", "proj1")
	defer release()

	done := make(chan map[string]any)
	go func() { _, out := rpc(t, srv, token, "tools/call", call("Bash")); done <- out }()
	if err := b.DecideAnswers(waitPending(t, b).ID, "allow", "", map[string]string{"x": "y"}); err != nil {
		t.Fatal(err)
	}
	a := answer(t, <-done)
	in := a["updatedInput"].(map[string]any)
	if a["behavior"] != "allow" || in["command"] != "npm install" || in["answers"] != nil {
		t.Fatalf("allow: %v", a)
	}
}
