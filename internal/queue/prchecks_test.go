package queue

import (
	"slices"
	"testing"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// Failed checks no change to the branch can fix don't go back to Claude.
func TestClassifyFailed(t *testing.T) {
	parallel(t)

	failed := []gitops.PRCheck{{Name: "assistant"}, {Name: "test"}, {Name: "lint"}, {Context: "license/cla"}}
	events := map[string]string{"assistant": "pull_request_target", "test": "pull_request", "lint": "pull_request"}
	code, other := classifyFailed(failed, map[string]bool{"assistant": true, "test": true}, events)
	if len(code) != 1 || code[0].Name != "test" || len(other) != 1 || other[0].Name != "assistant" {
		t.Fatalf("code = %+v, other = %+v", code, other)
	}
	// Without required checks every check counts.
	code, other = classifyFailed(failed, nil, events)
	if len(code) != 2 || len(other) != 2 {
		t.Fatalf("code = %+v, other = %+v", code, other)
	}
}

// A merging card says what its pull request waits for.
func TestPRStatusLine(t *testing.T) {
	parallel(t)

	done := func(name, conclusion string) gitops.PRCheck {
		return gitops.PRCheck{Name: name, Status: "COMPLETED", Conclusion: conclusion}
	}
	running := func(name string) gitops.PRCheck { return gitops.PRCheck{Name: name, Status: "IN_PROGRESS"} }
	open := func(cs ...gitops.PRCheck) gitops.PRStatus {
		return gitops.PRStatus{State: "OPEN", Mergeable: "MERGEABLE", HeadRefOid: "abc", Checks: cs}
	}
	cla := gitops.PRCheck{Context: "license/cla", State: "FAILURE"}
	tests := []struct {
		name          string
		st            gitops.PRStatus
		code, other   []gitops.PRCheck
		auto, handled bool
		want          string
	}{
		{"pending", open(done("lint", "SUCCESS"), running("Go"), running("Docker"), done("a", "SUCCESS"), done("b", "SKIPPED")), nil, nil, true, false,
			"Waiting for checks: Go, Docker (3/5 passed)"},
		{"failed", open(done("Go", "FAILURE")), []gitops.PRCheck{done("Go", "FAILURE")}, nil, true, false,
			"Check failed: Go, sent back to Claude"},
		{"failed again", open(done("Go", "FAILURE"), done("Lint", "FAILURE")), []gitops.PRCheck{done("Go", "FAILURE"), done("Lint", "FAILURE")}, nil, true, true,
			"Checks still failing after Claude's fix: Go, Lint"},
		{"merging", open(done("Go", "SUCCESS")), nil, nil, true, false, "Merging PR #10…"},
		{"no checks", open(), nil, nil, true, false, "Merging PR #10…"},
		{"blocked", open(cla), nil, []gitops.PRCheck{cla}, true, false, "Blocked: CLA not signed"},
		{"blocked from base", open(done("assistant", "FAILURE")), nil, []gitops.PRCheck{done("assistant", "FAILURE")}, true, false,
			"Blocked: assistant failed (it runs from the base branch)"},
		{"by hand", open(done("Go", "SUCCESS")), nil, nil, false, false, "Waiting for you to merge on GitHub"},
		{"refused", open(done("Go", "SUCCESS")), nil, nil, true, true, "Waiting for you to merge on GitHub"},
		{"conflict", gitops.PRStatus{State: "OPEN", Mergeable: "CONFLICTING"}, nil, nil, true, false, "Conflicts with main, sent back to Claude"},
	}
	for _, tc := range tests {
		if got := prStatusLine(10, "main", tc.st, tc.code, tc.other, tc.auto, tc.handled); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPRChecks(t *testing.T) {
	parallel(t)

	got := prChecks([]gitops.PRCheck{
		{Name: "Go", Status: "COMPLETED", Conclusion: "SUCCESS", DetailsURL: "https://ci/1"},
		{Name: "Docker", Status: "QUEUED"},
		{Context: "license/cla", State: "FAILURE", TargetURL: "https://cla"},
	})
	want := []model.PRCheck{
		{Name: "Go", State: "passed", URL: "https://ci/1"},
		{Name: "Docker", State: "pending"},
		{Name: "license/cla", State: "failed", URL: "https://cla"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if prChecks(nil) != nil {
		t.Fatal("no checks: want nil")
	}
}
