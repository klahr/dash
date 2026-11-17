package main

import (
	"errors"
	"testing"
	"time"

	"github.com/klahr/dash/internal/runnerctl"
)

func TestNextWait(t *testing.T) {
	cases := []struct {
		name string
		prev time.Duration
		code int
		ran  time.Duration
		want time.Duration
	}{
		{"updated: at once", time.Minute, runnerctl.ExitUpdated, time.Second, 0},
		{"first stop", 0, 1, time.Second, superviseFirst},
		{"keeps stopping: doubles", superviseFirst, 1, time.Second, 2 * superviseFirst},
		{"capped", 4 * time.Minute, 1, time.Second, superviseMax},
		{"at the cap", superviseMax, -1, time.Second, superviseMax},
		{"ran well: starts over", superviseMax, 1, superviseSteady, superviseFirst},
		{"clean exit waits too", 0, 0, time.Second, superviseFirst},
	}
	for _, c := range cases {
		if got := nextWait(c.prev, c.code, c.ran); got != c.want {
			t.Errorf("%s: nextWait(%v, %d, %v) = %v, want %v", c.name, c.prev, c.code, c.ran, got, c.want)
		}
	}
}

func TestExitCode(t *testing.T) {
	if got := exitCode(nil); got != 0 {
		t.Errorf("exitCode(nil) = %d, want 0", got)
	}
	if got := exitCode(errors.New("no such file")); got != -1 {
		t.Errorf("exitCode(not started) = %d, want -1", got)
	}
}
