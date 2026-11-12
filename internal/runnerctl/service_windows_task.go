package runnerctl

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// WindowsTask is the name of the scheduled task that runs the runner on
// Windows.
const WindowsTask = "Dash runner"

// windowsDir is where the task's definition and the runner's log are kept.
func (s *Service) windowsDir() string {
	return filepath.Join(s.Home, "AppData", "Local", "dash-runner")
}

// WindowsLog is the runner's log file under the scheduled task.
func (s *Service) WindowsLog() string { return filepath.Join(s.windowsDir(), "dash-runner.log") }

func (s *Service) windowsUser() string {
	if s.User != "" {
		return s.User
	}
	if u, err := user.Current(); err == nil {
		return u.Username // DOMAIN\name
	}
	return os.Getenv("USERDOMAIN") + `\` + os.Getenv("USERNAME")
}

// windowsArg quotes a word for a command line as Windows programs (Go's
// among them) split it: backslashes are literal except before a quote.
func windowsArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			slashes++
		case '"':
			// The backslashes before it doubled, and the quote escaped.
			b.WriteString(strings.Repeat(`\`, slashes+1))
			slashes = 0
		default:
			slashes = 0
		}
		b.WriteByte(s[i])
	}
	b.WriteString(strings.Repeat(`\`, slashes)) // doubled before the closing quote
	b.WriteByte('"')
	return b.String()
}

// TaskXML is a Task Scheduler definition that runs bin args for userID
// (DOMAIN\name) when they log on, in their session, and starts it again
// a minute after a failure. args should log to a file (-log-file): the
// runner hides its console window when it does.
func TaskXML(bin string, args []string, userID, workDir string) []byte {
	words := make([]string, len(args))
	for i, a := range args {
		words[i] = windowsArg(a)
	}
	x := xmlText
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-16"?>` + "\r\n")
	b.WriteString(`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">` + "\r\n")
	b.WriteString("  <RegistrationInfo>\r\n")
	b.WriteString("    <Description>Dash runner. Written by `dash runner install`; remove with `dash runner uninstall`.</Description>\r\n")
	b.WriteString("  </RegistrationInfo>\r\n")
	b.WriteString("  <Triggers>\r\n    <LogonTrigger>\r\n      <Enabled>true</Enabled>\r\n")
	fmt.Fprintf(&b, "      <UserId>%s</UserId>\r\n", x(userID))
	b.WriteString("    </LogonTrigger>\r\n  </Triggers>\r\n")
	b.WriteString("  <Principals>\r\n    <Principal id=\"Author\">\r\n")
	fmt.Fprintf(&b, "      <UserId>%s</UserId>\r\n", x(userID))
	b.WriteString("      <LogonType>InteractiveToken</LogonType>\r\n      <RunLevel>LeastPrivilege</RunLevel>\r\n")
	b.WriteString("    </Principal>\r\n  </Principals>\r\n")
	b.WriteString("  <Settings>\r\n")
	b.WriteString("    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>\r\n")
	b.WriteString("    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>\r\n")
	b.WriteString("    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>\r\n")
	b.WriteString("    <StartWhenAvailable>true</StartWhenAvailable>\r\n")
	b.WriteString("    <AllowStartOnDemand>true</AllowStartOnDemand>\r\n")
	b.WriteString("    <IdleSettings>\r\n      <StopOnIdleEnd>false</StopOnIdleEnd>\r\n    </IdleSettings>\r\n")
	b.WriteString("    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>\r\n")
	b.WriteString("    <RestartOnFailure>\r\n      <Interval>PT1M</Interval>\r\n      <Count>999</Count>\r\n    </RestartOnFailure>\r\n")
	b.WriteString("  </Settings>\r\n")
	b.WriteString("  <Actions Context=\"Author\">\r\n    <Exec>\r\n")
	fmt.Fprintf(&b, "      <Command>%s</Command>\r\n", x(windowsArg(bin)))
	fmt.Fprintf(&b, "      <Arguments>%s</Arguments>\r\n", x(strings.Join(words, " ")))
	fmt.Fprintf(&b, "      <WorkingDirectory>%s</WorkingDirectory>\r\n", x(workDir))
	b.WriteString("    </Exec>\r\n  </Actions>\r\n</Task>\r\n")
	return utf16LE(b.String())
}

// utf16LE encodes s as UTF-16 little-endian with a byte order mark, the
// encoding schtasks /XML reads.
func utf16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 2, 2+2*len(u))
	out[0], out[1] = 0xff, 0xfe
	for _, c := range u {
		out = append(out, byte(c), byte(c>>8))
	}
	return out
}

func (s *Service) taskDefinition() []byte {
	args := append(append([]string(nil), s.Args...), "-log-file", s.WindowsLog())
	return TaskXML(s.Bin, args, s.windowsUser(), s.Home)
}

// stopWindowsRunner ends the task and the runner it started (which may
// have restarted itself in place after an update, outside the task).
func (s *Service) stopWindowsRunner() {
	_, _ = s.run("schtasks", "/End", "/TN", WindowsTask) // not running is fine
	if s.Data == "" {
		return
	}
	if st, ok := ReadState(StatePath(s.Data)); ok && st.PID != os.Getpid() {
		kill := s.Kill
		if kill == nil {
			kill = killProcessTree
		}
		_ = kill(st.PID)
	}
}

func (s *Service) installWindows(file string) error {
	if out, err := s.run("schtasks", "/Create", "/TN", WindowsTask, "/XML", file, "/F"); err != nil {
		return fmt.Errorf("schtasks /Create /TN %q /XML %s: %v %s (install with --no-service and start dash runner another way)", WindowsTask, file, err, out)
	}
	s.stopWindowsRunner()
	if out, err := s.run("schtasks", "/Run", "/TN", WindowsTask); err != nil {
		return fmt.Errorf("schtasks /Run /TN %q: %v %s", WindowsTask, err, out)
	}
	return nil
}

func (s *Service) uninstallWindows() {
	s.stopWindowsRunner()
	_, _ = s.run("schtasks", "/Delete", "/TN", WindowsTask, "/F")
}

// windowsState is the task's status as schtasks reports it (Ready,
// Running, …, in the system's language).
func (s *Service) windowsState(file string) string {
	out, err := s.run("schtasks", "/Query", "/TN", WindowsTask, "/FO", "CSV", "/NH")
	if err != nil {
		return "installed, not registered with Task Scheduler (" + file + "; run dash runner install)"
	}
	status := "registered"
	if recs, err := csv.NewReader(bytes.NewReader([]byte(strings.TrimSpace(out)))).ReadAll(); err == nil && len(recs) > 0 && len(recs[0]) >= 3 {
		status = recs[0][len(recs[0])-1]
	}
	return fmt.Sprintf("%s (scheduled task %q, starts at logon; logs: %s)", status, WindowsTask, s.WindowsLog())
}
