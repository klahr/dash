package queue

import (
	"slices"
	"strings"
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
	approved := prWait{autoMerge: true, review: model.PRApproved}
	tests := []struct {
		name        string
		st          gitops.PRStatus
		code, other []gitops.PRCheck
		w           prWait
		want        string
	}{
		{"pending", open(done("lint", "SUCCESS"), running("Go"), running("Docker"), done("a", "SUCCESS"), done("b", "SKIPPED")), nil, nil, approved,
			"Waiting for checks: Go, Docker (3/5 passed)"},
		{"failed", open(done("Go", "FAILURE")), []gitops.PRCheck{done("Go", "FAILURE")}, nil, approved,
			"Check failed: Go, sent back to Claude"},
		{"failed again", open(done("Go", "FAILURE"), done("Lint", "FAILURE")), []gitops.PRCheck{done("Go", "FAILURE"), done("Lint", "FAILURE")}, nil,
			prWait{autoMerge: true, handled: true}, "Checks still failing after Claude's fix: Go, Lint"},
		{"merging", open(done("Go", "SUCCESS")), nil, nil, approved, "Merging PR #10…"},
		{"no checks", open(), nil, nil, approved, "Merging PR #10…"},
		{"auto approve", open(), nil, nil, prWait{autoMerge: true, autoApprove: true, review: model.PRReviewPending}, "Merging PR #10…"},
		{"awaiting review", open(done("Go", "SUCCESS")), nil, nil, prWait{autoMerge: true, review: model.PRReviewPending}, "Waiting for a review on GitHub"},
		{"changes requested", open(done("Go", "SUCCESS")), nil, nil, prWait{autoMerge: true, autoApprove: true, review: model.PRChangesRequested},
			"Changes requested on GitHub; waiting for the reviewer to look again"},
		{"blocked", open(cla), nil, []gitops.PRCheck{cla}, approved, "Blocked: CLA not signed"},
		{"blocked from base", open(done("assistant", "FAILURE")), nil, []gitops.PRCheck{done("assistant", "FAILURE")}, approved,
			"Blocked: assistant failed (it runs from the base branch)"},
		{"by hand, approved", open(done("Go", "SUCCESS")), nil, nil, prWait{review: model.PRApproved}, "Approved; waiting for you to merge on GitHub"},
		{"by hand", open(done("Go", "SUCCESS")), nil, nil, prWait{review: model.PRReviewPending}, "Waiting for a review on GitHub"},
		{"by hand, auto approve", open(done("Go", "SUCCESS")), nil, nil, prWait{autoApprove: true}, "Waiting for you to merge on GitHub"},
		{"refused", open(done("Go", "SUCCESS")), nil, nil, prWait{autoMerge: true, handled: true, review: model.PRApproved}, "Waiting for you to merge on GitHub"},
		{"conflict", gitops.PRStatus{State: "OPEN", Mergeable: "CONFLICTING"}, nil, nil, approved, "Conflicts with main, sent back to Claude"},
	}
	for _, tc := range tests {
		if got := prStatusLine(10, "main", tc.st, tc.code, tc.other, tc.w); got != tc.want {
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

// Where a review stands: GitHub's decision when the branch requires
// reviews, else each person's latest approval or request for changes.
func TestReviewState(t *testing.T) {
	parallel(t)

	r := func(id int64, login, typ, state string) gitops.PRReview {
		return gitops.PRReview{ID: id, User: gitops.GHUser{Login: login, Type: typ}, State: state}
	}
	tests := []struct {
		name     string
		decision string
		reviews  []gitops.PRReview
		want     string
	}{
		{"none", "", nil, model.PRReviewPending},
		{"comment only", "", []gitops.PRReview{r(1, "a", "User", "COMMENTED")}, model.PRReviewPending},
		{"approved", "", []gitops.PRReview{r(1, "a", "User", "APPROVED")}, model.PRApproved},
		{"bot approval", "", []gitops.PRReview{r(1, "b[bot]", "Bot", "APPROVED")}, model.PRReviewPending},
		{"changes then approved", "", []gitops.PRReview{r(1, "a", "User", "CHANGES_REQUESTED"), r(2, "a", "User", "APPROVED")}, model.PRApproved},
		{"one asks for changes", "", []gitops.PRReview{r(1, "a", "User", "APPROVED"), r(2, "b", "User", "CHANGES_REQUESTED")}, model.PRChangesRequested},
		{"dismissed", "", []gitops.PRReview{r(1, "a", "User", "DISMISSED")}, model.PRReviewPending},
		{"required, not yet", "REVIEW_REQUIRED", []gitops.PRReview{r(1, "a", "User", "APPROVED")}, model.PRReviewPending},
		{"required, approved", "APPROVED", nil, model.PRApproved},
		{"required, changes", "CHANGES_REQUESTED", nil, model.PRChangesRequested},
	}
	for _, tc := range tests {
		if got := reviewState(tc.decision, tc.reviews); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Claude's note carries each review's text and its comments on lines, and
// nothing when the reviews say nothing.
func TestReviewNote(t *testing.T) {
	parallel(t)

	alice := gitops.GHUser{Login: "alice", Type: "User"}
	rs := []gitops.PRReview{
		{ID: 1, User: alice, State: "CHANGES_REQUESTED", Body: "Two things.\nSee below."},
		{ID: 2, User: gitops.GHUser{Login: "bob", Type: "User"}, State: "COMMENTED"},
	}
	cs := []gitops.PRComment{
		{ReviewID: 1, User: alice, Path: "a.go", Line: 3, Body: "Use a constant"},
		{ReviewID: 2, User: alice, Path: "b.go", OriginalLine: 9, Body: "Still wrong", InReplyTo: 5},
		{ReviewID: 9, User: alice, Path: "c.go", Line: 1, Body: "Another review's"},
	}
	note := reviewNote(4, rs, cs)
	for _, want := range []string{"pull request #4", "**alice** requested changes", "> Two things.\n> See below.",
		"On `a.go:3`", "> Use a constant", "**bob** commented", "On `b.go:9` (in reply to an earlier comment)"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "c.go") {
		t.Errorf("another review's comment in the note:\n%s", note)
	}
	if got := reviewNote(4, []gitops.PRReview{{ID: 3, User: alice, State: "COMMENTED"}}, nil); got != "" {
		t.Errorf("an empty review made a note: %q", got)
	}
}
