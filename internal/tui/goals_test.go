package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/klahr/dash/internal/model"
)

func TestGoalFilter(t *testing.T) {
	a := NewApp(nil, "")
	a.goals = map[string]model.Goal{"g1": {ID: "g1", Title: "Faster search"}, "g2": {ID: "g2", Title: "Sign-in"}}
	a.todos = map[string]model.Todo{
		"t1": {ID: "t1", Title: "Index", Status: model.StatusQueued, GoalID: "g1"},
		"t2": {ID: "t2", Title: "Label", Status: model.StatusQueued, GoalID: "g2"},
		"t3": {ID: "t3", Title: "Other", Status: model.StatusQueued},
	}
	a.rebuildCols()
	if n := len(a.cols[1]); n != 3 {
		t.Fatalf("unfiltered: %d queued, want 3", n)
	}

	a.runCommand("goal faster")
	if a.goal != "g1" || len(a.cols[1]) != 1 || a.cols[1][0].ID != "t1" {
		t.Fatalf(":goal faster: filter %q, queued %v", a.goal, a.cols[1])
	}
	a.runCommand("goal")
	if a.goal != "" || len(a.cols[1]) != 3 {
		t.Fatalf(":goal clears: filter %q, %d queued", a.goal, len(a.cols[1]))
	}
	a.runCommand("goal nope")
	if a.goal != "" || !a.flashErr {
		t.Fatalf(":goal nope: filter %q, flash %q", a.goal, a.flash)
	}

	// gf filters by the selected task's goal, and again shows every task.
	a.col, a.row = 1, 0
	sel, _ := a.selected()
	a.normalKey("g")
	a.normalKey("f")
	if sel.GoalID == "" || a.goal != sel.GoalID {
		t.Fatalf("gf: filter %q, want %q", a.goal, sel.GoalID)
	}
	a.filterGoal(a.goal)
	if a.goal != "" {
		t.Fatalf("gf again: filter %q, want none", a.goal)
	}

	card := ansi.Strip(strings.Join(a.renderCard(a.todos["t1"], 40, false), "\n"))
	if !strings.Contains(card, "◆ Faster search") {
		t.Fatalf("card lacks its goal: %q", card)
	}
	if s1, s2 := goalStyle("g1"), goalStyle("g1"); s1.GetForeground() != s2.GetForeground() {
		t.Fatal("a goal's colour changes")
	}
}
