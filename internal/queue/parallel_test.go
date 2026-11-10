package queue

import (
	"sync"
	"testing"
)

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
