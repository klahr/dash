//go:build !windows

package runner

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
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

// reapGroup ends what is left of the process group pgid once its leader has
// exited and been waited for: a server the command started in the background
// (`./server &`) would otherwise keep running, re-parented to init, long
// after the run has finished. It sends SIGTERM at once and returns; whatever
// is still alive after grace gets SIGKILL from a goroutine, which stops
// looking as soon as the group is empty, so a reused pgid is never signalled
// (the kernel doesn't hand out a pid while a group of that id still exists).
//
// A process that called setsid (or setpgid) has left the group, and this
// doesn't reach it: daemons that detach themselves on purpose keep running.
func reapGroup(pgid int, grace time.Duration) {
	if pgid <= 0 {
		return
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
		return // ESRCH: nothing left of the group
	}
	go func() {
		deadline := time.Now().Add(grace)
		for time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
			if syscall.Kill(-pgid, 0) != nil {
				return
			}
		}
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}()
}
