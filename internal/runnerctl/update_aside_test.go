package runnerctl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// asideLeft lists what was moved aside next to bin (bin.prev-*, bin.old-*).
func asideLeft(t *testing.T, bin string) []string {
	t.Helper()
	prev, _ := filepath.Glob(bin + ".prev-*")
	old, _ := filepath.Glob(bin + ".old-*")
	return append(prev, old...)
}

// On Windows a program that runs can't be removed under any of its names:
// the supervisor keeps running the binary the first update kept as
// dash.exe.prev. The next update moves that aside instead of failing, puts
// its own backup at .prev for a rollback, and a later update removes the
// leftover once it no longer runs. Not parallel: it swaps removeFile.
func TestUpdateMovesAsideABackupThatCantBeRemoved(t *testing.T) {
	bin, data := installed(t), t.TempDir()
	u := Updater{Version: "1.3.0", Bin: bin, Data: data, Current: "1.2.0", Releases: releaseServer(t, ""), GOOS: "linux", GOARCH: "amd64"}
	if err := u.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	tr, err := BeginTrial(data, "1.3.0")
	if err != nil || tr == nil {
		t.Fatalf("trial: %v", err)
	}
	tr.Confirm()

	// The 1.2.0 binary, now bin.prev, still runs: it can't be removed.
	running := true
	t.Cleanup(func() { removeFile = os.Remove })
	removeFile = func(p string) error {
		if b, err := os.ReadFile(p); running && err == nil && strings.Contains(string(b), "dash 1.2.0") {
			return &os.PathError{Op: "remove", Path: p, Err: os.ErrPermission}
		}
		return os.Remove(p)
	}
	installedNow := "#!/bin/sh\necho dash 1.3.0\n# installed\n"
	if err := os.WriteFile(bin, []byte(installedNow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := u.Run(context.Background()); err != nil {
		t.Fatalf("second update: %v", err)
	}
	if got := read(t, bin+".prev"); got != installedNow {
		t.Errorf("backup after the second update: %q", got)
	}
	left := asideLeft(t, bin)
	if len(left) != 1 || !strings.Contains(read(t, left[0]), "dash 1.2.0") {
		t.Fatalf("moved aside: %v", left)
	}

	// A rollback puts back this update's backup, not the leftover.
	tr, err = BeginTrial(data, "1.3.0")
	if err != nil || tr == nil {
		t.Fatalf("trial: %v", err)
	}
	if err := tr.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, bin); got != installedNow {
		t.Errorf("after the rollback: %q", got)
	}

	// Still running, the leftover stays through another update.
	if err := u.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if left := asideLeft(t, bin); len(left) != 1 {
		t.Errorf("while it runs: %v", left)
	}
	// It stopped: the next update removes it, and keeps its own backup.
	running = false
	if err := u.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if left := asideLeft(t, bin); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
	if got := read(t, bin+".prev"); !strings.Contains(got, "dash 1.3.0") {
		t.Errorf("backup: %q", got)
	}
}

// sweepAside removes only leftovers: not bin, bin.prev, or another
// program's files.
func TestSweepAsideKeepsTheBinaryAndItsBackup(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "dash.exe")
	keep := []string{bin, bin + ".prev", filepath.Join(dir, "dash.exe.prevx"), filepath.Join(dir, "other.exe.old-1")}
	gone := []string{bin + ".old", bin + ".old-1", bin + ".prev-2"}
	for _, p := range append(append([]string{}, keep...), gone...) {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sweepAside(bin)
	for _, p := range keep {
		if !exists(p) {
			t.Errorf("%s was removed", filepath.Base(p))
		}
	}
	for _, p := range gone {
		if exists(p) {
			t.Errorf("%s is left", filepath.Base(p))
		}
	}
}
