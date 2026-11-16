// Package tutorials holds the tutorial video pipeline (see README.md),
// which is Node; this runs its Node tests with go test.
package tutorials

import (
	"os/exec"
	"testing"
)

// TestJS runs lib/tutorials_test.mjs: the script parser, the voice cache
// key, the --no-voice timing and the subtitles. It is skipped where Node
// isn't installed.
func TestJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	out, err := exec.Command(node, "--test", "lib/tutorials_test.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("node --test: %v\n%s", err, out)
	}
}
