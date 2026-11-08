package queue

import (
	"testing"

	"github.com/klahr/dash/internal/gitops"
)

// Only failed checks a change to the branch could fix go back to Claude.
func TestBranchFixable(t *testing.T) {
	failed := []gitops.PRCheck{{Name: "CLAAssistant"}, {Name: "test"}, {Name: "lint"}, {Context: "ci/legacy"}}
	info := map[string]gitops.CheckInfo{
		"CLAAssistant": {Event: "pull_request_target", Required: true},
		"test":         {Event: "pull_request", Required: true},
		"lint":         {Event: "pull_request"},
	}
	fix, ignored := branchFixable(failed, info, true)
	if len(fix) != 1 || fix[0].Name != "test" || len(ignored) != 3 {
		t.Fatalf("fix = %+v, ignored = %q", fix, ignored)
	}
	// Without required checks every check counts, but pull_request_target ones still can't be fixed.
	fix, ignored = branchFixable(failed, info, false)
	if len(fix) != 3 || len(ignored) != 1 {
		t.Fatalf("fix = %+v, ignored = %q", fix, ignored)
	}
}
