package queue

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/klahr/dash/internal/localrun"
)

const tailLines = 200

// runShell runs command with bash in dir, in its own process group, streaming
// each output line to emit. It returns the exit code and the last lines of
// output. err is only set when the command could not run or timed out.
func runShell(ctx context.Context, dir, command string, timeout time.Duration, env []string, emit func(string)) (exit int, tail string, err error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if err := localrun.Check("shell: " + command); err != nil {
		return -1, "", err
	}
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "CI=1"), env...)
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
	pw.Close()
	<-done

	tail = strings.Join(lines, "\n")
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return -1, tail, errors.New("timed out after " + timeout.String())
		}
		return -1, tail, ctx.Err()
	}
	var ee *exec.ExitError
	if errors.As(werr, &ee) {
		return ee.ExitCode(), tail, nil
	}
	if werr != nil {
		return -1, tail, werr
	}
	return 0, tail, nil
}
