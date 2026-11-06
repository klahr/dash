package runner

import (
	"bytes"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

type nopPub struct{}

func (nopPub) Publish(model.Event) {}

func readUntil(t *testing.T, s *Session, want string) string {
	t.Helper()
	var buf bytes.Buffer
	buf.Write(s.Scrollback)
	deadline := time.After(5 * time.Second)
	for !strings.Contains(buf.String(), want) {
		select {
		case b, ok := <-s.Output:
			if !ok {
				t.Fatalf("output closed before %q; got %q", want, buf.String())
			}
			buf.Write(b)
		case <-deadline:
			t.Fatalf("timeout waiting for %q; got %q", want, buf.String())
		}
	}
	return buf.String()
}

func TestPTYRoundTripAndStop(t *testing.T) {
	m := NewManager(nopPub{})
	p, err := m.StartPTY("proj", t.TempDir(), "bash --norc --noprofile", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.Attach(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Detach()

	if err := s.Write([]byte("echo dash-$((40+2))\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, s, "dash-42")

	// A child in the background must die with the group.
	if err := s.Write([]byte("sleep 300 &\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)

	if err := m.Stop(p.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(-p.PID, 0) == syscall.ESRCH {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("process group still alive after Stop")
}

func TestTaskExitCode(t *testing.T) {
	m := NewManager(nopPub{})
	p, err := m.StartTask("proj", t.TempDir(), "echo out; exit 3")
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.Attach(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, s, "out")
	for range s.Output {
	}
	time.Sleep(50 * time.Millisecond)
	got := m.List("proj")[0]
	if got.Running || got.ExitCode == nil || *got.ExitCode != 3 {
		t.Fatalf("want exited with 3, got %+v", got)
	}
}

func TestKeepRestoresLastRuns(t *testing.T) {
	path := t.TempDir() + "/processes.json"
	dir := t.TempDir()
	m := NewManager(nopPub{})
	if err := m.Keep(path); err != nil {
		t.Fatal(err)
	}
	pass, _ := m.StartTask("proj", dir, "echo ok")
	fail, _ := m.StartTask("proj", dir, "exit 3")
	srv, _ := m.StartWeb("proj", dir, "sleep 30")
	for _, id := range []string{pass.ID, fail.ID} {
		s, err := m.Attach(id)
		if err != nil {
			t.Fatal(err)
		}
		for range s.Output {
		}
	}
	time.Sleep(50 * time.Millisecond) // wait() saves after closing subscribers
	m.StopAll()

	r := NewManager(nopPub{})
	if err := r.Keep(path); err != nil {
		t.Fatal(err)
	}
	got := map[string]model.Process{}
	for _, p := range r.List("proj") {
		got[p.ID] = p
	}
	if p := got[pass.ID]; p.Running || p.ExitCode == nil || *p.ExitCode != 0 {
		t.Fatalf("passed task restored as %+v", p)
	}
	if p := got[fail.ID]; p.ExitCode == nil || *p.ExitCode != 3 {
		t.Fatalf("failed task restored as %+v", p)
	}
	if p, ok := got[srv.ID]; !ok || p.Running || p.ExitCode != nil {
		t.Fatalf("interrupted server restored as %+v", p)
	}
	s, err := r.Attach(pass.ID)
	if err != nil || !strings.Contains(string(s.Scrollback), "ok") {
		t.Fatalf("scrollback not restored: %v %q", err, s.Scrollback)
	}
	if err := r.Stop(srv.ID); err != nil {
		t.Fatal(err)
	}
}
