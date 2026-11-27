//go:build !windows

package runner

import (
	"errors"
	"os/exec"
	"syscall"
)

// ownGroup makes cmd the leader of its own process group, so Stop reaches
// everything it starts.
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// terminateGroup sends SIGTERM to the process group pgid leads.
func terminateGroup(pgid int) error {
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// killGroup sends SIGKILL to the process group pgid leads.
func killGroup(pgid int) { _ = syscall.Kill(-pgid, syscall.SIGKILL) }

// hangUpGroup sends SIGHUP to the process group pgid leads.
func hangUpGroup(pgid int) { _ = syscall.Kill(-pgid, syscall.SIGHUP) }
