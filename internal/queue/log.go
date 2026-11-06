package queue

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

// TodoLog is a todo's execution log: its events, appended to a JSON-lines
// file and streamed to dashboards as todo.event events, and a text log of
// their summaries, appended to a file and streamed as todo.output events.
type TodoLog struct {
	Pub  Publisher
	Root string // <data>/logs
	mu   sync.Mutex
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
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(l.Root, 0o755); err != nil {
		return
	}
	appendTo(l.eventsPath(t.ID), string(b)+"\n")
	if line != "" {
		appendTo(l.path(t.ID), fmt.Sprintf("%s %s\n", ev.At.Format(time.TimeOnly), line))
	}
}

func appendTo(path, s string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(s)
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
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = os.Remove(l.path(id))
	_ = os.Remove(l.eventsPath(id))
}

func (l *TodoLog) path(id string) string       { return filepath.Join(l.Root, id+".log") }
func (l *TodoLog) eventsPath(id string) string { return filepath.Join(l.Root, id+".events.jsonl") }
