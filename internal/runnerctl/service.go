package runnerctl

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/klahr/dash/internal/fsutil"
)

// Names of the background service.
const (
	SystemdUnit  = "dash-runner.service"
	LaunchdLabel = "com.github.klahr.dash-runner"
	ExitRevoked  = 3 // `dash runner` exits with this when the hub revoked it: don't restart
)

// Service is how `dash runner` is started in the background.
type Service struct {
	Bin  string   // absolute path of the dash binary
	Args []string // after the binary, e.g. ["runner"] or ["runner", "-data", "/x"]
	Home string   // the user's home directory
	Path string   // PATH for the runner, so it finds git and claude
	// Run runs a service manager command (systemctl, launchctl, loginctl)
	// and returns its combined output; nil = exec.
	Run func(name string, args ...string) (string, error)
	// GOOS picks systemd (linux) or launchd (darwin); "" = runtime.GOOS.
	GOOS string
}

func (s *Service) goos() string {
	if s.GOOS != "" {
		return s.GOOS
	}
	return runtime.GOOS
}

func (s *Service) run(name string, args ...string) (string, error) {
	if s.Run != nil {
		return s.Run(name, args...)
	}
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// File is where the service definition is written.
func (s *Service) File() (string, error) {
	switch s.goos() {
	case "linux":
		return filepath.Join(s.Home, ".config", "systemd", "user", SystemdUnit), nil
	case "darwin":
		return filepath.Join(s.Home, "Library", "LaunchAgents", LaunchdLabel+".plist"), nil
	}
	return "", fmt.Errorf("no background service on %s: run dash runner yourself", s.goos())
}

// Definition is the service file's content: a systemd user unit or a
// launchd LaunchAgent.
func (s *Service) Definition() ([]byte, error) {
	switch s.goos() {
	case "linux":
		return SystemdUnitFile(s.Bin, s.Args, s.Path), nil
	case "darwin":
		return LaunchdPlist(s.Bin, s.Args, s.Path, filepath.Join(s.Home, "Library", "Logs", "dash-runner.log")), nil
	}
	_, err := s.File()
	return nil, err
}

// systemdQuote quotes a word for a unit's ExecStart or Environment, and
// escapes % specifiers; in ExecStart $ is escaped too (variable expansion).
func systemdQuote(s string, inExec bool) string {
	s = strings.ReplaceAll(s, "%", "%%")
	if inExec {
		s = strings.ReplaceAll(s, "$", "$$")
	}
	if s != "" && !strings.ContainsAny(s, " \t\"'\\;") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// SystemdUnitFile is a systemd user unit that runs bin args.
func SystemdUnitFile(bin string, args []string, path string) []byte {
	words := []string{systemdQuote(bin, true)}
	for _, a := range args {
		words = append(words, systemdQuote(a, true))
	}
	var b bytes.Buffer
	b.WriteString("# Written by `dash runner install`; remove with `dash runner uninstall`.\n")
	b.WriteString("[Unit]\nDescription=Dash runner\nWants=network-online.target\nAfter=network-online.target\n\n")
	b.WriteString("[Service]\nType=simple\n")
	b.WriteString("WorkingDirectory=%h\n")
	if path != "" {
		b.WriteString("Environment=" + systemdQuote("PATH="+path, false) + "\n")
	}
	b.WriteString("ExecStart=" + strings.Join(words, " ") + "\n")
	b.WriteString("Restart=on-failure\nRestartSec=10\n")
	fmt.Fprintf(&b, "# A revoked runner exits %d: it needs a new code, not a restart.\nRestartPreventExitStatus=%d\n", ExitRevoked, ExitRevoked)
	b.WriteString("\n[Install]\nWantedBy=default.target\n")
	return b.Bytes()
}

func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// LaunchdPlist is a LaunchAgent that runs bin args at login and keeps it
// running, logging to logPath.
func LaunchdPlist(bin string, args []string, path, logPath string) []byte {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<!-- Written by `dash runner install`; remove with `dash runner uninstall`. -->\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	fmt.Fprintf(&b, "  <key>Label</key>\n  <string>%s</string>\n", LaunchdLabel)
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, a := range append([]string{bin}, args...) {
		fmt.Fprintf(&b, "    <string>%s</string>\n", xmlText(a))
	}
	b.WriteString("  </array>\n")
	if path != "" {
		fmt.Fprintf(&b, "  <key>EnvironmentVariables</key>\n  <dict>\n    <key>PATH</key>\n    <string>%s</string>\n  </dict>\n", xmlText(path))
	}
	b.WriteString("  <key>RunAtLoad</key>\n  <true/>\n")
	// Restart after a crash, not after a clean exit; a revoked runner is
	// held off by the throttle.
	b.WriteString("  <key>KeepAlive</key>\n  <dict>\n    <key>SuccessfulExit</key>\n    <false/>\n  </dict>\n")
	b.WriteString("  <key>ThrottleInterval</key>\n  <integer>30</integer>\n")
	fmt.Fprintf(&b, "  <key>StandardOutPath</key>\n  <string>%s</string>\n", xmlText(logPath))
	fmt.Fprintf(&b, "  <key>StandardErrorPath</key>\n  <string>%s</string>\n", xmlText(logPath))
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

// InstallResult says what Install did.
type InstallResult struct {
	File    string
	Changed bool   // the file was written (new or different)
	Hint    string // something for the user to do, e.g. enable lingering
}

// Install writes the service file and (re)starts the service. Running it
// again is safe: an unchanged file is left alone, and the service is
// restarted so it runs the binary installed now.
func (s *Service) Install() (InstallResult, error) {
	file, err := s.File()
	if err != nil {
		return InstallResult{}, err
	}
	def, err := s.Definition()
	if err != nil {
		return InstallResult{}, err
	}
	res := InstallResult{File: file}
	if old, err := os.ReadFile(file); err != nil || !bytes.Equal(old, def) {
		if err := fsutil.WriteFileAtomic(file, def, 0o644); err != nil {
			return res, err
		}
		res.Changed = true
	}
	switch s.goos() {
	case "linux":
		steps := [][]string{
			{"systemctl", "--user", "daemon-reload"},
			{"systemctl", "--user", "enable", SystemdUnit},
			{"systemctl", "--user", "restart", SystemdUnit},
		}
		for _, c := range steps {
			if out, err := s.run(c[0], c[1:]...); err != nil {
				return res, fmt.Errorf("%s: %v %s (no systemd user session? install with --no-service and start dash runner another way)", strings.Join(c, " "), err, out)
			}
		}
		user := os.Getenv("USER")
		if out, err := s.run("loginctl", "show-user", user, "--property=Linger"); err == nil && strings.TrimSpace(out) == "Linger=no" {
			res.Hint = "The runner stops when you log out. To keep it running (and start it at boot), ask an admin to run: sudo loginctl enable-linger " + user
		}
	case "darwin":
		target := s.launchdTarget()
		_, _ = s.run("launchctl", "bootout", target+"/"+LaunchdLabel) // not loaded yet is fine
		if out, err := s.run("launchctl", "bootstrap", target, file); err != nil {
			return res, fmt.Errorf("launchctl bootstrap: %v %s", err, out)
		}
	}
	return res, nil
}

func (s *Service) launchdTarget() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

// Uninstall stops the service and removes its file. Nothing installed is
// not an error.
func (s *Service) Uninstall() (removed bool, err error) {
	file, err := s.File()
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	switch s.goos() {
	case "linux":
		_, _ = s.run("systemctl", "--user", "disable", "--now", SystemdUnit)
	case "darwin":
		_, _ = s.run("launchctl", "bootout", s.launchdTarget()+"/"+LaunchdLabel)
	}
	if err := os.Remove(file); err != nil {
		return false, err
	}
	if s.goos() == "linux" {
		_, _ = s.run("systemctl", "--user", "daemon-reload")
	}
	return true, nil
}

// State describes the service: "not installed", "running", "stopped", …
func (s *Service) State() string {
	file, err := s.File()
	if err != nil {
		return "unsupported on " + s.goos()
	}
	if _, err := os.Stat(file); err != nil {
		return "not installed"
	}
	switch s.goos() {
	case "linux":
		active, _ := s.run("systemctl", "--user", "is-active", SystemdUnit)
		enabled, _ := s.run("systemctl", "--user", "is-enabled", SystemdUnit)
		if active == "" {
			active = "unknown"
		}
		return fmt.Sprintf("%s, %s (%s; logs: journalctl --user -u %s)", active, enabled, file, SystemdUnit)
	case "darwin":
		out, err := s.run("launchctl", "print", s.launchdTarget()+"/"+LaunchdLabel)
		if err != nil {
			return "installed, not loaded (" + file + ")"
		}
		state := "loaded"
		for _, l := range strings.Split(out, "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "state = "); ok {
				state = v
				break
			}
		}
		return state + " (" + file + "; logs: ~/Library/Logs/dash-runner.log)"
	}
	return "unknown"
}
