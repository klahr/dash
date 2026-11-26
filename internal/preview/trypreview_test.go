package preview

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tunnel"
)

// A commit preview's host label is <project>--<runner>; it never reads as
// a task's (<todo>, or <todo>-<runner> on a hub), and a task's never reads
// as one.
func TestTryLabel(t *testing.T) {
	t.Parallel()

	if l := TryLabel("a1b2c3", "work"); l != "a1b2c3--work" {
		t.Fatalf("label: %q", l)
	}
	for _, c := range []struct {
		label, project, runner string
		ok                     bool
	}{
		{"a1b2c3--work", "a1b2c3", "work", true},
		{"a1b2c3--my-box", "a1b2c3", "my-box", true},
		{"a1b2c3--my--box", "a1b2c3", "my--box", true},
		{"abc123", "", "", false},           // a task here
		{"abc123-work", "", "", false},      // a task on runner "work"
		{"abc123-my--box", "", "", false},   // a task on runner "my--box"
		{"a1b2c3---work", "", "", false},    // runner names start with a letter or digit
		{"--work", "", "", false},           // no project
		{"a1b2c3--", "", "", false},         // no runner
		{"try:a1b2c3--work", "", "", false}, // not a project id
	} {
		p, r, ok := ParseTryLabel(c.label)
		if p != c.project || r != c.runner || ok != c.ok {
			t.Errorf("ParseTryLabel(%q) = %q, %q, %v; want %q, %q, %v", c.label, p, r, ok, c.project, c.runner, c.ok)
		}
	}
}

// fakeRemote has the runners named.
type fakeRemote map[string]bool

func (f fakeRemote) Has(name string) bool { return f[name] }
func (f fakeRemote) DialPreview(context.Context, string, string) (net.Conn, int, error) {
	return nil, 0, errors.New("not used")
}

// On a hub, a commit preview's label names the runner and asks it for
// TryDialID(project); a task's label still asks for the task.
func TestRemoteLabelOfACommitPreview(t *testing.T) {
	t.Parallel()

	m := New(nil, nil, nil, "preview.dash.test")
	m.Remote = fakeRemote{"work": true, "my--box": true}
	for _, c := range []struct{ label, runner, id string }{
		{"a1b2c3--work", "work", "try:a1b2c3"},
		{"a1b2c3--my--box", "my--box", "try:a1b2c3"},
		{"abc123-work", "work", "abc123"},
		{"abc123-my--box", "my--box", "abc123"},
	} {
		r, id, ok := m.remoteLabel(c.label)
		if !ok || r != c.runner || id != c.id {
			t.Errorf("remoteLabel(%q) = %q, %q, %v; want %q, %q", c.label, r, id, ok, c.runner, c.id)
		}
	}
	if _, _, ok := m.remoteLabel("a1b2c3--home"); ok {
		t.Error("a commit preview on a runner that isn't registered")
	}
}

// A web try session runs the command as a server, starting until its port
// answers, then reachable as TryDialID(key), never as a task, and not as a
// terminal. It publishes no preview event (it isn't a task's), and stops
// after IdleAfter without traffic, cleaning up.
func TestWebTrySession(t *testing.T) {
	t.Parallel()

	m, _, _ := setup(t, "")
	pub := &events{}
	m.Pub = pub
	var cleaned atomic.Bool
	command := "sleep 30"
	py := havePython3()
	if py {
		command = "exec python3 -m http.server --bind 127.0.0.1 $PORT"
	}
	s, err := m.StartTry(TryStart{Key: "hp1", Project: model.Project{ID: "p1", DefaultBranch: "main"}, Dir: t.TempDir(),
		Commit: model.CommitInfo{SHA: "abc", Subject: "old"}, Web: true, Command: command, Cleanup: func() { cleaned.Store(true) }})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Web() || s.State != model.PreviewStarting || s.Port == 0 || s.TerminalURL != "" || s.Commit != "abc" {
		t.Fatalf("session: %+v", s)
	}
	if _, _, err := m.TryTerminal("hp1"); err != ErrNotTerminal {
		t.Errorf("attach a terminal to a commit preview: %v", err)
	}
	if _, _, err := m.DialPreview("hp1"); err != tunnel.ErrPreviewNotRunning {
		t.Errorf("dialled the project's key as a task: %v", err)
	}
	if len(m.List()) != 0 {
		t.Errorf("a commit preview in the tasks' list: %+v", m.List())
	}
	if py {
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			if x, _ := m.Try("hp1"); x.State == model.PreviewLive {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the commit preview never went live")
			}
		}
		c, port, err := m.DialPreview(TryDialID("hp1"))
		if err != nil || port != s.Port {
			t.Fatalf("dial: %d %v", port, err)
		}
		_, _ = io.WriteString(c, "GET / HTTP/1.0\r\nHost: localhost\r\n\r\n")
		b, _ := io.ReadAll(c)
		c.Close()
		if !strings.HasPrefix(string(b), "HTTP/1.0 200") {
			t.Fatalf("server answered %q", b)
		}
	} else if _, _, err := m.DialPreview(TryDialID("hp1")); err != tunnel.ErrPreviewStarting {
		t.Fatalf("dial while starting: %v", err)
	}
	pub.mu.Lock()
	n := len(pub.evs)
	pub.mu.Unlock()
	if n != 0 {
		t.Errorf("a commit preview published %d events: %+v", n, pub.evs)
	}

	m.IdleAfter = time.Minute
	m.Sweep(time.Now().Add(2 * time.Minute))
	if _, ok := m.Try("hp1"); ok {
		t.Fatal("an idle commit preview kept running")
	}
	if !cleaned.Load() {
		t.Error("its worktree wasn't cleaned up")
	}
}

// The hub serves a runner's commit preview at <project>--<runner>, through
// the runner's preview connection, as it serves a task's.
func TestCommitPreviewThroughTheTunnel(t *testing.T) {
	t.Parallel()

	dev := newDevServer(t)
	hubSrv, hub, runner := remoteSetup(t)
	runner.mu.Lock()
	runner.entries[tryKey("a1b2c3")] = &entry{done: make(chan struct{}), addr: strings.TrimPrefix(dev.URL, "http://"),
		info: model.Preview{ProjectID: "p1", Kind: model.PreviewWeb, State: model.PreviewLive, Port: dev.port, StartedAt: time.Now()},
		try:  &model.TrySession{ProjectID: "a1b2c3"}}
	runner.mu.Unlock()
	_, hubPort, _ := net.SplitHostPort(strings.TrimPrefix(hubSrv.URL, "http://"))
	get := func(label string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", hubSrv.URL+"/", nil)
		req.Host = label + ".preview.dash.test:" + hubPort
		req.Header.Set("Cookie", (&http.Cookie{Name: cookieName, Value: hub.sign("c", label, time.Now().Add(time.Hour))}).String())
		res, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	var code int
	var body string
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		code, body = get("a1b2c3--work") // once the preview connection is up
		if code != http.StatusBadGateway || time.Now().After(deadline) {
			break
		}
	}
	if code != http.StatusOK || !strings.Contains(body, "hello from the runner") {
		t.Fatalf("commit preview: %d %q", code, body)
	}
	// The project's id isn't a task's preview there.
	if code, _ := get("a1b2c3-work"); code != http.StatusNotFound {
		t.Errorf("the project's id as a task's label: %d", code)
	}
	// Nor is another project's commit preview running.
	if code, _ := get("ffff00--work"); code != http.StatusNotFound {
		t.Errorf("a project with no commit preview: %d", code)
	}
}
