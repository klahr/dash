package runnerctl

import (
	"errors"
	"syscall"

	"github.com/klahr/dash/internal/localrun"
)

// killProcessTree ends a process and everything it started.
func killProcessTree(pid int) error { return localrun.KillTree(pid) }

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259 // STILL_ACTIVE
)

func alive(pid int) bool {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		// Access denied: it exists, but belongs to someone else.
		return errors.Is(err, syscall.ERROR_ACCESS_DENIED)
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
