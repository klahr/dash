//go:build !windows

package runnerctl

import (
	"errors"
	"syscall"
)

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// killProcessTree stops a process (only Windows' service uses it).
func killProcessTree(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }
