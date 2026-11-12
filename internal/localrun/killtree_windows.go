package localrun

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW: taskkill doesn't flash a console.
const createNoWindow = 0x08000000

// KillTree forcibly ends the process pid and every process it started
// (taskkill /T /F). If taskkill fails (the process already gone, or taskkill
// missing), the process itself is killed directly.
func KillTree(pid int) error {
	cmd := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
	if err := cmd.Run(); err == nil {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
