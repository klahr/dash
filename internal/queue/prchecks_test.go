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
	approved := prWait{review: model.PRApproved}
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
			prWait{handled: true}, "Checks still failing after Claude's fix: Go, Lint"},
		{"approved", open(done("Go", "SUCCESS")), nil, nil, approved, "Approved; waiting for you to merge on GitHub"},
		{"no checks", open(), nil, nil, approved, "Approved; waiting for you to merge on GitHub"},
		{"auto approve", open(), nil, nil, prWait{autoApprove: true, review: model.PRReviewPending}, "Waiting for you to merge on GitHub"},
		{"awaiting review", open(done("Go", "SUCCESS")), nil, nil, prWait{review: model.PRReviewPending}, "Waiting for a review on GitHub"},
		{"changes requested", open(done("Go", "SUCCESS")), nil, nil, prWait{autoApprove: true, review: model.PRChangesRequested},
			"Changes requested on GitHub; waiting for the reviewer to look again"},
		{"blocked", open(cla), nil, []gitops.PRCheck{cla}, approved, "Blocked: CLA not signed"},
		{"blocked from base", open(done("assistant", "FAILURE")), nil, []gitops.PRCheck{done("assistant", "FAILURE")}, approved,
			"Blocked: assistant failed (it runs from the base branch)"},
		{"handled, approved", open(done("Go", "SUCCESS")), nil, nil, prWait{handled: true, review: model.PRApproved}, "Approved; waiting for you to merge on GitHub"},
		{"handled, awaiting review", open(done("Go", "SUCCESS")), nil, nil, prWait{handled: true, review: model.PRReviewPending}, "Waiting for a review on GitHub"},
		{"conflict", gitops.PRStatus{State: "OPEN", Mergeable: "CONFLICTING"}, nil, nil, approved, "Conflicts with main, sent back to Claude"},
	}
	for _, tc := range tests {
		if got := prStatusLine("main", tc.st, tc.code, tc.other, tc.w); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Whether a pull request waits for a person, for the wall.
func TestPRWaitsOn(t *testing.T) {
	parallel(t)

	passed := gitops.PRCheck{Name: "Go", Status: "COMPLETED", Conclusion: "SUCCESS"}
	running := gitops.PRCheck{Name: "Go", Status: "IN_PROGRESS"}
	failed := gitops.PRCheck{Name: "Go", Status: "COMPLETED", Conclusion: "FAILURE"}
	cla := gitops.PRCheck{Context: "license/cla", State: "FAILURE"}
	open := func(draft bool, cs ...gitops.PRCheck) gitops.PRStatus {
		return gitops.PRStatus{State: "OPEN", Mergeable: "MERGEABLE", HeadRefOid: "abc", IsDraft: draft, Checks: cs}
	}
	pending, approved, changes := prWait{review: model.PRReviewPending}, prWait{review: model.PRApproved}, prWait{review: model.PRChangesRequested}
	tests := []struct {
		name        string
		st          gitops.PRStatus
		code, other []gitops.PRCheck
		w           prWait
		want        string
	}{
		{"draft", open(true, running), nil, nil, pending, ""},
		{"draft, checks fail", open(true, failed), []gitops.PRCheck{failed}, nil, pending, ""},
		{"awaiting review", open(false, passed), nil, nil, pending, model.PRWaitReview},
		{"awaiting review, checks rerun", open(false, running), nil, nil, pending, model.PRWaitReview},
		{"changes requested", open(false, passed), nil, nil, changes, ""},
		{"approved, checks running", open(false, running), nil, nil, approved, ""},
		{"approved", open(false, passed), nil, nil, approved, model.PRWaitMerge},
		{"handled, approved", open(false, passed), nil, nil, prWait{handled: true, review: model.PRApproved}, model.PRWaitMerge},
		{"handled, awaiting review", open(false, passed), nil, nil, prWait{handled: true, review: model.PRReviewPending}, model.PRWaitReview},
		{"auto approve", open(false, passed), nil, nil, prWait{autoApprove: true}, model.PRWaitMerge},
		{"blocked", open(false, cla), nil, []gitops.PRCheck{cla}, approved, model.PRWaitBlocked},
		{"conflict", gitops.PRStatus{State: "OPEN", Mergeable: "CONFLICTING"}, nil, nil, pending, ""},
	}
	for _, tc := range tests {
		if got := prWaitsOn(tc.st, tc.code, tc.other, tc.w); got != tc.want {
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
		{ID: 20, ReviewID: 1, User: alice, Path: "a.go", Line: 3, Body: "Use a constant"},
		{ID: 21, ReviewID: 2, User: alice, Path: "b.go", OriginalLine: 9, Body: "Still wrong", InReplyTo: 5},
		{ID: 22, ReviewID: 9, User: alice, Path: "c.go", Line: 1, Body: "Another review's"},
	}
	note, items := reviewNote(4, rs, cs, 1)
	for _, want := range []string{"pull request #4", "**alice** requested changes", "[1]\n> Two things.\n> See below.",
		"[2] On `a.go:3`", "> Use a constant", "**bob** commented", "[3] On `b.go:9` (in reply to an earlier comment)", "```dash-review"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "c.go") {
		t.Errorf("another review's comment in the note:\n%s", note)
	}
	// Numbered for Claude's answers; a reply goes to the thread's first comment.
	want := []model.PRReplyItem{{Ref: 1, Author: "alice"}, {Ref: 2, Author: "alice", Thread: 20, Where: "a.go:3"},
		{Ref: 3, Author: "alice", Thread: 5, Where: "b.go:9"}}
	if !slices.Equal(items, want) {
		t.Errorf("items = %+v, want %+v", items, want)
	}
	// Numbering carries on after the items of a reply still owed.
	if _, items := reviewNote(4, rs[:1], cs, 7); len(items) != 2 || items[0].Ref != 7 || items[1].Ref != 8 {
		t.Errorf("items from 7 = %+v", items)
	}
	if got, items := reviewNote(4, []gitops.PRReview{{ID: 3, User: alice, State: "COMMENTED"}}, nil, 1); got != "" || items != nil {
		t.Errorf("an empty review made a note: %q", got)
	}
}

// Claude's final message on a review fix: the summary, and its answer to
// each numbered item from the dash-review block.
func TestParseReviewReply(t *testing.T) {
	parallel(t)

	r := parseReviewReply("Renamed the file and kept the loop.\n\n```dash-review\n" +
		"[1] addressed: renamed review.txt to review.md\n" +
		"- [2] Disagreed — the loop is clearer than a map here;\n  a map would allocate.\n" +
		"3. fixed\n" +
		"garbage before any answer is ignored? no: it carries on [3]\n```\nThanks!")
	if r.Summary != "Renamed the file and kept the loop.\n\nThanks!" {
		t.Errorf("summary %q", r.Summary)
	}
	want := map[int]reviewAnswer{
		1: {Addressed: true, Text: "renamed review.txt to review.md"},
		2: {Text: "the loop is clearer than a map here; a map would allocate."},
		3: {Addressed: true, Text: "garbage before any answer is ignored? no: it carries on [3]"},
	}
	if len(r.Answers) != len(want) {
		t.Fatalf("answers = %+v", r.Answers)
	}
	for k, v := range want {
		if r.Answers[k] != v {
			t.Errorf("answer %d = %+v, want %+v", k, r.Answers[k], v)
		}
	}
	// Without a block: all of it is the summary, and nothing is answered.
	if r := parseReviewReply("  Done.  "); r.Summary != "Done." || len(r.Answers) != 0 {
		t.Errorf("no block: %+v", r)
	}
	// The last block counts; an unclosed one runs to the end.
	r = parseReviewReply("```dash-review\n[1] disagreed: old\n```\nThen:\n```dash-review\n[1] addressed: new")
	if a := r.Answers[1]; !a.Addressed || a.Text != "new" || len(r.Answers) != 1 {
		t.Errorf("last block: %+v", r.Answers)
	}
}

// The summary comment lists what Claude didn't change and why; a thread
// reply is resolved only when all its comments were addressed.
func TestReplyComment(t *testing.T) {
	parallel(t)

	reply := &model.PRReply{Items: []model.PRReplyItem{{Ref: 1, Author: "alice"}, {Ref: 2, Author: "alice", Thread: 20, Where: "a.go:3"},
		{Ref: 3, Author: "bob", Thread: 20, Where: "a.go:4"}}}
	answers := map[int]reviewAnswer{1: {Addressed: true, Text: "done"}, 2: {Text: "used once"}, 3: {Addressed: true}}
	c := replyComment(reply, reviewReply{Summary: "Fixed it.", Answers: answers})
	for _, want := range []string{"review by alice, bob", "Fixed it.", "**Not changed**", "- `a.go:3` (alice): used once"} {
		if !strings.Contains(c, want) {
			t.Errorf("comment lacks %q:\n%s", want, c)
		}
	}
	if strings.Contains(c, "a.go:4") {
		t.Errorf("an addressed comment listed as not changed:\n%s", c)
	}
	body, all := threadReply(reply.Items[1:], answers)
	if all || body != "**Claude:** Not changed: used once\n\nAddressed." {
		t.Errorf("thread reply %q, all %v", body, all)
	}
	if body, all := threadReply(reply.Items[2:], answers); !all || body != "**Claude:** Addressed." {
		t.Errorf("thread reply %q, all %v", body, all)
	}
	if body, _ := threadReply(reply.Items[1:], nil); body != "" {
		t.Errorf("unanswered thread got a reply %q", body)
	}
}
