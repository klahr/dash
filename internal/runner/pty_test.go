package runner

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/model"
)

type nopPub struct{}

func (nopPub) Publish(model.Event) {}

func readUntil(t *testing.T, s *Session, want string) string {
	t.Helper()
	var buf bytes.Buffer
	buf.Write(s.Scrollback)
	deadline := time.After(10 * time.Second)
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

// TestPTYEchoResizeStop runs on every system, Windows (ConPTY) included.
func TestPTYEchoResizeStop(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath(localrun.Bash()); err != nil {
		t.Skip("no bash")
	}

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

	// The sum shows only in the output, not in the echoed input.
	if err := s.Write([]byte("echo h$((1-1))i\r")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, s, "h0i")
	if err := s.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	if err := s.Write([]byte("stty size\r")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, s, "30 100")

	if err := m.Stop(p.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-s.Output:
		case <-deadline:
			t.Fatal("output still open after Stop")
		}
	}
	for range 100 {
		if got, _ := m.Get(p.ID); !got.Running {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("still running after Stop")
}
