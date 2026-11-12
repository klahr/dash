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
// still hold (see exec.Cmd.WaitDelay). Call it before cmd.Start.
func Group(cmd *exec.Cmd, sig syscall.Signal, wait time.Duration) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, sig) }
	cmd.WaitDelay = wait
}

// Detach starts cmd in a process group of its own, so it outlives the
// request (or terminal) that started it. Call it before cmd.Start.
func Detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
