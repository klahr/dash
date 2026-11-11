package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/model"
)

func TestShellCommand(t *testing.T) {
	for in, want := range map[string]string{"!ls -la": "ls -la", "  ! git status ": "git status", "!": "", "! ": "", "ls": "", "hi !x": ""} {
		got, ok := ShellCommand(in)
		if got != want && ok || ok != (want != "") {
			t.Errorf("ShellCommand(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

// collect gathers what RunShell streams.
type collect struct {
	mu  sync.Mutex
	out strings.Builder
}

func (c *collect) emit(d ChatDelta) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d.Kind == "shell_output" {
		c.out.WriteString(d.Text)
	}
}

func (c *collect) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.String()
}

// A command runs in the given directory with stdout and stderr together,
// streamed as it comes and kept in the result with its exit code.
func TestRunShellDirOutputAndExitCode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var c collect
	res := RunShell(context.Background(), dir, "pwd; ls; echo oops >&2; exit 3", time.Minute, c.emit)
	if res.ExitCode != 3 || res.Error != "" {
		t.Fatalf("exit %d, error %q", res.ExitCode, res.Error)
	}
	real, _ := filepath.EvalSymlinks(dir)
	for _, want := range []string{real, "marker.txt", "oops"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("output %q lacks %q", res.Output, want)
		}
	}
	if c.String() != res.Output {
		t.Errorf("streamed %q, kept %q", c.String(), res.Output)
	}
	if res.Dir != dir || res.Command == "" {
		t.Errorf("result %+v", res)
	}

	if res := RunShell(context.Background(), dir, "true", time.Minute, nil); res.ExitCode != 0 || res.Error != "" || res.Output != "" {
		t.Errorf("true: %+v", res)
	}
	if res := RunShell(context.Background(), filepath.Join(dir, "gone"), "true", time.Minute, nil); res.ExitCode != -1 || res.Error == "" {
		t.Errorf("missing dir: %+v", res)
	}
	// No terminal and no input: what reads stdin gets nothing.
	if res := RunShell(context.Background(), dir, "test -t 0 || echo notty; cat", time.Minute, nil); res.ExitCode != 0 || strings.TrimSpace(res.Output) != "notty" {
		t.Errorf("stdin: %+v", res)
	}
}

// alive reports whether the process pid (read from file) still runs.
func alive(t *testing.T, file string) (int, bool) {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return pid, syscall.Kill(pid, 0) == nil
}

