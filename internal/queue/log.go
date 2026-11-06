package queue

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/klahr/dash/internal/model"
)

// TodoLog is a todo's execution log: appended to a file and streamed to
// dashboards as todo.output events.
type TodoLog struct {
	Pub  Publisher
	Root string // <data>/logs
	mu   sync.Mutex
}

func (l *TodoLog) Line(t model.Todo, line string) {
	l.Pub.Publish(model.Event{Type: model.EvTodoOutput, ProjectID: t.ProjectID, TodoID: t.ID, Data: line, Time: time.Now()})
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(l.Root, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(l.path(t.ID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.TimeOnly), line)
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
}

func (l *TodoLog) path(id string) string { return filepath.Join(l.Root, id+".log") }
