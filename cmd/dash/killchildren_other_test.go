//go:build !windows

package main

// killChildrenOnExit does nothing off Windows, where a killed test run's
// process group (signalled as a whole) takes the servers with it.
func killChildrenOnExit() error { return nil }
