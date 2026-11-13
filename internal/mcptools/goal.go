package mcptools

import (
	"errors"
	"fmt"
	"slices"

	"github.com/klahr/dash/internal/model"
)

// GoalBoard is the board as one goal's coordinator may use it: the goal's
// project only, and only the goal's own tasks to change. It has no way to
// approve or merge a task: that stays the user's.
type GoalBoard interface {
	Board
	Restarter
	// Goal is the coordinator's goal, with its log.
	Goal() (model.Goal, error)
	// GoalTodos are the tasks the goal's coordinator made.
	GoalTodos() ([]model.Todo, error)
	// ProposePlan proposes the goal's tasks: in "ask" mode they wait for
	// the user to accept them, in "queue" mode they are queued now. It
	// returns what happened, for the coordinator.
	ProposePlan(items []model.GoalPlanItem, why string) (string, error)
	// SendBack queues one of the goal's tasks in review or failed again,
	// with notes for Claude.
	SendBack(todo, notes string) (model.Todo, error)
	// ReviewNote sets the coordinator's short review note on one of the
	// goal's tasks, shown on its Changes tab.
	ReviewNote(todo, note string) (model.Todo, error)
	// AddLog adds an entry to the goal's log: what the coordinator did or
	// concluded, and why.
	AddLog(text, why, todo string) error
	// AskUser makes the goal wait for the user, with a question.
	AskUser(question string) error
	// FinishGoal marks the goal done, with a summary for the user.
	FinishGoal(summary string) error
	// Diff and Log are what one of the project's tasks changed, and its
	// Claude run log.
	Diff(todo string) (string, error)
	Log(todo string) (string, error)
}

// GoalInstructions describe the dash_board server to a coordinator.
const GoalInstructions = "The Dash board of this goal's project. You are the goal's coordinator: you plan, create and send back tasks, " +
	"but never approve or merge them; the user does."

// GoalBoardTools are the board tools a coordinator gets (see GoalTools):
// none that publishes (create_release, the checklist writes) or starts a
// task despite the weekly pace.
var GoalBoardTools = []string{"list_todos", "get_todo", "create_todo", "queue_todo", "stop_todo", "restart_todo", "update_todo", "delete_todo",
	"read_checklist", "list_issues", "report_issue"}

// GoalToolNames are the tools only a coordinator has.
var GoalToolNames = []string{"get_goal", "propose_plan", "send_back", "post_review_note", "add_log", "ask_user", "finish_goal", "get_diff", "get_log"}

