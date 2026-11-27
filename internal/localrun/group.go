package localrun

import (
	"os/exec"
	"time"
)

// ReapGrace is how long what a finished command left running in its process
// group gets between SIGTERM and SIGKILL (see ReapGroup).
const ReapGrace = 5 * time.Second

// Reap ends what a command run with Group left running in its process group,
// once it has exited and been waited for (see ReapGroup). Call it after
// cmd.Wait (or Run, Output) returns, however it ended; a command that never
// started is fine.
func Reap(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	ReapGroup(cmd.Process.Pid, ReapGrace)
}
