package runnerctl

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/tunnel"
)

func TestSystemdUnit(t *testing.T) {
	u := string(SystemdUnitFile("/home/me/my bin/dash", []string{"runner", "-data", "/x/50%"}, "/usr/bin:/home/me/.local/bin"))
	for _, want := range []string{
		`ExecStart="/home/me/my bin/dash" runner -data /x/50%%` + "\n",
		"Environment=PATH=/usr/bin:/home/me/.local/bin\n",
		"WorkingDirectory=%h\n",
		"Restart=on-failure\n",
		fmt.Sprintf("RestartPreventExitStatus=%d\n", ExitRevoked),
		"WantedBy=default.target\n",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
	if got := systemdQuote(`a"b$c`, true); got != `"a\"b$$c"` {
		t.Errorf("quote: %s", got)
	}
}

func TestLaunchdPlist(t *testing.T) {
	p := LaunchdPlist("/Users/me/bin/dash", []string{"runner", "-data", "/x & <y>"}, "/usr/bin:/opt/homebrew/bin", "/Users/me/Library/Logs/dash-runner.log")
	// Well-formed XML, with the arguments back as given.
	var doc struct {
		Dict struct {
			Inner []byte `xml:",innerxml"`
		} `xml:"dict"`
	}
	if err := xml.Unmarshal(p, &doc); err != nil {
		t.Fatalf("plist isn't XML: %v\n%s", err, p)
	}
	d := xml.NewDecoder(bytes.NewReader(p))
	var strs []string
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "string" {
			var s string
			_ = d.DecodeElement(&s, &se)
			strs = append(strs, s)
		}
	}
	want := []string{LaunchdLabel, "/Users/me/bin/dash", "runner", "-data", "/x & <y>", "/usr/bin:/opt/homebrew/bin"}
	if len(strs) < len(want) || !slices.Equal(strs[:len(want)], want) {
		t.Errorf("plist strings %q, want %q first", strs, want)
	}
	for _, k := range []string{"<key>RunAtLoad</key>", "<key>KeepAlive</key>", "<key>StandardErrorPath</key>"} {
		if !bytes.Contains(p, []byte(k)) {
			t.Errorf("plist lacks %s", k)
		}
	}
}

// fakeManager records service manager calls.
type fakeManager struct {
	calls  []string
	linger string
	fail   string // a call that fails
}

func (f *fakeManager) run(name string, args ...string) (string, error) {
	c := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, c)
	if f.fail != "" && strings.HasPrefix(c, f.fail) {
		return "Failed to connect to bus", errors.New("exit status 1")
	}
	switch {
	case strings.HasPrefix(c, "loginctl show-user"):
		return "Linger=" + f.linger, nil
	case strings.HasPrefix(c, "systemctl --user is-active"):
		return "active", nil
	case strings.HasPrefix(c, "systemctl --user is-enabled"):
		return "enabled", nil
	}
	return "", nil
}

func TestServiceInstallIsIdempotent(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			home := t.TempDir()
			m := &fakeManager{linger: "no"}
			s := &Service{Bin: "/opt/dash", Args: []string{"runner"}, Home: home, Path: "/usr/bin", GOOS: goos, Run: m.run}
			if got := s.State(); got != "not installed" {
				t.Fatalf("state before install: %s", got)
			}
			res, err := s.Install()
			if err != nil || !res.Changed {
				t.Fatalf("install: %+v %v", res, err)
			}
			first, _ := os.ReadFile(res.File)
			again, err := s.Install()
			if err != nil || again.Changed || again.File != res.File {
				t.Fatalf("re-install: %+v %v", again, err)
			}
			second, _ := os.ReadFile(res.File)
			if !bytes.Equal(first, second) {
				t.Error("re-install changed the file")
			}
			// Both runs (re)start it, so an updated binary is picked up.
			var starts int
			for _, c := range m.calls {
				if c == "systemctl --user restart "+SystemdUnit || strings.HasPrefix(c, "launchctl bootstrap") {
					starts++
				}
			}
			if starts != 2 {
				t.Errorf("started %d times, want 2: %q", starts, m.calls)
			}
			if goos == "linux" {
				if !strings.Contains(res.Hint, "loginctl enable-linger") {
					t.Errorf("no linger hint: %q", res.Hint)
				}
				if st := s.State(); !strings.HasPrefix(st, "active, enabled") {
					t.Errorf("state: %s", st)
				}
			}
			// A new binary path rewrites the file.
			s.Bin = "/opt/dash2"
			if res, _ := s.Install(); !res.Changed {
				t.Error("changed service not rewritten")
			}
			removed, err := s.Uninstall()
			if err != nil || !removed {
				t.Fatalf("uninstall: %v %v", removed, err)
			}
			if _, err := os.Stat(res.File); !os.IsNotExist(err) {
				t.Error("service file left behind")
			}
			if removed, _ := s.Uninstall(); removed {
				t.Error("second uninstall removed something")
			}
		})
	}
}

