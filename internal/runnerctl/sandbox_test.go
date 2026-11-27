package runnerctl

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/doctor"
	"github.com/klahr/dash/internal/testbin"
)

// The sandbox scripts are in deploy/sandbox, executable, and the release
// archives hold that folder; SandboxScripts names every one.
func TestReleaseArchivesHoldTheSandboxScripts(t *testing.T) {
	testbin.SkipOnWindows(t, "checks the scripts' executable bits")
	root := filepath.Join("..", "..")
	entries, err := os.ReadDir(filepath.Join(root, "deploy", "sandbox"))
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, e := range entries {
		found = append(found, e.Name())
		st, err := os.Stat(filepath.Join(root, "deploy", "sandbox", e.Name()))
		if err != nil || st.Mode()&0o111 == 0 {
			t.Errorf("deploy/sandbox/%s isn't executable: %v", e.Name(), err)
		}
	}
	slices.Sort(found)
	want := slices.Sorted(slices.Values(SandboxScripts))
	if !slices.Equal(found, want) {
		t.Errorf("deploy/sandbox holds %q, SandboxScripts names %q", found, want)
	}
	gr, err := os.ReadFile(filepath.Join(root, ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gr), "\n      - deploy/sandbox/*\n") {
		t.Error(".goreleaser.yaml's archives don't hold deploy/sandbox/*")
	}
}

func lookPathOf(tools ...string) func(string) (string, error) {
	return func(f string) (string, error) {
		if slices.Contains(tools, f) {
			return "/usr/bin/" + f, nil
		}
		return "", errors.New("not found")
	}
}

func TestSandboxScript(t *testing.T) {
	for _, c := range []struct {
		goos, tools, script, needs string
	}{
		{"darwin", "", "claude-seatbelt", ""},
		{"linux", "bwrap", "claude-bwrap", ""},
		{"linux", "", "claude-bwrap", "bubblewrap"},
		{"windows", "", "", ""},
	} {
		script, needs := SandboxScript(c.goos, lookPathOf(strings.Fields(c.tools)...))
		if script != c.script || needs != c.needs {
			t.Errorf("%s with %q: %q %q, want %q %q", c.goos, c.tools, script, needs, c.script, c.needs)
		}
	}
}

