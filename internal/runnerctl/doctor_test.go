package runnerctl

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/disk"
	"github.com/klahr/dash/internal/doctor"
)

// fakeMachine is a computer for the doctor: the tools on its PATH, the
// files that exist, and what `claude auth status` and `gh auth status` say.
type fakeMachine struct {
	tools    map[string]bool
	files    map[string]bool
	loggedIn bool // claude
	ghAuth   bool
	asked    []string
}

func (m *fakeMachine) doctor(t *testing.T, goos string) *Doctor {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"version":"1.2.3"}`))
	}))
	t.Cleanup(hub.Close)
	return &Doctor{
		Server: hub.URL, ClaudeBin: "claude",
		Preflight: Preflight{GOOS: goos, Bash: "bash", Client: hub.Client(),
			LookPath: func(f string) (string, error) {
				m.asked = append(m.asked, f)
				if m.tools[f] {
					return "/usr/bin/" + f, nil
				}
				return "", errors.New("not found")
			},
			Output: func(_ context.Context, name string, args ...string) ([]byte, error) {
				switch {
				case strings.HasSuffix(name, "claude"):
					if m.loggedIn {
						return []byte(`{"loggedIn":true,"email":"a@example.com"}`), nil
					}
					return []byte(`{"loggedIn":false}`), errors.New("exit status 1")
				case strings.HasSuffix(name, "gh"):
					if m.ghAuth {
						return nil, nil
					}
					return nil, errors.New("exit status 1")
				}
				return nil, errors.New("unexpected " + name)
			}},
		Stat: func(p string) (os.FileInfo, error) {
			if m.files[p] {
				return nil, nil
			}
			return nil, os.ErrNotExist
		},
		ReadFile: func(p string) ([]byte, error) {
			if p == "/etc/os-release" {
				return []byte("ID=ubuntu\nID_LIKE=debian\n"), nil
			}
			return nil, os.ErrNotExist
		},
		Getenv: func(string) string { return "" },
	}
}

func byName(r doctor.Report) map[string]doctor.Check {
	out := map[string]doctor.Check{}
	for _, c := range r.Checks {
		k := c.Name
		if c.Group == doctor.GroupProject {
			k = c.Project + "/" + c.Name
		}
		out[k] = c
	}
	return out
}

// A runner with everything gets only OKs; the groups come in order.
func TestDoctorAllGood(t *testing.T) {
	m := &fakeMachine{tools: map[string]bool{"git": true, "claude": true, "gh": true, "chromium": true, "go": true, "npm": true},
		loggedIn: true, ghAuth: true}
	d := m.doctor(t, "linux")
	d.Sandbox = "/home/me/.local/bin/claude-bwrap"
	d.Disk = func() (disk.Status, disk.Settings) {
		return disk.Status{Volumes: []disk.Volume{{Label: "projects", Free: 50 * disk.GB, Total: 100 * disk.GB}}}, disk.Settings{}
	}
	d.Projects = func() []DoctorProject {
		return []DoctorProject{{ID: "hp1", Name: "api", TestCommand: "go test ./...", RunCommand: "./run.sh && npm start"}}
	}
	rep := d.Run(context.Background())
	if rep.System != "Linux (Debian/Ubuntu)" || rep.At.IsZero() {
		t.Errorf("system %q at %v", rep.System, rep.At)
	}
	for _, c := range rep.Checks {
		if c.Status != doctor.OK {
			t.Errorf("%s:%s = %s %q", c.Group, c.Name, c.Status, c.Message)
		}
	}
	var names []string
	for _, c := range rep.Checks {
		names = append(names, c.Group+":"+c.Name)
	}
	want := "required:git required:claude required:hub required:disk features:gh features:chrome features:sandbox project:go project:npm"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("checks = %s, want %s", got, want)
	}
	if d.Last() == nil || !d.Last().At.Equal(rep.At) {
		t.Error("Last should be the run's report")
	}
}

// What is missing says so, with a fix for the runner's system: required
// failures are errors, features and projects warnings.
func TestDoctorMissing(t *testing.T) {
	m := &fakeMachine{tools: map[string]bool{"claude": true, "gh": true, "go": true}}
	d := m.doctor(t, "darwin")
	d.Disk = func() (disk.Status, disk.Settings) {
		return disk.Status{Volumes: []disk.Volume{
			{Label: "projects", Free: 40 * disk.GB, Total: 500 * disk.GB},
			{Label: "worktrees", Free: 3 * disk.GB, Total: 100 * disk.GB},
			{Label: "cache", Free: 1 * disk.GB, Total: 100 * disk.GB}, // not the worktrees' disk
		}}, disk.Settings{}
	}
	d.Projects = func() []DoctorProject {
		return []DoctorProject{
			{ID: "hp1", Name: "api", TestCommand: "go test ./...", RunCommand: "make run"},
			{ID: "hp2", Name: "web", TestCommand: "npm test && make lint"},
		}
	}
	rep := d.Run(context.Background())
	got := byName(rep)
	expect := func(key string, st doctor.Status, fix, msg string) {
		t.Helper()
		c, ok := got[key]
		if !ok {
			t.Errorf("no check %s", key)
			return
		}
		if c.Status != st || c.Fix != fix || !strings.Contains(c.Message, msg) {
			t.Errorf("%s = %s %q %q; want %s %q …%q…", key, c.Status, c.Fix, c.Message, st, fix, msg)
		}
	}
	expect("git", doctor.Error, "xcode-select --install", "not found")
	expect("claude", doctor.Error, "claude", "log in")
	expect("hub", doctor.OK, "", "Dash 1.2.3")
	expect("disk", doctor.Error, "du -sh ~/.cache/* 2>/dev/null | sort -h | tail", "3.0 GB free")
	expect("gh", doctor.Warning, "gh auth login", "not logged in")
	expect("chrome", doctor.Warning, "brew install --cask google-chrome", "screenshots are skipped")
	expect("sandbox", doctor.Warning, "", "docs/sandbox.md") // no claude-seatbelt beside dash
	expect("hp1/go", doctor.OK, "", "/usr/bin/go")
	expect("hp1/make", doctor.Warning, "xcode-select --install", "api's test or run command")
	expect("hp2/npm", doctor.Warning, "brew install node", "web's")
	expect("hp2/make", doctor.Warning, "xcode-select --install", "web's")
	// make is looked up once for both projects.
	n := 0
	for _, a := range m.asked {
		if a == "make" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("make looked up %d times", n)
	}
}

// Claude not installed gets its installer; Windows checks bash, finds
// Chrome where it installs itself, and gets winget commands.
func TestDoctorWindows(t *testing.T) {
	m := &fakeMachine{tools: map[string]bool{"git": true, "bash": true},
		files: map[string]bool{`C:\Program Files\Google\Chrome\Application\chrome.exe`: true}}
	d := m.doctor(t, "windows")
	d.Getenv = func(k string) string {
		if k == "ProgramFiles" {
			return `C:\Program Files`
		}
		return ""
	}
	got := byName(d.Run(context.Background()))
	if c := got["bash"]; c.Status != doctor.OK || c.Group != doctor.GroupRequired {
		t.Errorf("bash = %+v", c)
	}
	if c := got["claude"]; c.Status != doctor.Error || c.Fix != "irm https://claude.ai/install.ps1 | iex" {
		t.Errorf("claude = %+v", c)
	}
	if c := got["gh"]; c.Status != doctor.Warning || c.Fix != "winget install GitHub.cli" || !strings.Contains(c.Message, "No gh") {
		t.Errorf("gh = %+v", c)
	}
	if c := got["chrome"]; c.Status != doctor.OK || !strings.Contains(c.Message, "chrome.exe") {
		t.Errorf("chrome = %+v", c)
	}
	if _, ok := got["disk"]; ok {
		t.Error("no disk check without a disk monitor")
	}
	if c, ok := got["sandbox"]; ok {
		t.Errorf("a sandbox check on Windows, which has no bundled sandbox: %+v", c)
	}
}

// The hub unreachable is an error, with the preflight's advice; a second
// run asked while one is under way shares its report.
func TestDoctorHubDownAndRunsOnce(t *testing.T) {
	m := &fakeMachine{tools: map[string]bool{"git": true, "claude": true}, loggedIn: true}
	d := m.doctor(t, "linux")
	d.Server = "http://127.0.0.1:1"
	c := byName(d.Run(context.Background()))["hub"]
	if c.Status != doctor.Error || !strings.Contains(c.Message, "can't reach") {
		t.Errorf("hub = %+v", c)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d.Now = func() time.Time { return at }
	first := d.Run(context.Background())
	m.asked = nil
	// Asked at the time of the last run (as if it ended while this waited):
	// that report is the answer, nothing runs again.
	if again := d.Run(context.Background()); !again.At.Equal(first.At) || len(m.asked) != 0 {
		t.Errorf("ran again: %v %q", again.At, m.asked)
	}
}
