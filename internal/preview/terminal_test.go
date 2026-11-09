package preview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/runner"
)

func terminalProject(command string) model.Project {
	return model.Project{ID: "p1", PreviewKind: model.PreviewTerminal, PreviewCommand: command}
}

// readUntil reads sess's output until it contains want.
func readUntil(t *testing.T, sess *runner.Session, want string) string {
	t.Helper()
	out := string(sess.Scrollback)
	timeout := time.After(10 * time.Second)
	for !strings.Contains(out, want) {
		select {
		case b, ok := <-sess.Output:
			if !ok {
				t.Fatalf("terminal ended before %q:\n%s", want, out)
			}
			out += string(b)
		case <-timeout:
			t.Fatalf("waiting for %q:\n%s", want, out)
		}
	}
	return out
}

func waitGone(t *testing.T, m *Manager, id, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := m.Get(id); !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTerminalPreview(t *testing.T) {
	m, procs, td := setup(t, "preview.dash.test")
	// No run_command needed; it goes live at once, with no port or URL.
	p, err := m.Start(td, terminalProject(`printf '#!/bin/sh\necho tool-ran\n' > "$DASH_BIN/tool"; chmod +x "$DASH_BIN/tool"; echo built; exec bash --norc --noprofile`))
	if err != nil {
		t.Fatal(err)
	}
	if p.State != model.PreviewLive || p.Kind != model.PreviewTerminal || p.Port != 0 || p.URL != "" || p.OpenURL != "" || p.LocalURL != "" ||
		p.TerminalURL != "/ws/todos/abc123/preview" || p.ProcessID == "" {
		t.Fatalf("unexpected start %+v", p)
	}
	if proc, err := procs.Get(p.ProcessID); err != nil || proc.Kind != model.ProcPTY || proc.TodoID != td.ID || proc.Dir != td.Worktree.Path {
		t.Fatalf("process %+v %v", proc, err)
	}
	// Starting again returns the same one.
	if again, err := m.Start(td, terminalProject("")); err != nil || again.ProcessID != p.ProcessID {
		t.Fatalf("second start: %+v %v", again, err)
	}
	// The build folder is kept out of git.
	if b, err := os.ReadFile(filepath.Join(td.Worktree.Path, BinDir, ".gitignore")); err != nil || string(b) != "*\n" {
		t.Fatalf(".dash-bin/.gitignore: %q %v", b, err)
	}

	sess, touch, err := m.Terminal(td.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Detach()
	readUntil(t, sess, "built")
	if err := sess.Write([]byte("tool; echo \"bin=$DASH_BIN\"; echo sum-$((40+2))\r")); err != nil {
		t.Fatal(err)
	}
	out := readUntil(t, sess, "sum-42")
	if !strings.Contains(out, "tool-ran") || !strings.Contains(out, "bin="+filepath.Join(td.Worktree.Path, BinDir)) {
		t.Fatalf("terminal output:\n%s", out)
	}
	if got := waitState(t, m, td.ID, model.PreviewLive); got.State != model.PreviewLive {
		t.Fatalf("state %+v", got)
	}

	// The scrollback, as plain lines for rework notes.
	log, err := m.Log(td.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(string(log), "\n"); len(lines) > 3 || !strings.Contains(string(log), "sum-42") || strings.Contains(string(log), "\x1b") {
		t.Fatalf("log: %q", log)
	}
	if full, _ := m.Log(td.ID, 0); !strings.Contains(string(full), "built") {
		t.Fatalf("full log: %q", full)
	}

	// Keystrokes count as activity; time without them is idle.
	m.IdleAfter = time.Minute
	e := m.entry(td.ID)
	e.seen.Store(time.Now().Add(-2 * time.Minute).UnixNano())
	touch()
	m.sweep(time.Now())
	if _, ok := m.Get(td.ID); !ok {
		t.Fatal("stopped a terminal someone is typing in")
	}

	// Approving the task stops it, and its shell.
	td.Status = model.StatusMerging
	m.Observe(model.Event{Type: model.EvTodoUpdated, TodoID: td.ID, Data: td})
	waitGone(t, m, td.ID, "terminal preview not stopped when the task was approved")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if proc, _ := procs.Get(p.ProcessID); !proc.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the preview's shell kept running")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTerminalPreviewIdle(t *testing.T) {
	m, _, td := setup(t, "")
	if _, err := m.Start(td, terminalProject("exec sleep 30")); err != nil {
		t.Fatal(err)
	}
	m.IdleAfter = time.Minute
	m.sweep(time.Now())
	if _, ok := m.Get(td.ID); !ok {
		t.Fatal("stopped a terminal preview that isn't idle")
	}
	m.sweep(time.Now().Add(2 * time.Minute))
	if _, ok := m.Get(td.ID); ok {
		t.Fatal("idle terminal preview kept running")
	}
}

func TestTerminalPreviewCrashKeepsLog(t *testing.T) {
	m, _, td := setup(t, "")
	if _, err := m.Start(td, terminalProject("echo 'main.go:3: undefined: foo' >&2; exit 2")); err != nil {
		t.Fatal(err)
	}
	c := waitState(t, m, td.ID, model.PreviewCrashed)
	if c.ExitCode == nil || *c.ExitCode != 2 {
		t.Fatalf("exit code: %+v", c)
	}
	log, err := m.Log(td.ID, 0)
	if err != nil || !strings.Contains(string(log), "undefined: foo") {
		t.Fatalf("log: %q %v", log, err)
	}
	// Starting again after a fix replaces the crashed one.
	p2, err := m.Start(td, terminalProject("exec sleep 30"))
	if err != nil || p2.ProcessID == c.ProcessID || p2.State != model.PreviewLive {
		t.Fatalf("restart: %+v %v", p2, err)
	}
}

func TestTerminalOnlyForTerminalPreviews(t *testing.T) {
	m, _, td := setup(t, "")
	if _, _, err := m.Terminal(td.ID); err != ErrNotFound {
		t.Fatalf("no preview: %v", err)
	}
	if _, err := m.Start(td, model.Project{ID: "p1", RunCommand: "sleep 30"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Terminal(td.ID); err != ErrNotTerminal {
		t.Fatalf("web preview: %v", err)
	}
	if _, _, err := m.DialPreview(td.ID); err == nil {
		t.Fatal("dialled a web preview that isn't live yet")
	}
}

func TestPlainText(t *testing.T) {
	for in, want := range map[string]string{
		"\x1b[1;32mok\x1b[0m\r\n":                "ok\n",
		"\x1b]0;title\x07$ ls\r\n":               "$ ls\n",
		"10%\r50%\r100%\n":                       "100%\n",
		"abx\b \bc\r\n":                          "abc\n",
		"\x1b[?2004hprompt$ \x1b[?2004l\r\ndone": "prompt$ \ndone",
		"\x1b(Bplain":                            "plain",
	} {
		if got := string(PlainText([]byte(in))); got != want {
			t.Errorf("PlainText(%q) = %q, want %q", in, got, want)
		}
	}
	if got := string(LastLines([]byte("a\nb\nc\n\n"), 2)); got != "b\nc" {
		t.Errorf("LastLines: %q", got)
	}
}
