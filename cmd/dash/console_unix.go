//go:build !windows

package main

// hideConsole does nothing: only Windows opens a console window for a
// program started in the background.
func hideConsole() {}
