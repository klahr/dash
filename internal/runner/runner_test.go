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
