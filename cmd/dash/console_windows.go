package main

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"github.com/klahr/dash/internal/runnerctl"
)

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	user32                  = syscall.NewLazyDLL("user32.dll")
	procGetConsoleWindow    = kernel32.NewProc("GetConsoleWindow")
	procGetConsoleProcesses = kernel32.NewProc("GetConsoleProcessList")
	procFreeConsole         = kernel32.NewProc("FreeConsole")
	procShowWindow          = user32.NewProc("ShowWindow")
)

// createNoWindow is CREATE_NO_WINDOW: the child gets a console with no
// window, which the programs it starts share.
const createNoWindow = 0x08000000

// consoleAlone reports whether dash is the only process on its console, or
// has none: it was started in the background (by its scheduled task), not
// from a shell.
func consoleAlone() bool {
	var pids [2]uint32
	n, _, _ := procGetConsoleProcesses.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n <= 1 // 0: no console
}

// hideConsole hides the console window Windows opened for dash when it was
// started in the background: it is the only process attached to it. A
// console shared with a shell stays. When Windows Terminal is the default
// terminal the window is its own, which this can't hide: superviseRunner
// lets go of it instead.
func hideConsole() {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 || !consoleAlone() {
		return
	}
	const swHide = 0
	_, _, _ = procShowWindow.Call(hwnd, swHide)
}

// superviseRunner runs the runner under a supervisor (supervise) when
// this is `dash runner -log-file …` started in the background, by its
// scheduled task. It returns only when it doesn't: the runner then runs in
// this process.
func superviseRunner(logFile string) {
	if runnerctl.Supervised || !consoleAlone() {
		return
	}
	bin, err := os.Executable() // now: an update renames this binary aside
	if err != nil {
		return
	}
	hideConsole()
	_, _, _ = procFreeConsole.Call() // closes the window, Windows Terminal's too
	out := appendLog(logFile)
	log := slog.New(slog.NewTextHandler(out, nil))
	supervise(log, func() error {
		cmd := exec.Command(bin, os.Args[1:]...)
		cmd.Env = append(os.Environ(), runnerctl.SupervisedEnv+"=1")
		cmd.Stdout, cmd.Stderr = out, out
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
		cmd.WaitDelay = 10 * time.Second
		return cmd.Run()
	}, time.Sleep)
}

// appendLog opens the log file at path for the supervisor and the runner's
// output. Without it the output is dropped: the supervisor has no console.
func appendLog(path string) io.Writer {
	f, err := openLogFile(path)
	if err != nil {
		return io.Discard
	}
	return f
}

// supervise starts the runner with run, and again whenever it stops: at once
// after an update (ExitUpdated), otherwise after a wait that grows while it
// keeps stopping. It logs why the runner stopped. It returns never: when the
// hub revoked the runner (ExitRevoked) it exits, as the runner did. On
// Windows the scheduled task starts it (see superviseRunner); systemd and
// launchd do this themselves.
func supervise(log *slog.Logger, run func() error, sleep func(time.Duration)) {
	wait := time.Duration(0)
	for {
		started := time.Now()
		err := run()
		ran := time.Since(started)
		code := exitCode(err)
		if code == runnerctl.ExitRevoked {
			log.Error("supervisor: the hub revoked the runner: not starting it again", "code", code)
			os.Exit(code)
		}
		wait = nextWait(wait, code, ran)
		log.Warn("supervisor: the runner stopped: starting it again", "code", code, "err", err, "ran", ran.Round(time.Second), "wait", wait)
		sleep(wait)
	}
}