func waitDead(t *testing.T, file string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		pid, ok := alive(t, file)
		if !ok {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("child %d still runs", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A command past its timeout is stopped, with what it started, and keeps
// what it printed until then.
func TestRunShellTimeoutKillsChildren(t *testing.T) {
	dir := t.TempDir()
	pidf := filepath.Join(dir, "child.pid")
	start := time.Now()
	res := RunShell(context.Background(), dir, "sleep 60 & echo $! > child.pid; echo started; wait", 300*time.Millisecond, nil)
	if time.Since(start) > 10*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	if res.ExitCode != -1 || !strings.Contains(res.Error, "timed out") || !strings.Contains(res.Output, "started") {
		t.Fatalf("timed out: %+v", res)
	}
	waitDead(t, pidf)
}

// Ending the context (Stop) stops the command and what it started.
func TestRunShellStopKillsChildren(t *testing.T) {
	dir := t.TempDir()
	pidf := filepath.Join(dir, "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	var c collect
	done := make(chan model.ChatShell)
	go func() {
		done <- RunShell(ctx, dir, "sleep 60 & echo $! > child.pid; echo started; wait", time.Minute, c.emit)
	}()
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(c.String(), "started"); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("never started")
		}
	}
	cancel()
	select {
	case res := <-done:
		if res.Error != "stopped" || res.ExitCode != -1 || !strings.Contains(res.Output, "started") {
			t.Fatalf("stopped: %+v", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("not stopped")
	}
	waitDead(t, pidf)
}

// The hub runs no commands itself.
func TestRunShellRefusedOnTheHub(t *testing.T) {
	localrun.Set(func() bool { return false })
	defer localrun.Set(nil)
	dir := t.TempDir()
	res := RunShell(context.Background(), dir, "touch ran", time.Minute, nil)
	if res.Error == "" || res.ExitCode != -1 {
		t.Fatalf("ran on the hub: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); err == nil {
		t.Fatal("the command ran")
	}
}

// Long output keeps its start and end, says what was left out, and stays
// within the cap whether it is cut while streaming or after.
func TestShellOutputCap(t *testing.T) {
	var all strings.Builder
	for i := range 20_000 {
		all.WriteString("line " + strconv.Itoa(i) + " é\n")
	}
	s := all.String()
	o := &shellOutput{}
	for i := 0; i < len(s); i += 1000 {
		_, _ = o.Write([]byte(s[i:min(i+1000, len(s))]))
	}
	got, cut := o.result()
	want, wcut := CapShellOutput(s)
	if !cut || !wcut || got != want {
		t.Fatalf("streamed cap differs from CapShellOutput (cut %v %v)", cut, wcut)
	}
	if len(got) > MaxShellOutput+200 {
		t.Errorf("kept %d bytes", len(got))
	}
	if !strings.HasPrefix(got, "line 0 é\n") || !strings.HasSuffix(got, "line 19999 é\n") || !strings.Contains(got, "of output left out") {
		t.Errorf("kept %q…%q", got[:40], got[len(got)-40:])
	}
	if !strings.Contains(got, "lines) of output left out") || !isUTF8(got) {
		t.Error("cut inside a character or without the note")
	}
	if short, cut := CapShellOutput("hello\n"); cut || short != "hello\n" {
		t.Errorf("short output changed: %q", short)
	}
}

func isUTF8(s string) bool { return strings.ToValidUTF8(s, "\uFFFD") == s }

// Claude reads a "!" command as one the user ran, with its output and how
// it ended, wherever it is in the conversation.
func TestTranscriptShowsShellCommands(t *testing.T) {
	history := []ChatMessage{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
		{Role: "user", Content: "!go test ./...", Shell: &model.ChatShell{Command: "go test ./...", Dir: "/src/web", Output: "FAIL x_test.go:3\n", ExitCode: 1}},
		{Role: "user", Content: "!sleep 9", Shell: &model.ChatShell{Command: "sleep 9", ExitCode: -1, Error: "stopped"}},
		{Role: "user", Content: "why did it fail?"},
	}
	got := transcript(history)
	for _, want := range []string{
		"I ran this shell command myself", "<command>go test ./...</command>", "in /src/web",
		"FAIL x_test.go:3", "<exit_code>1</exit_code>", "<command>sleep 9</command>", "<status>stopped</status>",
		"Reply to my latest message:\n\nwhy did it fail?",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "!go test") {
		t.Error("the raw !command was sent instead of the command block")
	}

	// A long output is capped when replayed, even if it was saved whole.
	long := strings.Repeat("x", 3*MaxShellOutput)
	text := ShellText(ChatMessage{Role: "user", Shell: &model.ChatShell{Command: "cat big", Output: long}})
	if len(text) > MaxShellOutput+500 || !strings.Contains(text, `truncated="true"`) {
		t.Errorf("replayed %d bytes", len(text))
	}

	// WithShellText makes them plain text, so context added to a message
	// reaches Claude.
	plain := WithShellText(history)
	if plain[2].Shell != nil || !strings.Contains(plain[2].Content, "<user_shell_command>") || history[2].Shell == nil {
		t.Errorf("WithShellText: %+v", plain[2])
	}
}

func TestReadOnlyChat(t *testing.T) {
	if !ReadOnlyChat(ClaudeCodeChat{Tools: ChatReadOnly}) || !ReadOnlyChat(ClaudeCodeChat{}) || !ReadOnlyChat(&Chat{}) || !ReadOnlyChat(nil) {
		t.Error("a read-only chat runs commands")
	}
	if ReadOnlyChat(ClaudeCodeChat{Tools: ChatShell}) {
		t.Error("a shell chat is read-only")
	}
}
