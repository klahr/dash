package queue

import (
	"sync"
	"testing"
	"time"
)

// waitTimeout bounds a test's wait for background work (a merge job, a
// fake gh call). Alone that work takes well under a second, but under a
// full `go test ./...` on a busy machine (macOS especially) it has taken
// over 10 seconds, so the bound is generous: a passing test never waits
// for it.
const waitTimeout = 60 * time.Second

// parallelTests holds the tests that already called t.Parallel, which
// panics when called twice.
var parallelTests sync.Map // *testing.T → struct{}

// parallel runs t in parallel with the other tests, once: both a test and
// newHarness call it.
func parallel(t *testing.T) {
	if _, set := parallelTests.LoadOrStore(t, struct{}{}); set {
		return
	}
	t.Cleanup(func() { parallelTests.Delete(t) })
	t.Parallel()
}
