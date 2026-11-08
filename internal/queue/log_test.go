package queue

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/klahr/dash/internal/model"
)

type recordPub struct {
	mu  sync.Mutex
	evs []model.Event
}

func (p *recordPub) Publish(ev model.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evs = append(p.evs, ev)
}

func TestTodoLogEvents(t *testing.T) {
	pub := &recordPub{}
	l := &TodoLog{Pub: pub, Root: t.TempDir()}
	td := model.Todo{ID: "t1", ProjectID: "p1"}

	if evs, err := l.Events(td.ID); err != nil || len(evs) != 0 {
		t.Fatalf("no events yet: %v %v", evs, err)
	}
	l.Event(td, model.RunEvent{Kind: "run_start", Text: "Started"})
	l.Event(td, model.RunEvent{Kind: "tool_use", ID: "tu1", Tool: "Bash", Input: []byte(`{"command":"go test ./..."}`)})
	l.Event(td, model.RunEvent{Kind: "tool_result", ID: "tu1", Text: "ok"})
	l.Line(td, "✔ ready for review")

	evs, err := l.Events(td.ID)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range evs {
		if e.At.IsZero() {
			t.Errorf("%s has no time", e.Kind)
		}
		kinds = append(kinds, e.Kind)
	}
	if got := strings.Join(kinds, ","); got != "run_start,tool_use,tool_result,status" {
		t.Fatalf("kinds: %s", got)
	}
	if string(evs[1].Input) != `{"command":"go test ./..."}` || evs[2].Text != "ok" || evs[3].Text != "✔ ready for review" {
		t.Errorf("events: %+v", evs)
	}

	// The text log keeps the summaries only.
	text, _ := l.Read(td.ID)
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], " $ go test ./...") || !strings.HasSuffix(lines[1], " ✔ ready for review") {
		t.Errorf("text log:\n%s", text)
	}

	var events, outputs int
	for _, e := range pub.evs {
		switch e.Type {
		case model.EvTodoEvent:
			events++
		case model.EvTodoOutput:
			outputs++
		}
		if e.TodoID != td.ID || e.ProjectID != td.ProjectID {
			t.Errorf("published for %s/%s", e.ProjectID, e.TodoID)
		}
	}
	if events != 4 || outputs != 2 {
		t.Errorf("published %d events, %d outputs", events, outputs)
	}

	// A line cut short by a crash is skipped.
	f, _ := os.OpenFile(filepath.Join(l.Root, "t1.events.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"kind":"te`)
	f.Close()
	if evs, err := l.Events(td.ID); err != nil || len(evs) != 4 {
		t.Errorf("after a torn line: %d events, %v", len(evs), err)
	}

	l.Remove(td.ID)
	if evs, _ := l.Events(td.ID); len(evs) != 0 {
		t.Errorf("events after Remove: %+v", evs)
	}
	if text, _ := l.Read(td.ID); text != "" {
		t.Errorf("text log after Remove: %q", text)
	}
}
