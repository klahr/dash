//go:build !windows

package tui

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyResize sends to ch when the terminal is resized.
func notifyResize(ch chan<- os.Signal) { signal.Notify(ch, syscall.SIGWINCH) }
