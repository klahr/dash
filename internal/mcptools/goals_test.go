package mcptools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// goalAPI is one computer ("" prefix) holding goal g1, recording calls.
type goalAPI struct{ calls []string }

func (f *goalAPI) Call(method, path string, body, out any) error {
	b, _ := json.Marshal(body)
	f.calls = append(f.calls, method+" "+path+" "+string(b))
	switch {
	case path == "/api/machines":
		*out.(*[]machine) = []machine{{Name: "here", OK: true}}
	case path == "/api/goals":
		if o, ok := out.(*[]GoalView); ok {
			*o = []GoalView{{Goal: model.Goal{ID: "g1", Status: model.GoalWorking}}}
		}
	case strings.HasPrefix(path, "/api/goals/g1"):
		g := model.Goal{ID: "g1", Status: model.GoalWaiting, Budget: model.GoalBudget{MaxTasks: 4}}
		switch o := out.(type) {
		case *GoalView:
			*o = GoalView{Goal: g}
		case nil:
		default:
			raw, _ := json.Marshal(map[string]any{"id": "g1", "created": []model.Todo{{ID: "t1", Status: model.StatusDraft}}})
			return json.Unmarshal(raw, out)
		}
	case strings.HasPrefix(path, "/api/goals/"):
		return errNoGoal
	}
	return nil
}

var errNoGoal = errFake("not found")

type errFake string

func (e errFake) Error() string { return string(e) }

func run(t *testing.T, tools []Tool, name, args string) string {
	t.Helper()
	for _, tl := range tools {
		if tl.Name == name {
			var a Args
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				t.Fatal(err)
			}
			out, err := tl.Run(a)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			b, _ := json.Marshal(out)
			return string(b)
		}
	}
	t.Fatalf("no tool %s", name)
	return ""
}

// dash-mcp's goal tools find the goal's computer and call its REST API.
func TestFleetGoalTools(t *testing.T) {
	api := &goalAPI{}
	tools := FleetTools(Fleet{API: api})
	last := func() string { return api.calls[len(api.calls)-1] }

	if out := run(t, tools, "list_goals", `{}`); !strings.Contains(out, `"machine":"here"`) || !strings.Contains(out, `"0 of 0 merged"`) {
		t.Errorf("list_goals %s", out)
	}
	if out := run(t, tools, "get_goal", `{"goal":"g1"}`); !strings.Contains(out, `"id":"g1"`) {
		t.Errorf("get_goal %s", out)
	}
	// Only the given budget field changes; the other is kept.
	run(t, tools, "update_goal", `{"goal":"g1","weekly_share":10}`)
	if last() != `PATCH /api/goals/g1 {"budget":{"max_tasks":4,"weekly_share":10}}` {
		t.Errorf("update sent %s", last())
	}
	for _, action := range []string{"pause", "resume", "stop"} {
		run(t, tools, action+"_goal", `{"goal":"g1"}`)
		if !strings.HasPrefix(last(), "POST /api/goals/g1/"+action+" ") {
			t.Errorf("%s sent %s", action, last())
		}
	}
	if out := run(t, tools, "accept_goal_plan", `{"goal":"g1","queue":true}`); !strings.Contains(out, `"created":[{"id":"t1"`) {
		t.Errorf("accept_goal_plan %s", out)
	}
	if last() != `POST /api/goals/g1/plan/accept {"queue":true}` {
		t.Errorf("accept sent %s", last())
	}
	run(t, tools, "reject_goal_plan", `{"goal":"g1","feedback":"smaller"}`)
	if last() != `POST /api/goals/g1/plan/reject {"feedback":"smaller"}` {
		t.Errorf("reject sent %s", last())
	}
	run(t, tools, "message_goal", `{"goal":"g1","text":"use SQLite"}`)
	if last() != `POST /api/goals/g1/message {"text":"use SQLite"}` {
		t.Errorf("message sent %s", last())
	}

	// A read-only connector may look at goals but not change them.
	ro := ReadOnly(tools, FleetReadTools, errFake("read-only"))
	run(t, ro, "get_goal", `{"goal":"g1"}`)
	for _, tl := range ro {
		if tl.Name == "stop_goal" {
			if _, err := tl.Run(Args{"goal": "g1"}); err == nil {
				t.Error("read-only stop_goal ran")
			}
		}
	}
}
