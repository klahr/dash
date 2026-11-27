//go:build !windows

package localrun

import (
	"os/exec"
	"syscall"
	"time"
)

// Group runs cmd in its own process group, so cancelling it (its context
// ending) sends sig to everything it started, not just the direct child. wait
// bounds how long Wait then waits for output pipes a leftover grandchild may
// still hold (see exec.Cmd.WaitDelay). Call it before cmd.Start, and Reap
// after cmd.Wait, so what it left running in the background ends too.
func Group(cmd *exec.Cmd, sig syscall.Signal, wait time.Duration) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, sig) }
	cmd.WaitDelay = wait
}

// ReapGroup ends what is left of the process group pgid once its leader has
// exited and been waited for: a server the command started in the background
// (`./server &`) would otherwise keep running, re-parented to init, long
// after the command has finished. It sends SIGTERM at once and returns;
// whatever is still alive after grace gets SIGKILL from a goroutine, which
// stops looking as soon as the group is empty, so a reused pgid is never
// signalled (the kernel doesn't hand out a pid while a group of that id still
// exists).
//
// A process that called setsid (or setpgid) has left the group, and this
// doesn't reach it: daemons that detach themselves on purpose keep running.
func ReapGroup(pgid int, grace time.Duration) {
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

// Detach starts cmd in a process group of its own, so it outlives the
// request (or terminal) that started it. Call it before cmd.Start.
func Detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
