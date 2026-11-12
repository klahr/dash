package runner

import (
	"os/exec"
	"syscall"

	"github.com/klahr/dash/internal/localrun"
)

// createNewProcessGroup is CREATE_NEW_PROCESS_GROUP.
const createNewProcessGroup = 0x00000200

// ownGroup starts cmd in its own process group, so the runner's Ctrl+C
// doesn't reach it; Stop ends it with its process tree.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// terminateGroup ends the process tree under pid. Windows has no SIGTERM
// for console programs without a console of their own, so it is killed.
func terminateGroup(pid int) error {
	_ = localrun.KillTree(pid) // already gone is fine, as with ESRCH
	return nil
}

// killGroup ends the process tree under pid, if it is still there.
func killGroup(pid int) { _ = localrun.KillTree(pid) }

// hangUpGroup does nothing: there is no SIGHUP, and Stop kills the tree.
func hangUpGroup(int) {}
