package queue

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/disk"
	"github.com/klahr/dash/internal/model"
)

// TodoLog is a todo's execution log: its events, appended to a JSON-lines
// file and streamed to dashboards as todo.event events, and a text log of
// their summaries, appended to a file and streamed as todo.output events.
type TodoLog struct {
	Pub  Publisher
	Root string // <data>/logs
	// MaxBytes caps each of a todo's two log files (0 = no cap). A file
	// that grows past it is cut to its newest half, behind a note saying
	// earlier output was trimmed, so a long or chatty run can't fill the disk.
	MaxBytes int64

	mu    sync.Mutex
	files map[string]*todoLogFiles // by todo ID
	dir   atomic.Bool              // Root has been made
}

// todoLogFiles serialises the writes to one todo's two log files, so that
// todos running side by side don't wait on each other, and keeps the files'
// sizes so the cap needs no Stat per event.
type todoLogFiles struct {
	mu           sync.Mutex
	sized        bool // events and text hold the sizes
	events, text int64
}

func (l *TodoLog) filesOf(id string) *todoLogFiles {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.files[id]
	if f == nil {
		if l.files == nil {
			l.files = map[string]*todoLogFiles{}
		}
		f = &todoLogFiles{}
		l.files[id] = f
	}
	return f
}

// Line logs one of Dash's own progress notes.
func (l *TodoLog) Line(t model.Todo, line string) {
	l.Event(t, model.RunEvent{Kind: "status", Text: line})
}

// Event logs one event of the todo's worker, and its summary in the text log.
func (l *TodoLog) Event(t model.Todo, ev model.RunEvent) {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	l.Pub.Publish(model.Event{Type: model.EvTodoEvent, ProjectID: t.ProjectID, TodoID: t.ID, Data: ev, Time: ev.At})
	line := agent.Summary(ev)
	if line != "" {
		l.Pub.Publish(model.Event{Type: model.EvTodoOutput, ProjectID: t.ProjectID, TodoID: t.ID, Data: line, Time: ev.At})
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	f := l.filesOf(t.ID)
	f.mu.Lock()
	defer f.mu.Unlock()
	if l.MaxBytes > 0 && !f.sized {
		f.events, f.text, f.sized = fileSize(l.eventsPath(t.ID)), fileSize(l.path(t.ID)), true
	}
	f.events += l.appendTo(l.eventsPath(t.ID), string(b)+"\n")
	if line != "" {
		f.text += l.appendTo(l.path(t.ID), fmt.Sprintf("%s %s\n", ev.At.Format(time.TimeOnly), line))
	}
	if l.MaxBytes > 0 && f.events > l.MaxBytes {
		f.events = capFile(l.eventsPath(t.ID), l.MaxBytes, func(first []byte) string {
			var next model.RunEvent
			at := ev.At
			if json.Unmarshal(first, &next) == nil && !next.At.IsZero() {
				at = next.At
			}
			b, _ := json.Marshal(model.RunEvent{Kind: "status", Text: trimmedNote(l.MaxBytes), At: at})
			return string(b) + "\n"
		})
	}
	if l.MaxBytes > 0 && f.text > l.MaxBytes {
		f.text = capFile(l.path(t.ID), l.MaxBytes, func([]byte) string {
			return fmt.Sprintf("%s %s\n", ev.At.Format(time.TimeOnly), trimmedNote(l.MaxBytes))
		})
	}
}

func trimmedNote(max int64) string {
	return fmt.Sprintf("✂ earlier output trimmed: this log keeps about its newest %s", disk.Size(uint64(max/2)))
}

// capFile cuts a file larger than max to its newest max/2 bytes, from the
// start of a line, behind the line note returns (given the first line
// kept). The newer half is written to a new file renamed over the old, so a
// reader sees either whole file. It returns the file's size after.
func capFile(path string, max int64, note func(first []byte) string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	size := info.Size()
	if size <= max {
		return size
	}
	f, err := os.Open(path)
	if err != nil {
		return size
	}
	defer f.Close()
	keep := max / 2
	tail := make([]byte, keep)
	n, err := f.ReadAt(tail, info.Size()-keep)
	if err != nil && n < len(tail) {
		return size
	}
	// Drop the line the cut lands in, unless it lands right after a newline.
	prev := make([]byte, 1)
	if _, err := f.ReadAt(prev, info.Size()-keep-1); err != nil || prev[0] != '\n' {
		i := bytes.IndexByte(tail, '\n')
		if i < 0 {
			tail = nil // one line longer than keep: nothing whole to keep
		} else {
			tail = tail[i+1:]
		}
	}
	first, _, _ := bytes.Cut(tail, []byte("\n"))
	tmp := path + ".trim"
	out, err := os.Create(tmp)
	if err != nil {
		return size
	}
	head := note(first)
	_, werr := out.WriteString(head)
	if werr == nil {
		_, werr = out.Write(tail)
	}
	if cerr := out.Close(); werr != nil || cerr != nil || os.Rename(tmp, path) != nil {
		_ = os.Remove(tmp)
		return size
	}
	return int64(len(head) + len(tail))
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// appendTo appends s to the file at path, making Root first if it isn't
// there, and returns the bytes written.
func (l *TodoLog) appendTo(path, s string) int64 {
	if !l.dir.Load() {
		if os.MkdirAll(l.Root, 0o755) != nil {
			return 0
		}
		l.dir.Store(true)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if errors.Is(err, os.ErrNotExist) && os.MkdirAll(l.Root, 0o755) == nil { // Root removed since
		f, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	}
	if err != nil {
		return 0
	}
	defer f.Close()
	n, _ := f.WriteString(s)
	return int64(n)
}

// Events reads a todo's events, oldest first. Todos logged before events
// were kept have none.
func (l *TodoLog) Events(id string) ([]model.RunEvent, error) {
	f, err := os.Open(l.eventsPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return []model.RunEvent{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	evs := []model.RunEvent{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for sc.Scan() {
		var ev model.RunEvent
		if json.Unmarshal(sc.Bytes(), &ev) == nil { // skip a line cut short by a crash
			evs = append(evs, ev)
		}
	}
	return evs, sc.Err()
}

func (l *TodoLog) Read(id string) (string, error) {
	b, err := os.ReadFile(l.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// Remove deletes a todo's log.
func (l *TodoLog) Remove(id string) {
	f := l.filesOf(id)
	f.mu.Lock()
	defer f.mu.Unlock()
	l.mu.Lock()
	delete(l.files, id)
	l.mu.Unlock()
	_ = os.Remove(l.path(id))
	_ = os.Remove(l.eventsPath(id))
}

func (l *TodoLog) path(id string) string       { return filepath.Join(l.Root, id+".log") }
func (l *TodoLog) eventsPath(id string) string { return filepath.Join(l.Root, id+".events.jsonl") }
