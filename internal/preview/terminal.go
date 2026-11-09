package preview

// Terminal previews: for CLI projects, which never open a port, a task in
// review is previewed in a terminal in its worktree. The project's
// preview_command runs there in a PTY (or a plain shell when it is empty),
// with ./.dash-bin on PATH and $DASH_BIN pointing at it, so a command like
// "go build -o $DASH_BIN/tool . && exec $SHELL" leaves the reviewer at a
// prompt where the task's build is the one they run.
//
// The UI attaches over a WebSocket (api's /ws/todos/{tid}/preview, procWS's
// protocol; through /peer/{runner}/… for a runner's tasks). Keystrokes count
// as activity for the idle timeout; output alone doesn't.

import (
	"bytes"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/runner"
)

// BinDir is the folder in a terminal preview's worktree that is put on PATH
// (and exported as $DASH_BIN) for the builds preview_command makes.
const BinDir = ".dash-bin"

// ErrNotTerminal answers an attach to a preview that isn't a terminal.
var ErrNotTerminal = errors.New("this task's preview is not a terminal")

// TerminalPath is the WebSocket path that attaches to todoID's terminal preview.
func TerminalPath(todoID string) string { return "/ws/todos/" + url.PathEscape(todoID) + "/preview" }

func (m *Manager) startTerminal(t model.Todo, p model.Project) (model.Process, error) {
	bin := filepath.Join(t.Worktree.Path, BinDir)
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return model.Process{}, err
	}
	// Ignored by git without touching the repo's .gitignore: builds there
	// never end up in the task's commits.
	_ = os.WriteFile(filepath.Join(bin, ".gitignore"), []byte("*\n"), 0o644)
	command := strings.TrimSpace(p.PreviewCommand)
	shown := command
	if command == "" {
		command, shown = `exec "${SHELL:-bash}"`, "$SHELL"
	}
	// A login shell may set PATH afresh from the profile: put the folder
	// in front of whatever that leaves.
	script := `export PATH="$DASH_BIN:$PATH"` + "\n" + command
	return m.Procs.StartPTYFor(p.ID, t.ID, t.Worktree.Path, shown, script, []string{"DASH_BIN=" + bin}, 0, 0)
}

// Terminal attaches to todoID's terminal preview. touch marks activity
// (call it on every keystroke) and keeps the preview from going idle.
func (m *Manager) Terminal(todoID string) (sess *runner.Session, touch func(), err error) {
	e := m.entry(todoID)
	if e == nil {
		return nil, nil, ErrNotFound
	}
	info := e.snapshot()
	if info.Kind != model.PreviewTerminal {
		return nil, nil, ErrNotTerminal
	}
	sess, err = m.Procs.Attach(info.ProcessID)
	if err != nil {
		return nil, nil, err
	}
	e.touch()
	return sess, e.touch, nil
}

// DefaultLogLines is how many lines of a terminal preview Log returns by default.
const DefaultLogLines = 200

// Log returns the end of todoID's preview output: for a web preview its
// server's last 16 KiB as written; for a terminal, its last lines (at most
// lines; 0 for DefaultLogLines) as plain text, without the terminal's escape
// sequences, e.g. to send back to Claude as rework notes.
func (m *Manager) Log(todoID string, lines int) ([]byte, error) {
	e := m.entry(todoID)
	if e == nil {
		return nil, ErrNotFound
	}
	info := e.snapshot()
	if info.Kind != model.PreviewTerminal {
		return m.Procs.Tail(info.ProcessID, 16*1024)
	}
	b, err := m.Procs.Tail(info.ProcessID, 1<<20)
	if err != nil {
		return nil, err
	}
	if lines <= 0 {
		lines = DefaultLogLines
	}
	return LastLines(PlainText(b), lines), nil
}

// LastLines is the last n lines of b (fewer if it has fewer), without
// trailing blank lines.
func LastLines(b []byte, n int) []byte {
	ls := bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n"))
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return bytes.Join(ls, []byte("\n"))
}

// PlainText turns a terminal's output into what it showed, roughly: escape
// sequences (colours, cursor moves, titles) are dropped, a carriage return
// starts its line over (progress bars keep their last state), backspace
// deletes, and CRLF is a newline.
func PlainText(b []byte) []byte {
	var out, line []byte
	col := 0 // where the next character goes on line (after a \r, overwrite)
	put := func(c byte) {
		if col < len(line) {
			line[col] = c
		} else {
			line = append(line, c)
		}
		col++
	}
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case c == 0x1b && i+1 < len(b):
			i++
			switch b[i] {
			case '[': // CSI: parameters, then a final byte in @–~
				for i+1 < len(b) && !(b[i+1] >= 0x40 && b[i+1] <= 0x7e) {
					i++
				}
				i++
			case ']', 'P', '_', '^': // OSC/DCS/APC/PM: up to BEL or ST
				for i+1 < len(b) {
					i++
					if b[i] == 0x07 {
						break
					}
					if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\' {
						i++
						break
					}
				}
			case '(', ')', '*', '+': // charset designation: one more byte
				i++
			}
		case c == '\n':
			out = append(append(out, line...), '\n')
			line, col = line[:0], 0
		case c == '\r':
			col = 0
		case c == '\b':
			if col > 0 {
				col--
			}
		case c == '\t' || c >= 0x20 && c != 0x7f:
			put(c)
		}
	}
	return append(out, line...)
}
