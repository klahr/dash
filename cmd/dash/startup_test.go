package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Stripe Checkout links the terms the hub serves itself by its public URL.
func TestAbsURL(t *testing.T) {
	for _, c := range []struct{ pub, u, want string }{
		{"https://dash.example.com/", "/terms", "https://dash.example.com/terms"},
		{"https://dash.example.com", "https://example.com/terms", "https://example.com/terms"},
		{"https://dash.example.com", "", ""},
	} {
		if got := absURL(c.pub, c.u); got != c.want {
			t.Errorf("absURL(%q, %q) = %q, want %q", c.pub, c.u, got, c.want)
		}
	}
}

// A second hub on a busy port says what to do, and stops before it makes
// a data directory or an account.
func TestBusyPort(t *testing.T) {
	endToEnd(t)
	bin, _ := buildDash(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	data := filepath.Join(t.TempDir(), "data")
	cmd := exec.Command(bin, "-addr", ln.Addr().String(), "-data", data)
	cmd.Env = append(os.Environ(), "DASH_USERNAME=admin", "DASH_PASSWORD=a-long-test-password")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("dash started on a busy port:\n%s", out)
	}
	if !strings.Contains(string(out), "port "+port+" is in use: is another Dash running? Choose another with -addr") {
		t.Errorf("no hint about the busy port:\n%s", out)
	}
	if strings.Contains(string(out), "dash listening") {
		t.Errorf("said it listens:\n%s", out)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Errorf("made the data directory %s (%v)", data, err)
	}
}

// `dash runner -h` leaves out the flags only the hub reads, and `dash -h`
// the runner's.
func TestFlagHelp(t *testing.T) {
	endToEnd(t)
	bin, _ := buildDash(t)
	help := func(args ...string) string {
		out, _ := exec.Command(bin, args...).CombinedOutput() // -h exits with 0 or 2
		return string(out)
	}
	shows := func(out, name string) bool {
		return strings.Contains(out, "\n  -"+name+" ") || strings.Contains(out, "\n  -"+name+"\n")
	}
	runner := help("runner", "-h")
	for _, name := range []string{"server", "code", "projects", "parallel", "claude"} {
		if !shows(runner, name) {
			t.Errorf("dash runner -h leaves out -%s:\n%s", name, runner)
		}
	}
	for _, name := range append(append([]string{}, hubOnlyFlags...), removedFlags...) {
		if shows(runner, name) {
			t.Errorf("dash runner -h shows the hub's -%s", name)
		}
	}
	hub := help("-h")
	for _, name := range runnerFlags {
		if shows(hub, name) {
			t.Errorf("dash -h shows the runner's -%s", name)
		}
	}
	for _, name := range hubOnlyFlags {
		if !shows(hub, name) {
			t.Errorf("dash -h leaves out -%s", name)
		}
	}
}
