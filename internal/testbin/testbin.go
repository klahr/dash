// Package testbin writes stand-in executables (a fake gh, git or claude)
// for tests that run in parallel.
//
// A test that writes a script with os.WriteFile and runs it can fail with
// "text file busy" when another test forks a process meanwhile: the child
// inherits the open, writable file until it execs, and Linux refuses to
// run a file someone has open for writing (golang.org/issue/22315). Write
// has a short-lived shell write the file instead, so this process never
// holds it open.
//
// The stand-ins are #!/bin/sh scripts with no extension, which Windows
// can't run: Write skips the test there, and tests that write such a
// script some other way call SkipOnWindows first.
package testbin

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// Write makes path an executable (mode 0755) holding script. It skips the
// test on Windows, which can't run a shell script as a program.
func Write(t testing.TB, path, script string) {
	t.Helper()
	SkipOnWindows(t, "runs a #!/bin/sh stand-in")
	cmd := exec.Command("sh", "-c", `cat > "$1" && chmod 755 "$1"`, "sh", path)
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("write %s: %v: %s", path, err, out)
	}
}

// SkipOnWindows skips the test on Windows, saying why. Call it in a test
// that runs a #!/bin/sh stand-in, or that checks Unix paths or permission
// bits, none of which Windows has.
func SkipOnWindows(t testing.TB, why string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("not on Windows: " + why)
	}
}

// Exe returns name with the ".exe" suffix Windows needs on a program, so a
// test that builds a binary can run it.
func Exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}
