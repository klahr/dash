package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// The end-to-end tests (hub*_test.go, browser_test.go) run dash's real
// binaries. They build them once per package run, into a directory TestMain
// removes, and run in parallel: each has its own temporary directories and
// free ports. `go test -short` skips them.

func TestMain(m *testing.M) {
	code := m.Run()
	if bins.dir != "" {
		_ = os.RemoveAll(bins.dir)
	}
	os.Exit(code)
}

var bins struct {
	once       sync.Once
	dir        string
	dash, fake string
	err        error
}

// endToEnd marks an end-to-end test: skipped with -short, and run in
// parallel with the other end-to-end tests.
func endToEnd(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end: builds and runs dash (skipped with -short)")
	}
	t.Parallel()
}

// buildDash returns dash and testdata/fakeclaude, built on the first call.
// The fake claude is alone in its directory, named claude, for a PATH.
func buildDash(t *testing.T) (dash, fakeClaude string) {
	t.Helper()
	bins.once.Do(func() {
		if bins.dir, bins.err = os.MkdirTemp("", "dash-e2e-"); bins.err != nil {
			return
		}
		bins.dash = filepath.Join(bins.dir, "dash")
		bins.fake = filepath.Join(bins.dir, "fakebin", "claude")
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, b := range [][2]string{{bins.dash, "."}, {bins.fake, "./testdata/fakeclaude"}} {
			wg.Go(func() {
				if out, err := exec.Command("go", "build", "-o", b[0], b[1]).CombinedOutput(); err != nil {
					errs[i] = fmt.Errorf("go build %s: %v\n%s", b[1], err, out)
				}
			})
		}
		wg.Wait()
		bins.err = errors.Join(errs...)
	})
	if bins.err != nil {
		t.Fatal(bins.err)
	}
	return bins.dash, bins.fake
}
