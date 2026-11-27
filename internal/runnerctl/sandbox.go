package runnerctl

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// SandboxScripts are the scripts in deploy/sandbox that run Claude for a
// runner in a sandbox (-claude-sandbox, docs/sandbox.md). The release
// archives hold them in deploy/sandbox; the install script puts them next
// to dash, and `dash runner update` refreshes the ones it finds there.
var SandboxScripts = []string{"claude-bwrap", "claude-seatbelt", "claude-as-user"}

// SandboxEnv is the variable that sets a runner's -claude-sandbox.
const SandboxEnv = "DASH_CLAUDE_SANDBOX"

// SandboxScript is the bundled script that sandboxes Claude on goos with
// nothing else to set up: claude-seatbelt on macOS, claude-bwrap on Linux
// with bubblewrap installed ("" when none fits). needs is a package to
// install first ("bubblewrap" on Linux without it), with script still
// named.
func SandboxScript(goos string, lookPath func(string) (string, error)) (script, needs string) {
	switch goos {
	case "darwin":
		return "claude-seatbelt", ""
	case "linux":
		if _, err := lookPath("bwrap"); err != nil {
			return "claude-bwrap", "bubblewrap"
		}
		return "claude-bwrap", ""
	}
	return "", ""
}

// FindSandboxScript is where script is beside the dash binary bin: in its
// folder (where the install script puts it) or in deploy/sandbox there (an
// unpacked release archive). "" when it is in neither.
func FindSandboxScript(bin, script string) string {
	dir := filepath.Dir(bin)
	for _, p := range []string{filepath.Join(dir, script), filepath.Join(dir, "deploy", "sandbox", script)} {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && executable(st.Mode()) {
			return p
		}
	}
	return ""
}

// executable reports a file mode that may be run. Windows keeps no
// executable bits: there any file may.
func executable(m os.FileMode) bool { return runtime.GOOS == "windows" || m&0o111 != 0 }

// SandboxSet reports whether a runner with data directory data has a
// -claude-sandbox: DASH_CLAUDE_SANDBOX in its environment (getenv) or in
// its runner.env.
func SandboxSet(getenv func(string) string, data string) bool {
	if strings.TrimSpace(getenv(SandboxEnv)) != "" {
		return true
	}
	b, err := os.ReadFile(EnvPath(data))
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimPrefix(strings.TrimSpace(l), "export ")
		if k, v, ok := strings.Cut(l, "="); ok && strings.TrimSpace(k) == SandboxEnv && strings.Trim(strings.TrimSpace(v), `"'`) != "" {
			return true
		}
	}
	return false
}

// SandboxCommand is the command that turns script on for the runner with
// data directory data: it adds DASH_CLAUDE_SANDBOX to its runner.env, then
// restarts the background service (`dash runner install`). With service
// false (a runner started by hand) it only adds the line: restart the
// runner after.
func SandboxCommand(script, data string, service bool) string {
	cmd := "echo " + ShellQuote(SandboxEnv+"="+script) + " >> " + ShellQuote(EnvPath(data))
	if !service {
		return cmd
	}
	cmd += " && dash runner install"
	if home, err := os.UserHomeDir(); err != nil || filepath.Clean(data) != RunnerData(home) {
		cmd += " --data " + ShellQuote(data)
	}
	return cmd
}

// SandboxHint is the line `dash runner setup` prints when Claude would run
// unsandboxed though a bundled script fits this computer: how to turn it
// on ("" when a sandbox is set, or none fits). It never turns it on.
func SandboxHint(goos, bin, data string, service bool, getenv func(string) string, lookPath func(string) (string, error)) string {
	script, needs := SandboxScript(goos, lookPath)
	if script == "" || needs != "" || SandboxSet(getenv, data) {
		return ""
	}
	path := FindSandboxScript(bin, script)
	if path == "" {
		return "Claude runs tasks unsandboxed, as you. To sandbox it, install " + script + " from the release archive: see docs/sandbox.md"
	}
	hint := "Claude runs tasks unsandboxed, as you. To sandbox it: " + SandboxCommand(path, data, service)
	if !service {
		hint += " (then restart the runner)"
	}
	return hint
}
