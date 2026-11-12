package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/klahr/dash/internal/buildinfo"
	"github.com/klahr/dash/internal/runnerctl"
)

// selfUpdate is a runner's side of updating itself when its hub asks
// (runnerctl.Updater): the binary it runs, the trial of a binary it just
// updated to, and whether it restarts once it stops.
//
// Restarting: under its service (systemd, launchd) the runner exits with
// runnerctl.ExitUpdated and the service starts the new binary. Started by
// hand (installed with --no-service), it replaces its own process with
// the new binary (exec), with the same flags and environment.
type selfUpdate struct {
	bin  string // this binary, symlinks resolved ("": unknown, no updates)
	data string

	mu      sync.Mutex
	trial   *runnerctl.Trial
	restart bool
	stop    func()
}

// newSelfUpdate looks at the runner's start: right after an update, the new
// binary is on trial. A binary that keeps failing to start is put back here
// (restarting on the old one, so this never returns then).
func newSelfUpdate(data string) *selfUpdate {
	u := &selfUpdate{data: data}
	if p, err := os.Executable(); err == nil {
		u.bin, _ = filepath.EvalSymlinks(p)
	}
	trial, err := runnerctl.BeginTrial(data, buildinfo.Version)
	switch {
	case errors.Is(err, runnerctl.ErrRolledBack):
		slog.Error("runner: " + err.Error())
		restartRunner(u.bin)
	case err != nil:
		slog.Warn("runner: the update's trial", "err", err)
	}
	u.trial = trial
	return u
}

// watch starts the trial's clock: not connected to the hub within
// runnerctl.TrialTimeout, the old binary is put back and the runner
// restarts on it. stop stops the runner.
func (u *selfUpdate) watch(stop func()) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.stop = stop
	if u.trial == nil {
		return
	}
	slog.Info("runner: trying the updated version", "version", buildinfo.Version, "previous", u.trial.From())
	time.AfterFunc(runnerctl.TrialTimeout, func() {
		u.mu.Lock()
		t := u.trial
		u.trial = nil
		u.mu.Unlock()
		if t == nil {
			return
		}
		if err := t.Rollback(); err != nil {
			slog.Error("runner: no connection to the hub on the updated version, and the previous one can't be put back", "err", err)
			return
		}
		slog.Error("runner: no connection to the hub on the updated version: put back the previous one, restarting", "version", t.From())
		u.restartSoon(0)
	})
}

// connected ends the trial: the new binary works.
func (u *selfUpdate) connected() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.trial != nil {
		u.trial.Confirm()
		u.trial = nil
		slog.Info("runner: the update works", "version", buildinfo.Version)
	}
}

// Update installs release version in place of this binary and restarts on
// it after a moment, for the hub to get the answer (api.Server.SelfUpdate).
func (u *selfUpdate) Update(ctx context.Context, version string) error {
	if u.bin == "" {
		return errors.New("this runner can't tell where its binary is")
	}
	if runnerctl.SameVersion(version, buildinfo.Version) {
		return fmt.Errorf("it runs %s already", version)
	}
	slog.Info("runner: updating", "from", buildinfo.Version, "to", version, "bin", u.bin)
	up := runnerctl.Updater{Version: version, Bin: u.bin, Data: u.data, Current: buildinfo.Version}
	if err := up.Run(ctx); err != nil {
		return err
	}
	slog.Info("runner: updated, restarting", "version", version)
	u.restartSoon(time.Second)
	return nil
}

func (u *selfUpdate) restartSoon(after time.Duration) {
	u.mu.Lock()
	u.restart = true
	stop := u.stop
	u.mu.Unlock()
	if stop != nil {
		time.AfterFunc(after, stop)
	}
}

// restarting says whether the runner stopped to restart on a new binary.
func (u *selfUpdate) restarting() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.restart
}

// restartRunner starts the runner again on bin with the same flags: its
// service does, once it exits with ExitUpdated; without one it execs bin.
func restartRunner(bin string) {
	if runnerctl.UnderService() || bin == "" {
		slog.Info("runner: exiting for the service to start it again", "code", runnerctl.ExitUpdated)
		os.Exit(runnerctl.ExitUpdated)
	}
	slog.Info("runner: restarting in place", "bin", bin)
	err := execSelf(bin)
	slog.Error("runner: restart failed: start it again by hand", "bin", bin, "err", err)
	os.Exit(runnerctl.ExitUpdated)
}
