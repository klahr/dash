package localrun

import (
	"os/exec"
	"syscall"
	"time"
)

// createNewProcessGroup is CREATE_NEW_PROCESS_GROUP: the child doesn't get
// the Ctrl+C meant for the runner's console.
const createNewProcessGroup = 0x00000200

// Group runs cmd in its own process group, so cancelling it (its context
// ending) ends everything it started, not just the direct child. Windows has
// no signals to send, so sig is ignored and the whole tree is killed. wait
// bounds how long Wait then waits for output pipes a leftover grandchild may
// still hold (see exec.Cmd.WaitDelay). Call it before cmd.Start.
func Group(cmd *exec.Cmd, _ syscall.Signal, wait time.Duration) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
	cmd.Cancel = func() error { return KillTree(cmd.Process.Pid) }
	cmd.WaitDelay = wait
}

// Detach starts cmd in a process group of its own, so it outlives the
// request (or console) that started it. Call it before cmd.Start.
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}
