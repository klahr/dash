//go:build !windows

package main

import (
	"os"
	"syscall"
)

// execSelf replaces this process with bin, run with the same arguments and
// environment. It returns only on failure.
func execSelf(bin string) error { return syscall.Exec(bin, os.Args, os.Environ()) }
