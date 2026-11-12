package runnerctl

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func fromUTF16(t *testing.T, b []byte) string {
	t.Helper()
	if len(b) < 2 || b[0] != 0xff || b[1] != 0xfe || len(b)%2 != 0 {
		t.Fatalf("not UTF-16LE with a byte order mark: % x", b[:min(len(b), 4)])
	}
	u := make([]uint16, 0, len(b)/2-1)
	for i := 2; i < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return string(utf16.Decode(u))
}

func TestTaskXML(t *testing.T) {
	doc := fromUTF16(t, TaskXML(`C:\Users\Me Too\dash.exe`, []string{"runner", "-data", `C:\x & <y>\`, "-log-file", `C:\l.log`}, `PC\me`, `C:\Users\Me Too`))
	var task struct {
		Triggers struct {
			Logon struct{ UserId string } `xml:"LogonTrigger"`
		}
		Principals struct {
			Principal struct{ UserId, LogonType string }
		}
		Settings struct {
			ExecutionTimeLimit string
			RestartOnFailure   struct{ Interval string }
		}
		Actions struct {
			Exec struct{ Command, Arguments, WorkingDirectory string }
		}
	}
	// encoding/xml reads only UTF-8: the declaration says UTF-16.
	body := strings.Replace(doc, `encoding="UTF-16"`, `encoding="UTF-8"`, 1)
	if err := xml.Unmarshal([]byte(body), &task); err != nil {
		t.Fatalf("task isn't XML: %v\n%s", err, doc)
	}
	if task.Triggers.Logon.UserId != `PC\me` || task.Principals.Principal.UserId != `PC\me` || task.Principals.Principal.LogonType != "InteractiveToken" {
		t.Errorf("runs as: %+v %+v", task.Triggers, task.Principals)
	}
	if task.Settings.ExecutionTimeLimit != "PT0S" || task.Settings.RestartOnFailure.Interval == "" {
		t.Errorf("settings: %+v", task.Settings)
	}
	e := task.Actions.Exec
	if e.Command != `"C:\Users\Me Too\dash.exe"` || e.WorkingDirectory != `C:\Users\Me Too` {
		t.Errorf("exec: %+v", e)
	}
	if want := `runner -data "C:\x & <y>\\" -log-file C:\l.log`; e.Arguments != want {
		t.Errorf("arguments %s, want %s", e.Arguments, want)
	}
}

func TestWindowsArg(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:          `plain`,
		``:               `""`,
		`C:\a b\`:        `"C:\a b\\"`,
		`say "hi"`:       `"say \"hi\""`,
		`back\"slash`:    `"back\\\"slash"`,
		`C:\no\spaces\x`: `C:\no\spaces\x`,
	} {
		if got := windowsArg(in); got != want {
			t.Errorf("windowsArg(%q) = %s, want %s", in, got, want)
		}
	}
}

// On Windows the service is a scheduled task: installing registers it,
// stops the runner running now (by its state file: after an update it may
// run outside the task) and starts the task; uninstalling removes it.
func TestWindowsServiceInstall(t *testing.T) {
	home, data := t.TempDir(), t.TempDir()
	if err := WriteState(StatePath(data), State{PID: os.Getpid() + 1, Since: time.Now()}); err != nil {
		t.Fatal(err)
	}
	var calls []string
	var killed []int
	svc := &Service{
		Bin: `C:\dash\dash.exe`, Args: []string{"runner"}, Home: home, GOOS: "windows", Data: data, User: `PC\me`,
		Run: func(name string, args ...string) (string, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			if slices.Contains(args, "/Query") {
				return `"\Dash runner","N/A","Running"`, nil
			}
			return "", nil
		},
		Kill: func(pid int) error { killed = append(killed, pid); return nil },
	}
	res, err := svc.Install()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "AppData", "Local", "dash-runner", "dash-runner-task.xml"); res.File != want || !res.Changed {
		t.Errorf("install: %+v", res)
	}
	if doc := fromUTF16(t, must(os.ReadFile(res.File))); !strings.Contains(doc, "-log-file "+svc.WindowsLog()) {
		t.Errorf("the task doesn't log to %s:\n%s", svc.WindowsLog(), doc)
	}
	want := []string{
		"schtasks /Create /TN Dash runner /XML " + res.File + " /F",
		"schtasks /End /TN Dash runner",
		"schtasks /Run /TN Dash runner",
	}
	if !slices.Equal(calls, want) {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
	// alive() is the real one: our own PID+1 may or may not run, so the
	// kill is checked only when it did.
	if len(killed) > 1 || (len(killed) == 1 && killed[0] != os.Getpid()+1) {
		t.Errorf("killed %v", killed)
	}
	if st := svc.State(); !strings.HasPrefix(st, "Running (scheduled task") {
		t.Errorf("state: %s", st)
	}

	calls = nil
	if removed, err := svc.Uninstall(); err != nil || !removed {
		t.Fatalf("uninstall: %v %v", removed, err)
	}
	if !slices.Contains(calls, "schtasks /Delete /TN Dash runner /F") {
		t.Errorf("uninstall calls: %q", calls)
	}
	if _, err := os.Stat(res.File); !os.IsNotExist(err) {
		t.Errorf("task file left: %v", err)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// On Windows the checks include the bash project commands run with.
func TestPreflightChecksBashOnWindows(t *testing.T) {
	p := Preflight{
		GOOS: "windows", Bash: `C:\Program Files\Git\bin\bash.exe`,
		LookPath: func(f string) (string, error) {
			if strings.HasSuffix(f, "bash.exe") {
				return "", os.ErrNotExist
			}
			return `C:\bin\` + f, nil
		},
		Output: func(context.Context, string, ...string) ([]byte, error) { return []byte(`{"loggedIn":true}`), nil },
	}
	c := p.Run(context.Background(), "http://dash.example.com", "claude") // plain http: no request
	if len(c) != 4 || c[1].Name != "bash" || c[1].OK || !strings.Contains(c[1].Fix, "Git for Windows") {
		t.Fatalf("%+v", c)
	}
	if c := (Preflight{GOOS: "linux", LookPath: p.LookPath, Output: p.Output}).Run(context.Background(), "http://dash.example.com", "claude"); slices.ContainsFunc(c, func(c Check) bool { return c.Name == "bash" }) {
		t.Errorf("bash checked on linux: %+v", c)
	}
}
