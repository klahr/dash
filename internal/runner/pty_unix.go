//go:build !windows

package runner

import (
	"errors"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// unixTerminal is a process on a pseudo-terminal from pty.Start, which uses
// Setsid: that already makes the child a process-group leader, so Setpgid
// must not be set as well (the kernel rejects the combination).
type unixTerminal struct {
	*os.File
	cmd *exec.Cmd
}

func startTerminal(cmd *exec.Cmd, cols, rows uint16) (terminal, error) {
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if errors.Is(err, pty.ErrUnsupported) {
		return nil, ErrNoTerminals
	}
	if err != nil {
		return nil, err
	}
	return &unixTerminal{File: tty, cmd: cmd}, nil
}

func (t *unixTerminal) Resize(cols, rows uint16) error {
	return pty.Setsize(t.File, &pty.Winsize{Cols: cols, Rows: rows})
}

func (t *unixTerminal) Pid() int    { return t.cmd.Process.Pid }
func (t *unixTerminal) Wait() error { return t.cmd.Wait() }
