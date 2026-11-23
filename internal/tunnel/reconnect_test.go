package tunnel

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReconnectStopsOnFatal(t *testing.T) {
	final := errors.New("final")
	calls := 0
	dial := func(context.Context) error {
		calls++
		if calls == 3 {
			return final
		}
		return errors.New("dropped")
	}
	err := reconnect(context.Background(), "test", time.Millisecond, 2*time.Millisecond, dial, func(err error) bool { return err == final })
	if err != final || calls != 3 {
		t.Fatalf("err = %v after %d calls, want %v after 3", err, calls, final)
	}
}

func TestReconnectStopsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	dial := func(context.Context) error {
		calls++
		if calls == 2 {
			cancel()
		}
		return errors.New("dropped")
	}
	if err := reconnect(ctx, "test", time.Millisecond, time.Millisecond, dial, nil); !errors.Is(err, context.Canceled) || calls != 2 {
		t.Fatalf("err = %v after %d calls, want context.Canceled after 2", err, calls)
	}

	// Cancelled while waiting to retry.
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := reconnect(ctx, "test", time.Hour, time.Hour, func(context.Context) error { return errors.New("dropped") }, nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %v, want deadline exceeded soon", err, time.Since(start))
	}
}
