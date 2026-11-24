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

// TestReconnectBackoff checks that the wait doubles from minB up to maxB with
// jitter in [b/2, b], and starts over from minB after a connection that
// stayed up longer than stableUptime.
func TestReconnectBackoff(t *testing.T) {
	const minB, maxB = time.Second, 5 * time.Second
	clock := time.Unix(0, 0)
	now := func() time.Time { return clock }
	var waits []time.Duration
	after := func(d time.Duration) <-chan time.Time {
		waits = append(waits, d)
		clock = clock.Add(d)
		ch := make(chan time.Time, 1)
		ch <- clock
		return ch
	}

	final := errors.New("final")
	calls := 0
	dial := func(context.Context) error {
		calls++
		switch calls {
		case 5: // stays up past stableUptime, then drops
			clock = clock.Add(stableUptime + time.Second)
		case 7:
			return final
		}
		return errors.New("dropped")
	}
	err := reconnectClock(context.Background(), "test", minB, maxB, dial, func(err error) bool { return err == final }, now, after)
	if err != final {
		t.Fatalf("err = %v, want %v", err, final)
	}
	// Ceilings for the waits after dials 1..6: doubling, capped at maxB,
	// back to minB after the long-lived dial 5, then doubling again.
	want := []time.Duration{minB, 2 * minB, 4 * minB, maxB, minB, 2 * minB}
	if len(waits) != len(want) {
		t.Fatalf("waits = %v, want %d of them", waits, len(want))
	}
	for i, b := range want {
		if waits[i] < b/2 || waits[i] > b {
			t.Errorf("wait %d = %v, want in [%v, %v]", i+1, waits[i], b/2, b)
		}
	}
}
