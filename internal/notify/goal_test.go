package notify

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// goalStore is a fakeStore with goals, whose statuses Start remembers.
type goalStore struct {
	*fakeStore
	goals []model.Goal
}

func (g goalStore) ListGoals(string) []model.Goal { return g.goals }

// A goal notifies when it starts waiting for its user, is done or runs out
// of budget: once per change, not for statuses it had when Dash started.
func TestGoalNotifications(t *testing.T) {
	r := newReceiver(t)
	_, fs, _ := setup(t)
	fs.hooks = []model.Webhook{{ID: "a", ProjectID: "p1", URL: r.URL, Format: model.WebhookJSON, Enabled: true, Events: model.NotifyEvents, Secret: "s"}}
	gs := goalStore{fakeStore: fs, goals: []model.Goal{{ID: "g1", ProjectID: "p1", Title: "Faster search", Status: model.GoalWaiting}}}
	n := &Notifier{Store: gs, AllowPrivate: true, Origin: func(string) string { return "https://dash.example.com" }}
	n.Start(t.Context())
	goal := func(id string, st model.GoalStatus, note string) {
		n.Observe(model.Event{Type: model.EvGoalUpdated, ProjectID: "p1", Data: model.Goal{ID: id, ProjectID: "p1", Title: "Faster search", Status: st, Note: note}})
	}
	goal("g1", model.GoalWaiting, "") // as it was when Dash started
	goal("g1", model.GoalWorking, "")
	goal("g1", model.GoalWaiting, "A plan of 3 tasks waits for you to accept it.")
	goal("g1", model.GoalWaiting, "") // no change
	goal("g1", model.GoalWorking, "")
	goal("g1", model.GoalOutOfBudget, "Out of budget: its 3 tasks are used up and finished.")
	goal("g2", model.GoalPlanning, "") // a new goal
	goal("g2", model.GoalDone, "Stopped by you.")
	settle(n)
	if got := strings.Join(r.events(), " "); got != "goal.attention goal.attention goal.attention" {
		t.Fatalf("events %s", got)
	}
	// Deliveries run at once, so they come in any order.
	var p struct {
		Message string  `json:"message"`
		Goal    GoalRef `json:"goal"`
	}
	r.mu.Lock()
	bodies := slices.Clone(r.bodies)
	r.mu.Unlock()
	var statuses []string
	for _, b := range bodies {
		_ = json.Unmarshal([]byte(b), &p)
		statuses = append(statuses, p.Goal.ID+":"+string(p.Goal.Status))
		if p.Goal.Status == model.GoalWaiting && (p.Goal.URL != "https://dash.example.com/?goal=g1&project=p1" || !strings.Contains(p.Message, "plan of 3 tasks")) {
			t.Fatalf("payload %s", b)
		}
	}
	slices.Sort(statuses)
	if got := strings.Join(statuses, " "); got != "g1:out_of_budget g1:waiting_for_you g2:done" {
		t.Fatalf("goals told %s", got)
	}
	note := Notification{Type: model.NotifyGoal, Project: ProjectRef{ID: "p1", Name: "Shop"}, Goal: &GoalRef{ID: "g1", Title: "Faster search", Status: model.GoalOutOfBudget}}
	if note.Head() != "Goal out of budget" || note.subject() != "Faster search" {
		t.Fatalf("head %q subject %q", note.Head(), note.subject())
	}
	if pp := pushPayload(note, nil); pp.Tag != "dash-goal-g1" || !strings.Contains(pp.URL, "goal=g1") {
		t.Fatalf("push %+v", pp)
	}
}
