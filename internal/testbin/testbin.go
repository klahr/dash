// Package testbin writes stand-in executables (a fake gh, git or claude)
// for tests that run in parallel.
//
// A test that writes a script with os.WriteFile and runs it can fail with
// "text file busy" when another test forks a process meanwhile: the child
// inherits the open, writable file until it execs, and Linux refuses to
// run a file someone has open for writing (golang.org/issue/22315). Write
// has a short-lived shell write the file instead, so this process never
// holds it open.
package testbin

import (
	"os/exec"
	"strings"
	"testing"
)

// Write makes path an executable (mode 0755) holding script.
func Write(t testing.TB, path, script string) {
	t.Helper()
	cmd := exec.Command("sh", "-c", `cat > "$1" && chmod 755 "$1"`, "sh", path)
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("write %s: %v: %s", path, err, out)
	}
}
