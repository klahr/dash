package runnerctl

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The two kinds of Dash process, each with a data directory of its own: a
// hub and a runner on one computer must not share dash.db, logs or
// worktrees.
const (
	ModeHub    = "hub"
	ModeRunner = "runner"
)

// modeFile marks which kind of process a data directory belongs to.
const modeFile = "dash-mode"

// HubData is the hub's default data directory.
func HubData(home string) string { return filepath.Join(home, ".local", "share", "dash") }

// newRunnerData is where a runner keeps its data unless it already has a
// registration in HubData (from before runners had a directory of their
// own).
func newRunnerData(home string) string { return filepath.Join(home, ".local", "share", "dash-runner") }

// RunnerData is the runner's default data directory: ~/.local/share/dash-
// runner, except for a runner registered in ~/.local/share/dash before
// that default existed, which keeps its directory as long as no hub uses
// it too (MigrateRunnerData then moves the registration).
func RunnerData(home string) string {
	dir, legacy := newRunnerData(home), HubData(home)
	if exists(filepath.Join(dir, "runner.json")) || exists(filepath.Join(dir, modeFile)) {
		return dir
	}
	if exists(filepath.Join(legacy, "runner.json")) && Owner(legacy) == ModeRunner {
		return legacy
	}
	return dir
}

// MigrateRunnerData gives a runner whose data directory is the new default
// the registration it had in the hub's directory when a hub uses that
// directory too, so it keeps working without registering again. The
// runner's other state there (its database, worktrees) stays with the hub's
// directory; the runner starts afresh and the hub sends it its projects.
func MigrateRunnerData(home, dir string) error {
	if filepath.Clean(dir) != newRunnerData(home) {
		return nil
	}
	dst := filepath.Join(dir, "runner.json")
	src := filepath.Join(HubData(home), "runner.json")
	if exists(dst) || !exists(src) || Owner(HubData(home)) != ModeHub {
		return nil
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		return err
	}
	// The hub's directory no longer registers this computer: a runner
	// started there by hand would be a second runner with the same token.
	return os.Rename(src, src+".moved")
}

// Owner says which kind of process uses the data directory dir: its mark,
// else a guess from the files there (runners.json is the hub's list of its
// runners, runner.json a runner's registration), else "".
func Owner(dir string) string {
	if b, err := os.ReadFile(filepath.Join(dir, modeFile)); err == nil {
		return strings.TrimSpace(string(b))
	}
	switch {
	case exists(filepath.Join(dir, "runners.json")):
		return ModeHub
	case exists(filepath.Join(dir, "runner.json")):
		return ModeRunner
	}
	return ""
}

// ClaimData marks dir as mode's data directory, refusing one that belongs
// to the other kind of process.
func ClaimData(dir, mode string) error {
	switch owner := Owner(dir); {
	case owner == "":
	case owner != mode:
		return fmt.Errorf("%s is the data directory of a Dash %s, not of a %s: give this one its own with -data (e.g. %s)",
			dir, owner, mode, suggestion(mode))
	case exists(filepath.Join(dir, modeFile)):
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, modeFile), []byte(mode+"\n"), 0o600)
}

func suggestion(mode string) string {
	if mode == ModeRunner {
		return "~/.local/share/dash-runner"
	}
	return "~/.local/share/dash-hub"
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, os.ErrNotExist)
}
