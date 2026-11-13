package mcptools

import (
	"slices"
	"strings"
	"testing"
)

func TestPlanItems(t *testing.T) {
	items, err := planItems([]any{
		map[string]any{"title": "Index", "prompt": "Add an index"},
		map[string]any{"title": "Use it", "after": []any{0.0, 0.0}},
		map[string]any{"prompt": "Docs", "after": 1.0, "model": "haiku"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[1].Prompt != "Use it" || !slices.Equal(items[1].After, []int{0}) || !slices.Equal(items[2].After, []int{1}) || items[2].Model != "haiku" {
		t.Fatalf("items %+v", items)
	}
	for _, bad := range []any{
		"x",
		[]any{},
		[]any{map[string]any{"title": "a", "after": []any{0.0}}}, // after itself
		[]any{map[string]any{"title": "a"}, map[string]any{"title": "b", "after": []any{"0"}}},
		[]any{map[string]any{"title": ""}},
	} {
		if _, err := planItems(bad); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}
}

// A coordinator never gets a tool that approves, merges or publishes.
func TestGoalToolNames(t *testing.T) {
	for _, name := range append(slices.Clone(GoalBoardTools), GoalToolNames...) {
		for _, banned := range []string{"approve", "merge", "release", "start_now", "checklist_item"} {
			if strings.Contains(name, banned) {
				t.Errorf("coordinator tool %s", name)
			}
		}
	}
}