func TestServiceInstallWithoutSystemd(t *testing.T) {
	m := &fakeManager{fail: "systemctl"}
	s := &Service{Bin: "/opt/dash", Args: []string{"runner"}, Home: t.TempDir(), GOOS: "linux", Run: m.run}
	if _, err := s.Install(); err == nil || !strings.Contains(err.Error(), "--no-service") {
		t.Fatalf("want an error pointing at --no-service, got %v", err)
	}
	if _, err := (&Service{GOOS: "windows"}).Install(); err == nil {
		t.Error("windows accepted")
	}
}

// testCLI is a CLI against a fake hub, a fake service manager and checks
// that pass unless told otherwise.
type testCLI struct {
	*CLI
	out       *bytes.Buffer
	mgr       *fakeManager
	registers int
	loggedIn  bool
}

func newTestCLI(t *testing.T) *testCLI {
	t.Helper()
	tc := &testCLI{out: &bytes.Buffer{}, mgr: &fakeManager{linger: "yes"}, loggedIn: true}
	tc.CLI = &CLI{
		Version:    "v1.2.3",
		Home:       t.TempDir(),
		Hostname:   "box",
		Executable: func() (string, error) { return "/home/me/.local/bin/dash", nil },
		Preflight: Preflight{
			LookPath: func(f string) (string, error) { return "/usr/bin/" + f, nil },
			Output: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return json.Marshal(map[string]any{"loggedIn": tc.loggedIn, "email": "me@example.com"})
			},
		},
		Register: func(ctx context.Context, server, code, name, prev string) (tunnel.Credentials, error) {
			tc.registers++
			if code != "ABCD-EFGH" {
				return tunnel.Credentials{}, errors.New("unknown or expired registration code")
			}
			return tunnel.Credentials{Server: server, ID: "id1", Name: name, Token: "dr_x"}, nil
		},
		Service: func(s *Service) { s.GOOS, s.Run = "linux", tc.mgr.run },
	}
	tc.Out, tc.Err = tc.out, tc.out
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(tc.out.String())
		}
	})
	return tc
}

