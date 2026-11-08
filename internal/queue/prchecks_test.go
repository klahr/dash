package queue

import (
	"testing"

	"github.com/klahr/dash/internal/gitops"
)

// Failed checks no change to the branch can fix don't go back to Claude.
func TestClassifyFailed(t *testing.T) {
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
