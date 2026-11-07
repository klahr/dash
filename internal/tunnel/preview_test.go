package tunnel

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// fakePreviews answers DialPreview for "abc123" with a server that floods
// the connection and never reads, like a heavy page or chatty HMR socket.
type fakePreviews struct {
	mu     sync.Mutex
	opened int
}

func (f *fakePreviews) DialPreview(id string) (net.Conn, int, error) {
	switch id {
	case "abc123":
	case "starting":
		return nil, 0, ErrPreviewStarting
	default:
		return nil, 0, ErrPreviewNotRunning
	}
	f.mu.Lock()
	f.opened++
	f.mu.Unlock()
	a, b := net.Pipe()
	go func() {
		chunk := make([]byte, 64<<10)
		for {
			if _, err := b.Write(chunk); err != nil {
				return
			}
		}
	}()
	return a, 5173, nil
}

func TestPreviewStreamsAreLimitedAndKeepOffTaskTraffic(t *testing.T) {
	th := newTestHub(t)
	creds := th.register(t, "work")
	fp := &fakePreviews{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	cl := &Client{Creds: creds, Handler: runnerAPI(), Previews: fp}
	go func() { _ = cl.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	th.waitOnline(t, "work")

	dial := func(id string, wait time.Duration) (net.Conn, int, error) {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		return th.DialPreview(ctx, "work", id)
	}
	// Wait for the preview connection.
	var first net.Conn
	for deadline := time.Now().Add(10 * time.Second); ; {
		c, port, err := dial("abc123", time.Second)
		if err == nil {
			if port != 5173 {
				t.Fatalf("port %d", port)
			}
			first = c
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, _, err := dial("starting", 5*time.Second); !errors.Is(err, ErrPreviewStarting) {
		t.Fatalf("starting: %v", err)
	}
	if _, _, err := dial("zzz999", 5*time.Second); !errors.Is(err, ErrPreviewNotRunning) {
		t.Fatalf("not running: %v", err)
	}
	if _, _, err := dial("127.0.0.1:22", 5*time.Second); !errors.Is(err, ErrPreviewNotRunning) {
		t.Fatalf("an address as todo: %v", err)
	}

	// Fill every stream with a server pushing data nobody reads.
	conns := []net.Conn{first}
	for len(conns) < previewStreams {
		c, _, err := dial("abc123", 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	// One more waits for a free stream.
	if _, _, err := dial("abc123", 200*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stream %d: %v", previewStreams+1, err)
	}
	// Meanwhile task traffic on the main tunnel goes through at once.
	start := time.Now()
	if code, body := get(t, th.srv.URL+"/peer/work/api/me"); code != 200 || body != "runner:/api/me" {
		t.Fatalf("api while previews are busy: %d %q", code, body)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("api took %v while previews were busy", d)
	}
	// Closing a stream frees its slot.
	conns[0].Close()
	c, _, err := dial("abc123", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conns[0] = c
	for _, c := range conns {
		c.Close()
	}
}

func TestPreviewOfUnknownOrOfflineRunner(t *testing.T) {
	th := newTestHub(t)
	th.register(t, "work")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := th.DialPreview(ctx, "work", "abc123"); err == nil {
		t.Fatal("dialled an offline runner")
	}
	if _, _, err := th.DialPreview(ctx, "home", "abc123"); err == nil {
		t.Fatal("dialled an unknown runner")
	}
}
