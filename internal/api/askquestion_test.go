package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/perm"
)

// Claude's question (AskUserQuestion) is answered through the decide
// endpoint: the answers reach Claude in the tool's input; a plain allow
// is refused.
func TestDecideCarriesAnswersToAQuestion(t *testing.T) {
	s, _, p := boardServer(t)
	token, release := s.Perms.Register(testOwner(t, s), "t1", p.ID)
	defer release()
	answer := make(chan string, 1)
	go func() {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + perm.ToolName +
			`","arguments":{"tool_name":"AskUserQuestion","input":{"questions":[{"question":"Which color?","header":"Color",` +
			`"options":[{"label":"Red","description":"warm"},{"label":"Blue","description":"cool"}],"multiSelect":false}]}}}}`
		r := httptest.NewRequest("POST", "/mcp/permissions", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Perms.ServeMCP(w, r)
		answer <- w.Body.String()
	}()
	eventually(t, "the question to wait", func() bool { return s.Perms.Waiting("t1") })
	id := s.Perms.Pending(p.ID)[0].ID

	call := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest("POST", "/api/permissions/"+id, strings.NewReader(body))))
		return w
	}
	if w := call(`{"decision":"allow"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("allow without answers: %d %s", w.Code, w.Body)
	}
	if w := call(`{"decision":"allow","answers":{"Which color?":"Green, mostly"}}`); w.Code != http.StatusNoContent {
		t.Fatalf("answer: %d %s", w.Code, w.Body)
	}
	select {
	case got := <-answer:
		if !strings.Contains(got, `\"answers\":{\"Which color?\":\"Green, mostly\"}`) || !strings.Contains(got, `\"questions\":[`) {
			t.Errorf("Claude was told: %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no answer reached Claude")
	}
}
