package perm

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	token, release := b.Register("todo1", "proj1")
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
	token, release := b.Register("todo1", "proj1")
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

func TestTimeoutAndReleaseDeny(t *testing.T) {
	b := NewBroker(nopPub{})
	b.Timeout = 50 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(b.ServeMCP))
	defer srv.Close()
	token, release := b.Register("todo1", "proj1")

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
