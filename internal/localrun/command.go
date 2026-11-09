package localrun

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// The tools Dash runs through Command. Every git and gh process in Dash is
// started by Command (a test in this package fails on any other
// exec.Command of them), so the hub's policy covers them even when a code
// path forgets to forward to a runner.
const (
	Git = "git"
	Gh  = "gh"
)

// Command returns the command running bin (tool's binary: "git", or a
// configured path to it) with args, or, on the hub, an error naming tool
// ("this hub doesn't run git: …", wrapping ErrHub) without starting
// anything. The refusal is logged with Refused.
func Command(ctx context.Context, tool, bin string, args ...string) (*exec.Cmd, error) {
	if !Allowed() {
		what := strings.TrimSpace(tool + " " + strings.Join(args, " "))
		slog.Error(Refused, "what", what, "tool", tool)
		return nil, fmt.Errorf("this hub doesn't run %s (%s): %w", tool, what, ErrHub)
	}
	return exec.CommandContext(ctx, bin, args...), nil
}
