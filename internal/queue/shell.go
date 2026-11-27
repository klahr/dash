package queue

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/klahr/dash/internal/localrun"
)

const tailLines = 200

// runShell runs command with bash in dir, in its own process group, streaming
// each output line to emit. env is its environment (nil: this process's,
// with CI=1). It returns the exit code and the last lines of output. err is
// only set when the command could not run or timed out.
func runShell(ctx context.Context, dir, command string, timeout time.Duration, env []string, emit func(string)) (exit int, tail string, err error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if err := localrun.Check("shell: " + command); err != nil {
		return -1, "", err
	}
	cmd := exec.CommandContext(ctx, localrun.Bash(), "-lc", command)
	cmd.Dir = dir
	cmd.Env = env
	if env == nil {
		cmd.Env = localrun.Environ("CI=1")
	}
	localrun.Group(cmd, syscall.SIGKILL, 5*time.Second)

	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return -1, "", err
	}

	var lines []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			l := sc.Text()
			emit(l)
			lines = append(lines, l)
			if len(lines) > tailLines {
				lines = lines[len(lines)-tailLines:]
			}
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	werr := cmd.Wait()
	localrun.Reap(cmd) // a server it started with & (and forgot) goes too
	pw.Close()
	<-done

	tail = strings.Join(lines, "\n")
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return -1, tail, errors.New("timed out after " + timeout.String())
		}
		return -1, tail, ctx.Err()
	}
	code, ok := localrun.ExitStatus(werr)
	if !ok {
		return code, tail, werr
	}
	return code, tail, nil
}
