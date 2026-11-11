package web

import (
	"os/exec"
	"testing"
)

// TestJS runs the web client's Node tests (*_test.mjs here), which cover
// the parts of it kept free of the DOM, such as the PROJECTS list's groups and the
// History graph's lanes and its commit pane's selection.
// It is skipped where Node isn't installed.
func TestJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	out, err := exec.Command(node, "--test", "groups_test.mjs", "graphlayout_test.mjs", "commitnav_test.mjs", "activityclear_test.mjs", "runnerversion_test.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("node --test: %v\n%s", err, out)
	}
}
