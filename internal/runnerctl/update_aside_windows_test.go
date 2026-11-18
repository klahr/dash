package runnerctl

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// runningEnv makes TestRunningProgramHelper wait for its stdin to close:
// a copy of the test binary started with it is a program that runs.
const runningEnv = "DASH_TEST_RUNNING_PROGRAM"

func TestRunningProgramHelper(t *testing.T) {
	if os.Getenv(runningEnv) == "" {
		t.Skip("started by TestUpdateOnWindowsMovesAsideARunningBackup")
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// The supervisor runs the binary the first update kept as dash.exe.prev
// (and renamed to dash.exe.old). Windows won't delete it under either
// name: the next update moves .prev aside, and once the program stopped a
// later update removes what was left.
func TestUpdateOnWindowsMovesAsideARunningBackup(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "dash.exe")
	newBin := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	newBin("dash.exe")

	// The supervisor starts on dash.exe.
	cmd := exec.Command(bin, "-test.run=^TestRunningProgramHelper$")
	cmd.Env = append(os.Environ(), runningEnv+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			stdin.Close()
			_ = cmd.Wait()
		}
	}
	t.Cleanup(stop)

	// The first update: its image is now dash.exe.prev and dash.exe.old.
	if err := backup(bin, bin+".prev"); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(newBin("1.exe"), bin); err != nil {
		t.Fatal(err)
	}
	// The second one can't remove dash.exe.prev, so moves it aside.
	sweepAside(bin)
	if err := backup(bin, bin+".prev"); err != nil {
		t.Fatalf("second backup: %v", err)
	}
	if err := replaceFile(newBin("2.exe"), bin); err != nil {
		t.Fatal(err)
	}
	if !exists(bin + ".prev") {
		t.Fatal("no dash.exe.prev")
	}
	if left := asideLeft(t, bin); len(left) == 0 {
		t.Error("nothing was moved aside")
	}

	// Still running: the sweep leaves it. Stopped: it removes it.
	sweepAside(bin)
	if !exists(bin+".old") && len(asideLeft(t, bin)) == 0 {
		t.Error("the running program's file was removed")
	}
	stop()
	sweepAside(bin)
	if left := asideLeft(t, bin); len(left) != 0 || exists(bin+".old") {
		t.Errorf("left behind: %v", left)
	}
	if !exists(bin+".prev") || !exists(bin) {
		t.Error("the binary or its backup was removed")
	}
}