// binWith is a dash binary path in a fresh folder that holds scripts.
func binWith(t *testing.T, scripts ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, s := range scripts {
		if err := os.WriteFile(filepath.Join(dir, s), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "dash")
}

// The hint gives the exact command for the script beside dash, only where
// one fits and no sandbox is set.
func TestSandboxHint(t *testing.T) {
	testbin.SkipOnWindows(t, "finds the sandbox scripts by their executable bits")
	bin := binWith(t, "claude-seatbelt", "claude-bwrap")
	dir := filepath.Dir(bin)
	data := filepath.Join(t.TempDir(), "runner data")
	none := func(string) string { return "" }

	got := SandboxHint("darwin", bin, data, true, none, lookPathOf())
	want := "To sandbox it: echo DASH_CLAUDE_SANDBOX=" + filepath.Join(dir, "claude-seatbelt") + " >> '" + filepath.Join(data, "runner.env") +
		"' && dash runner install --data '" + data + "'"
	if !strings.Contains(got, "unsandboxed") || !strings.HasSuffix(got, want) {
		t.Errorf("macOS hint:\n%s\nwant it to end with\n%s", got, want)
	}
	if got := SandboxHint("linux", bin, data, false, none, lookPathOf("bwrap")); !strings.Contains(got, "DASH_CLAUDE_SANDBOX="+filepath.Join(dir, "claude-bwrap")) ||
		strings.Contains(got, "dash runner install") || !strings.Contains(got, "restart the runner") {
		t.Errorf("Linux hint without the service: %s", got)
	}
	if got := SandboxHint("linux", bin, data, true, none, lookPathOf()); got != "" {
		t.Errorf("hint on Linux without bubblewrap: %s", got)
	}
	if got := SandboxHint("windows", bin, data, true, none, lookPathOf()); got != "" {
		t.Errorf("hint on Windows: %s", got)
	}
	if got := SandboxHint("darwin", binWith(t), data, true, none, lookPathOf()); !strings.Contains(got, "docs/sandbox.md") || strings.Contains(got, "echo") {
		t.Errorf("hint without the script beside dash: %s", got)
	}

	// A sandbox already set, in the environment or runner.env: no hint.
	set := func(k string) string {
		if k == SandboxEnv {
			return "/opt/my-sandbox"
		}
		return ""
	}
	if got := SandboxHint("darwin", bin, data, true, set, lookPathOf()); got != "" {
		t.Errorf("hint with DASH_CLAUDE_SANDBOX set: %s", got)
	}
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(EnvPath(data), []byte("# mine\nexport DASH_CLAUDE_SANDBOX='/opt/x y'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := SandboxHint("darwin", bin, data, true, none, lookPathOf()); got != "" {
		t.Errorf("hint with DASH_CLAUDE_SANDBOX in runner.env: %s", got)
	}
}

// Setup prints the hint and never turns the sandbox on.
func TestSetupSaysHowToSandboxClaude(t *testing.T) {
	testbin.SkipOnWindows(t, "finds the sandbox scripts by their executable bits")
	t.Setenv(SandboxEnv, "")
	tc := newTestCLI(t)
	tc.Preflight.GOOS = "linux" // with bwrap on its PATH, as all tools are
	bin := binWith(t, "claude-bwrap")
	tc.Executable = func() (string, error) { return bin, nil }
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"version":"v1.2.3"}`)
	}))
	defer hub.Close()
	args := []string{"--server", hub.URL, "--code", "ABCD-EFGH"}
	if code := tc.Main(context.Background(), "setup", args); code != 0 {
		t.Fatalf("setup exited %d", code)
	}
	data := filepath.Join(tc.Home, ".local", "share", "dash-runner")
	want := "To sandbox it: echo DASH_CLAUDE_SANDBOX=" + filepath.Join(filepath.Dir(bin), "claude-bwrap") + " >> " + filepath.Join(data, "runner.env") + " && dash runner install"
	if !strings.Contains(tc.out.String(), want) {
		t.Errorf("setup output lacks %q", want)
	}
	if _, err := os.Stat(EnvPath(data)); err == nil {
		t.Error("setup wrote runner.env")
	}
	if err := os.WriteFile(EnvPath(data), []byte("DASH_CLAUDE_SANDBOX="+filepath.Join(filepath.Dir(bin), "claude-bwrap")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tc.out.Reset()
	if code := tc.Main(context.Background(), "setup", []string{"--server", hub.URL}); code != 0 {
		t.Fatalf("setup again exited %d", code)
	}
	if strings.Contains(tc.out.String(), "sandbox") {
		t.Errorf("setup with a sandbox set still hints:\n%s", tc.out.String())
	}
}

// The doctor warns of unsandboxed runs where a bundled script fits, with
// the command that turns it on (after installing bubblewrap, on Linux).
func TestDoctorSandbox(t *testing.T) {
	testbin.SkipOnWindows(t, "finds the sandbox scripts by their executable bits")
	bin := binWith(t, "claude-seatbelt", "claude-bwrap")
	dir := filepath.Dir(bin)
	data := t.TempDir()

	m := &fakeMachine{tools: map[string]bool{"git": true, "claude": true}, loggedIn: true}
	d := m.doctor(t, "linux")
	d.Bin, d.Data, d.Service = bin, data, true
	c := byName(d.Run(context.Background()))[doctor.CheckSandbox]
	fix := "sudo apt install -y bubblewrap && echo DASH_CLAUDE_SANDBOX=" + filepath.Join(dir, "claude-bwrap") + " >> " + EnvPath(data) + " && dash runner install --data " + data
	if c.Status != doctor.Warning || c.Group != doctor.GroupFeatures || !strings.HasPrefix(c.Message, "Claude runs unsandboxed") || c.Fix != fix {
		t.Errorf("Linux without bubblewrap: %+v\nwant fix %s", c, fix)
	}

	d = m.doctor(t, "darwin")
	d.Bin, d.Data = bin, data // started by hand
	c = byName(d.Run(context.Background()))[doctor.CheckSandbox]
	if fix := "echo DASH_CLAUDE_SANDBOX=" + filepath.Join(dir, "claude-seatbelt") + " >> " + EnvPath(data); c.Status != doctor.Warning || c.Fix != fix || !strings.Contains(c.Message, "Restart the runner") {
		t.Errorf("macOS: %+v", c)
	}

	d.Sandbox = filepath.Join(dir, "claude-seatbelt")
	if c := byName(d.Run(context.Background()))[doctor.CheckSandbox]; c.Status != doctor.OK || !strings.Contains(c.Message, "claude-seatbelt") {
		t.Errorf("sandboxed: %+v", c)
	}
}
