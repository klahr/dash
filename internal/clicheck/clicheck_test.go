package clicheck

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func read(b *Base) (string, []string) {
	var v string
	var ws []string
	b.Read(func(version string, warnings []string) { v, ws = version, warnings })
	return v, ws
}

func TestSetForgetsRunWarningsOnNewVersion(t *testing.T) {
	var b Base
	calls := 0
	b.OnChange(func() { calls++ })

	b.Set("1.0.0", []string{"probe"}, nil)
	b.Observed([]string{"x"})
	if _, ws := read(&b); !slices.Equal(ws, []string{"probe", "x"}) {
		t.Fatalf("warnings = %q, want [probe x]", ws)
	}

	// Same version: the runs' warnings stay, and nothing changed.
	before := calls
	b.Set("1.0.0", []string{"probe"}, nil)
	if _, ws := read(&b); !slices.Equal(ws, []string{"probe", "x"}) {
		t.Fatalf("after same-version Set, warnings = %q, want [probe x]", ws)
	}
	if calls != before {
		t.Errorf("OnChange called on an unchanged Set")
	}

	b.Set("2.0.0", nil, nil)
	v, ws := read(&b)
	if v != "2.0.0" || len(ws) != 0 {
		t.Fatalf("after new version, got %q %q, want 2.0.0 and no warnings", v, ws)
	}
	if calls != before+1 {
		t.Errorf("OnChange calls = %d, want %d", calls, before+1)
	}
}

func TestSetUpdateReportsChange(t *testing.T) {
	var b Base
	calls := 0
	b.OnChange(func() { calls++ })
	b.Set("1.0.0", nil, nil)
	b.Set("1.0.0", nil, func() bool { return true })
	if calls != 2 {
		t.Errorf("OnChange calls = %d, want 2", calls)
	}
}

func TestObservedCapsAndDedupes(t *testing.T) {
	var b Base
	calls := 0
	b.OnChange(func() { calls++ })
	b.Set("1.0.0", []string{"probe"}, nil)
	calls = 0

	b.Observed([]string{"probe", "a", "a"})
	if _, ws := read(&b); !slices.Equal(ws, []string{"probe", "a"}) {
		t.Fatalf("warnings = %q, want [probe a]", ws)
	}
	b.Observed([]string{"a"})
	b.Observed(nil)
	if calls != 1 {
		t.Errorf("OnChange calls = %d, want 1 (only for the new warning)", calls)
	}

	var many []string
	for i := range 10 {
		many = append(many, fmt.Sprintf("w%d", i))
	}
	b.Observed(many)
	_, ws := read(&b)
	if got := len(ws) - 1; got != MaxWarnings { // less the probe's warning
		t.Fatalf("kept %d run warnings, want %d: %q", got, MaxWarnings, ws)
	}
}

func TestLoopCallsCheckAndSideUntilCancel(t *testing.T) {
	var checks, sides atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Loop(ctx, 50*time.Millisecond, 10*time.Millisecond,
			func(context.Context) { checks.Add(1) },
			func(context.Context) { sides.Add(1) })
		close(done)
	}()
	time.Sleep(120 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Loop didn't return after cancel")
	}
	if c := checks.Load(); c < 2 {
		t.Errorf("check ran %d times, want at least 2", c)
	}
	if s := sides.Load(); s < 3 {
		t.Errorf("side ran %d times, want at least 3", s)
	}
}

func TestLoopUnsetSideEvery(t *testing.T) {
	for _, side := range []time.Duration{0, -time.Second} {
		ctx, cancel := context.WithCancel(context.Background())
		var checks atomic.Int32
		done := make(chan struct{})
		go func() {
			defer close(done)
			Loop(ctx, 20*time.Millisecond, side,
				func(context.Context) { checks.Add(1) },
				func(context.Context) {})
		}()
		time.Sleep(50 * time.Millisecond)
		cancel()
		<-done
		if checks.Load() < 2 {
			t.Errorf("sideEvery %v: check ran %d times, want at least 2", side, checks.Load())
		}
	}
}
