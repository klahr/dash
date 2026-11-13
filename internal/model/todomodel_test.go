package model

import "testing"

func TestCleanModel(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "  ": "", " opus ": "opus", "sonnet": "sonnet",
		"claude-sonnet-5-5": "claude-sonnet-5-5", "claude-opus-5-5[1m]": "claude-opus-5-5[1m]",
		"us.anthropic.claude-haiku-5-5:0": "us.anthropic.claude-haiku-5-5:0",
	} {
		if got, err := CleanModel(in); err != nil || got != want {
			t.Errorf("CleanModel(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"--dangerously-skip-permissions", "-x", "opus sonnet", "op;us", "1opus", "op\nus"} {
		if _, err := CleanModel(bad); err == nil {
			t.Errorf("CleanModel(%q) accepted", bad)
		}
	}
}

func TestBlockersWaitForTheTaskBuiltOnUntilItIsFinished(t *testing.T) {
	for st, blocks := range map[TodoStatus]bool{StatusDraft: true, StatusReview: true, StatusMerging: true, StatusMerged: false, StatusFailed: false} {
		all := []Todo{{ID: "p", Status: st}}
		if got := len(Todo{BuildsOn: "p"}.Blockers(all)) > 0; got != blocks {
			t.Errorf("%s: blocks %v, want %v", st, got, blocks)
		}
		// On a runner, the hub already waited.
		if len(Todo{BuildsOn: "p", HubTodoID: "h"}.Blockers(all)) > 0 {
			t.Errorf("%s: a hub's task waits on its runner", st)
		}
	}
	if b := (Todo{BuildsOn: "p", DependsOn: []string{"p"}}).Blockers([]Todo{{ID: "p", Status: StatusReview}}); len(b) != 1 {
		t.Errorf("listed once: %v", b)
	}
}
