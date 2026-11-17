// The runner's supervisor on Windows: supervise in console_windows.go.

package main

import (
	"errors"
	"os/exec"
	"time"

	"github.com/klahr/dash/internal/runnerctl"
)

// The supervisor's waits before it starts the runner again: superviseFirst
// after a stop, doubled while the runner keeps stopping, up to superviseMax.
// A runner that ran for superviseSteady counts as running well, and the
// wait starts over.
const (
	superviseFirst  = 5 * time.Second
	superviseMax    = 5 * time.Minute
	superviseSteady = 10 * time.Minute
)

// nextWait is how long to wait before starting the runner again, after the
// previous wait, the code it exited with and how long it ran.
func nextWait(prev time.Duration, code int, ran time.Duration) time.Duration {
	switch {
	case code == runnerctl.ExitUpdated:
		return 0
	case prev == 0 || ran >= superviseSteady:
		return superviseFirst
	case prev*2 > superviseMax:
		return superviseMax
	default:
		return prev * 2
	}
}

// exitCode is the code the runner exited with: 0 for no error, -1 when it
// didn't start or didn't exit normally.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}
