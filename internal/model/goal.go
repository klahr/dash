package model

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// GoalStatus is where a goal's coordinator stands.
//
//	Planning ──plan──▶ WaitingForYou ──accept──▶ Working ──finish──▶ Done
//	    │      (ask mode)                           ▲  │
//	    └──────────plan (queue on its own)──────────┘  └──budget used up──▶ OutOfBudget
//
// Paused holds any of the active ones (planning, working, waiting for you);
// resuming goes back to where it was. Stopping a goal makes it Done.
type GoalStatus string

const (
	GoalPlanning    GoalStatus = "planning"        // the coordinator reads the code and proposes a plan
	GoalWorking     GoalStatus = "working"         // its tasks run; task events wake the coordinator
	GoalWaiting     GoalStatus = "waiting_for_you" // a plan to accept, or a question for the user
	GoalPaused      GoalStatus = "paused"          // task events are kept, the coordinator isn't woken
	GoalDone        GoalStatus = "done"            // finished, or stopped by the user
	GoalOutOfBudget GoalStatus = "out_of_budget"   // its budget is used up
)

// Active reports whether the coordinator may still be woken (not paused,
// done or out of budget).
func (s GoalStatus) Active() bool {
	return s == GoalPlanning || s == GoalWorking || s == GoalWaiting
}

// Ended reports whether the goal is over: done, or out of budget until the
// user raises the budget and resumes it.
func (s GoalStatus) Ended() bool { return s == GoalDone || s == GoalOutOfBudget }

// Goal modes: what the coordinator may do with the tasks it plans.
const (
	GoalAsk   = "ask"   // ask before queuing: its plan and new tasks wait for the user (the default)
	GoalQueue = "queue" // queue on its own: its plan and follow-ups are queued right away
)

// GoalBudget caps what a goal's coordinator may spend. Zero means no cap.
type GoalBudget struct {
	// MaxTasks is how many tasks the coordinator may create for the goal
	// (deleted ones don't count).
	MaxTasks int `json:"max_tasks,omitempty"`
	// WeeklyShare is the share of the weekly usage window, in percent
	// (1–100), the goal's tasks may use: estimated as the window's
	// utilization times the goal's tasks' part of all tasks' cost since the
	// window started.
	WeeklyShare int `json:"weekly_share,omitempty"`
}

