package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
)

// askPerm has the run of task todo in project p ask for tool with input, and
// waits until the request is pending. The request stays open until the test
// ends.
func askPerm(t *testing.T, s *Server, todo, project, tool, input string) {
	t.Helper()
	token, release := s.Perms.Register(testOwner(t, s), todo, project)
	t.Cleanup(release)
	before := len(s.Perms.Pending(""))
	go func() {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + perm.ToolName +
			`","arguments":{"tool_name":"` + tool + `","input":` + input + `}}}`
		r := httptest.NewRequest("POST", "/mcp/permissions", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		s.Perms.ServeMCP(httptest.NewRecorder(), r)
	}()
	eventually(t, tool+" to wait", func() bool { return len(s.Perms.Pending("")) > before })
}

// Needs you lists every project's reviews, failures, questions and
// permission requests, oldest first, and nothing that doesn't wait on a
// person or is another org's.
func TestNeedsAcrossProjects(t *testing.T) {
	s, _, web := boardServer(t)
	other, err := raw(s).CreateProject(model.Project{Name: "api"})
	if err != nil {
		t.Fatal(err)
	}
	setTodo(t, s, web, func(x *model.Todo) {
		x.Title, x.Status, x.FinishedAt = "old review", model.StatusReview, at(-3*time.Hour)
	})
	setTodo(t, s, other, func(x *model.Todo) { x.Title, x.Status, x.FinishedAt = "broke", model.StatusFailed, at(-2*time.Hour) })
	setTodo(t, s, web, func(x *model.Todo) {
		x.Title, x.Status, x.FinishedAt = "new review", model.StatusReview, at(-5*time.Minute)
	})
	// A pull request that waits on its checks, not on a person, as on the Wall.
	setTodo(t, s, web, func(x *model.Todo) {
		x.Title, x.Status, x.FinishedAt, x.PR = "on GitHub", model.StatusReview, at(-4*time.Hour), &model.PullRequest{Number: 3, State: "OPEN"}
	})
	setTodo(t, s, web, func(x *model.Todo) { x.Title, x.Status = "running", model.StatusInProgress })
	setTodo(t, s, web, func(x *model.Todo) { x.Title, x.Status, x.FinishedAt = "landed", model.StatusMerged, at(-time.Hour) })
	asking := setTodo(t, s, other, func(x *model.Todo) { x.Title, x.Status = "asking", model.StatusInProgress })
	askPerm(t, s, asking.ID, other.ID, perm.AskTool, `{"questions":[{"question":"Which color?","options":[{"label":"Red"},{"label":"Blue"}]}]}`)
	askPerm(t, s, asking.ID, other.ID, "Bash", `{"command":"make deploy"}`)
	// Another org's review never shows.
	owner := testOwner(t, s)
	two, err := s.Auth.Accounts().CreateOrg(owner.User, "Two")
	if err != nil {
		t.Fatal(err)
	}
	theirs, _ := raw(s).CreateProject(model.Project{Name: "theirs", OrgID: two.ID})
	setTodo(t, s, theirs, func(x *model.Todo) {
		x.Title, x.Status, x.FinishedAt = "their review", model.StatusReview, at(-10*time.Hour)
	})

	w := httptest.NewRecorder()
	s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest("GET", "/api/needs", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/needs: %d %s", w.Code, w.Body)
	}
	var got []need
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	type row struct{ kind, project, title string }
	want := []row{
		{"review", web.ID, "old review"},
		{"failed", other.ID, "broke"},
		{"review", web.ID, "new review"},
		{"question", other.ID, "asking"},
		{"permission", other.ID, "asking"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d needs, want %d: %s", len(got), len(want), w.Body)
	}
	for i, n := range got {
		title := ""
		if n.Todo != nil {
			title = n.Todo.Title
		}
		if (row{n.Kind, n.ProjectID, title}) != want[i] {
			t.Errorf("need %d is %v, want %v", i, row{n.Kind, n.ProjectID, title}, want[i])
		}
		if (n.Kind == "question" || n.Kind == "permission") != (n.Perm != nil) {
			t.Errorf("need %d (%s): perm %v", i, n.Kind, n.Perm)
		}
		if i > 0 && n.Since.Before(got[i-1].Since) {
			t.Errorf("need %d waited longer than need %d: not oldest first", i, i-1)
		}
	}
}
