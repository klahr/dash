package api

import (
	"fmt"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
)

// The board's optional interfaces stay served to a project's runs.
var (
	_ mcptools.Board       = projectBoard{}
	_ mcptools.Restarter   = projectBoard{}
	_ mcptools.StartNower  = projectBoard{}
	_ mcptools.TryReader   = projectBoard{}
	_ mcptools.GoalManager = projectBoard{}
)

// otherProject is boardServer's second project, "other", with a task, an
// issue and a goal that runs in "web" must never see.
func otherProject(t *testing.T, s *Server) (model.Project, model.Todo, model.Goal) {
	t.Helper()
	var other model.Project
	for _, p := range raw(s).ListProjects() {
		if p.Name == "other" {
			other = p
		}
	}
	if other.ID == "" {
		t.Fatal("no project other")
	}
	td, err := raw(s).CreateTodo(model.Todo{ProjectID: other.ID, Title: "Secret plan", Prompt: "secret prompt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw(s).ReportFinding(model.Issue{ProjectID: other.ID, Title: "Secret issue", Type: model.IssueBug, Severity: model.SevHigh, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	g, err := raw(s).CreateGoal(model.Goal{ProjectID: other.ID, Title: "Secret goal"})
	if err != nil {
		t.Fatal(err)
	}
	return other, td, g
}

func TestTaskRunCantReachOtherProjects(t *testing.T) {
	s, _, p := boardServer(t)
	h := s.Routes()
	other, _, _ := otherProject(t, s)
	td, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "build it"})
	if err != nil {
		t.Fatal(err)
	}
	token, release := s.Board.RegisterTask(testOwner(t, s), p.ID, td.ID)
	defer release()

	for _, ref := range []string{other.ID, other.Name} {
		call := fmt.Sprintf(`{"project":%q,"title":"Planted","type":"bug","severity":"low"}`, ref)
		if text, isErr := toolCall(h, token, "report_issue", call); !isErr {
			t.Errorf("report_issue in %s: %s", ref, text)
		}
	}
	for _, is := range reportedIssues(raw(s), other.ID) {
		if is.Title == "Planted" {
			t.Errorf("a task planted an issue in another project: %+v", is)
		}
	}
	// Its own project still works, by name too.
	if text, isErr := toolCall(h, token, "report_issue", `{"project":"web","title":"Mine"}`); isErr {
		t.Errorf("report_issue in its own project: %s", text)
	}

	// A token whose project doesn't match its task's is refused outright.
	bad, release2 := s.Board.RegisterTask(testOwner(t, s), other.ID, td.ID)
	defer release2()
	if w := rpc(h, bad, "tools/list", "{}"); w.Code == 200 {
		t.Errorf("task token for another project: %d %s", w.Code, w.Body)
	}
}

func TestProjectChatCantReachOtherProjects(t *testing.T) {
	s, _, p := boardServer(t)
	h := s.Routes()
	other, otherTodo, otherGoal := otherProject(t, s)
	token, release := s.Board.Register(chatCaller(t, s), p.ID)
	defer release()

	text, isErr := toolCall(h, token, "list_projects", `{}`)
	if isErr || !strings.Contains(text, `"web"`) || strings.Contains(text, other.ID) || strings.Contains(text, `"other"`) {
		t.Errorf("list_projects: %s", text)
	}

	inOther := fmt.Sprintf(`{"project":%q}`, other.Name)
	onTask := fmt.Sprintf(`{"todo":%q}`, otherTodo.ID)
	onGoal := fmt.Sprintf(`{"goal":%q}`, otherGoal.ID)
	for _, c := range []struct{ tool, args string }{
		{"list_todos", inOther},
		{"list_todos", fmt.Sprintf(`{"project":%q}`, other.ID)},
		{"create_todo", fmt.Sprintf(`{"project":%q,"title":"x"}`, other.Name)},
		{"report_issue", fmt.Sprintf(`{"project":%q,"title":"x","type":"bug","severity":"low"}`, other.Name)},
		{"list_goals", inOther},
		{"create_goal", fmt.Sprintf(`{"project":%q,"title":"x"}`, other.Name)},
		{"get_todo", onTask},
		{"queue_todo", onTask},
		{"stop_todo", onTask},
		{"restart_todo", onTask},
		{"update_todo", fmt.Sprintf(`{"todo":%q,"title":"renamed"}`, otherTodo.ID)},
		{"delete_todo", onTask},
		{"get_goal", onGoal},
		{"update_goal", fmt.Sprintf(`{"goal":%q,"title":"renamed"}`, otherGoal.ID)},
		{"pause_goal", onGoal},
		{"stop_goal", onGoal},
		{"delete_goal", onGoal},
		{"accept_goal_plan", onGoal},
		{"reject_goal_plan", onGoal},
		{"message_goal", fmt.Sprintf(`{"goal":%q,"text":"hi"}`, otherGoal.ID)},
		// Last: they reach git, which this test server has none of.
		{"read_try_terminal", inOther},
		{"create_release", fmt.Sprintf(`{"project":%q,"bump":"patch"}`, other.Name)},
	} {
		if text, isErr := toolCall(h, token, c.tool, c.args); !isErr || strings.Contains(text, "Secret") {
			t.Errorf("%s %s: %s", c.tool, c.args, text)
		}
	}
	// "*" lists this project's goals only.
	if text, isErr := toolCall(h, token, "list_goals", `{"project":"*"}`); isErr || strings.Contains(text, "Secret") {
		t.Errorf("list_goals *: %s", text)
	}

	// Nothing in the other project changed.
	if got, _ := raw(s).GetTodo(otherTodo.ID); got.Title != "Secret plan" || got.Status != model.StatusDraft {
		t.Errorf("other task %+v", got)
	}
	if got, _ := raw(s).GetGoal(otherGoal.ID); got.Title != "Secret goal" || got.Status != otherGoal.Status {
		t.Errorf("other goal %+v", got)
	}
	if n := len(raw(s).ListTodos(other.ID)); n != 2 { // the plan, and the secret issue's draft
		t.Errorf("other project has %d tasks", n)
	}
	if n := len(reportedIssues(raw(s), other.ID)); n != 1 {
		t.Errorf("other project has %d issues", n)
	}
}

func TestGeneralChatSeesEveryProject(t *testing.T) {
	s, _, _ := boardServer(t)
	h := s.Routes()
	_, otherTodo, _ := otherProject(t, s)
	token, release := s.Board.Register(chatCaller(t, s), "")
	defer release()

	if text, isErr := toolCall(h, token, "list_projects", `{}`); isErr || !strings.Contains(text, `"web"`) || !strings.Contains(text, `"other"`) {
		t.Errorf("list_projects: %s", text)
	}
	if text, isErr := toolCall(h, token, "get_todo", fmt.Sprintf(`{"todo":%q}`, otherTodo.ID)); isErr || !strings.Contains(text, "Secret plan") {
		t.Errorf("get_todo: %s", text)
	}
}
