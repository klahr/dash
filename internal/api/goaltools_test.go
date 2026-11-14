package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// The user's goal tools on the chat's board: start a goal, reject and
// accept its plan, write to its coordinator (which answers its question),
// change, pause, resume and stop it.
func TestGoalToolsForUser(t *testing.T) {
	s, p, fc := goalServer(t)
	h := s.Routes()
	fc.setAct(func(msg string, call func(string, string) (string, bool)) {
		switch {
		case strings.Contains(msg, "propose a plan") || strings.Contains(msg, "Propose a new plan"):
			call("propose_plan", `{"tasks":[{"title":"Index","prompt":"Add a search index"}],"why":"small"}`)
		case strings.Contains(msg, "The user wrote to you: which one?"):
			call("ask_user", `{"question":"Which database?"}`)
		}
	})
	token, release := s.Board.Register(testOwner(t, s), p.ID)
	defer release()
	tool := func(name, args string) string {
		t.Helper()
		out, isErr := toolCall(h, token, name, args)
		if isErr {
			t.Fatalf("%s: %s", name, out)
		}
		return out
	}
	var row struct {
		ID         string           `json:"id"`
		Status     model.GoalStatus `json:"status"`
		WaitingFor string           `json:"waiting_for"`
		PlanTasks  int              `json:"plan_tasks"`
		Budget     model.GoalBudget `json:"budget"`
		Created    []struct {
			ID     string           `json:"id"`
			Status model.TodoStatus `json:"status"`
		} `json:"created"`
	}
	read := func(out string) {
		t.Helper()
		row.Created = nil
		if err := json.Unmarshal([]byte(out), &row); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}

	read(tool("create_goal", `{"title":"Faster search","description":"Search under 100 ms","max_tasks":3}`))
	id := row.ID
	if id == "" || row.Budget.MaxTasks != 3 {
		t.Fatalf("created %+v", row)
	}
	settleGoals(s)
	read(tool("get_goal", `{"goal":"`+id+`"}`))
	if row.Status != model.GoalWaiting || row.WaitingFor != model.GoalWaitPlan {
		t.Fatalf("after planning %+v", row)
	}

	read(tool("reject_goal_plan", `{"goal":"`+id+`","feedback":"split it"}`))
	settleGoals(s)
	if !strings.Contains(fc.lastTurn(), "split it") {
		t.Fatalf("replan turn %q", fc.lastTurn())
	}
	read(tool("accept_goal_plan", `{"goal":"`+id+`"}`))
	if len(row.Created) != 1 || row.Created[0].Status != model.StatusDraft {
		t.Fatalf("accepted %+v", row)
	}

	// A message wakes the coordinator; a reply to its question answers it.
	tool("message_goal", `{"goal":"`+id+`","text":"which one?"}`)
	settleGoals(s)
	g := getGoal(t, loggedIn(t, h), id)
	if g.WaitingFor != model.GoalWaitQuestion {
		t.Fatalf("after asking %+v", g.Goal)
	}
	tool("message_goal", `{"goal":"`+id+`","text":"SQLite"}`)
	settleGoals(s)
	if !strings.Contains(fc.lastTurn(), "The user wrote to you: SQLite") {
		t.Fatalf("message turn %q", fc.lastTurn())
	}
	if g := getGoal(t, loggedIn(t, h), id); g.WaitingFor == model.GoalWaitQuestion || !hasLog(g.Goal, "You wrote to the coordinator: SQLite") {
		t.Fatalf("after answering %+v", g.Goal)
	}

	read(tool("update_goal", `{"goal":"`+id+`","weekly_share":20}`))
	if row.Budget != (model.GoalBudget{MaxTasks: 3, WeeklyShare: 20}) {
		t.Fatalf("budget %+v", row.Budget)
	}
	if read(tool("pause_goal", `{"goal":"`+id+`"}`)); row.Status != model.GoalPaused {
		t.Fatalf("paused %+v", row)
	}
	if read(tool("resume_goal", `{"goal":"`+id+`"}`)); row.Status == model.GoalPaused {
		t.Fatalf("resumed %+v", row)
	}
	settleGoals(s)
	if read(tool("stop_goal", `{"goal":"`+id+`"}`)); row.Status != model.GoalDone {
		t.Fatalf("stopped %+v", row)
	}
	if out, isErr := toolCall(h, token, "message_goal", `{"goal":"`+id+`","text":"more"}`); !isErr || !strings.Contains(out, "done") {
		t.Fatalf("message to a done goal: %s", out)
	}

	var list []struct{ ID string }
	if err := json.Unmarshal([]byte(tool("list_goals", `{"status":"done"}`)), &list); err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("done goals %+v %v", list, err)
	}
	if err := json.Unmarshal([]byte(tool("list_goals", `{"project":"*","status":"working"}`)), &list); err != nil || len(list) != 0 {
		t.Fatalf("working goals %+v %v", list, err)
	}
	if out, isErr := toolCall(h, token, "get_goal", `{"goal":"nope"}`); !isErr || !strings.Contains(out, "no goal") {
		t.Fatalf("unknown goal: %s", out)
	}
}
