package main

import (
	"syscall"
	"unsafe"
)

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	user32                  = syscall.NewLazyDLL("user32.dll")
	procGetConsoleWindow    = kernel32.NewProc("GetConsoleWindow")
	procGetConsoleProcesses = kernel32.NewProc("GetConsoleProcessList")
	procShowWindow          = user32.NewProc("ShowWindow")
)

// hideConsole hides the console window Windows opened for dash when it was
// started in the background (by its scheduled task): it is the only
// process attached to it. A console shared with a shell stays. The
// programs the runner starts share the hidden console, so they open no
// windows of their own.
func hideConsole() {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return
	}
	var pids [2]uint32
	n, _, _ := procGetConsoleProcesses.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	if n != 1 {
		return // shared with a shell (or unknown)
	}
	const swHide = 0
	_, _, _ = procShowWindow.Call(hwnd, swHide)
}
