package localrun

import (
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