// GoalTools returns a coordinator's tools: GoalBoardTools on the goal's
// project, which only change the goal's own tasks, and the goal's tools.
func GoalTools(b GoalBoard, o Options) []Tool {
	var tools []Tool
	descs := map[string]string{
		"create_todo": "Create a task for this goal (a follow-up, or a task the plan missed). It counts against the goal's task budget. " +
			"In \"ask\" mode it stays a draft for the user to queue; in \"queue\" mode it is queued unless draft is true.",
		"queue_todo": "Queue one of this goal's draft or failed tasks to run. Only in \"queue\" mode; in \"ask\" mode the user queues tasks.",
		"stop_todo": "Stop one of this goal's queued or running tasks (a running one ends failed, keeping its worktree and Claude session; " +
			"a queued one goes back to draft), e.g. when the plan changed and it is no longer needed as it is.",
		"restart_todo": "Run one of this goal's running, queued or failed tasks again: stop it if needed, change its prompt or title if given, and queue it.",
		"update_todo":  "Rework one of this goal's tasks: its title, prompt, priority, dependencies, the task it builds on or its model. The prompt only while it is a draft, queued or failed.",
		"delete_todo":  "Drop one of this goal's draft, queued or failed tasks that is no longer needed.",
	}
	for _, t := range Tools(b, o) {
		if !slices.Contains(GoalBoardTools, t.Name) {
			continue
		}
		if d, ok := descs[t.Name]; ok {
			t.Desc = d
		}
		tools = append(tools, t)
	}
	need := func(a Args, k string) (string, error) {
		v := a.Str(k)
		if v == "" {
			return "", fmt.Errorf("%s is required", k)
		}
		return v, nil
	}
	return append(tools, []Tool{
		{"get_goal", "Show the goal: what the user wants done, its mode, budget, status, the plan waiting for the user, " +
			"its tasks with their status, and the latest entries of its log.", nil,
			func(Args) (any, error) {
				g, err := b.Goal()
				if err != nil {
					return nil, err
				}
				ts, err := b.GoalTodos()
				if err != nil {
					return nil, err
				}
				type task struct {
					ID        string           `json:"id"`
					Title     string           `json:"title"`
					Status    model.TodoStatus `json:"status"`
					DependsOn []string         `json:"depends_on,omitempty"`
					Error     string           `json:"error,omitempty"`
				}
				out := struct {
					model.Goal
					Tasks []task `json:"tasks"`
				}{Goal: g, Tasks: []task{}}
				if n := len(out.Log) - 20; n > 0 {
					out.Log = out.Log[n:]
				}
				out.Pending = nil
				for _, t := range ts {
					out.Tasks = append(out.Tasks, task{t.ID, t.Title, t.Status, t.DependsOn, t.Error})
				}
				return out, nil
			}},
		{"propose_plan", "Propose the goal's plan: the tasks to do it, in order, each with instructions for Claude and the earlier tasks it needs " +
			"merged first (after: their indexes in this list, which become its depends_on). In \"ask\" mode the plan waits for the user to accept it " +
			"(they may edit it), then becomes draft tasks; in \"queue\" mode the tasks are queued now. Proposing again replaces a plan not yet accepted.",
			[]Param{{"tasks", ObjectArray, "The tasks, as JSON objects {\"title\", \"prompt\", \"after\": [indexes of earlier tasks], \"model\"}.", true},
				{"why", "string", "Why this plan: the approach, in a few sentences, for the goal's log.", false}},
			func(a Args) (any, error) {
				items, err := planItems(a["tasks"])
				if err != nil {
					return nil, err
				}
				return b.ProposePlan(items, a.Str("why"))
			}},
		{"send_back", "Send one of this goal's tasks in review (or failed) back to Claude with notes: it continues in its worktree and session " +
			"with your notes, like the reviewer's Rework.", []Param{PTodo,
			{"notes", "string", "What to change, for Claude.", true}},
			func(a Args) (any, error) {
				id, err := need(a, "todo")
				if err != nil {
					return nil, err
				}
				notes, err := need(a, "notes")
				if err != nil {
					return nil, err
				}
				t, err := b.SendBack(id, notes)
				if err != nil {
					return nil, err
				}
				return queued(t), nil
			}},
		{"post_review_note", "Post a short review note on one of this goal's tasks, for the user reviewing its changes (shown on its Changes tab): " +
			"what you checked and what you think of it. A new note replaces the last.", []Param{PTodo,
			{"note", "string", "A few sentences of Markdown.", true}},
			func(a Args) (any, error) {
				id, err := need(a, "todo")
				if err != nil {
					return nil, err
				}
				note, err := need(a, "note")
				if err != nil {
					return nil, err
				}
				if _, err := b.ReviewNote(id, note); err != nil {
					return nil, err
				}
				return "Posted.", nil
			}},
		{"add_log", "Add an entry to the goal's log: what you did or concluded, and why. Add one on every wake-up.", []Param{
			{"text", "string", "What you did or concluded, in one or two sentences.", true},
			{"why", "string", "Why.", false},
			{"todo", "string", "The task it is about, if any.", false}},
			func(a Args) (any, error) {
				text, err := need(a, "text")
				if err != nil {
					return nil, err
				}
				if err := b.AddLog(text, a.Str("why"), a.Str("todo")); err != nil {
					return nil, err
				}
				return "Logged.", nil
			}},
		{"ask_user", "Make the goal wait for the user with a question, when you can't go on without them (a decision, or something unclear). " +
			"They answer in the goal's chat.", []Param{{"question", "string", "The question.", true}},
			func(a Args) (any, error) {
				q, err := need(a, "question")
				if err != nil {
					return nil, err
				}
				if err := b.AskUser(q); err != nil {
					return nil, err
				}
				return "The goal now waits for the user.", nil
			}},
		{"finish_goal", "Mark the goal done, once what the user wanted is merged (or can't be done). Nothing wakes you after it.",
			[]Param{{"summary", "string", "What was done, for the user.", true}},
			func(a Args) (any, error) {
				sum, err := need(a, "summary")
				if err != nil {
					return nil, err
				}
				if err := b.FinishGoal(sum); err != nil {
					return nil, err
				}
				return "The goal is done.", nil
			}},
		{"get_diff", "Read the changes a task made (a unified diff against its base branch).", []Param{PTodo},
			func(a Args) (any, error) {
				id, err := need(a, "todo")
				if err != nil {
					return nil, err
				}
				d, err := b.Diff(id)
				return clip(d), err
			}},
		{"get_log", "Read a task's Claude run log.", []Param{PTodo},
			func(a Args) (any, error) {
				id, err := need(a, "todo")
				if err != nil {
					return nil, err
				}
				l, err := b.Log(id)
				return clip(l), err
			}},
	}...)
}

// maxGoalText caps a diff or log a coordinator reads in one call: its end
// is kept, where a log's outcome is.
const maxGoalText = 200_000

func clip(s string) string {
	if len(s) <= maxGoalText {
		return s
	}
	return "[… the first " + fmt.Sprint(len(s)-maxGoalText) + " bytes are cut]\n" + s[len(s)-maxGoalText:]
}

// planItems reads propose_plan's tasks argument.
func planItems(v any) ([]model.GoalPlanItem, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, errors.New("tasks must be an array of {title, prompt, after, model} objects")
	}
	var out []model.GoalPlanItem
	for i, x := range list {
		m, ok := x.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("task %d must be an object {title, prompt, after, model}", i)
		}
		a := Args(m)
		it := model.GoalPlanItem{Title: a.Str("title"), Prompt: a.Str("prompt"), Model: a.Str("model")}
		switch after := m["after"].(type) {
		case nil:
		case []any:
			for _, n := range after {
				f, ok := n.(float64)
				if !ok {
					return nil, fmt.Errorf("task %d: after must list indexes of earlier tasks", i)
				}
				it.After = append(it.After, int(f))
			}
		case float64:
			it.After = []int{int(after)}
		default:
			return nil, fmt.Errorf("task %d: after must list indexes of earlier tasks", i)
		}
		out = append(out, it)
	}
	return model.CleanPlan(out)
}
