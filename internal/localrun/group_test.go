//go:build !windows

package localrun

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A timed-out command's grandchild, holding the output pipe, is killed with
// the group, so Run returns promptly instead of waiting for it.
func TestGroupKillsGrandchild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 30 & wait")
	Group(cmd, syscall.SIGKILL, 10*time.Second)
	var out bytes.Buffer
	cmd.Stdout = &out
	start := time.Now()
	if err := cmd.Run(); err == nil {
		t.Fatal("Run succeeded; want it killed by the timeout")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Run took %v; the grandchild wasn't killed", d)
	}
}

// A command that exits by itself takes what it left in the background with
// it once Reap runs: the sleep it started is gone, not re-parented to init.
func TestReapEndsLeftovers(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "sleep 30 >/dev/null 2>&1 & echo $!")
	Group(cmd, syscall.SIGKILL, 5*time.Second)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("pid %q: %v", out, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the background sleep isn't running before Reap: %v", err)
	}
	Reap(cmd)
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatal("the background sleep still runs after Reap")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
