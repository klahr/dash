package main

import (
	"errors"
	"os"
	"os/exec"
)

// execSelf runs bin with the same arguments, environment and console in
// place of this process: Windows can't replace a running process, so it
// starts bin, waits for it and exits with its status. It returns only when
// bin can't be started.
func execSelf(bin string) error {
	cmd := exec.Command(bin, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return err
	}
	err := cmd.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	if err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