// Goal is a long-lived aim in one project, worked towards by a coordinator:
// Claude, run with the chat machinery, that plans and dispatches tasks and
// reviews what they did, but never approves or merges them. Task events of
// the goal's tasks (entered review, failed, merged) wake it; nothing else
// does. The coordinator's session is its conversation, kept with the goal
// (Store.GoalChat): each wake-up and each message the user sends it
// continues it.
type Goal struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	// Owner is the user who made the goal: the coordinator acts as them, in
	// the project's org (DeletedUser once their account is deleted; then it
	// can't run).
	Owner       int64      `json:"owner"`
	Title       string     `json:"title"`
	Description string     `json:"description"` // what the user wants done
	Budget      GoalBudget `json:"budget"`
	Mode        string     `json:"mode"` // GoalAsk or GoalQueue
	Status      GoalStatus `json:"status"`
	// PausedFrom is the status a paused goal goes back to on resume.
	PausedFrom GoalStatus `json:"paused_from,omitempty"`
	// WaitingFor is what a goal waiting for the user waits for:
	// GoalWaitPlan, GoalWaitQuestion or GoalWaitDrafts.
	WaitingFor string `json:"waiting_for,omitempty"`
	// Note says why the goal waits, ended or can't go on (e.g. the
	// coordinator's question, or that its budget is used up).
	Note string `json:"note,omitempty"`
	// Plan is the coordinator's proposed plan, waiting for the user to
	// accept it (ask mode); empty once accepted or rejected.
	Plan []GoalPlanItem `json:"plan,omitempty"`
	// Pending are task events that came while the coordinator couldn't be
	// woken (it was busy, paused, or couldn't run): the next wake-up gets
	// them all.
	Pending []GoalWake `json:"pending,omitempty"`
	// Running is set while a coordinator turn runs (never stored as true
	// across a restart).
	Running bool `json:"running,omitempty"`
	// Log is what the coordinator did and why, oldest first (at most
	// MaxGoalLog entries; older ones are dropped).
	Log       []GoalLogEntry `json:"log,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// GoalPlanItem is one task of a coordinator's plan.
type GoalPlanItem struct {
	Title  string `json:"title"`
	Prompt string `json:"prompt"`
	// After are the items (indexes into the plan, all before this one) that
	// must be merged before this one starts: its depends_on.
	After []int  `json:"after,omitempty"`
	Model string `json:"model,omitempty"`
}

// What a goal waiting for the user waits for (Goal.WaitingFor).
const (
	GoalWaitPlan     = "plan"     // to accept or reject the coordinator's plan
	GoalWaitQuestion = "question" // to answer the coordinator's question in the goal's chat
	GoalWaitDrafts   = "drafts"   // to queue the goal's draft tasks (ask mode)
)

// GoalWake is something a coordinator is woken for: one of the goal's
// tasks entering review, failing or being merged, or the user starting the
// goal or rejecting its plan.
type GoalWake struct {
	// Event is the task's new status (review, failed, merged), or
	// GoalWakeStart or GoalWakeReplan.
	Event string    `json:"event"`
	Todo  string    `json:"todo,omitempty"`
	Title string    `json:"title,omitempty"`
	Note  string    `json:"note,omitempty"` // a failed task's error; the user's feedback on a rejected plan
	At    time.Time `json:"at"`
}

// Wake-ups that aren't task events.
const (
	GoalWakeStart   = "start"   // the goal was made: read the code and propose a plan
	GoalWakeReplan  = "replan"  // the user rejected the plan
	GoalWakeMessage = "message" // the user sent the coordinator a message (Note) outside its chat
)

// Kinds of a goal's log entries.
const (
	GoalLogPlan   = "plan"   // the coordinator proposed or made a plan
	GoalLogWake   = "wake"   // a task event woke it
	GoalLogAction = "action" // it created, edited, queued, sent back or dropped a task
	GoalLogNote   = "note"   // what it concluded, and why
	GoalLogUser   = "user"   // the user accepted, paused, resumed, edited or stopped the goal
	GoalLogStatus = "status" // Dash changed the goal's status (budget used up, …)
)

// GoalLogEntry is one thing a coordinator did, and why.
type GoalLogEntry struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	Text string    `json:"text"`
	Why  string    `json:"why,omitempty"`
	Todo string    `json:"todo,omitempty"` // the task it is about, if any
}

// MaxGoalLog is how many log entries a goal keeps.
const MaxGoalLog = 500

// AddLog appends an entry to g's log, dropping the oldest beyond MaxGoalLog.
func (g *Goal) AddLog(e GoalLogEntry) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	g.Log = append(g.Log, e)
	if n := len(g.Log) - MaxGoalLog; n > 0 {
		g.Log = append([]GoalLogEntry(nil), g.Log[n:]...)
	}
}

// Validation limits of a goal's fields.
const (
	MaxGoalTitle       = 200
	MaxGoalDescription = 20000
	MaxGoalPlan        = 30
)

// CleanGoal trims g's user-editable fields and checks them: a title (or a
// description to name it from), a known mode ("" is GoalAsk) and a budget
// within range.
func CleanGoal(g *Goal) error {
	g.Title = strings.TrimSpace(g.Title)
	g.Description = strings.TrimSpace(g.Description)
	switch {
	case g.Title == "" && g.Description == "":
		return errors.New("a goal needs a title or a description")
	case len(g.Title) > MaxGoalTitle:
		return fmt.Errorf("the title is longer than %d characters", MaxGoalTitle)
	case len(g.Description) > MaxGoalDescription:
		return fmt.Errorf("the description is longer than %d characters", MaxGoalDescription)
	}
	if g.Title == "" {
		g.Title = firstLine(g.Description, 80)
	}
	switch g.Mode {
	case "":
		g.Mode = GoalAsk
	case GoalAsk, GoalQueue:
	default:
		return fmt.Errorf("unknown mode %q: use %q (ask before queuing) or %q (queue on its own)", g.Mode, GoalAsk, GoalQueue)
	}
	if g.Budget.MaxTasks < 0 || g.Budget.MaxTasks > 1000 {
		return errors.New("max_tasks must be between 0 (no limit) and 1000")
	}
	if g.Budget.WeeklyShare < 0 || g.Budget.WeeklyShare > 100 {
		return errors.New("weekly_share must be a percentage between 0 (no limit) and 100")
	}
	return nil
}

// CleanPlan trims a proposed plan and checks it: 1 to MaxGoalPlan items,
// each with instructions, each after earlier items only.
func CleanPlan(items []GoalPlanItem) ([]GoalPlanItem, error) {
	if len(items) == 0 {
		return nil, errors.New("the plan has no tasks")
	}
	if len(items) > MaxGoalPlan {
		return nil, fmt.Errorf("the plan has %d tasks; at most %d", len(items), MaxGoalPlan)
	}
	out := make([]GoalPlanItem, 0, len(items))
	for i, it := range items {
		it.Title, it.Prompt = strings.TrimSpace(it.Title), strings.TrimSpace(it.Prompt)
		if it.Prompt == "" {
			it.Prompt = it.Title
		}
		if it.Prompt == "" {
			return nil, fmt.Errorf("task %d of the plan has no instructions", i)
		}
		m, err := CleanModel(it.Model)
		if err != nil {
			return nil, fmt.Errorf("task %d of the plan: %w", i, err)
		}
		it.Model = m
		var after []int
		for _, a := range it.After {
			if a < 0 || a >= i {
				return nil, fmt.Errorf("task %d of the plan comes after task %d: it can only come after earlier tasks (0 to %d)", i, a, i-1)
			}
			if !slices.Contains(after, a) {
				after = append(after, a)
			}
		}
		it.After = after
		out = append(out, it)
	}
	return out, nil
}

// firstLine is s's first non-empty line, cut at max runes.
func firstLine(s string, max int) string {
	for l := range strings.SplitSeq(s, "\n") {
		if l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#>*-+ ")); l != "" {
			if r := []rune(l); len(r) > max {
				return string(r[:max]) + "…"
			}
			return l
		}
	}
	return ""
}

// GoalTimeline is the prefix of the notes a coordinator leaves on its
// tasks' timelines, e.g. "By coordinator · goal Faster search: created
// this task".
const GoalTimeline = "By coordinator · goal "
