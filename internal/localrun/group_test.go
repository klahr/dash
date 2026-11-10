package localrun

import (
	"bytes"
	"context"
	"os/exec"
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
