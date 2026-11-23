// Package landingshots holds the demo Dash for the landing page's
// screenshots, the tutorials and the demo video (see README.md), which is
// shell and Node; this runs its Node tests with go test.
package landingshots

import (
	"os/exec"
	"testing"
)

// TestDemoVideo runs demo_test.mjs: the seed repository, the stand-in's
// change for each of the demo video's tasks, the planted conflict and the
// webhook task's rework. It is skipped where Node, git or sh isn't
// installed.
func TestDemoVideo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	for _, tool := range []string{"git", "sh"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	out, err := exec.Command(node, "--test", "demo_test.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("node --test: %v\n%s", err, out)
	}
}
