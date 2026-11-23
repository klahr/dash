package localrun

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Bash is the bash binary project commands (setup, test, run, deploy) and
// terminals run with. On Windows it is $DASH_BASH if set, else the bash.exe
// that comes with Git for Windows (found next to git on the PATH), so
// "bash" doesn't resolve to WSL's launcher in System32; elsewhere "bash".
func Bash() string {
	if runtime.GOOS != "windows" {
		return "bash"
	}
	return windowsBash(os.Getenv("DASH_BASH"), exec.LookPath, fileExists)
}

func windowsBash(env string, lookPath func(string) (string, error), exists func(string) bool) string {
	if env != "" {
		return env
	}
	if git, err := lookPath("git"); err == nil {
		// Git for Windows: <root>\cmd\git.exe, <root>\mingw64\bin\git.exe or
		// <root>\bin\git.exe; bash is <root>\bin\bash.exe.
		dir := filepath.Dir(git)
		for _, root := range []string{filepath.Dir(dir), filepath.Dir(filepath.Dir(dir))} {
			if b := filepath.Join(root, "bin", "bash.exe"); exists(b) {
				return b
			}
		}
	}
	for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramW6432"), os.Getenv("LocalAppData") + `\Programs`} {
		if root == "" || root == `\Programs` {
			continue
		}
		if b := filepath.Join(root, "Git", "bin", "bash.exe"); exists(b) {
			return b
		}
	}
	return "bash"
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// ExitStatus reads the error cmd.Run or cmd.Wait returned for a command that
// ran: code is 0 for no error, the exit code for an *exec.ExitError (-1 when a
// signal killed it), and 0 for exec.ErrWaitDelay, which means it exited fine
// but something it left in the background kept its output open. ok is false,
// with code -1, for any other error: it didn't start, or waiting for it failed.
func ExitStatus(err error) (code int, ok bool) {
	var exit *exec.ExitError
	switch {
	case err == nil, errors.Is(err, exec.ErrWaitDelay):
		return 0, true
	case errors.As(err, &exit):
		return exit.ExitCode(), true
	default:
		return -1, false
	}
}