func TestSetupRegistersAndReinstallKeepsTheRegistration(t *testing.T) {
	tc := newTestCLI(t)
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"version":"v1.2.3"}`)
	}))
	defer hub.Close()
	ctx := context.Background()

	if code := tc.Main(ctx, "setup", []string{"--server", hub.URL, "--code", "ABCD-EFGH", "--projects", "~/work"}); code != 0 {
		t.Fatalf("setup exited %d", code)
	}
	creds, err := tunnel.LoadCredentials(filepath.Join(tc.Home, ".local", "share", "dash-runner", "runner.json"))
	if err != nil || creds.Name != "box" || creds.Server != hub.URL || creds.Projects != filepath.Join(tc.Home, "work") {
		t.Fatalf("saved %+v, %v", creds, err)
	}
	unit := filepath.Join(tc.Home, ".config", "systemd", "user", SystemdUnit)
	first, err := os.ReadFile(unit)
	if err != nil || !bytes.Contains(first, []byte("ExecStart=/home/me/.local/bin/dash runner\n")) {
		t.Fatalf("unit %s: %v", first, err)
	}

	// The installer run again without a code: same registration, same
	// unit, the service restarted.
	tc.mgr.calls = nil
	if code := tc.Main(ctx, "setup", []string{"--server", hub.URL}); code != 0 {
		t.Fatalf("re-setup exited %d", code)
	}
	if tc.registers != 1 {
		t.Errorf("registered %d times", tc.registers)
	}
	again, _ := tunnel.LoadCredentials(filepath.Join(tc.Home, ".local", "share", "dash-runner", "runner.json"))
	if again != creds {
		t.Errorf("registration changed: %+v → %+v", creds, again)
	}
	if second, _ := os.ReadFile(unit); !bytes.Equal(first, second) {
		t.Error("unit changed")
	}
	if !slices.Contains(tc.mgr.calls, "systemctl --user restart "+SystemdUnit) {
		t.Errorf("service not restarted: %q", tc.mgr.calls)
	}
	if !strings.Contains(tc.out.String(), "Already registered") {
		t.Error("didn't say it kept the registration")
	}

	// Another hub without a code is refused.
	if code := tc.Main(ctx, "setup", []string{"--server", "https://other.example.com"}); code == 0 {
		t.Error("switched hubs without a code")
	}

	// status shows it all.
	tc.out.Reset()
	if code := tc.Main(ctx, "status", nil); code != 0 {
		t.Fatal("status failed")
	}
	for _, want := range []string{"Registration: box", "Hub:          " + hub.URL, "Connection:   not running", "Service:      active, enabled"} {
		if !strings.Contains(tc.out.String(), want) {
			t.Errorf("status lacks %q", want)
		}
	}
	if err := WriteState(StatePath(filepath.Join(tc.Home, ".local", "share", "dash-runner")), State{PID: os.Getpid(), Version: "v1.2.2", Connected: true}); err != nil {
		t.Fatal(err)
	}
	tc.out.Reset()
	tc.Main(ctx, "status", nil)
	if s := tc.out.String(); !strings.Contains(s, "Connection:   connected") || !strings.Contains(s, "restart it to run v1.2.3") {
		t.Errorf("status:\n%s", s)
	}

	// uninstall keeps the registration.
	if code := tc.Main(ctx, "uninstall", nil); code != 0 {
		t.Fatal("uninstall failed")
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Error("unit left behind")
	}
	if _, err := tunnel.LoadCredentials(filepath.Join(tc.Home, ".local", "share", "dash-runner", "runner.json")); err != nil {
		t.Error("uninstall dropped the registration")
	}
}

func TestSetupChecksBeforeUsingTheCode(t *testing.T) {
	tc := newTestCLI(t)
	tc.loggedIn = false
	if code := tc.Main(context.Background(), "setup", []string{"--server", "https://127.0.0.1:1", "--code", "ABCD-EFGH"}); code == 0 {
		t.Fatal("setup passed with failing checks")
	}
	if tc.registers != 0 {
		t.Error("used the code although the checks failed")
	}
	out := tc.out.String()
	for _, want := range []string{"[ok  ] git", "[FAIL] claude", "not logged in", "→ run claude and log in", "[FAIL] hub", "can't reach"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	// Not registered and no code: say how to get one.
	tc.out.Reset()
	if code := tc.Main(context.Background(), "setup", []string{"--server", "https://dash.example.com"}); code == 0 || !strings.Contains(tc.out.String(), "get a code") {
		t.Errorf("unregistered setup without a code: %s", tc.out.String())
	}
}

func TestPreflightPlainHTTPHub(t *testing.T) {
	c := Preflight{LookPath: func(string) (string, error) { return "", errors.New("nope") }}.Run(context.Background(), "http://dash.example.com", "claude")
	if !Failed(c) || c[0].OK || c[1].OK || c[2].OK || !strings.Contains(c[2].Fix, "HTTPS") {
		t.Fatalf("%+v", c)
	}
}

// gh isn't needed to register a runner: every check passes without it.
func TestPreflightDoesNotNeedGh(t *testing.T) {
	var looked []string
	p := Preflight{
		LookPath: func(f string) (string, error) {
			looked = append(looked, f)
			if f == "gh" {
				return "", errors.New("not found")
			}
			return "/usr/bin/" + f, nil
		},
		Output: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return json.Marshal(map[string]any{"loggedIn": true, "email": "me@example.com"})
		},
	}
	for _, c := range p.Run(context.Background(), "http://dash.example.com", "claude")[:2] {
		if !c.OK {
			t.Errorf("%s: %+v", c.Name, c)
		}
	}
	if slices.Contains(looked, "gh") {
		t.Errorf("preflight looked for gh: %v", looked)
	}
}

func TestServicePath(t *testing.T) {
	if got := ServicePath("/usr/bin::/bin", "/bin", "/opt/x", ""); got != "/usr/bin:/bin:/opt/x" {
		t.Errorf("got %s", got)
	}
}
