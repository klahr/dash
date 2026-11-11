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
	"os"
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

// IsolateGit drops the environment variables that tell git which repository
// to use. Tests run from a git hook or a worktree tool inherit them, and every
// git a test starts then works on the caller's repository instead of the temp
// one it was pointed at: test commits land on the caller's branch. Call it
// from TestMain, before any test starts.
func IsolateGit() {
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_PREFIX"} {
		os.Unsetenv(k)
	}
}
