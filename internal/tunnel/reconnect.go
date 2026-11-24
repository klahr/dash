package tunnel

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"
)

// stableUptime is how long a connection must have lasted for its drop to
// count as a fresh outage, which starts the backoff from minB again.
const stableUptime = 30 * time.Second

// reconnect calls dial, which serves one connection until it drops, again and
// again until ctx ends or fatal(err) says the error is final. Between calls
// it waits with exponential backoff from minB up to maxB, with jitter, so
// runners don't all knock at once after a hub restart. what names the
// connection in the log. It returns ctx.Err() or the fatal error.
func reconnect(ctx context.Context, what string, minB, maxB time.Duration, dial func(context.Context) error, fatal func(error) bool) error {
	return reconnectClock(ctx, what, minB, maxB, dial, fatal, time.Now, time.After)
}

// reconnectClock is reconnect with its clock passed in: now reads the time
// and after waits, as time.Now and time.After do. Tests use it to record the
// waits and fake how long a connection stayed up.
func reconnectClock(ctx context.Context, what string, minB, maxB time.Duration, dial func(context.Context) error, fatal func(error) bool,
	now func() time.Time, after func(time.Duration) <-chan time.Time) error {
	backoff := minB
	for {
		start := now()
		err := dial(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if fatal != nil && fatal(err) {
			return err
		}
		if now().Sub(start) > stableUptime {
			backoff = minB // it was up for a while: this is a fresh outage
		}
		wait := backoff/2 + rand.N(backoff/2+1)
		slog.Warn("runner: "+what+" connection lost, retrying", "err", err, "in", wait.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-after(wait):
		}
		backoff = min(backoff*2, maxB)
	}
}
